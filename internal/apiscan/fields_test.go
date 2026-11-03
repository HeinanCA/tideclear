package apiscan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/rules"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedisc "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"
)

var (
	t135 = rules.Version{Major: 1, Minor: 35}
	t136 = rules.Version{Major: 1, Minor: 36}

	svcGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}
)

func svc(name, ns string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     spec,
	}}
}

func TestFieldHitsPresence(t *testing.T) {
	rs := []rules.FieldRule{{Kind: "Service", Path: "spec.externalIPs", DeprecatedIn: t136}}
	cases := []struct {
		name  string
		group string
		kind  string
		obj   map[string]any
		want  int
	}{
		{"set", "", "Service", map[string]any{"spec": map[string]any{"externalIPs": []any{"192.0.2.1"}}}, 1},
		{"empty list", "", "Service", map[string]any{"spec": map[string]any{"externalIPs": []any{}}}, 0},
		{"null", "", "Service", map[string]any{"spec": map[string]any{"externalIPs": nil}}, 0},
		{"empty string", "", "Service", map[string]any{"spec": map[string]any{"externalIPs": ""}}, 0},
		{"empty map", "", "Service", map[string]any{"spec": map[string]any{"externalIPs": map[string]any{}}}, 0},
		{"absent", "", "Service", map[string]any{"spec": map[string]any{"type": "ClusterIP"}}, 0},
		{"no spec", "", "Service", map[string]any{}, 0},
		{"spec not a map", "", "Service", map[string]any{"spec": "oops"}, 0},
		{"other kind", "", "ConfigMap", map[string]any{"spec": map[string]any{"externalIPs": []any{"x"}}}, 0},
		{"other group", "example.com", "Service", map[string]any{"spec": map[string]any{"externalIPs": []any{"x"}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := fieldHits(rs, tc.group, tc.kind, "ns", "n", "o", tc.obj)
			if len(got) != tc.want {
				t.Fatalf("hits = %+v, want %d", got, tc.want)
			}
			if tc.want == 1 && (got[0].Namespace != "ns" || got[0].Name != "n" || got[0].Origin != "o" || got[0].Rule.Path != "spec.externalIPs") {
				t.Fatalf("hit = %+v", got[0])
			}
		})
	}
}

func serviceCluster(set *rules.Set, objs ...*unstructured.Unstructured) Source {
	disc := newDisc(resList("v1", map[string]string{"services": "Service"}))
	var ro []runtime.Object
	for _, o := range objs {
		ro = append(ro, o)
	}
	dyn := newDyn(map[schema.GroupVersionResource]string{svcGVR: "ServiceList"}, ro...)
	return NewClusterSource(disc, dyn, set)
}

func TestLiveScanFieldFindings(t *testing.T) {
	cases := []struct {
		name   string
		target rules.Version
		spec   map[string]any
		want   []string
	}{
		{"1.36 warns", t136, map[string]any{"externalIPs": []any{"192.0.2.1"}}, []string{"demo/web WARNING spec.externalIPs"}},
		{"1.35 silent", t135, map[string]any{"externalIPs": []any{"192.0.2.1"}}, nil},
		{"empty list silent", t136, map[string]any{"externalIPs": []any{}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := mustSet(t)
			src := serviceCluster(set, svc("web", "demo", tc.spec), svc("clean", "demo", map[string]any{"type": "ClusterIP"}))
			res, err := Scan(context.Background(), src, set, tc.target)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range res.FieldFindings {
				got = append(got, f.Hit.Namespace+"/"+f.Hit.Name+" "+string(f.Severity)+" "+f.Hit.Rule.Path)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
			if len(res.Findings) != 0 || len(res.Live) != 0 || res.Unverified != 0 {
				t.Errorf("field-only kind leaked into apiVersion results: findings=%v live=%v unverified=%d", res.Findings, res.Live, res.Unverified)
			}
			if !reflect.DeepEqual(res.Listed, []Listed{{Resource: "services", Kind: "Service"}}) {
				t.Errorf("listed = %+v", res.Listed)
			}
		})
	}
}

func TestLiveScanFieldFindingsAreSorted(t *testing.T) {
	set := mustSet(t)
	ips := map[string]any{"externalIPs": []any{"192.0.2.1"}}
	src := serviceCluster(set, svc("b", "z", ips), svc("a", "z", ips), svc("c", "a", ips))
	res, err := Scan(context.Background(), src, set, t136)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range res.FieldFindings {
		got = append(got, f.Hit.Namespace+"/"+f.Hit.Name)
	}
	if want := []string{"a/c", "z/a", "z/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestPreflightIncludesListServices(t *testing.T) {
	disc := newDisc(resList("v1", map[string]string{"services": "Service"}))
	got, _, err := neededPermissions(context.Background(), disc, mustSet(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []Permission{{"", "nodes", "list"}, {"", "services", "list"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("needed = %+v, want %+v", got, want)
	}
}

func TestManifestFieldFindings(t *testing.T) {
	dir := t.TempDir()
	doc := "apiVersion: v1\nkind: Service\nmetadata:\n  name: web\n  namespace: demo\nspec:\n  externalIPs:\n    - 192.0.2.1\n---\n" +
		"apiVersion: v1\nkind: Service\nmetadata:\n  name: empty\nspec:\n  externalIPs: []\n---\n" +
		"apiVersion: v1\nkind: List\nitems:\n  - apiVersion: v1\n    kind: Service\n    metadata:\n      name: listed\n    spec:\n      externalIPs: [\"192.0.2.2\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "svc.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	set := mustSet(t)
	for _, tc := range []struct {
		target rules.Version
		want   []string
	}{
		{t135, nil},
		{t136, []string{"-/listed WARNING svc.yaml:2/0", "demo/web WARNING svc.yaml:0"}},
	} {
		var warn bytes.Buffer
		res, err := Scan(context.Background(), NewManifestSourceWithRules(dir, &warn, set), set, tc.target)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range res.FieldFindings {
			ns := f.Hit.Namespace
			if ns == "" {
				ns = "-"
			}
			got = append(got, ns+"/"+f.Hit.Name+" "+string(f.Severity)+" "+f.Hit.Origin)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("target %s: findings = %v, want %v", tc.target, got, tc.want)
		}
	}
	// Without rules the plain constructor reports no fields and still scans.
	res, err := Scan(context.Background(), NewManifestSource(dir, &bytes.Buffer{}), set, t136)
	if err != nil || len(res.FieldFindings) != 0 {
		t.Fatalf("plain source: %v %v", res.FieldFindings, err)
	}
}

func TestFieldHitsReportsUncheckableObject(t *testing.T) {
	rs := []rules.FieldRule{{Kind: "Service", Path: "spec.externalIPs", DeprecatedIn: t136}}
	hits, unchecked := fieldHits(rs, "", "Service", "demo", "web", "svc.yaml:0", map[string]any{"spec": "oops"})
	if len(hits) != 0 || len(unchecked) != 1 {
		t.Fatalf("hits=%v unchecked=%v, want 0 hits and 1 note", hits, unchecked)
	}
	for _, want := range []string{"svc.yaml:0", "Service demo/web", "spec.externalIPs", "not checked"} {
		if !strings.Contains(unchecked[0], want) {
			t.Errorf("note %q lacks %q", unchecked[0], want)
		}
	}
}

func TestManifestMalformedFieldPathIsSkippedNotClean(t *testing.T) {
	dir := t.TempDir()
	doc := "apiVersion: v1\nkind: Service\nmetadata:\n  name: bad\n  namespace: demo\nspec: oops\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	set := mustSet(t)
	var warn bytes.Buffer
	res, err := Scan(context.Background(), NewManifestSourceWithRules(dir, &warn, set), set, t136)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.FieldFindings) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "Service demo/bad") ||
		!strings.Contains(warn.String(), "not checked") {
		t.Fatalf("findings=%v skipped=%+v warn=%q", res.FieldFindings, res.Skipped, warn.String())
	}
}

func TestMissingFieldRuleGroupIsSkipped(t *testing.T) {
	set := mustSet(t)
	// Built bare: newDisc would add the core group.
	disc := &fakedisc.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{resList(ingNew, map[string]string{"ingresses": "Ingress"})}}}
	dyn := newDyn(map[schema.GroupVersionResource]string{ingGVR: "IngressList"})
	res, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, t136)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range res.Skipped {
		if s.Group == "" && s.Kind == "" && strings.Contains(s.Reason, "not served") {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v, want a core-group entry saying it is not served", res.Skipped)
	}
}
