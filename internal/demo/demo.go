// Package demo builds the sample scan behind `tideclear demo`. It holds inputs
// only (a fictional 1.31 cluster moving to 1.32) and runs them through the
// real judges, so the report cannot show a row the tool cannot produce. It
// never reads AWS, Kubernetes, the network or the disk.
package demo

import (
	"context"
	"time"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
)

const (
	flowSchemaUsage = "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas"
	priorityUsage   = "/apis/flowcontrol.apiserver.k8s.io/v1beta3/prioritylevelconfigurations"
	flowGroup       = "flowcontrol.apiserver.k8s.io"
	clusterName     = "sample-cluster"
)

var (
	current = rules.Version{Major: 1, Minor: 31}
	target  = rules.Version{Major: 1, Minor: 32}
)

// Sample returns the sample scan result and extras as of now. Every timestamp
// is an offset from now in UTC, because reconcile rejects an insight refreshed
// more than 7 days before now: fixed dates would turn the sample UNKNOWN.
func Sample(now time.Time) (apiscan.Result, report.Extras, error) {
	now = now.UTC()
	set, err := rules.Load()
	if err != nil {
		return apiscan.Result{}, report.Extras{}, err
	}
	nr, err := rules.LoadNodes()
	if err != nil {
		return apiscan.Result{}, report.Extras{}, err
	}
	ctx := context.Background()
	res, err := apiscan.Scan(ctx, sampleScanSource(), set, target)
	if err != nil {
		return apiscan.Result{}, report.Extras{}, err
	}
	res.Cluster = apiscan.ClusterInfo{Context: "sample-context", EKSCluster: clusterName}

	ins := sampleInsights(now)
	ex := report.Extras{
		InsightsRan:  true,
		AddonsRan:    true,
		RefreshStale: report.RefreshStale(ins, now),
		Insights:     reconcile.Reconcile(ins, res, now),
	}
	if ex.Addons, err = addons.Check(ctx, sampleAddonAPI(), clusterName, target); err != nil {
		return apiscan.Result{}, report.Extras{}, err
	}
	groups := sampleNodegroups{name: "sample-workers-al2", version: current.String(), release: "1.31.13-20251001"}
	if ex.Nodes, _, _, err = nodes.CheckNodegroups(ctx, groups, clusterName, current, target, nr); err != nil {
		return apiscan.Result{}, report.Extras{}, err
	}
	return res, ex, nil
}

// sampleScanSource is a live FlowSchema on a removed API version, and a
// PriorityLevelConfiguration kind the scan read in full with no objects: the
// STALE verdict needs the scan to have been able to see the kind.
func sampleScanSource() apiscan.Source {
	return sampleSource{current: current, obs: apiscan.Observed{
		Objects: []apiscan.Object{{APIVersion: flowGroup + "/v1beta3", Kind: "FlowSchema", Name: "web", Origin: "last-applied", Resource: "flowschemas"}},
		Listed: []apiscan.Listed{
			{Group: flowGroup, Resource: "flowschemas", Kind: "FlowSchema"},
			{Group: flowGroup, Resource: "prioritylevelconfigurations", Kind: "PriorityLevelConfiguration"},
		},
	}}
}

// sampleInsights is the 1.32 deprecated-API insight pair: one backed by the
// live FlowSchema (BLOCKER), one whose only caller, an old kubectl, last called
// over 7 days before the insight refreshed (STALE).
func sampleInsights(now time.Time) []insights.Insight {
	refresh := now.Add(-13 * time.Hour)
	lastCall := refresh.Add(-9 * 24 * time.Hour)
	insight := func(id string, st insights.Status, usage string, cs ...insights.Client) insights.Insight {
		return insights.Insight{ID: id, Name: "Deprecated APIs removed in Kubernetes v" + target.String(), Target: target.String(), Status: st,
			LastRefresh: refresh, Deprecations: []insights.Deprecation{{Usage: usage, Clients: cs}}}
	}
	return []insights.Insight{
		insight("sample-1", insights.StatusError, flowSchemaUsage),
		insight("sample-2", insights.StatusWarning, priorityUsage, insights.Client{UserAgent: "kubectl/1.28", Requests: 3, LastRequest: &lastCall}),
	}
}

// sampleAddonAPI has one add-on already compatible with 1.32 and one below the minimum.
func sampleAddonAPI() sampleAddons {
	return sampleAddons{
		names:     []string{"sample-dns", "sample-net"},
		installed: map[string]string{"sample-dns": "v1.2.0-eksbuild.1", "sample-net": "v1.0.0-eksbuild.1"},
		supported: map[string][]string{
			"sample-dns": {"v1.1.0-eksbuild.1", "v1.2.0-eksbuild.1"},
			"sample-net": {"v1.2.0-eksbuild.1", "v1.3.0-eksbuild.1"},
		},
	}
}
