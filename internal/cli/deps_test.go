package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"k8s.io/client-go/rest"
)

func TestClientFactoryLoadsConfigOnceAndOverridesRegion(t *testing.T) {
	loads := 0
	f := clientFactory(func(profile, region string) (aws.Config, error) {
		loads++
		return aws.Config{Region: "from-profile"}, nil
	})
	for _, r := range []string{"eu-x", "us-y", "ap-z"} {
		api, err := f("prof", r)
		if err != nil {
			t.Fatal(err)
		}
		if got := api.(*eks.Client).Options().Region; got != r {
			t.Errorf("client region = %q, want %q", got, r)
		}
	}
	if loads != 1 {
		t.Errorf("config loaded %d times, want 1", loads)
	}
}

func TestClientFactoryConfigErrorIsPlain(t *testing.T) {
	f := clientFactory(func(string, string) (aws.Config, error) { return aws.Config{}, errors.New("bad profile") })
	if _, err := f("p", "r1"); err == nil || err.Error() != "bad profile" {
		t.Fatalf("err = %v", err)
	}
}

func TestClientFactoryPassesRegionToConfigLoad(t *testing.T) {
	var got []string
	f := clientFactory(func(profile, region string) (aws.Config, error) {
		got = append(got, region)
		return aws.Config{Region: region}, nil
	})
	for _, r := range []string{"eu-x", "us-y"} {
		if _, err := f("prof", r); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0] != "eu-x" {
		t.Errorf("load regions = %v, want [eu-x]", got)
	}
}

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	cfg := `apiVersion: v1
kind: Config
current-context: demo
clusters:
- name: demo
  cluster: {server: "https://127.0.0.1:1"}
contexts:
- name: demo
  context: {cluster: demo, user: demo}
users:
- name: demo
  user: {token: not-a-real-token}
`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKubeRestConfigBoundsRequestsAndSilencesWarnings(t *testing.T) {
	cfg, err := kubeRestConfig(writeKubeconfig(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
	if _, ok := cfg.WarningHandler.(rest.NoWarnings); !ok {
		t.Errorf("WarningHandler = %T, want rest.NoWarnings", cfg.WarningHandler)
	}
}

func TestKubeRestConfigUnknownContextIsOneLine(t *testing.T) {
	_, err := kubeRestConfig(writeKubeconfig(t), "nope")
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v, want one-line error", err)
	}
}

func TestRealKubeMissingKubeconfigExitsTwoOneLine(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	t.Setenv("KUBECONFIG", missing)
	t.Setenv("HOME", t.TempDir()) // belt-and-braces: RecommendedHomeFile is fixed at init
	if _, _, err := realKube("", ""); err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v, want one-line error", err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"scan", "--target", "1.35"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr %q)", code, errOut.String())
	}
	assertOneLine(t, errOut.String())
}

func TestRealKubeExplicitMissingKubeconfig(t *testing.T) {
	_, _, err := realKube(filepath.Join(t.TempDir(), "absent"), "")
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v, want one-line error", err)
	}
}

func TestEKSAndClusterClientsShareOneConfigLoad(t *testing.T) {
	loads := 0
	cfg := cachedConfig(func(string, string) (aws.Config, error) {
		loads++
		return aws.Config{Region: "from-profile"}, nil
	})
	eksFn, clusterFn := clientFor(cfg, buildEKS), clientFor(cfg, buildCluster)
	if _, err := eksFn("prof", "eu-x"); err != nil {
		t.Fatal(err)
	}
	api, err := clusterFn("prof", "us-y")
	if err != nil {
		t.Fatal(err)
	}
	if got := api.(*eks.Client).Options().Region; got != "us-y" {
		t.Errorf("cluster client region = %q, want us-y", got)
	}
	if loads != 1 {
		t.Errorf("config loaded %d times, want 1", loads)
	}
}

func TestRealDepsWiresCluster(t *testing.T) {
	if realDeps().Cluster == nil {
		t.Fatal("realDeps().Cluster is nil")
	}
}
