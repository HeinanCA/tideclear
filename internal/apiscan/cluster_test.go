package apiscan

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/rules"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakedisc "k8s.io/client-go/discovery/fake"
	fakedyn "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const (
	ingNew = "networking.k8s.io/v1"
	ingOld = "networking.k8s.io/v1beta1"
)

var (
	ingGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	extGVR = schema.GroupVersionResource{Group: "extensions", Version: "v1beta1", Resource: "ingresses"}
	fsGVR  = schema.GroupVersionResource{Group: "flowcontrol.apiserver.k8s.io", Version: "v1", Resource: "flowschemas"}
)

func mustSet(t *testing.T) *rules.Set {
	t.Helper()
	s, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ver(major, minor int) rules.Version { return rules.Version{Major: major, Minor: minor} }

func resList(gv string, kinds map[string]string) *metav1.APIResourceList {
	l := &metav1.APIResourceList{GroupVersion: gv}
	for name, kind := range kinds {
		l.APIResources = append(l.APIResources,
			metav1.APIResource{Name: name, Kind: kind, Namespaced: kind != "FlowSchema"},
			metav1.APIResource{Name: name + "/status", Kind: kind})
	}
	return l
}

func newDisc(lists ...*metav1.APIResourceList) *fakedisc.FakeDiscovery {
	return &fakedisc.FakeDiscovery{Fake: &clienttesting.Fake{Resources: lists}}
}

func newDyn(gvrs map[schema.GroupVersionResource]string, objs ...runtime.Object) *fakedyn.FakeDynamicClient {
	return fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, objs...)
}

// mf is one managedFields entry.
type mf struct {
	apiVersion, manager, sub string
	at                       *time.Time
}

func at(min int) *time.Time {
	t := time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC)
	return &t
}

// ingress builds an Ingress served at the preferred version, carrying
// evidence of the apiVersion it was written with.
func ingress(name, lastApplied string, entries ...mf) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": ingNew, "kind": "Ingress",
		"metadata": map[string]any{"name": name, "namespace": "demo"},
	}}
	if lastApplied != "" {
		u.SetAnnotations(map[string]string{
			"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"` + lastApplied + `","kind":"Ingress"}`,
		})
	}
	var m []metav1.ManagedFieldsEntry
	for _, e := range entries {
		ent := metav1.ManagedFieldsEntry{Manager: e.manager, Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: e.apiVersion, Subresource: e.sub}
		if e.at != nil {
			ent.Time = &metav1.Time{Time: *e.at}
		}
		m = append(m, ent)
	}
	u.SetManagedFields(m)
	return u
}

func ingressCluster(objs ...runtime.Object) (*fakedisc.FakeDiscovery, *fakedyn.FakeDynamicClient) {
	return newDisc(resList(ingNew, map[string]string{"ingresses": "Ingress"})),
		newDyn(map[schema.GroupVersionResource]string{ingGVR: "IngressList"}, objs...)
}

func scan(t *testing.T, disc *fakedisc.FakeDiscovery, dyn *fakedyn.FakeDynamicClient, target rules.Version) Result {
	t.Helper()
	set := mustSet(t)
	res, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, target)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEvidenceRule(t *testing.T) {
	cases := []struct {
		name       string
		obj        *unstructured.Unstructured
		wantOrigin string // "" = clean
		wantAPIVer string
	}{
		{"newest main entry deprecated", ingress("web", "", mf{ingOld, "a", "", at(5)}), "managedFields", ingOld},
		{"old main deprecated, newer status entry current",
			ingress("web", "", mf{ingOld, "a", "", at(1)}, mf{ingNew, "ctl", "status", at(9)}), "managedFields", ingOld},
		{"stale deprecated from other manager older than current",
			ingress("web", "", mf{ingOld, "b", "", at(1)}, mf{ingNew, "a", "", at(9)}), "", ""},
		{"tie of newest, one deprecated",
			ingress("web", "", mf{ingNew, "a", "", at(5)}, mf{ingOld, "b", "", at(5)}), "managedFields", ingOld},
		{"nil time is oldest",
			ingress("web", "", mf{ingOld, "a", "", nil}, mf{ingNew, "b", "", at(1)}), "", ""},
		{"nil time loses to a dated deprecated entry",
			ingress("web", "", mf{ingNew, "a", "", nil}, mf{ingOld, "b", "", at(1)}), "managedFields", ingOld},
		{"last-applied deprecated, managedFields current",
			ingress("web", ingOld, mf{ingNew, "a", "", at(5)}), "last-applied", ingOld},
		{"no evidence", ingress("web", ""), "", ""},
		{"fixed: both current", ingress("web", ingNew, mf{ingNew, "a", "", at(5)}), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			disc, dyn := ingressCluster(tc.obj)
			res := scan(t, disc, dyn, ver(1, 31))
			if tc.wantOrigin == "" {
				if len(res.Findings) != 0 {
					t.Fatalf("want clean, got %+v", res.Findings)
				}
				return
			}
			if len(res.Findings) != 1 {
				t.Fatalf("findings = %+v, want 1", res.Findings)
			}
			o := res.Findings[0].Object
			want := Object{APIVersion: tc.wantAPIVer, Kind: "Ingress", Namespace: "demo", Name: "web", Origin: tc.wantOrigin}
			if o != want {
				t.Fatalf("object = %+v, want %+v", o, want)
			}
		})
	}
}

func TestBothEvidenceSourcesReportOncePerAPIVersion(t *testing.T) {
	disc, dyn := ingressCluster(ingress("web", ingOld, mf{ingOld, "a", "", at(5)}))
	if res := scan(t, disc, dyn, ver(1, 31)); len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want 1 (distinct apiVersion)", res.Findings)
	}
	disc, dyn = ingressCluster(ingress("web", "extensions/v1beta1", mf{ingOld, "a", "", at(5)}))
	if res := scan(t, disc, dyn, ver(1, 31)); len(res.Findings) != 2 {
		t.Fatalf("findings = %+v, want 2 (two distinct apiVersions)", res.Findings)
	}
}

func TestTargetControlsSeverity(t *testing.T) {
	disc := newDisc(resList("flowcontrol.apiserver.k8s.io/v1", map[string]string{"flowschemas": "FlowSchema"}))
	fs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flowcontrol.apiserver.k8s.io/v1", "kind": "FlowSchema",
		"metadata": map[string]any{"name": "web"},
	}}
	fs.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "a", Operation: "Update", APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Time: &metav1.Time{Time: *at(1)}}})
	dyn := newDyn(map[schema.GroupVersionResource]string{fsGVR: "FlowSchemaList"}, fs)

	if res := scan(t, disc, dyn, ver(1, 31)); len(res.Findings) != 1 || res.Findings[0].Finding.Severity != rules.SeverityWarning {
		t.Fatalf("1.31: %+v, want one WARNING", res.Findings)
	}
	if res := scan(t, disc, dyn, ver(1, 32)); len(res.Findings) != 1 || res.Findings[0].Finding.Severity != rules.SeverityBlocker {
		t.Fatalf("1.32: %+v, want one BLOCKER", res.Findings)
	}
}

func TestSharedKindMapsToItsOwnGroup(t *testing.T) {
	disc := newDisc(
		resList("extensions/v1beta1", map[string]string{"ingresses": "Ingress"}),
		resList(ingNew, map[string]string{"ingresses": "Ingress"}))
	ext := ingress("legacy", "", mf{"extensions/v1beta1", "a", "", at(1)})
	ext.SetAPIVersion("extensions/v1beta1")
	dyn := newDyn(map[schema.GroupVersionResource]string{extGVR: "IngressList", ingGVR: "IngressList"},
		ext, ingress("modern", "", mf{ingOld, "a", "", at(1)}))
	res := scan(t, disc, dyn, ver(1, 31))
	got := map[string]string{}
	for _, f := range res.Findings {
		got[f.Object.Name] = f.Object.APIVersion
	}
	want := map[string]string{"legacy": "extensions/v1beta1", "modern": ingOld}
	if len(got) != 2 || got["legacy"] != want["legacy"] || got["modern"] != want["modern"] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUnknownKindsAndGroupsAreNeverListed(t *testing.T) {
	disc := newDisc(
		resList("example.dev/v1", map[string]string{"widgets": "Widget"}),
		resList(ingNew, map[string]string{"ingresses": "Ingress", "frobs": "Frob"}))
	_, dyn := ingressCluster(ingress("web", ""))
	scan(t, disc, dyn, ver(1, 31))
	for _, a := range dyn.Actions() {
		if r := a.GetResource().Resource; r != "ingresses" {
			t.Errorf("listed unexpected resource %q", r)
		}
	}
	if len(dyn.Actions()) == 0 {
		t.Fatal("ingresses were never listed")
	}
}

func TestOnlyReadVerbs(t *testing.T) {
	disc, dyn := ingressCluster(ingress("web", ingOld))
	scan(t, disc, dyn, ver(1, 31))
	if len(dyn.Actions()) == 0 {
		t.Fatal("no actions recorded")
	}
	for _, a := range dyn.Actions() {
		if v := a.GetVerb(); v != "list" && v != "get" {
			t.Errorf("write verb used: %s %s", v, a.GetResource().Resource)
		}
	}
}

func TestBrokenGroupIsSkippedOthersScanned(t *testing.T) {
	disc := newDisc(
		resList("extensions/v1beta1", map[string]string{"ingresses": "Ingress"}),
		resList(ingNew, map[string]string{"ingresses": "Ingress"}))
	calls := 0
	// Groups are visited in name order, so the first call is "extensions".
	disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewServiceUnavailable("aggregated api down")
		}
		return false, nil, nil
	})
	dyn := newDyn(map[schema.GroupVersionResource]string{ingGVR: "IngressList"}, ingress("web", "", mf{ingOld, "a", "", at(1)}))
	res := scan(t, disc, dyn, ver(1, 31))
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want the surviving group's one finding", res.Findings)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Group != "extensions" || res.Skipped[0].Kind != "" ||
		!strings.Contains(res.Skipped[0].Reason, "aggregated api down") {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

func TestForbiddenListSkipsThatKindOnly(t *testing.T) {
	disc := newDisc(
		resList(ingNew, map[string]string{"ingresses": "Ingress"}),
		resList("flowcontrol.apiserver.k8s.io/v1", map[string]string{"flowschemas": "FlowSchema"}))
	fs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flowcontrol.apiserver.k8s.io/v1", "kind": "FlowSchema", "metadata": map[string]any{"name": "web"}}}
	fs.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "a", Operation: "Update", APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Time: &metav1.Time{Time: *at(1)}}})
	dyn := newDyn(map[schema.GroupVersionResource]string{ingGVR: "IngressList", fsGVR: "FlowSchemaList"}, fs)
	dyn.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"}, "", errors.New("no list"))
	})
	res := scan(t, disc, dyn, ver(1, 32))
	if len(res.Findings) != 1 || res.Findings[0].Object.Kind != "FlowSchema" {
		t.Fatalf("findings = %+v, want the FlowSchema only", res.Findings)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Group != "networking.k8s.io" || res.Skipped[0].Kind != "Ingress" {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

func TestOtherErrorsAreFatal(t *testing.T) {
	t.Run("non-403 list error", func(t *testing.T) {
		disc, dyn := ingressCluster(ingress("web", ""))
		dyn.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("boom"))
		})
		set := mustSet(t)
		res, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, ver(1, 31))
		if err == nil || !strings.Contains(err.Error(), "boom") || len(res.Skipped) != 0 {
			t.Fatalf("err=%v skipped=%+v", err, res.Skipped)
		}
	})
	t.Run("ServerGroups failure", func(t *testing.T) {
		disc, dyn := ingressCluster()
		disc.PrependReactor("get", "group", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("connection refused")
		})
		set := mustSet(t)
		res, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, ver(1, 31))
		if err == nil || !strings.Contains(err.Error(), "connection refused") || len(res.Skipped) != 0 {
			t.Fatalf("err=%v skipped=%+v", err, res.Skipped)
		}
	})
}

func TestServerVersion(t *testing.T) {
	cases := []struct {
		info    version.Info
		want    rules.Version
		wantErr bool
	}{
		{version.Info{Major: "1", Minor: "31+"}, ver(1, 31), false},
		{version.Info{Major: "1", Minor: "35"}, ver(1, 35), false},
		{version.Info{Major: "x", Minor: "?"}, rules.Version{}, true},
	}
	for _, tc := range cases {
		disc, dyn := ingressCluster()
		info := tc.info
		disc.FakedServerVersion = &info
		got, known, err := NewClusterSource(disc, dyn, mustSet(t)).ServerVersion(context.Background())
		if (err != nil) != tc.wantErr || (err == nil && (!known || got != tc.want)) {
			t.Errorf("%+v: got %v known=%v err=%v", tc.info, got, known, err)
		}
	}
}

func TestServerVersionUnreachableIsError(t *testing.T) {
	disc, dyn := ingressCluster()
	disc.PrependReactor("get", "version", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusBadGateway, Message: "bad gateway"}}
	})
	if _, _, err := NewClusterSource(disc, dyn, mustSet(t)).ServerVersion(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
