package apiscan

import "testing"

func TestLiveHoldsEveryObservedObjectNotOnlyRuleMatches(t *testing.T) {
	disc, dyn := ingressCluster(
		ingress("old", "", mf{ingOld, "a", "", at(1)}),
		ingress("new", "", mf{ingNew, "a", "", at(1)}))
	res := scan(t, disc, dyn, ver(1, 31))
	if len(res.Findings) != 1 || len(res.Live) != 2 {
		t.Fatalf("findings=%d live=%d, want 1 and 2: %+v", len(res.Findings), len(res.Live), res.Live)
	}
}

func TestListedCountsUnverifiedObjectsPerResource(t *testing.T) {
	disc, dyn := ingressCluster(ingress("none", ""), ingress("none2", ""), ingress("ok", "", mf{ingNew, "a", "", at(1)}))
	res := scan(t, disc, dyn, ver(1, 31))
	if len(res.Listed) != 1 || res.Listed[0].Unverified != 2 || res.Unverified != 2 {
		t.Fatalf("listed=%+v unverified=%d, want 2 on ingresses and 2 global", res.Listed, res.Unverified)
	}
}
