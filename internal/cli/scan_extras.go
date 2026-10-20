package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
)

// collectExtras runs the optional checks a scan was asked for. A check that
// fails for a non-credential reason degrades (a stderr line and a skipped
// entry, the rest of the report intact); a credential failure or an
// interrupted run is returned and exits 2.
func collectExtras(ctx context.Context, stderr io.Writer, deps Deps, fl scanFlags, res apiscan.Result, target rules.Version) (report.Extras, error) {
	if !fl.insights {
		return report.Extras{}, nil
	}
	return insightsExtras(ctx, stderr, deps, fl, res, target)
}

func insightsExtras(ctx context.Context, stderr io.Writer, deps Deps, fl scanFlags, res apiscan.Result, target rules.Version) (report.Extras, error) {
	ex := report.Extras{InsightsRan: true}
	regions, err := resolveRegions(deps, fl.profile, fl.region, false)
	if err != nil {
		return ex, err
	}
	api, err := deps.Cluster(fl.profile, regions[0])
	if err != nil {
		return ex, err
	}
	ins, err := insights.Load(ctx, api, fl.cluster, &target)
	switch {
	case err == nil:
		now := time.Now
		if deps.Now != nil {
			now = deps.Now
		}
		ex.Insights = reconcile.Reconcile(ins, res, now())
		if !slices.ContainsFunc(ins, reconcile.IsDeprecatedInsight) {
			ex.Insights = append(ex.Insights, reconcile.MissingInsight(target.String()))
		}
	case eksinfo.IsCredentialFailure(err), ctx.Err() != nil:
		return ex, fmt.Errorf("read insights for %s: %w", fl.cluster, err)
	default:
		reason := oneLine(err.Error())
		ex.Insights = []reconcile.Row{reconcile.Unavailable(reason)}
		ex.Skipped = []report.CheckSkipped{{Check: "insights", Reason: reason}}
		_, _ = fmt.Fprintf(stderr, "tideclear: warning: insights check skipped: %s\n", reason)
	}
	return ex, nil
}

// exitFor is the scan's verdict: ErrBlockers on any live BLOCKER finding or
// BLOCKER reconcile row; with strict, also on STALE or UNKNOWN reconcile rows.
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
	return nil
}
