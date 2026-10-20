package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
	"k8s.io/client-go/dynamic"
)

// extrasTimeout bounds each EKS check in a scan (a var so tests can shorten it).
var extrasTimeout = eksinfo.DefaultRegionTimeout

// collectExtras runs the optional checks a scan was asked for. A check that
// fails for a non-credential reason degrades (a stderr line and a skipped
// entry, the rest of the report intact); a credential failure or an
// interrupted run is returned and exits 2. A --path scan has no cluster
// reads at all. A live scan without --cluster notes the skipped add-on and
// nodegroup checks in the report (stdout and JSON), never on stderr; the node
// list runs on every live scan.
func collectExtras(ctx context.Context, stderr io.Writer, deps Deps, fl scanFlags, res apiscan.Result, current, target rules.Version, dyn dynamic.Interface) (report.Extras, error) {
	if fl.pathSet {
		return report.Extras{}, nil
	}
	nr, err := rules.LoadNodes()
	if err != nil {
		return report.Extras{}, err
	}
	var ex report.Extras
	var ng nodegroupState
	if fl.cluster == "" {
		ex.Skipped = []report.CheckSkipped{{Check: "addons", Reason: "needs --cluster"}, {Check: "nodegroups", Reason: "needs --cluster"}}
	} else if ex, ng, err = clusterExtras(ctx, stderr, deps, fl, res, current, target, nr); err != nil {
		return ex, err
	}
	return nodesExtras(ctx, stderr, dyn, current, target, nr, ng, ex)
}

// clusterExtras runs the checks that read the EKS cluster: insights (when
// asked), add-ons and managed nodegroups.
func clusterExtras(ctx context.Context, stderr io.Writer, deps Deps, fl scanFlags, res apiscan.Result, current, target rules.Version, nr *rules.NodeRules) (report.Extras, nodegroupState, error) {
	regions, err := resolveRegions(deps, fl.profile, fl.region, false)
	if err != nil {
		return report.Extras{}, nodegroupState{}, err
	}
	api, err := deps.Cluster(fl.profile, regions[0])
	if err != nil {
		return report.Extras{}, nodegroupState{}, err
	}
	var ex report.Extras
	var compat []insights.AddonCompat
	if fl.insights {
		if ex, compat, err = insightsExtras(ctx, stderr, api, deps, fl, res, target); err != nil {
			return ex, nodegroupState{}, err
		}
	}
	if ex, err = addonsExtras(ctx, stderr, api, fl, target, ex, compat); err != nil {
		return ex, nodegroupState{}, err
	}
	return nodegroupExtras(ctx, stderr, api, fl, current, target, nr, ex)
}

// addonsExtras adds the add-on check, bounded by extrasTimeout; the ctx.Err()
// test below is on the parent, so only a parent cancel or timeout is fatal. The insight's add-on view is only
// cross-checked when insights ran; the direct check always wins.
func addonsExtras(ctx context.Context, stderr io.Writer, api ClusterAPI, fl scanFlags, target rules.Version, ex report.Extras, compat []insights.AddonCompat) (report.Extras, error) {
	callCtx, cancel := context.WithTimeout(ctx, extrasTimeout)
	defer cancel()
	rows, err := addons.Check(callCtx, api, fl.cluster, target)
	switch {
	case err == nil:
		if fl.insights {
			rows = addons.CrossCheck(rows, compat)
		}
		ex.Addons, ex.AddonsRan = rows, true
	case eksinfo.IsCredentialFailure(err), ctx.Err() != nil:
		return ex, fmt.Errorf("read add-ons for %s: %w", fl.cluster, err)
	default:
		ex = skipCheck(stderr, ex, "addons", err)
	}
	return ex, nil
}

func insightsExtras(ctx context.Context, stderr io.Writer, api ClusterAPI, deps Deps, fl scanFlags, res apiscan.Result, target rules.Version) (report.Extras, []insights.AddonCompat, error) {
	ex := report.Extras{InsightsRan: true}
	callCtx, cancel := context.WithTimeout(ctx, extrasTimeout)
	defer cancel()
	ins, err := insights.Load(callCtx, api, fl.cluster, &target)
	switch {
	case err == nil:
		now := time.Now
		if deps.Now != nil {
			now = deps.Now
		}
		ex.RefreshStale = report.RefreshStale(ins, now())
		ex.Insights = reconcile.Reconcile(ins, res, now())
		if !slices.ContainsFunc(ins, func(in insights.Insight) bool {
			return in.Target == target.String() && reconcile.IsDeprecatedInsight(in)
		}) {
			ex.Insights = append(ex.Insights, reconcile.MissingInsight(target.String()))
		}
	case eksinfo.IsCredentialFailure(err), ctx.Err() != nil:
		return ex, nil, fmt.Errorf("read insights for %s: %w", fl.cluster, err)
	default:
		ex = skipCheck(stderr, ex, "insights", err)
		ex.Insights = []reconcile.Row{reconcile.Unavailable(oneLine(err.Error()))}
	}
	var compat []insights.AddonCompat
	for _, in := range ins {
		compat = append(compat, in.AddonCompat...)
	}
	return ex, compat, nil
}

// exitFor is the scan's verdict: ErrBlockers on any live BLOCKER finding or
// BLOCKER reconcile row or node row; with strict, also on STALE or UNKNOWN reconcile rows
// and UNKNOWN add-on rows. A skipped check and an incompatible add-on (a
// WARNING) never count.
func exitFor(res apiscan.Result, ex report.Extras, strict bool) error {
	for _, it := range res.Findings {
		if it.Finding.Severity == rules.SeverityBlocker {
			return ErrBlockers
		}
	}
	if slices.ContainsFunc(ex.Nodes, func(r nodes.Row) bool { return r.Severity == rules.SeverityBlocker }) {
		return ErrBlockers
	}
	for _, r := range ex.Insights {
		if r.Verdict == reconcile.VerdictBlocker {
			return ErrBlockers
		}
	}
	if strict && ex.StrictItems() > 0 {
		return ErrBlockers
	}
	return nil
}

// strictBlockers counts the STALE and UNKNOWN items that make --strict exit 1
// where the same scan would exit 0; zero when strict is off or a real blocker
// already decides.
func strictBlockers(res apiscan.Result, ex report.Extras, strict bool) int {
	if !strict || exitFor(res, ex, false) != nil {
		return 0
	}
	return ex.StrictItems()
}
