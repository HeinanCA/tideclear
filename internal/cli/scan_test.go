package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	fakedisc "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	fakedyn "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const (
	fsGV  = "flowcontrol.apiserver.k8s.io/v1"
	fsOld = "flowcontrol.apiserver.k8s.io/v1beta3"
)

var (
	fsGVR  = schema.GroupVersionResource{Group: "flowcontrol.apiserver.k8s.io", Version: "v1", Resource: "flowschemas"}
	ingGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
)

// cluster is a fake API server: a version plus one FlowSchema written at the
// deprecated v1beta3 (warning at 1.31, blocker from 1.32).
type cluster struct {
	disc *fakedisc.FakeDiscovery
	dyn  *fakedyn.FakeDynamicClient
}

func newCluster(major, minor string) cluster {
	fs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": fsGV, "kind": "FlowSchema", "metadata": map[string]any{"name": "web"}}}
	fs.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: "Update", APIVersion: fsOld}})
	disc := &fakedisc.FakeDiscovery{
		Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{
			{GroupVersion: fsGV, APIResources: []metav1.APIResource{{Name: "flowschemas", Kind: "FlowSchema"}}},
			{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "ingresses", Kind: "Ingress", Namespaced: true}}},
		}},
		FakedServerVersion: &version.Info{Major: major, Minor: minor},
	}
	dyn := fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{fsGVR: "FlowSchemaList", ingGVR: "IngressList"}, fs)
	return cluster{disc: disc, dyn: dyn}
}

func kubeDeps(t *testing.T, c cluster) Deps {
	t.Helper()
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"demo-cluster": "1.30"}}})
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		return c.disc, c.dyn, nil
	}
	return d
}

func lines(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }

func TestScanBlockersExitOne(t *testing.T) {
	code, out, se := run(t, kubeDeps(t, newCluster("1", "31+")), "scan")
	if code != 1 || se != "" {
		t.Fatalf("exit=%d stderr=%q, want 1 and empty", code, se)
	}
	if !strings.Contains(out, "FlowSchema") || !strings.Contains(out, "BLOCKER") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestScanWarningsOnlyExitZero(t *testing.T) {
	code, out, se := run(t, kubeDeps(t, newCluster("1", "30")), "scan")
	if code != 0 || se != "" || !strings.Contains(out, "WARNING") {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, se, out)
	}
}

func TestScanTargetEqualToCurrentAllowed(t *testing.T) {
	code, _, se := run(t, kubeDeps(t, newCluster("1", "31")), "scan", "--target", "1.31")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q, want 0 (1.31 is a warning)", code, se)
	}
}

func TestScanTargetTooFarRejected(t *testing.T) {
	code, _, se := run(t, kubeDeps(t, newCluster("1", "31")), "scan", "--target", "1.33")
	if code != 2 || !strings.Contains(se, "more than one minor") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	assertOneLine(t, se)
}

func TestScanUnreachableServerIsConnectivityError(t *testing.T) {
	c := newCluster("1", "31")
	c.disc.PrependReactor("get", "version", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("dial tcp 10.0.0.1:443: connection refused")
	})
	code, _, se := run(t, kubeDeps(t, c), "scan", "--target", "1.32")
	if code != 2 || !strings.Contains(se, "connection refused") || strings.Contains(se, "--target required") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	assertOneLine(t, se)
}

func TestScanKubeConfigErrorIsOneLine(t *testing.T) {
	d := kubeDeps(t, newCluster("1", "31"))
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		return nil, nil, errors.New("no kubeconfig\nsecond line")
	}
	code, _, se := run(t, d, "scan")
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	assertOneLine(t, se)
}

func TestScanFlagsReachKubeAdapter(t *testing.T) {
	d := kubeDeps(t, newCluster("1", "31"))
	var gotCfg, gotCtx string
	inner := d.Kube
	d.Kube = func(cfg, kctx string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		gotCfg, gotCtx = cfg, kctx
		return inner(cfg, kctx)
	}
	run(t, d, "scan", "--kubeconfig", "/k/config", "--context", "demo-ctx")
	if gotCfg != "/k/config" || gotCtx != "demo-ctx" {
		t.Fatalf("adapter got (%q, %q)", gotCfg, gotCtx)
	}
}

func TestScanClusterFlagWinsOverDiscoveryVersion(t *testing.T) {
	c := newCluster("1", "40") // discovery would say 1.40; EKS says 1.30
	c.disc.PrependReactor("get", "version", func(clienttesting.Action) (bool, runtime.Object, error) {
		t.Error("ServerVersion must not be called with --cluster")
		return false, nil, nil
	})
	code, out, se := run(t, kubeDeps(t, c), "scan", "--cluster", "demo-cluster", "--region", "r1")
	if code != 0 || !strings.Contains(out, "WARNING") {
		t.Fatalf("exit=%d stderr=%q stdout=%q (target should default to 1.31)", code, se, out)
	}
}

func TestScanUnknownClusterExitsTwo(t *testing.T) {
	code, _, se := run(t, kubeDeps(t, newCluster("1", "31")), "scan", "--cluster", "nope", "--region", "r1")
	if code != 2 || !strings.Contains(se, "nope") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	assertOneLine(t, se)
}

func TestScanClusterLookupErrorIsToolError(t *testing.T) {
	d := kubeDeps(t, newCluster("1", "31"))
	d.EKS = func(string, string) (eksinfo.EKSAPI, error) { return fakeEKS{err: authFail()}, nil }
	code, _, se := run(t, d, "scan", "--cluster", "demo-cluster", "--region", "r1")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	assertOneLine(t, se)
}

func TestScanSkippedReportedOnStderrAndJSON(t *testing.T) {
	c := newCluster("1", "31")
	calls := 0
	// Groups are visited in name order: flowcontrol first, then networking.
	c.disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 2 {
			return true, nil, apierrors.NewServiceUnavailable("aggregated api down")
		}
		return false, nil, nil
	})
	code, out, se := run(t, kubeDeps(t, c), "scan", "--output", "json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (blocker scanned)", code)
	}
	if n := len(lines(se)); n != 1 || !strings.HasPrefix(se, "tideclear: warning: skipped group networking.k8s.io") {
		t.Fatalf("stderr = %q", se)
	}
	var doc struct {
		SchemaVersion int `json:"schemaVersion"`
		Skipped       []struct{ Group, Kind, Reason string }
		Findings      []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || len(doc.Skipped) != 1 || doc.Skipped[0].Group != "networking.k8s.io" || len(doc.Findings) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestScanForbiddenKindIsSkippedWarningExitZero(t *testing.T) {
	c := newCluster("1", "30")
	c.dyn.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "ingresses"}, "", errors.New("no"))
	})
	code, _, se := run(t, kubeDeps(t, c), "scan")
	if code != 0 || !strings.HasPrefix(se, "tideclear: warning: skipped kind Ingress") || len(lines(se)) != 1 {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}

func TestScanOtherListErrorExitsTwo(t *testing.T) {
	c := newCluster("1", "31")
	c.dyn.PrependReactor("list", "flowschemas", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errors.New("boom"))
	})
	code, _, se := run(t, kubeDeps(t, c), "scan")
	if code != 2 || !strings.Contains(se, "boom") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	assertOneLine(t, se)
}

func TestScanJSONEmptySkippedIsArray(t *testing.T) {
	code, out, _ := run(t, kubeDeps(t, newCluster("1", "30")), "scan", "--output", "json")
	if code != 0 || !strings.Contains(out, `"skipped": []`) {
		t.Fatalf("exit=%d out=%q", code, out)
	}
}

func TestScanBadOutputAndTargetExitTwo(t *testing.T) {
	for _, args := range [][]string{{"scan", "--output", "xml"}, {"scan", "--target", "banana"}} {
		code, _, se := run(t, kubeDeps(t, newCluster("1", "31")), args...)
		if code != 2 {
			t.Fatalf("%v: exit = %d", args, code)
		}
		assertOneLine(t, se)
	}
}

func TestClustersNeverBuildsKubeClient(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"demo-cluster": "1.35"}}})
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		t.Fatal("clusters must not build a Kubernetes client")
		return nil, nil, nil
	}
	if code, _, se := run(t, d, "clusters"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}
