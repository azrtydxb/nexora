// Package envtestutil starts a kube-apiserver and etcd with the Nexora CRDs for tests.
package envtestutil

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
)

// Start runs kube-apiserver and etcd (KUBEBUILDER_ASSETS) with deploy/operator/crds plus extraCRDs,
// registers clientgoscheme and v1alpha1, and stops at test cleanup.
func Start(t *testing.T, extraCRDs ...string) (*rest.Config, client.Client) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set; run the tests with make operator-test")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     append([]string{filepath.Join(RepoRoot(t), "deploy/operator/crds")}, extraCRDs...),
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("envtest client: %v", err)
	}
	return cfg, c
}

// RepoRoot is the directory holding operator/go.mod's parent.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "operator", "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root (operator/go.mod) not found above the working directory")
		}
		dir = parent
	}
}
