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

package main

import (
	"flag"
	"github.com/coreos/go-iptables/iptables"
	clusterConfig "github.com/liqotech/liqo/apis/config/v1alpha1"
	netv1alpha1 "github.com/liqotech/liqo/apis/net/v1alpha1"
	route_operator "github.com/liqotech/liqo/internal/liqonet/route-operator"
	tunnel_operator "github.com/liqotech/liqo/internal/liqonet/tunnel-operator"
	"github.com/liqotech/liqo/internal/liqonet/tunnelEndpointCreator"
	"github.com/liqotech/liqo/pkg/liqonet"
	"github.com/liqotech/liqo/pkg/liqonet/overlay"
	"github.com/vishvananda/netlink"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog/v2"
	"net"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"strings"
	"sync"
	"time"
	// +kubebuilder:scaffold:imports
)

var (
	scheme        = runtime.NewScheme()
	defaultConfig = liqonet.VxlanNetConfig{
		NetworkPrefix: "240",
		DeviceName:    "liqonet",
		Port:          "4789", //IANA assigned
		Vni:           "200",
	}
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = netv1alpha1.AddToScheme(scheme)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var runAsRouteOperator bool
	var runAs string

	flag.StringVar(&metricsAddr, "metrics-addr", ":0", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&runAsRouteOperator, "run-as-route-operator", false,
		"Runs the controller as Route-Operator, the default value is false and will run as Tunnel-Operator")
	flag.StringVar(&runAs, "run-as", "tunnel-operator", "The accepted values are: tunnel-operator, route-operator, tunnelEndpointCreator-operator. The default value is \"tunnel-operator\"")
	flag.Parse()
	waitCleanUp := make(chan struct{})
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		LeaderElection:     enableLeaderElection,
		Port:               9443,
	})
	if err != nil {
		klog.Errorf("unable to get manager: %s", err)
		os.Exit(1)
	}
	// creates the in-cluster config or uses the .kube/config file
	config := ctrl.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	// +kubebuilder:scaffold:builder

	dynClient := dynamic.NewForConfigOrDie(mgr.GetConfig())
	switch runAs {
	case "route-operator":
		//get node name
		nodeName, err := liqonet.GetNodeName()
		if err != nil {
			klog.Errorf("unable to get node nome: %s", err)
			os.Exit(4)
		}
		podIP, err := liqonet.GetPodIP()
		if err != nil {
			klog.Errorf("unable to get pod ip: %s", err)
			os.Exit(4)
		}
		namespace, err := liqonet.GetPodNamespace()
		if err != nil {
			klog.Error(err)
			os.Exit(1)
		}
		wg, err := overlay.CreateInterface(nodeName, namespace, clientset)
		if err != nil {
			klog.Error(err)
			os.Exit(1)
		}
		overlayIP := strings.Join([]string{overlay.GetOverlayIP(podIP.String()), "4"}, "/")
		if err = wg.AddIP(overlayIP); err != nil{
			klog.Errorf("unable to configure ip on device %s: %v", wg.GetDeviceName(), err)
			os.Exit(1)
		}

		ipt, err := iptables.New()
		if err != nil {
			klog.Errorf("unable to initialize iptables, check if the binaries are present in the sysetm: %s", err)
			os.Exit(6)
		}

		r := &route_operator.RouteController{
			Client:                             mgr.GetClient(),
			Scheme:                             mgr.GetScheme(),
			Recorder:                           mgr.GetEventRecorderFor(strings.Join([]string{"route-OP", nodeName}, "-")),
			ClientSet:                          clientset,
			DynClient:                          dynClient,
			PodIP:                              podIP.String(),
			Namespace:                          namespace,
			Wg:                                 wg,
			IPTablesRuleSpecsReferencingChains: make(map[string]liqonet.IPtableRule),
			IPTablesChains:                     make(map[string]liqonet.IPTableChain),
			RoutesPerRemoteCluster:             make(map[string]netlink.Route),
			NodeName:                           nodeName,
			RetryTimeout:                       30 * time.Second,
			IPtables:                           ipt,
			NetLink:                            &liqonet.RouteManager{},
			Configured:                         make(chan bool, 1),
		}
		r.WatchConfiguration(config, &clusterConfig.GroupVersion)
		if !r.IsConfigured {
			<-r.Configured
			r.IsConfigured = true
			klog.Infof("route-operator configured with podCIDR %s", r.ClusterPodCIDR)
		}
		r.StartPodWatcher()
		r.StartServiceWatcher()
		//this go routing ensures that the general chains and rulespecs for LIQO exist and are
		//at the first position
		quit := make(chan struct{})
		go func() {
			for {
				if err := r.CreateAndEnsureIPTablesChains(); err != nil {
					klog.Error(err)
				}
				select {
				case <-quit:
					klog.Infof("stopping go routing that ensure liqo iptables rules")
					return
				case <-time.After(tunnelEndpointCreator.ResyncPeriod):
				}
			}
		}()
		if err = r.SetupWithManager(mgr); err != nil {
			klog.Errorf("unable to setup controller: %s", err)
			os.Exit(1)
		}
		klog.Info("Starting manager as Route-Operator")
		if err := mgr.Start(r.SetupSignalHandlerForRouteOperator(quit, waitCleanUp)); err != nil {
			klog.Errorf("unable to start controller: %s", err)
			os.Exit(1)
		}
		<-waitCleanUp

	case "tunnel-operator":
		namespace, err := liqonet.GetPodNamespace()
		if err != nil {
			klog.Error(err)
			os.Exit(1)
		}
		podIPAddr, err := liqonet.GetPodIP()
		if err != nil {
			klog.Error(err)
			os.Exit(1)
		}

		tc, err := tunnel_operator.NewTunnelController(mgr, podIPAddr.String(), "", namespace, dynClient, clientset)
		if err != nil {
			klog.Errorf("an error occurred while creating the tunnel controller: %v", err)
			os.Exit(1)
		}
		tc.StartGWPodWatcher()
		tc.StartServiceWatcher()
		if err := tc.IPTHandler.CreateAndEnsureIPTablesChains(); err != nil {
			klog.Errorf("an error occurred while creating iptables handler: %v", err)
			os.Exit(1)
		}
		if err = tc.SetupWithManager(mgr); err != nil {
			klog.Errorf("unable to setup tunnel controller: %s", err)
			os.Exit(1)
		}
		klog.Info("Starting manager as Tunnel-Operator")
		if err := mgr.Start(tc.SetupSignalHandlerForTunnelOperator()); err != nil {
			klog.Errorf("unable to start tunnel controller: %s", err)
			os.Exit(1)
		}

	case "tunnelEndpointCreator-operator":
		dynClient := dynamic.NewForConfigOrDie(mgr.GetConfig())
		r := &tunnelEndpointCreator.TunnelEndpointCreator{
			Client:                     mgr.GetClient(),
			Scheme:                     mgr.GetScheme(),
			ClientSet:                  clientset,
			DynClient:                  dynClient,
			Manager:                    mgr,
			Namespace:                  "liqo",
			WaitConfig:                 &sync.WaitGroup{},
			ReservedSubnets:            make(map[string]*net.IPNet),
			Configured:                 make(chan bool, 1),
			ForeignClusterStartWatcher: make(chan bool, 1),
			ForeignClusterStopWatcher:  make(chan struct{}),

			IPManager: liqonet.IpManager{
				UsedSubnets:        make(map[string]*net.IPNet),
				FreeSubnets:        make(map[string]*net.IPNet),
				SubnetPerCluster:   make(map[string]*net.IPNet),
				ConflictingSubnets: make(map[string]*net.IPNet),
			},
			RetryTimeout: 30 * time.Second,
		}
		r.WaitConfig.Add(3)
		//starting configuration watcher
		r.WatchConfiguration(config, &clusterConfig.GroupVersion)
		if err = r.SetupWithManager(mgr); err != nil {
			klog.Errorf("unable to create controller controller TunnelEndpointCreator: %s", err)
			os.Exit(1)
		}
		go r.StartForeignClusterWatcher()
		go r.StartGWPodWatcher()
		go r.StartServiceWatcher()
		go r.StartSecretWatcher()
		klog.Info("starting manager as tunnelEndpointCreator-operator")
		if err := mgr.Start(r.SetupSignalHandlerForTunEndCreator()); err != nil {
			klog.Errorf("an error occurred while starting manager: %s", err)
			os.Exit(1)
		}
	}
}
