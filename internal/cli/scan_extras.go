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
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
)

// extrasTimeout bounds each EKS check in a scan (a var so tests can shorten it).
var extrasTimeout = eksinfo.DefaultRegionTimeout

// collectExtras runs the optional checks a scan was asked for. A check that
// fails for a non-credential reason degrades (a stderr line and a skipped
// entry, the rest of the report intact); a credential failure or an
// interrupted run is returned and exits 2. A --path scan has no cluster
// reads at all. A live scan without --cluster notes the skipped add-on
// check in the report (stdout and JSON), never on stderr.
func collectExtras(ctx context.Context, stderr io.Writer, deps Deps, fl scanFlags, res apiscan.Result, target rules.Version) (report.Extras, error) {
	switch {
	case fl.pathSet:
		return report.Extras{}, nil
	case fl.cluster == "":
		return report.Extras{Skipped: []report.CheckSkipped{{Check: "addons", Reason: "needs --cluster"}}}, nil
	}
	regions, err := resolveRegions(deps, fl.profile, fl.region, false)
	if err != nil {
		return report.Extras{}, err
	}
	api, err := deps.Cluster(fl.profile, regions[0])
	if err != nil {
		return report.Extras{}, err
	}
	var ex report.Extras
	var compat []insights.AddonCompat
	if fl.insights {
		if ex, compat, err = insightsExtras(ctx, stderr, api, deps, fl, res, target); err != nil {
			return ex, err
		}
	}
	return addonsExtras(ctx, stderr, api, fl, target, ex, compat)
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
		reason := oneLine(err.Error())
		ex.Skipped = append(ex.Skipped, report.CheckSkipped{Check: "addons", Reason: reason})
		_, _ = fmt.Fprintf(stderr, "tideclear: warning: addons check skipped: %s\n", reason)
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
		ex.Insights = reconcile.Reconcile(ins, res, now())
		if !slices.ContainsFunc(ins, func(in insights.Insight) bool {
			return in.Target == target.String() && reconcile.IsDeprecatedInsight(in)
		}) {
			ex.Insights = append(ex.Insights, reconcile.MissingInsight(target.String()))
		}
	case eksinfo.IsCredentialFailure(err), ctx.Err() != nil:
		return ex, nil, fmt.Errorf("read insights for %s: %w", fl.cluster, err)
	default:
		reason := oneLine(err.Error())
		ex.Insights = []reconcile.Row{reconcile.Unavailable(reason)}
		ex.Skipped = []report.CheckSkipped{{Check: "insights", Reason: reason}}
		_, _ = fmt.Fprintf(stderr, "tideclear: warning: insights check skipped: %s\n", reason)
	}
	var compat []insights.AddonCompat
	for _, in := range ins {
		compat = append(compat, in.AddonCompat...)
	}
	return ex, compat, nil
}

// exitFor is the scan's verdict: ErrBlockers on any live BLOCKER finding or
// BLOCKER reconcile row; with strict, also on STALE or UNKNOWN reconcile rows
// and UNKNOWN add-on rows. A skipped check and an incompatible add-on (a
// WARNING) never count.
func exitFor(res apiscan.Result, ex report.Extras, strict bool) error {
	for _, it := range res.Findings {
		if it.Finding.Severity == rules.SeverityBlocker {
			return ErrBlockers
		}
	}
	for _, r := range ex.Insights {
		switch r.Verdict {
		case reconcile.VerdictBlocker:
			return ErrBlockers
		case reconcile.VerdictStale, reconcile.VerdictUnknown:
			if strict {
				return ErrBlockers
			}
		}
	}
	if strict && slices.ContainsFunc(ex.Addons, func(r addons.Row) bool { return r.Status == addons.StatusUnknown }) {
		return ErrBlockers
	}
	return nil
}
