package rules

import (
	"strings"
	"testing"
)

func TestParseStrictInputs(t *testing.T) {
	ok := "  - version: policy/v1beta1\n    kind: PodDisruptionBudget\n    deprecated-in: v1.21.0\n    removed-in: v1.25.0\n    component: k8s\n"
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"valid", "deprecated-versions:\n" + ok, ""},
		{"valid with target-versions section", "target-versions:\n  cert-manager: v1.1.0\ndeprecated-versions:\n" + ok, ""},
		{"mistyped top-level key", "deprecated-version:\n" + ok, "deprecated-version"},
		{"mistyped field", "deprecated-versions:\n" + strings.Replace(ok, "removed-in", "removed_in", 1), "removed_in"},
		{"empty input", "", "no k8s rules"},
		{"only non-k8s rules", "deprecated-versions:\n" + strings.Replace(ok, "component: k8s", "component: istio", 1), "no k8s rules"},
		{"empty version", "deprecated-versions:\n" + strings.Replace(ok, "version: policy/v1beta1", "version: \"\"", 1), "version"},
		{"empty kind", "deprecated-versions:\n" + strings.Replace(ok, "kind: PodDisruptionBudget", "kind: \"\"", 1), "kind"},
		{"neither deprecated nor removed", "deprecated-versions:\n  - version: a/v1\n    kind: K\n    component: k8s\n", "deprecated-in"},
		{"removed before deprecated", "deprecated-versions:\n" + strings.Replace(ok, "removed-in: v1.25.0", "removed-in: v1.20.0", 1), "removed-in"},
		{"bad version names field", "deprecated-versions:\n" + strings.Replace(ok, "removed-in: v1.25.0", "removed-in: v1.x.0", 1), "removed-in"},
	}
	for _, tc := range tests {
		_, err := parse([]byte(tc.in))
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && err == nil:
			t.Errorf("%s: want error containing %q, got nil", tc.name, tc.wantErr)
		case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
			t.Errorf("%s: error %q does not contain %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestMatchPicksMostSevere(t *testing.T) {
	warn := Rule{APIVersion: "a/v1beta1", Kind: "K", DeprecatedIn: &Version{1, 20}, Replacement: "warn-rule"}
	block := Rule{APIVersion: "a/v1beta1", Kind: "K", DeprecatedIn: &Version{1, 20}, RemovedIn: &Version{1, 25}, Replacement: "block-rule"}
	for _, order := range [][]Rule{{warn, block}, {block, warn}} {
		s := &Set{rules: order}
		f, ok := s.Match("a/v1beta1", "K", Version{1, 26})
		if !ok || f.Severity != SeverityBlocker || f.Rule.Replacement != "block-rule" {
			t.Errorf("order %v: got ok=%v %+v, want blocker block-rule", order[0].Replacement, ok, f)
		}
	}
}

func TestMergeLifecycleWinsForK8s(t *testing.T) {
	pluto := []Rule{
		{APIVersion: "resource.k8s.io/v1beta1", Kind: "DeviceClass", RemovedIn: &Version{1, 36}, Component: "k8s", Source: SourcePluto},
		{APIVersion: "policy/v1beta1", Kind: "PodDisruptionBudget", RemovedIn: &Version{1, 25}, Component: "k8s", Source: SourcePluto},
	}
	life := []Rule{
		{APIVersion: "resource.k8s.io/v1beta1", Kind: "DeviceClass", RemovedIn: &Version{1, 38}, Component: "k8s", Source: SourceK8sLifecycle},
		{APIVersion: "resource.k8s.io/v1beta1", Kind: "DeviceClass", RemovedIn: &Version{1, 38}, Component: "k8s", Source: SourceK8sLifecycle},
	}
	got := merge(pluto, life)
	if len(got) != 2 {
		t.Fatalf("merge len = %d, want 2 (one per key): %+v", len(got), got)
	}
	for _, r := range got {
		if r.Kind == "DeviceClass" && (r.Source != SourceK8sLifecycle || *r.RemovedIn != (Version{1, 38})) {
			t.Errorf("DeviceClass not won by lifecycle: %+v", r)
		}
		if r.Kind == "PodDisruptionBudget" && r.Source != SourcePluto {
			t.Errorf("PDB source = %q, want pluto", r.Source)
		}
	}
}

func TestLoadMergedLifecycle(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v, err := LifecycleSourceVersion(); err != nil || v != (Version{1, 37}) {
		t.Fatalf("LifecycleSourceVersion = %v, %v, want 1.37", v, err)
	}
	f, ok := s.Match("resource.k8s.io/v1beta1", "DeviceClass", Version{1, 37})
	if !ok || f.Severity != SeverityWarning || f.Rule.Source != SourceK8sLifecycle {
		t.Errorf("DeviceClass at 1.37: ok=%v %+v, want lifecycle warning (Pluto said removed 1.36)", ok, f)
	}
	if !f.Rule.Projected() {
		t.Errorf("DeviceClass removal 1.38 should be Projected")
	}
	// Removals after the bundled Kubernetes release are projections: capped at WARNING.
	f, ok = s.Match("resource.k8s.io/v1beta1", "DeviceClass", Version{1, 38})
	if !ok || f.Severity != SeverityWarning {
		t.Errorf("DeviceClass at 1.38: ok=%v %+v, want projected warning", ok, f)
	}
	f, ok = s.Match("admissionregistration.k8s.io/v1beta1", "MutatingAdmissionPolicy", Version{1, 40})
	if !ok || f.Severity != SeverityWarning {
		t.Errorf("MAP at 1.40: ok=%v %+v, want projected warning", ok, f)
	}
	// A removal already shipped in the bundled release stays a blocker.
	f, ok = s.Match("networking.k8s.io/v1beta1", "IPAddress", Version{1, 37})
	if !ok || f.Severity != SeverityBlocker || f.Rule.Projected() {
		t.Errorf("IPAddress at 1.37: ok=%v %+v, want released blocker", ok, f)
	}
	f, _ = s.Match("policy/v1beta1", "PodDisruptionBudget", Version{1, 25})
	if f.Rule.Projected() {
		t.Errorf("PDB removed 1.25 must not be Projected")
	}
	seen := map[string]bool{}
	for _, r := range s.rules {
		k := r.APIVersion + "|" + r.Kind
		if seen[k] {
			t.Errorf("duplicate key after merge: %s", k)
		}
		seen[k] = true
	}
}

func TestLifecycleHeaderVersion(t *testing.T) {
	v, err := lifecycleHeaderVersion([]byte("# Generated from k8s.io/api v0.37.1 (Kubernetes 1.37); DO NOT EDIT.\n"))
	if err != nil || v != (Version{1, 37}) {
		t.Errorf("got %v, %v want 1.37", v, err)
	}
	if _, err := lifecycleHeaderVersion([]byte("deprecated-versions:\n")); err == nil {
		t.Error("missing header: want error")
	}
}
