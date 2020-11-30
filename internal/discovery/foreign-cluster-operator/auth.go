package foreign_cluster_operator

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	discoveryv1alpha1 "github.com/liqotech/liqo/apis/discovery/v1alpha1"
	"github.com/liqotech/liqo/pkg/auth"
	"github.com/liqotech/liqo/pkg/crdClient"
	"github.com/liqotech/liqo/pkg/kubeconfig"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	client_scheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"net/http"
	"strings"
)

// get a client to the remote cluster
// if the ForeignCluster has a reference to the secret's role, load the configurations from that secret
// else try to get a role from the remote cluster
//
// first of all, if the status is pending we can try to get a role with an empty token, if the remote cluster allows it
// the status will become accepted
//
// if our status is EmptyRefused, this means that the remote cluster refused out request with the empty token, so we will
// wait to have a token to ask for the role again
//
// while we are waiting for that secret this function will return no error, but an empty client
func (r *ForeignClusterReconciler) getRemoteClient(fc *discoveryv1alpha1.ForeignCluster, gv *schema.GroupVersion) (*crdClient.CRDClient, error) {
	if strings.HasPrefix(fc.Spec.AuthUrl, "fake://") {
		config := *r.ForeignConfig

		config.ContentConfig.GroupVersion = gv
		config.APIPath = "/apis"
		config.NegotiatedSerializer = client_scheme.Codecs.WithoutConversion()
		config.UserAgent = rest.DefaultKubernetesUserAgent()

		fc.Status.AuthStatus = discoveryv1alpha1.AuthStatusAccepted

		return crdClient.NewFromConfig(&config)
	}

	if fc.Status.Outgoing.RoleRef != nil {
		roleSecret, err := r.crdClient.Client().CoreV1().Secrets(r.Namespace).Get(context.TODO(), fc.Status.Outgoing.RoleRef.Name, metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			// delete reference, in this way at next iteration it will be reloaded
			fc.Status.Outgoing.RoleRef = nil
			_, err2 := r.Update(fc)
			if err2 != nil {
				klog.Error(err2)
			}
			return nil, err
		}

		config, err := kubeconfig.LoadFromSecret(roleSecret)
		if err != nil {
			klog.Error(err)
			return nil, err
		}
		config.ContentConfig.GroupVersion = gv
		config.APIPath = "/apis"
		config.NegotiatedSerializer = client_scheme.Codecs.WithoutConversion()
		config.UserAgent = rest.DefaultKubernetesUserAgent()

		fc.Status.AuthStatus = discoveryv1alpha1.AuthStatusAccepted

		return crdClient.NewFromConfig(config)
	}

	if fc.Status.AuthStatus == discoveryv1alpha1.AuthStatusAccepted {
		// TODO: handle this possibility
		// this can happen if the role was accepted but the local secret has been removed
		err := errors.New("auth status is accepted but there is no role ref")
		klog.Error(err)
		return nil, err
	}

	// not existing role
	if fc.Status.AuthStatus == discoveryv1alpha1.AuthStatusPending || (fc.Status.AuthStatus == discoveryv1alpha1.AuthStatusEmptyRefused && r.getAuthToken(fc) != "") {
		kubeconfigStr, err := r.askRemoteRole(fc)
		if err != nil {
			klog.Error(err)
			return nil, err
		}
		if kubeconfigStr == "" {
			return nil, nil
		}
		roleSecret, err := kubeconfig.CreateSecret(r.crdClient.Client(), r.Namespace, kubeconfigStr, map[string]string{
			"cluster-id":       fc.Spec.ClusterIdentity.ClusterID,
			"liqo-remote-role": "",
		})
		if err != nil {
			klog.Error(err)
			return nil, err
		}

		// set ref in the FC
		fc.Status.Outgoing.RoleRef = &v1.ObjectReference{
			Name:      roleSecret.Name,
			Namespace: roleSecret.Namespace,
			UID:       roleSecret.UID,
		}

		config, err := kubeconfig.LoadFromSecret(roleSecret)
		if err != nil {
			klog.Error(err)
			return nil, err
		}
		config.ContentConfig.GroupVersion = gv
		config.APIPath = "/apis"
		config.NegotiatedSerializer = client_scheme.Codecs.WithoutConversion()
		config.UserAgent = rest.DefaultKubernetesUserAgent()

		return crdClient.NewFromConfig(config)
	}

	klog.V(4).Info("no available role")
	return nil, nil
}

// load the auth token form a labelled secret
func (r *ForeignClusterReconciler) getAuthToken(fc *discoveryv1alpha1.ForeignCluster) string {
	tokenSecrets, err := r.crdClient.Client().CoreV1().Secrets(r.Namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: strings.Join(
			[]string{
				strings.Join([]string{"cluster-id", fc.Spec.ClusterIdentity.ClusterID}, "="),
				"liqo-auth-token",
			},
			",",
		),
	})
	if err != nil {
		klog.Error(err)
		return ""
	}

	for _, tokenSecret := range tokenSecrets.Items {
		if token, found := tokenSecret.Data["token"]; found {
			return string(token)
		}
	}
	return ""
}

// send HTTP request to get the role from the remote cluster
func (r *ForeignClusterReconciler) askRemoteRole(fc *discoveryv1alpha1.ForeignCluster) (string, error) {
	token := r.getAuthToken(fc)

	roleRequest := auth.RoleRequest{
		ClusterID: r.DiscoveryCtrl.ClusterId.GetClusterID(),
		Token:     token,
	}
	jsonRequest, err := json.Marshal(roleRequest)
	if err != nil {
		klog.Error(err)
		return "", err
	}

	resp, err := sendRequest(fmt.Sprintf("%s/role", fc.Spec.AuthUrl), bytes.NewBuffer(jsonRequest))
	if err != nil {
		klog.Error(err)
		return "", err
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		klog.Error(err)
		return "", err
	}
	switch resp.StatusCode {
	case http.StatusCreated:
		fc.Status.AuthStatus = discoveryv1alpha1.AuthStatusAccepted
		klog.Info("Role Created")
		return string(body), nil
	case http.StatusForbidden:
		if token == "" {
			fc.Status.AuthStatus = discoveryv1alpha1.AuthStatusEmptyRefused
		} else {
			fc.Status.AuthStatus = discoveryv1alpha1.AuthStatusRefused
		}
		klog.Info(string(body))
		return "", nil
	default:
		klog.Info(string(body))
		return "", errors.New(string(body))
	}
}

func sendRequest(url string, payload *bytes.Buffer) (*http.Response, error) {
	tr := &http.Transport{}
	client := &http.Client{Transport: tr}
	resp, err := client.Post(url, "text/plain", payload)
	if err != nil && isUnknownAuthority(err) {
		// disable TLS CA check for untrusted remote clusters
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		client := &http.Client{Transport: tr}
		return client.Post(url, "text/plain", payload)
	}
	return resp, err
}
