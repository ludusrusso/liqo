package route_operator

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
	podResource     = "pods"
	podRouteLabelKey = "run"
	podRouteLabelValue = "route-operator"
	overlayPubKeyAnnotation = "net.liqo.io/overlay.pubkey"
)



func (r *RouteController) StartPodWatcher() {
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.DynClient, 0, r.Namespace, setGWPodSelectorLabel)
	go r.Watcher(dynFactory, corev1.SchemeGroupVersion.WithResource(podResource), cache.ResourceEventHandlerFuncs{
		AddFunc:    r.podHandlerAdd,
		UpdateFunc: r.podHandlerUpdate,
	}, make(chan struct{}))
}

func (r *RouteController) podHandlerAdd(obj interface{}) {
	c := r.ClientSet
	ns := r.Namespace
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
	//check if it is our pod
	if p.Status.PodIP != r.PodIP{
		return
	}
	currentPubKey := r.Wg.GetPubKey()
	pubKey, _ := p.GetAnnotations()[overlayPubKeyAnnotation]
	if pubKey != currentPubKey{
		pubKey = currentPubKey
	}else{
		return
	}
	retryError := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pod, err := c.CoreV1().Pods(ns).Get(context.Background(), p.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annotations := pod.GetAnnotations()
		if annotations == nil{
			annotations = make(map[string]string)
		}
		annotations[overlayPubKeyAnnotation] = pubKey
		pod.SetAnnotations(annotations)
		pod, err = c.CoreV1().Pods(ns).Update(context.Background(), pod, metav1.UpdateOptions{})
		return err
	})
	if retryError != nil {
		klog.Errorf("an error occurred while updating pod %s: %s", p.Name, retryError)
		return
	}
}

func (r *RouteController) podHandlerUpdate(oldObj interface{}, newObj interface{}) {
	r.podHandlerAdd(newObj)
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