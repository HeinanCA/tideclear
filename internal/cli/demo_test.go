package cli

import (
	"bytes"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

var demoNow = time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func runDemo(t *testing.T, deps Deps, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runDeps(deps, append([]string{"demo"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestDemoOutputGolden(t *testing.T) {
	code, out, errOut := runDemo(t, Deps{Now: func() time.Time { return demoNow }})
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q, want 0 and empty", code, errOut)
	}
	checkGolden(t, "demo.txt", []byte(out))
}

func TestDemoOutputShape(t *testing.T) {
	_, out, _ := runDemo(t, Deps{Now: func() time.Time { return demoNow }})
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "SAMPLE DATA: this is a built-in demonstration, not a scan of your cluster") {
		t.Errorf("first line is not the sample-data banner: %q", lines[0])
	}
	section := out[strings.Index(out, "upgrade insights:"):strings.Index(out, "\nadd-ons:")]
	if got := strings.Count(section, " BLOCKER "); got != 1 {
		t.Errorf("BLOCKER verdict rows in the upgrade insights section = %d, want 1", got)
	}
	if got := strings.Count(section, "STALE, clears 2026-11-09"); got != 1 {
		t.Errorf("STALE rows with a clears date = %d, want 1", got)
	}
	for _, want := range []string{
		"INCOMPATIBLE",
		"\nnodes:\n",
		"sample-cluster",
		"Nothing was read from AWS or Kubernetes",
		"A real scan with a BLOCKER exits 1; demo always exits 0",
		"result not clean:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	for _, banned := range []string{"http://", "https://", "verified", "validated"} {
		if strings.Contains(out, banned) {
			t.Errorf("output must not contain %q", banned)
		}
	}
}

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("demo made an HTTP request to %s", r.URL)
	return nil, errors.New("network is off")
}

func TestDemoUsesNoAWSKubeOrNetwork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	for k, v := range map[string]string{
		"AWS_PROFILE":                 "no-such-profile",
		"AWS_CONFIG_FILE":             missing,
		"AWS_SHARED_CREDENTIALS_FILE": missing,
		"AWS_ACCESS_KEY_ID":           "",
		"AWS_SECRET_ACCESS_KEY":       "",
		"AWS_REGION":                  "",
		"KUBECONFIG":                  missing,
	} {
		t.Setenv(k, v)
	}
	old := http.DefaultTransport
	http.DefaultTransport = failingTransport{t}
	t.Cleanup(func() { http.DefaultTransport = old })

	fatal := Deps{
		EKS: func(string, string) (eksinfo.EKSAPI, error) {
			t.Fatal("demo built an EKS client")
			return nil, nil
		},
		Cluster: func(string, string) (ClusterAPI, error) {
			t.Fatal("demo built a cluster client")
			return nil, nil
		},
		DefaultRegion: func(string) (string, error) {
			t.Fatal("demo resolved an AWS region")
			return "", nil
		},
		Kube: func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
			t.Fatal("demo built Kubernetes clients")
			return nil, nil, nil
		},
		KubeIdentity: func(string, string) (string, string) {
			t.Fatal("demo read the kube identity")
			return "", ""
		},
	}
	code, out, errOut := runDemo(t, fatal)
	if code != 0 || errOut != "" || !strings.Contains(out, "SAMPLE DATA") {
		t.Fatalf("exit = %d, stderr = %q, out = %q", code, errOut, out)
	}

	var realOut, realErr bytes.Buffer
	if code := Run([]string{"demo"}, &realOut, &realErr); code != 0 || realErr.Len() != 0 {
		t.Fatalf("Run exit = %d, stderr = %q with the real deps and bogus AWS/kube settings", code, realErr.String())
	}
}

func TestDemoRunsOnTheRealClockWithRealDeps(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"demo"}, &out, &errOut); code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "UNKNOWN") {
		t.Errorf("sample went UNKNOWN on the real clock:\n%s", out.String())
	}
}

func TestDemoRejectsArgsAndFlags(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--output", "json"}, {"--strict"}} {
		code, out, errOut := runDemo(t, Deps{}, args...)
		if code != 2 || out != "" {
			t.Errorf("demo %v: exit = %d, stdout = %q, want 2 and empty", args, code, out)
		}
		assertOneLine(t, errOut)
	}
}

func TestHelpListsDemo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "demo ") {
		t.Errorf("--help does not list demo:\n%s", out.String())
	}
}
