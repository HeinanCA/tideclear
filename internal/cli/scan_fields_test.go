package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakedisc "k8s.io/client-go/discovery/fake"
	fakedyn "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var svcGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}

// serviceCluster is a fake API server holding two Services, one with externalIPs.
func serviceCluster(minor string) cluster {
	mk := func(name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": name, "namespace": "demo"}, "spec": spec}}
	}
	disc := &fakedisc.FakeDiscovery{
		Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{
			{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "services", Kind: "Service", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}}}},
		}},
		FakedServerVersion: &version.Info{Major: "1", Minor: minor},
	}
	dyn := fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{svcGVR: "ServiceList", nodeGVR: "NodeList"},
		mk("web", map[string]any{"externalIPs": []any{"192.0.2.10"}}), mk("idle", map[string]any{"externalIPs": []any{}}))
	c := cluster{disc: disc, dyn: dyn}
	answerSSAR(c)
	return c
}

func TestScanLiveFieldFindings(t *testing.T) {
	tests := []struct {
		name, minor string
		wantRow     bool
	}{
		{"1.35 to 1.36 warns", "35", true},
		{"1.34 to 1.35 is clean", "34", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, se := run(t, kubeDeps(t, serviceCluster(tc.minor)), "scan")
			if code != 0 || se != "" {
				t.Fatalf("exit=%d stderr=%q, want 0 and empty (a field warning is not a blocker)", code, se)
			}
			if got := strings.Contains(out, "spec.externalIPs"); got != tc.wantRow {
				t.Fatalf("row present = %v, want %v:\n%s", got, tc.wantRow, out)
			}
			if tc.wantRow && (!strings.Contains(out, "demo") || !strings.Contains(out, "web") || strings.Contains(out, "idle") || !strings.Contains(out, "WARNING")) {
				t.Fatalf("stdout = %s", out)
			}
		})
	}
}

func TestScanLiveFieldFindingsJSON(t *testing.T) {
	code, out, _ := run(t, kubeDeps(t, serviceCluster("35")), "scan", "--output", "json")
	var doc struct {
		FieldFindings []struct{ Kind, Name, Field, Severity string } `json:"fieldFindings"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 {
		t.Fatalf("exit=%d err=%v out=%s", code, err, out)
	}
	if len(doc.FieldFindings) != 1 || doc.FieldFindings[0].Name != "web" || doc.FieldFindings[0].Field != "spec.externalIPs" || doc.FieldFindings[0].Severity != "WARNING" {
		t.Fatalf("fieldFindings = %+v", doc.FieldFindings)
	}
}

func TestScanPreflightChecksListServices(t *testing.T) {
	c := serviceCluster("35")
	answerSSAR(c, "services")
	code, _, se := run(t, kubeDeps(t, c), "scan")
	if code != 0 || !strings.Contains(se, "missing permissions: list services") {
		t.Fatalf("exit=%d stderr=%q, want a missing-permissions warning naming list services", code, se)
	}
}

func TestScanPathFieldFindings(t *testing.T) {
	tests := []struct {
		target  string
		wantRow bool
	}{{"1.36", true}, {"1.35", false}}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			d, _ := pathDeps(t)
			code, out, se := run(t, d, "scan", "--path", manifests("fields"), "--target", tc.target)
			if code != 0 || se != "" {
				t.Fatalf("exit=%d stderr=%q", code, se)
			}
			if got := strings.Contains(out, "spec.externalIPs"); got != tc.wantRow {
				t.Fatalf("row present = %v, want %v:\n%s", got, tc.wantRow, out)
			}
			if tc.wantRow && (!strings.Contains(out, "service.yaml:0") || strings.Contains(out, "idle")) {
				t.Fatalf("stdout = %s", out)
			}
		})
	}
}

func TestExitForFieldFindingSeverity(t *testing.T) {
	rem := rules.Version{Major: 1, Minor: 38}
	r := rules.FieldRule{Kind: "Service", Path: "spec.x", DeprecatedIn: rules.Version{Major: 1, Minor: 36}, RemovedIn: &rem}
	for _, tc := range []struct {
		sev     rules.Severity
		blocker bool
	}{{rules.SeverityWarning, false}, {rules.SeverityBlocker, true}} {
		res := apiscan.Result{FieldFindings: []apiscan.FieldFinding{{Hit: apiscan.FieldHit{Rule: r}, Severity: tc.sev}}}
		if got := exitFor(res, report.Extras{}, false) != nil; got != tc.blocker {
			t.Errorf("%s: blocker exit = %v, want %v", tc.sev, got, tc.blocker)
		}
	}
}

func TestScanStrictCountsFieldWarnings(t *testing.T) {
	code, _, se := run(t, kubeDeps(t, serviceCluster("35")), "scan", "--strict")
	if code != 1 || !strings.HasSuffix(se, "tideclear: --strict: 1 stale, unknown or field-warning item counts as a blocker\n") {
		t.Fatalf("exit=%d stderr=%q, want 1 and the strict count line", code, se)
	}
	if code, _, se := run(t, kubeDeps(t, serviceCluster("35")), "scan"); code != 0 || se != "" {
		t.Fatalf("without --strict: exit=%d stderr=%q, want 0 and empty", code, se)
	}
	if code, _, se := run(t, kubeDeps(t, serviceCluster("34")), "scan", "--strict"); code != 0 || se != "" {
		t.Fatalf("no warning with --strict: exit=%d stderr=%q, want 0 and empty", code, se)
	}
}

func TestScanHelpStrictMentionsFieldWarnings(t *testing.T) {
	_, out, _ := run(t, kubeDeps(t, serviceCluster("35")), "scan", "--help")
	if !strings.Contains(out, "deprecated-field WARNING") {
		t.Fatalf("--strict help does not mention field warnings:\n%s", out)
	}
}
