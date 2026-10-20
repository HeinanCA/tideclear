package main

import (
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	resourcev1alpha3 "k8s.io/api/resource/v1alpha3"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		resourcev1.AddToScheme, resourcev1alpha3.AddToScheme, resourcev1beta1.AddToScheme, storagev1.AddToScheme, storagev1beta1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	return s
}

func TestEntries(t *testing.T) {
	es := entries(testScheme(t))
	by := map[string]entry{}
	for _, e := range es {
		by[e.Version+"|"+e.Kind] = e
		if strings.Contains(e.Version, "alpha") {
			t.Errorf("alpha kept: %+v", e)
		}
		if strings.HasSuffix(e.Kind, "List") {
			t.Errorf("List kind kept: %+v", e)
		}
	}
	dc, ok := by["resource.k8s.io/v1beta1|DeviceClass"]
	if !ok {
		t.Fatalf("DeviceClass missing from %d entries", len(es))
	}
	want := entry{Version: "resource.k8s.io/v1beta1", Kind: "DeviceClass", DeprecatedIn: "v1.35.0", RemovedIn: "v1.38.0", Replacement: "resource.k8s.io/v1", ReplacementIn: "v1.34.0"}
	if dc != want {
		t.Errorf("DeviceClass = %+v, want %+v", dc, want)
	}
	vac := by["storage.k8s.io/v1beta1|VolumeAttributesClass"]
	if vac.RemovedIn != "v1.37.0" || vac.Replacement != "storage.k8s.io/v1" || vac.ReplacementIn != "v1.34.0" {
		t.Errorf("VolumeAttributesClass = %+v", vac)
	}
	for i := 1; i < len(es); i++ {
		a, b := es[i-1], es[i]
		if a.Version > b.Version || (a.Version == b.Version && a.Kind >= b.Kind) {
			t.Errorf("not sorted at %d: %+v then %+v", i, a, b)
		}
	}
}

func TestRender(t *testing.T) {
	out := string(render([]entry{{Version: "a/v1beta1", Kind: "K", DeprecatedIn: "v1.30.0", RemovedIn: "v1.37.0"}}, "v0.37.1"))
	for _, want := range []string{
		"# Generated from k8s.io/api v0.37.1 (Kubernetes 1.37); DO NOT EDIT.",
		"deprecated-versions:",
		"  - version: a/v1beta1",
		"    kind: K",
		"    deprecated-in: v1.30.0",
		"    removed-in: v1.37.0",
		"    replacement-api: \"\"",
		"    component: k8s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

// Beta kinds with no APILifecycleReplacement fall back to the same kind at the stable version
// when it is registered; never to another beta, and empty when no stable version exists.
func TestEntriesStableFallback(t *testing.T) {
	betaOnly := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{resourcev1beta1.AddToScheme, resourcev1beta2.AddToScheme} {
		if err := add(betaOnly); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	for _, e := range entries(betaOnly) {
		if e.Kind == "DeviceClass" && e.Replacement != "" {
			t.Errorf("no stable version registered: %s DeviceClass replacement = %q, want empty", e.Version, e.Replacement)
		}
	}
	for _, e := range entries(testScheme(t)) {
		if e.Version == "resource.k8s.io/v1beta1" && e.Kind == "ResourceSlice" && e.Replacement != "resource.k8s.io/v1" {
			t.Errorf("ResourceSlice replacement = %q, want resource.k8s.io/v1", e.Replacement)
		}
	}
}
