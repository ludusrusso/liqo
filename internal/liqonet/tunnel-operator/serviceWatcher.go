package tunnel_operator

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"strings"
)

var (
	serviceResource      = "services"
	serviceLabelKey      = "net.liqo.io/tunnelEndpoint"
	serviceLabelValue    = "true"
	serviceAnnotationKey = "net.liqo.io/gatewayNodeIP"
	overlayAnnotationKey = "net.liqo.io/overlay.pubkey"
)

func (tc *TunnelController) StartServiceWatcher() {
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(tc.DynClient, 0, tc.namespace, setServiceFilteringLabel)
	go tc.Watcher(dynFactory, corev1.SchemeGroupVersion.WithResource(serviceResource), cache.ResourceEventHandlerFuncs{
		AddFunc:    tc.serviceHandlerAdd,
		UpdateFunc: tc.serviceHandlerUpdate,
	}, make(chan struct{}))
}

func (tc *TunnelController) serviceHandlerAdd(obj interface{}) {
	c := tc.clientSet
	ns := tc.namespace
	objUnstruct, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("an error occurred while converting interface to unstructured object")
		return
	}
	s := &corev1.Service{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(objUnstruct.Object, s)
	if err != nil {
		klog.Errorf("an error occurred while converting resource %s of type %s to typed object: %s", objUnstruct.GetName(), objUnstruct.GetKind(), err)
		return
	}

	if s.Spec.Type != corev1.ServiceTypeNodePort && s.Spec.Type != corev1.ServiceTypeLoadBalancer {
		klog.Errorf("the service %s in namespace %s is of type %s, only types of %s and %s are accepted", s.GetName(), s.GetNamespace(), s.Spec.Type, corev1.ServiceTypeLoadBalancer, corev1.ServiceTypeNodePort)
		return
	}
	currentPubKey := tc.Wg.GetPubKey()
	pubKey, _ := s.GetAnnotations()[overlayPubKeyAnnotation]
	if pubKey != currentPubKey{
		pubKey = currentPubKey
	}else{
		return
	}
	retryError := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		svc, err := c.CoreV1().Services(ns).Get(context.Background(), s.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annotations := svc.GetAnnotations()
		if annotations == nil{
			annotations = make(map[string]string)
		}
		annotations[overlayPubKeyAnnotation] = pubKey
		svc.SetAnnotations(annotations)
		svc, err = c.CoreV1().Services(ns).Update(context.Background(), svc, metav1.UpdateOptions{})
		return err
	})
	if retryError != nil {
		klog.Errorf("an error occurred while updating pod %s: %s", s.Name, retryError)
		return
	}
	return
}

func (tc *TunnelController) serviceHandlerUpdate(oldObj interface{}, newObj interface{}) {
	tc.serviceHandlerAdd(newObj)
}

func setServiceFilteringLabel(options *metav1.ListOptions) {
	if options.LabelSelector == "" {
		newLabelSelector := []string{serviceLabelKey, "=", serviceLabelValue}
		options.LabelSelector = strings.Join(newLabelSelector, "")
	} else {
		newLabelSelector := []string{options.LabelSelector, serviceLabelKey, "=", serviceLabelValue}
		options.LabelSelector = strings.Join(newLabelSelector, "")
	}
}
