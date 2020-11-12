package tunnel_operator_test

import (
	netv1alpha1 "github.com/liqotech/liqo/apis/net/v1alpha1"
	tunnel_operator "github.com/liqotech/liqo/internal/liqonet/tunnel-operator"
	"github.com/onsi/gomega/gexec"
	"k8s.io/client-go/kubernetes/scheme"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var k8sClient client.Client
var k8sManager ctrl.Manager
var testEnv *envtest.Environment
var tc *tunnel_operator.TunnelController

func TestTunnelOperator(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TunnelOperator Suite")
}

var _ = BeforeSuite(func(done Done) {
	By("tunnel-operator: bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "deployments", "liqo", "crds")},
	}
	cfg, err := testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	err = scheme.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = netv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	tc, err = tunnel_operator.NewTunnelController(k8sManager, "default")
	Expect(err).ToNot(HaveOccurred())
	Expect(tc).ToNot(BeNil())

	err = tc.SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())
	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(tc.SetupSignalHandlerForTunnelOperator())
		Expect(err).ToNot(HaveOccurred())
	}()

	k8sClient = k8sManager.GetClient()
	Expect(k8sClient).ToNot(BeNil())
	close(done)

}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	tc.RemoveAllTunnels()
	gexec.KillAndWait(5 * time.Second)
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
