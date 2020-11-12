package tunnel_operator

import (
	"github.com/liqotech/liqo/pkg/liqonet/overlay"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"strings"
	"time"
)

var (
	podResource = "pods"
	podRouteLabelKey = "run"
	podRouteLabelValue = "route-operator"
	overlayPubKeyAnnotation = "net.liqo.io/overlay.pubkey"
	keepalive = 10*time.Second
)

func (tc *TunnelController) StartGWPodWatcher() {
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(tc.DynClient, 0, tc.namespace, setGWPodSelectorLabel)
	go tc.Watcher(dynFactory, corev1.SchemeGroupVersion.WithResource(podResource), cache.ResourceEventHandlerFuncs{
		AddFunc:    tc.gwPodHandlerAdd,
		UpdateFunc: tc.gwPodHandlerUpdate,
	}, make(chan struct{}))
}

func (tc *TunnelController) gwPodHandlerAdd(obj interface{}) {
	objUnstruct, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("an error occurred while converting interface to unstructured object")
		return
	}
	p := &corev1.Pod{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(objUnstruct.Object, p)
	if err != nil {
		klog.Errorf("an error occurred while converting resource %s of type %s to typed object: %s", objUnstruct.GetName(), objUnstruct.GetKind(), err)
		return
	}
	//check if the the public key has been set
	pubKey, ok := p.GetAnnotations()[overlayPubKeyAnnotation]
	if !ok{
		klog.Infof("public key for node %s has not been set", p.Spec.NodeName)
		return
	}
	if p.Status.PodIP == ""{
		return
	}
	overlayIP := strings.Join([]string{overlay.GetOverlayIP(p.Status.PodIP), "32"}, "/")
	podIP := strings.Join([]string{p.Status.PodIP, "32"}, "/")
	err = tc.Wg.AddPeer(pubKey, p.Status.PodIP, "51871", []string{overlayIP, podIP}, &keepalive)
	if err != nil{
		klog.Error(err)
		return
	}

}

func (tc *TunnelController) gwPodHandlerUpdate(oldObj interface{}, newObj interface{}) {
	tc.gwPodHandlerAdd(newObj)
}

func setGWPodSelectorLabel(options *metav1.ListOptions) {
	if options == nil {
		options = &metav1.ListOptions{}
		newLabelSelector := []string{options.LabelSelector, podRouteLabelKey, "=", podRouteLabelValue}
		options.LabelSelector = strings.Join(newLabelSelector, "")
	}
	if options.LabelSelector == "" {
		newLabelSelector := []string{podRouteLabelKey, "=", podRouteLabelValue}
		options.LabelSelector = strings.Join(newLabelSelector, "")
	}
}
