/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package tunnel_operator

import (
	"context"
	netv1alpha1 "github.com/liqotech/liqo/apis/net/v1alpha1"
	liqonetOperator "github.com/liqotech/liqo/pkg/liqonet"
	"github.com/liqotech/liqo/pkg/liqonet/overlay"
	"github.com/liqotech/liqo/pkg/liqonet/tunnel"
	_ "github.com/liqotech/liqo/pkg/liqonet/tunnel/wireguard"
	"github.com/liqotech/liqo/pkg/liqonet/wireguard"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	k8s "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
	"os"
	"os/signal"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"strings"
	"syscall"
	"time"
)

var (
	shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGKILL}
)

// TunnelController reconciles a TunnelEndpoint object
type TunnelController struct {
	client.Client
	tunnel.Driver
	namespace                    string
	podIP string
	hwAddress string
	clientSet *k8s.Clientset
	drivers                      map[string]tunnel.Driver
	Scheme                       *runtime.Scheme
	Recorder                     record.EventRecorder
	DynClient                    dynamic.Interface
	K8sClient                    *k8s.Clientset
	TunnelIFacesPerRemoteCluster map[string]int
	RetryTimeout                 time.Duration
	IPTHandler                   *liqonetOperator.IPTablesHandler
	Wg *wireguard.Wireguard
	liqonetOperator.RouteManager
}

// +kubebuilder:rbac:groups=net.liqo.io,resources=tunnelendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=net.liqo.io,resources=tunnelendpoints/status,verbs=get;update;patch

func (tc *TunnelController) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	var endpoint netv1alpha1.TunnelEndpoint
	//name of our finalizer
	tunnelEndpointFinalizer := "tunnelEndpointFinalizer.net.liqo.io"
	if err := tc.Get(ctx, req.NamespacedName, &endpoint); err != nil {
		klog.Errorf("unable to fetch resource %s: %s", req.Name, err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	//we wait for the resource to be ready. The resource is created in two steps, firt the spec and metadata fields
	//then the status field. so we wait for the status to be ready.
	if endpoint.Status.Phase != "Ready" {
		klog.Infof("%s -> resource %s is not ready", endpoint.Spec.ClusterID, endpoint.Name)
		return ctrl.Result{RequeueAfter: tc.RetryTimeout}, nil
	}
	// examine DeletionTimestamp to determine if object is under deletion
	if endpoint.ObjectMeta.DeletionTimestamp.IsZero() {
		if !liqonetOperator.ContainsString(endpoint.ObjectMeta.Finalizers, tunnelEndpointFinalizer) {
			// The object is not being deleted, so if it does not have our finalizer,
			// then lets add the finalizer and update the object. This is equivalent
			// registering our finalizer.
			endpoint.ObjectMeta.Finalizers = append(endpoint.Finalizers, tunnelEndpointFinalizer)
			if err := tc.Update(ctx, &endpoint); err != nil {
				klog.Errorf("%s -> unable to update resource %s: %s", endpoint.Spec.ClusterID, endpoint.Name, err)
				return ctrl.Result{RequeueAfter: tc.RetryTimeout}, err
			}
		}
	} else {
		//the object is being deleted
		if liqonetOperator.ContainsString(endpoint.Finalizers, tunnelEndpointFinalizer) {
			if err := tc.drivers[endpoint.Spec.BackendType].DisconnectFromEndpoint(&endpoint); err != nil {
				//record an event and return
				tc.Recorder.Event(&endpoint, "Warning", "Processing", err.Error())
				klog.Errorf("%s -> unable to remove tunnel network interface %s for resource %s: %s", endpoint.Spec.ClusterID, endpoint.Status.TunnelIFaceName, endpoint.Name, err)
				return ctrl.Result{}, err
			}
			tc.Recorder.Event(&endpoint, "Normal", "Processing", "tunnel network interface removed")
			//safe to do, even if the key does not exist in the map
			delete(tc.TunnelIFacesPerRemoteCluster, endpoint.Spec.ClusterID)
			klog.Infof("%s -> tunnel network interface %s removed for resource %s", endpoint.Spec.ClusterID, endpoint.Status.TunnelIFaceName, endpoint.Name)
			retryError := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := tc.Get(ctx, req.NamespacedName, &endpoint); err != nil {
					klog.Errorf("unable to fetch resource %s: %s", req.Name, err)
					return err
				}
				//remove the finalizer from the list and update it.
				endpoint.Finalizers = liqonetOperator.RemoveString(endpoint.Finalizers, tunnelEndpointFinalizer)
				if err := tc.Update(ctx, &endpoint); err != nil {
					return err
				}
				return nil
			})
			if retryError != nil {
				klog.Errorf("%s -> unable to update finalizers of resource %s: %s", endpoint.Spec.ClusterID, endpoint.Name, retryError)
				return ctrl.Result{RequeueAfter: tc.RetryTimeout}, retryError
			}
			return ctrl.Result{RequeueAfter: tc.RetryTimeout}, nil
		}
	}
	//try to install the GRE tunnel if it does not exist
	con, err := tc.drivers[endpoint.Spec.BackendType].ConnectToEndpoint(&endpoint)
	if err != nil {
		klog.Errorf("%s -> unable to create tunnel network interface for resource %s :%s", endpoint.Spec.ClusterID, endpoint.Name, err)
		tc.Recorder.Event(&endpoint, "Warning", "Processing", err.Error())
		return ctrl.Result{RequeueAfter: tc.RetryTimeout}, err
	}
	tc.Recorder.Event(&endpoint, "Normal", "Processing", "tunnel network interface installed")
	//klog.Infof("%s -> tunnel network interface with name %s for resource %s created successfully", endpoint.Spec.ClusterID, iFaceName, endpoint.Name)
	//save the IFace index in the map
	//update the status of CR if needed
	//here we recover from conflicting resource versions
	if err := tc.EnsureIPTablesRulesPerCluster(&endpoint); err != nil {
		klog.Errorf("%s -> unable to iptables rules for resoure %s: %v", endpoint.Spec.ClusterID, endpoint.Namespace, err)
		return ctrl.Result{RequeueAfter: tc.RetryTimeout}, err
	}
	if err := tc.EnsureRoutesPerCluster("liqo-wg", &endpoint); err != nil {
		klog.Errorf("%s -> unable to insert route: %v", endpoint.Spec.ClusterID, err)
	}
	retryError := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if con == nil {
			return nil
		}
		if err := tc.Get(ctx, req.NamespacedName, &endpoint); err != nil {
			return err
		}
		endpoint.Status.Connection = *con
		err = tc.Status().Update(context.Background(), &endpoint)
		return err
	})
	if retryError != nil {
		klog.Errorf("%s -> unable to update status of resource %s: %s", endpoint.Spec.ClusterID, endpoint.Name, retryError)
		return ctrl.Result{RequeueAfter: tc.RetryTimeout}, retryError
	}
	return ctrl.Result{RequeueAfter: tc.RetryTimeout}, nil
}

//used to remove all the tunnel interfaces when the controller is closed
//it does not return an error, but just logs them, cause we can not recover from
//them at exit time
func (tc *TunnelController) RemoveAllTunnels() {
	for driverType, driver := range tc.drivers {
		err := driver.Close()
		if err == nil {
			klog.Infof("removed tunnel interface of type %s", driverType)
		} else {
			klog.Errorf("unable to delete tunnel network interface of type %s: %s", driverType, err)
		}
	}
}

// SetupSignalHandlerForRouteOperator registers for SIGTERM, SIGINT, SIGKILL. A stop channel is returned
// which is closed on one of these signals.
func (tc *TunnelController) SetupSignalHandlerForTunnelOperator() (stopCh <-chan struct{}) {
	stop := make(chan struct{})
	c := make(chan os.Signal, 1)
	signal.Notify(c, shutdownSignals...)
	go func(r *TunnelController) {
		sig := <-c
		klog.Infof("received signal %s: cleaning up", sig.String())
		r.RemoveAllTunnels()
		<-c
		close(stop)
	}(tc)
	return stop
}

func (tc *TunnelController) SetupWithManager(mgr ctrl.Manager) error {
	resourceToBeProccesedPredicate := predicate.Funcs{
		DeleteFunc: func(e event.DeleteEvent) bool {
			//finalizers are used to check if a resource is being deleted, and perform there the needed actions
			//we don't want to reconcile on the delete of a resource.
			return false
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&netv1alpha1.TunnelEndpoint{}).WithEventFilter(resourceToBeProccesedPredicate).
		Complete(tc)
}

//for each registered tunnel implementation it creates and initializes the driver
func (tc *TunnelController) SetUpTunnelDrivers() error {
	tc.drivers = make(map[string]tunnel.Driver)
	for tunnelType, createDriverFunc := range tunnel.Drivers {
		klog.V(3).Infof("Creating driver for tunnel of type %s", tunnelType)
		d, err := createDriverFunc(tc.K8sClient, tc.namespace)
		if err != nil {
			return err
		}
		klog.V(3).Infof("Initializing driver for %s tunnel", tunnelType)
		err = d.Init()
		if err != nil {
			return err
		}
		klog.V(3).Infof("Driver for %s tunnel created and initialized", tunnelType)
		tc.drivers[tunnelType] = d
	}
	return nil
}

func (tc *TunnelController) SetUpIPTablesHandler() error {
	iptHandler, err := liqonetOperator.NewIPTablesHandler()
	if err != nil {
		return err
	}
	tc.IPTHandler = iptHandler
	return nil
}

func (tc *TunnelController) SetUpRouteManager() {
	tc.RouteManager = liqonetOperator.RouteManager{}
}

//Instantiates and initializes the tunnel controller
func NewTunnelController(mgr ctrl.Manager, podIP, hwAddress, namespace string, dynClient dynamic.Interface, clientSet *k8s.Clientset) (*TunnelController, error) {
	clientset, err := k8s.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}
	podName, err := liqonetOperator.GetPodName()
	//create overlay network interface
	wg, err := overlay.CreateInterface(podName, namespace, clientSet)
	if err != nil{
		return nil, err
	}
	overlayIP := strings.Join([]string{overlay.GetOverlayIP(podIP), "4"}, "/")
	if err = wg.AddIP(overlayIP); err != nil{
		klog.Errorf("unable to configure ip %s on device %s: %v",overlayIP, wg.GetDeviceName(), err)
		return nil, err
	}
	//enable ip forwarding
	if err = liqonetOperator.EnableIPForwarding(); err != nil{
		return nil, err
	}
	tc := &TunnelController{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("tunnel-operator"),
		K8sClient: clientset,
		namespace: namespace,
		podIP: podIP,
		hwAddress: hwAddress,
		DynClient: dynClient,
		clientSet: clientSet,
		Wg: wg,
	}
	err = tc.SetUpTunnelDrivers()
	if err != nil {
		return nil, err
	}
	err = tc.SetUpIPTablesHandler()
	if err != nil {
		return nil, err
	}
	tc.SetUpRouteManager()
	return tc, nil
}

func (tc *TunnelController) EnsureIPTablesRulesPerCluster(tep *netv1alpha1.TunnelEndpoint) error {
	if err := tc.IPTHandler.EnsureChainRulespecs(tep); err != nil {
		return err
	}
	if err := tc.IPTHandler.EnsurePostroutingRules(true, tep); err != nil {
		return err
	}
	if err := tc.IPTHandler.EnsurePreroutingRules(tep); err != nil {
		return err
	}
	/*if err := r.ensureForwardRules(tep); err != nil {
		return err
	}
	if err := r.ensureInputRules(tep); err != nil {
		return err
	}*/
	return nil
}

func (tc *TunnelController) Watcher(sharedDynFactory dynamicinformer.DynamicSharedInformerFactory, resourceType schema.GroupVersionResource, handlerFuncs cache.ResourceEventHandlerFuncs, stopCh chan struct{}) {
	dynInformer := sharedDynFactory.ForResource(resourceType)
	klog.Infof("starting watcher for %s", resourceType.String())
	//adding handlers to the informer
	dynInformer.Informer().AddEventHandler(handlerFuncs)
	dynInformer.Informer().Run(stopCh)
}
