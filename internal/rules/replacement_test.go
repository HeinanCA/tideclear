package rules

import "testing"

func TestReplacementFollowsChainPastRemovedVersions(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		apiVersion, kind string
		target           Version
		want             string
	}{
		{"flowcontrol.apiserver.k8s.io/v1beta1", "FlowSchema", Version{1, 37}, "flowcontrol.apiserver.k8s.io/v1"},
		{"flowcontrol.apiserver.k8s.io/v1beta2", "PriorityLevelConfiguration", Version{1, 37}, "flowcontrol.apiserver.k8s.io/v1"},
		{"flowcontrol.apiserver.k8s.io/v1beta2", "FlowSchema", Version{1, 31}, "flowcontrol.apiserver.k8s.io/v1beta3"},
		{"policy/v1beta1", "PodDisruptionBudget", Version{1, 37}, "policy/v1"},
	}
	for _, tc := range cases {
		f, ok := set.Match(tc.apiVersion, tc.kind, tc.target)
		if !ok || f.Replacement != tc.want {
			t.Errorf("%s %s @%s: replacement = %q (found=%v), want %q", tc.apiVersion, tc.kind, tc.target, f.Replacement, ok, tc.want)
		}
	}
}

func TestReplacementCycleTerminates(t *testing.T) {
	s, err := parse([]byte(`
deprecated-versions:
  - {version: g.io/v1alpha1, kind: X, deprecated-in: v1.20.0, removed-in: v1.25.0, replacement-api: g.io/v1beta1, component: k8s}
  - {version: g.io/v1beta1, kind: X, deprecated-in: v1.20.0, removed-in: v1.30.0, replacement-api: g.io/v1alpha1, component: k8s}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Match("g.io/v1alpha1", "X", Version{1, 31}); !ok {
		t.Fatal("no finding")
	}
}

func TestKindsIncludeReplacementGroups(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	have := map[GroupKind]bool{}
	for _, gk := range set.Kinds() {
		have[gk] = true
	}
	for _, want := range []GroupKind{
		{"networking.k8s.io", "NetworkPolicy"}, {"networking.k8s.io", "Ingress"}, {"apps", "Deployment"}, {"apps", "DaemonSet"}, {"apps", "ReplicaSet"},
	} {
		if !have[want] {
			t.Errorf("Kinds lacks %+v", want)
		}
	}
}
