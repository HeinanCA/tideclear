package rules

import "testing"

const miniRules = `
deprecated-versions:
  - version: policy/v1beta1
    kind: PodDisruptionBudget
    deprecated-in: v1.21.0
    removed-in: v1.25.0
    replacement-api: policy/v1
    replacement-available-in: v1.21.0
    component: k8s
  - version: autoscaling/v2beta2
    kind: HorizontalPodAutoscaler
    deprecated-in: v1.23.0
    removed-in: v1.26.0
    replacement-api: autoscaling/v2
    replacement-available-in: v1.23.0
    component: k8s
  - version: storage.k8s.io/v1beta1
    kind: VolumeAttributesClass
    deprecated-in: v1.28.0
    removed-in: ""
    replacement-api: storage.k8s.io/v1
    component: k8s
  - version: cert-manager.io/v1alpha2
    kind: Certificate
    deprecated-in: v1.0.0
    removed-in: v1.1.0
    component: cert-manager
`

func mini(t *testing.T) *Set {
	t.Helper()
	s, err := parse([]byte(miniRules))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func TestMatch(t *testing.T) {
	s := mini(t)
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		target     Version
		wantOK     bool
		wantSev    Severity
	}{
		{"removed at target", "policy/v1beta1", "PodDisruptionBudget", Version{1, 25}, true, SeverityBlocker},
		{"removed before target", "policy/v1beta1", "PodDisruptionBudget", Version{1, 29}, true, SeverityBlocker},
		{"deprecated but served 1.24", "policy/v1beta1", "PodDisruptionBudget", Version{1, 24}, true, SeverityWarning},
		{"not yet deprecated", "policy/v1beta1", "PodDisruptionBudget", Version{1, 20}, false, ""},
		{"hpa warning at 1.25", "autoscaling/v2beta2", "HorizontalPodAutoscaler", Version{1, 25}, true, SeverityWarning},
		{"hpa blocker at 1.26", "autoscaling/v2beta2", "HorizontalPodAutoscaler", Version{1, 26}, true, SeverityBlocker},
		{"clean apps/v1 Deployment", "apps/v1", "Deployment", Version{1, 29}, false, ""},
		{"unknown kind", "policy/v1beta1", "Nope", Version{1, 29}, false, ""},
		{"empty removed-in before deprecation", "storage.k8s.io/v1beta1", "VolumeAttributesClass", Version{1, 26}, false, ""},
		{"empty removed-in deprecated is warning", "storage.k8s.io/v1beta1", "VolumeAttributesClass", Version{1, 29}, true, SeverityWarning},
		{"non-k8s component ignored", "cert-manager.io/v1alpha2", "Certificate", Version{1, 29}, false, ""},
	}
	for _, tc := range tests {
		got, ok := s.Match(tc.apiVersion, tc.kind, tc.target)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if ok && got.Severity != tc.wantSev {
			t.Errorf("%s: severity = %v, want %v", tc.name, got.Severity, tc.wantSev)
		}
	}
}

func TestMatchFindingCarriesRule(t *testing.T) {
	f, ok := mini(t).Match("policy/v1beta1", "PodDisruptionBudget", Version{1, 25})
	if !ok {
		t.Fatal("no match")
	}
	r := f.Rule
	if r.Replacement != "policy/v1" || r.RemovedIn == nil || *r.RemovedIn != (Version{1, 25}) ||
		r.DeprecatedIn == nil || *r.DeprecatedIn != (Version{1, 21}) {
		t.Errorf("unexpected rule: %+v", r)
	}
}

func TestKinds(t *testing.T) {
	got := mini(t).Kinds()
	want := map[GroupKind]bool{
		{"policy", "PodDisruptionBudget"}:           true,
		{"autoscaling", "HorizontalPodAutoscaler"}:  true,
		{"storage.k8s.io", "VolumeAttributesClass"}: true,
	}
	if len(got) != len(want) {
		t.Fatalf("Kinds = %v, want %d entries", got, len(want))
	}
	for _, gk := range got {
		if !want[gk] {
			t.Errorf("unexpected kind %v", gk)
		}
	}
}

func TestLoadEmbedded(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.rules) == 0 {
		t.Fatal("no rules loaded")
	}
	for _, r := range s.rules {
		if r.Component != "k8s" {
			t.Errorf("non-k8s rule kept: %+v", r)
		}
	}
	f, ok := s.Match("apiextensions.k8s.io/v1beta1", "CustomResourceDefinition", Version{1, 22})
	if !ok || f.Severity != SeverityBlocker {
		t.Errorf("CRD v1beta1 at 1.22: ok=%v finding=%+v, want blocker", ok, f)
	}
}

func TestEmbeddedVersionsParse(t *testing.T) {
	rs, err := parseAll(rawVersions)
	if err != nil {
		t.Fatalf("parseAll: %v", err)
	}
	for _, r := range rs {
		// non-k8s upstream entries (istio) can have an empty kind; only k8s rules are used.
		if r.Component == "k8s" && (r.APIVersion == "" || r.Kind == "") {
			t.Errorf("rule missing apiVersion/kind: %+v", r)
		}
	}
}
