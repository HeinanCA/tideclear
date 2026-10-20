// Package reconcile joins AWS Upgrade Insights deprecation details with a live
// scan into one verdict per deprecated API version. It is pure: no I/O.
package reconcile

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/insights"
)

// Verdict is the outcome for one deprecated API version.
type Verdict string

// Verdicts.
const (
	VerdictBlocker Verdict = "BLOCKER"
	VerdictStale   Verdict = "STALE"
	VerdictOK      Verdict = "OK"
	VerdictUnknown Verdict = "UNKNOWN"
)

// Windows from the design: a client that called within ActiveWindow is still
// live; AWS keeps client stats for AuditWindow after the last call.
const (
	ActiveWindow = 7 * 24 * time.Hour
	AuditWindow  = 30 * 24 * time.Hour
)

// Row is one deprecated API version and what we conclude about it.
type Row struct {
	Usage, APIVersion, Kind, Resource, AWSStatus string
	Verdict                                      Verdict
	ClearsOn                                     *time.Time
	Objects                                      []apiscan.Object
	Clients                                      []insights.Client
	Reason                                       string
	LastRefresh                                  time.Time
}

// ParseUsage splits an insight usage path ("/apis/<group>/<version>/<resource>"
// or "/api/v1/<resource>") into group, version and resource. Anything else,
// including extra or empty segments, is not understood (ok=false).
func ParseUsage(usage string) (group, version, resource string, ok bool) {
	parts := strings.Split(usage, "/")
	if len(parts) < 2 || parts[0] != "" || slices.Contains(parts[1:], "") {
		return "", "", "", false
	}
	switch p := parts[1:]; {
	case p[0] == "apis" && len(p) == 4:
		return p[1], p[2], p[3], true
	case p[0] == "api" && len(p) == 3 && p[1] == "v1":
		return "", "v1", p[2], true
	}
	return "", "", "", false
}

// Unavailable is the UNKNOWN row for an insights call that failed.
func Unavailable(reason string) Row {
	return Row{AWSStatus: string(insights.StatusUnknown), Verdict: VerdictUnknown, Reason: "insights unavailable: " + reason}
}

// deprecatedInsightPrefix starts the documented name of the deprecated-API insight.
const deprecatedInsightPrefix = "Deprecated APIs removed in Kubernetes"

// IsDeprecatedInsight reports that in is the deprecated-API insight, matched
// case-insensitively on the documented name prefix.
func IsDeprecatedInsight(in insights.Insight) bool {
	return strings.HasPrefix(strings.ToLower(in.Name), strings.ToLower(deprecatedInsightPrefix))
}

// MissingInsight is the UNKNOWN row for a run where --insights returned no
// deprecated-API insight for target, so silence is not read as clean.
func MissingInsight(target string) Row {
	return Row{AWSStatus: string(insights.StatusUnknown), Verdict: VerdictUnknown,
		Reason: "no deprecated-API insight returned for target " + target}
}

// Reconcile returns one Row per deprecation detail of every insight. The
// verdict follows the first matching rule: live objects, unusable insight,
// PASSING, an active client, a stale client the scan could have seen, else UNKNOWN.
// A deprecated-API insight with no details gets one UNKNOWN row, so it is never
// reported as clean, unless it is PASSING with a trusted refresh time. Other insights produce no rows.
func Reconcile(ins []insights.Insight, res apiscan.Result, now time.Time) []Row {
	var rows []Row
	for _, in := range ins {
		for _, d := range in.Deprecations {
			rows = append(rows, one(in, d, res, now))
		}
		if len(in.Deprecations) == 0 && IsDeprecatedInsight(in) {
			if row, ok := detailless(in, now); ok {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// detailless is the row for a deprecated-API insight with no details: none for
// a PASSING one with a trusted refresh time (accepted as clean), else UNKNOWN.
func detailless(in insights.Insight, now time.Time) (Row, bool) {
	if in.Status == insights.StatusPassing {
		why := untrusted(in.LastRefresh, now)
		return Row{AWSStatus: string(in.Status), Verdict: VerdictUnknown, LastRefresh: in.LastRefresh, Reason: why}, why != ""
	}
	why := "no API details"
	if in.DetailsUnavailable {
		why = "details unavailable (no API details returned)"
	}
	return Row{AWSStatus: string(in.Status), Verdict: VerdictUnknown, LastRefresh: in.LastRefresh,
		Reason: fmt.Sprintf("AWS reported %s with %s", in.Status, why)}, true
}

func one(in insights.Insight, d insights.Deprecation, res apiscan.Result, now time.Time) Row {
	row := Row{Usage: d.Usage, AWSStatus: string(in.Status), LastRefresh: in.LastRefresh}
	group, version, resource, ok := ParseUsage(d.Usage)
	if !ok {
		row.Verdict, row.Reason = VerdictUnknown, "usage not understood: "+d.Usage
		return row
	}
	row.APIVersion, row.Resource = version, resource
	if group != "" {
		row.APIVersion = group + "/" + version
	}
	listed, hasListed := findListed(res.Listed, group, resource)
	row.Kind = listed.Kind
	for _, o := range res.Live {
		if o.APIVersion == row.APIVersion && o.Resource == resource {
			row.Objects = append(row.Objects, o)
			row.Kind = o.Kind
		}
	}
	row.Clients = counted(d.Clients)
	switch {
	case len(row.Objects) > 0:
		row.Verdict, row.Reason = VerdictBlocker, "live objects use this API version"
	case in.Status == insights.StatusUnknown:
		row.Verdict, row.Reason, row.Clients = VerdictUnknown, "insight status UNKNOWN", nil
	case in.Status == insights.StatusPassing:
		row.Verdict, row.Clients = VerdictOK, nil
		if why := untrusted(in.LastRefresh, now); why != "" {
			row.Verdict, row.Reason = VerdictUnknown, why
		}
	default:
		clientVerdict(&row, listed, hasListed, in.LastRefresh, now)
	}
	return row
}

// clientVerdict applies rules 4 to 6 from the counted clients' call times. A
// client is active when it called within ActiveWindow of the insight's
// LastRefresh (now when EKS gave none): the call times are as old as that refresh.
// A would-be STALE needs a known refresh within ActiveWindow of now and not in the
// future, else the call dates cannot be trusted and the verdict is UNKNOWN.
func clientVerdict(row *Row, listed apiscan.Listed, hasListed bool, refresh, now time.Time) {
	stored := refresh
	why := untrusted(stored, now)
	if refresh.IsZero() {
		refresh = now
	}
	var newest *time.Time
	for _, c := range row.Clients {
		if c.LastRequest == nil || refresh.Sub(*c.LastRequest) <= ActiveWindow {
			row.Verdict, row.Reason = VerdictBlocker, "a client still calls this API"
			return
		}
		if newest == nil || c.LastRequest.After(*newest) {
			newest = c.LastRequest
		}
	}
	switch {
	case newest == nil:
		row.Verdict, row.Reason = VerdictUnknown, "no client call date to judge by"
	case !hasListed:
		row.Verdict, row.Reason = VerdictUnknown, "not scanned live: the live scan did not read this resource"
	case listed.Unverified > 0:
		row.Verdict, row.Reason = VerdictUnknown, fmt.Sprintf("%d objects of this resource had no apiVersion evidence", listed.Unverified)
	case why != "":
		row.Verdict, row.Reason = VerdictUnknown, why
	default:
		clears := newest.Add(AuditWindow)
		row.Verdict, row.ClearsOn, row.Reason = VerdictStale, &clears, "last call is older than 7 days before the insight refreshed"
	}
}

// untrusted says why an insight's refresh time cannot vouch for its data (none,
// in the future, older than ActiveWindow), or "" when it can. Both OK and STALE need it.
func untrusted(refresh, now time.Time) string {
	switch {
	case refresh.IsZero():
		return "insight has no refresh time"
	case refresh.After(now):
		return "insight refresh time is in the future"
	case now.Sub(refresh) > ActiveWindow:
		return "insight data too old"
	}
	return ""
}

// counted drops clients that never called (zero requests and no time).
func counted(cs []insights.Client) []insights.Client {
	var out []insights.Client
	for _, c := range cs {
		if c.Requests > 0 || c.LastRequest != nil {
			out = append(out, c)
		}
	}
	return out
}

func findListed(ls []apiscan.Listed, group, resource string) (apiscan.Listed, bool) {
	for _, l := range ls {
		if l.Group == group && l.Resource == resource {
			return l, true
		}
	}
	return apiscan.Listed{}, false
}
