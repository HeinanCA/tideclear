package reconcile

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/insights"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const (
	usageFS   = "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas"
	usageCore = "/api/v1/componentstatuses"
)

// clearsFor is the expected clear-by date for a last call d ago: that call plus 30 days.
func clearsFor(d time.Duration) *time.Time { t := now.Add(AuditWindow - d); return &t }

// refreshAge is how long before now the test insights were refreshed.
const refreshAge = time.Hour

// beforeRefresh is a call time d before the insight's LastRefresh (not before now).
func beforeRefresh(d time.Duration) *time.Time { return ago(refreshAge + d) }

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func insight(st insights.Status, usage string, clients ...insights.Client) insights.Insight {
	return insights.Insight{ID: "i1", Name: "Deprecated API usage", Status: st, LastRefresh: now.Add(-refreshAge),
		Deprecations: []insights.Deprecation{{Usage: usage, Clients: clients}}}
}

func client(ua string, n int32, last *time.Time) insights.Client {
	return insights.Client{UserAgent: ua, Requests: n, LastRequest: last}
}

var fsObject = apiscan.Object{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "FlowSchema", Name: "web", Origin: "managedFields", Resource: "flowschemas"}

func scanned(objs ...apiscan.Object) apiscan.Result {
	return apiscan.Result{
		Listed: []apiscan.Listed{
			{Group: "flowcontrol.apiserver.k8s.io", Resource: "flowschemas", Kind: "FlowSchema"},
			{Group: "", Resource: "componentstatuses", Kind: "ComponentStatus"},
		},
		Findings: itemsOf(objs),
		Live:     objs,
	}
}

func itemsOf(objs []apiscan.Object) []apiscan.ResultItem {
	var out []apiscan.ResultItem
	for _, o := range objs {
		out = append(out, apiscan.ResultItem{Object: o})
	}
	return out
}

func TestReconcileMatrix(t *testing.T) {
	d := 24 * time.Hour
	cases := []struct {
		name      string
		in        insights.Insight
		res       apiscan.Result
		want      Verdict
		clears    *time.Time // nil = no clear-by date
		objects   int
		clients   int
		reasonHas string
	}{
		{"real_blocker", insight(insights.StatusError, usageFS), scanned(fsObject), VerdictBlocker, nil, 1, 0, ""},
		{"real_blocker_with_status_UNKNOWN", insight(insights.StatusUnknown, usageFS), scanned(fsObject), VerdictBlocker, nil, 1, 0, ""},
		{"real_blocker_with_status_PASSING", insight(insights.StatusPassing, usageFS), scanned(fsObject), VerdictBlocker, nil, 1, 0, ""},
		{"real_blocker_with_old_clients", insight(insights.StatusError, usageFS, client("kubectl", 3, ago(20*d))), scanned(fsObject), VerdictBlocker, nil, 1, 1, ""},
		{"active_client_blocker", insight(insights.StatusError, usageFS, client("ctl/1.0", 3, ago(d))), scanned(), VerdictBlocker, nil, 0, 1, "still calls"},
		{"active_client_resource_not_listed", insight(insights.StatusError, usageFS, client("ctl/1.0", 3, ago(d))), apiscan.Result{}, VerdictBlocker, nil, 0, 1, ""},
		{"boundary_7d_exactly_is_blocker", insight(insights.StatusError, usageFS, client("ctl", 1, beforeRefresh(ActiveWindow))), scanned(), VerdictBlocker, nil, 0, 1, ""},
		{"boundary_7d_plus_1s_is_stale", insight(insights.StatusError, usageFS, client("ctl", 1, beforeRefresh(ActiveWindow+time.Second))), scanned(), VerdictStale, clearsFor(refreshAge + ActiveWindow + time.Second), 0, 1, ""},
		{"stale_clears_last_plus_30d", insight(insights.StatusWarning, usageFS, client("ctl", 5, ago(10*d))), scanned(), VerdictStale, clearsFor(10 * d), 0, 1, ""},
		{"boundary_30d_exactly_clears_now", insight(insights.StatusWarning, usageFS, client("ctl", 5, ago(AuditWindow))), scanned(), VerdictStale, &now, 0, 1, ""},
		{"boundary_30d_plus_1s_clears_in_past", insight(insights.StatusWarning, usageFS, client("ctl", 5, ago(AuditWindow+time.Second))), scanned(), VerdictStale, ago(time.Second), 0, 1, ""},
		{"nil_last_request_with_requests_is_blocker", insight(insights.StatusError, usageFS, client("ctl", 4, nil)), scanned(), VerdictBlocker, nil, 0, 1, ""},
		{"requests_zero_with_recent_time_is_blocker", insight(insights.StatusError, usageFS, client("ctl", 0, ago(d))), scanned(), VerdictBlocker, nil, 0, 1, ""},
		{"requests_zero_nil_time_is_ignored", insight(insights.StatusError, usageFS, client("ctl", 0, nil)), scanned(), VerdictUnknown, nil, 0, 0, ""},
		{"multi_client_newest_wins", insight(insights.StatusError, usageFS, client("old", 1, ago(20*d)), client("new", 1, ago(2*d))), scanned(), VerdictBlocker, nil, 0, 2, ""},
		{"multi_client_all_stale_uses_newest", insight(insights.StatusError, usageFS, client("old", 1, ago(25*d)), client("new", 1, ago(9*d))), scanned(), VerdictStale, clearsFor(9 * d), 0, 2, ""},
		{"error_with_no_clientstats", insight(insights.StatusError, usageFS), scanned(), VerdictUnknown, nil, 0, 0, ""},
		{"warning_with_no_clientstats", insight(insights.StatusWarning, usageFS), scanned(), VerdictUnknown, nil, 0, 0, ""},
		{"status_unknown", insight(insights.StatusUnknown, usageFS, client("ctl", 1, ago(20*d))), scanned(), VerdictUnknown, nil, 0, 0, ""},
		{"unparseable_usage", insight(insights.StatusError, "something/odd", client("ctl", 1, ago(d))), scanned(), VerdictUnknown, nil, 0, 0, "something/odd"},
		{"stale_candidate_resource_not_listed", insight(insights.StatusError, usageFS, client("ctl", 1, ago(10*d))), apiscan.Result{}, VerdictUnknown, nil, 0, 1, "not scanned live"},
		{"passing", insight(insights.StatusPassing, usageFS), scanned(), VerdictOK, nil, 0, 0, ""},
		{"core_api_form_blocker", insight(insights.StatusError, usageCore, client("ctl", 1, ago(d))), scanned(), VerdictBlocker, nil, 0, 1, ""},
		{"core_api_form_stale", insight(insights.StatusError, usageCore, client("ctl", 1, ago(10*d))), scanned(), VerdictStale, clearsFor(10 * d), 0, 1, ""},
		{"object_other_version_not_matched", insight(insights.StatusPassing, usageFS),
			scanned(apiscan.Object{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta2", Kind: "FlowSchema", Name: "x", Resource: "flowschemas"}), VerdictOK, nil, 0, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := Reconcile([]insights.Insight{c.in}, c.res, now)
			if len(rows) != 1 {
				t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
			}
			r := rows[0]
			if r.Verdict != c.want {
				t.Fatalf("verdict = %s (%s), want %s", r.Verdict, r.Reason, c.want)
			}
			switch {
			case c.clears == nil && r.ClearsOn != nil:
				t.Errorf("ClearsOn = %v, want nil", r.ClearsOn)
			case c.clears != nil && (r.ClearsOn == nil || !r.ClearsOn.Equal(*c.clears)):
				t.Errorf("ClearsOn = %v, want %v", r.ClearsOn, c.clears)
			}
			if len(r.Objects) != c.objects || len(r.Clients) != c.clients {
				t.Errorf("objects=%d clients=%d, want %d and %d", len(r.Objects), len(r.Clients), c.objects, c.clients)
			}
			if c.reasonHas != "" && !strings.Contains(r.Reason, c.reasonHas) {
				t.Errorf("reason %q lacks %q", r.Reason, c.reasonHas)
			}
		})
	}
}

func TestReconcileRowFields(t *testing.T) {
	in := insight(insights.StatusError, usageFS, client("ctl/1.0", 3, ago(24*time.Hour)))
	r := Reconcile([]insights.Insight{in}, scanned(), now)[0]
	if r.Usage != usageFS || r.APIVersion != "flowcontrol.apiserver.k8s.io/v1beta3" || r.Resource != "flowschemas" ||
		r.Kind != "FlowSchema" || r.AWSStatus != "ERROR" || !r.LastRefresh.Equal(in.LastRefresh) {
		t.Fatalf("row = %+v", r)
	}
	core := Reconcile([]insights.Insight{insight(insights.StatusPassing, usageCore)}, scanned(), now)[0]
	if core.APIVersion != "v1" || core.Resource != "componentstatuses" {
		t.Fatalf("core row = %+v", core)
	}
}

func TestReconcileNoDeprecationsNoRows(t *testing.T) {
	in := insights.Insight{ID: "x", Name: "Add-on compatibility", Status: insights.StatusPassing}
	if rows := Reconcile([]insights.Insight{in}, scanned(), now); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

func TestUnavailable(t *testing.T) {
	r := Unavailable("access denied")
	if r.Verdict != VerdictUnknown || !strings.Contains(r.Reason, "access denied") || r.AWSStatus != "UNKNOWN" {
		t.Fatalf("row = %+v", r)
	}
}

func TestParseUsage(t *testing.T) {
	cases := []struct {
		in        string
		g, v, res string
		ok        bool
	}{
		{"/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas", "flowcontrol.apiserver.k8s.io", "v1beta3", "flowschemas", true},
		{"/apis/apps/v1/deployments", "apps", "v1", "deployments", true},
		{"/api/v1/componentstatuses", "", "v1", "componentstatuses", true},
		{"/api/v1", "", "", "", false},
		{"/api/v2/pods", "", "", "", false},
		{"/apis/apps/v1", "", "", "", false},
		{"/apis/apps/v1/deployments/extra", "", "", "", false},
		{"/apis//v1/deployments", "", "", "", false},
		{"/apis/apps/v1/", "", "", "", false},
		{"apis/apps/v1/deployments", "", "", "", false},
		{"", "", "", "", false},
		{"/healthz", "", "", "", false},
	}
	for _, c := range cases {
		g, v, res, ok := ParseUsage(c.in)
		if !reflect.DeepEqual([]any{g, v, res, ok}, []any{c.g, c.v, c.res, c.ok}) {
			t.Errorf("ParseUsage(%q) = %q %q %q %v, want %q %q %q %v", c.in, g, v, res, ok, c.g, c.v, c.res, c.ok)
		}
	}
}

const deprecatedInsightName = "Deprecated APIs removed in Kubernetes v1.32"

func TestRule1MatchesLiveObjectAtVersionWithNoBundledRule(t *testing.T) {
	// The object is live but no rule matches it, so it is in Live and not in Findings.
	res := scanned()
	res.Live = []apiscan.Object{fsObject}
	in := insight(insights.StatusError, usageFS, client("old", 1, ago(20*24*time.Hour)))
	r := Reconcile([]insights.Insight{in}, res, now)[0]
	if r.Verdict != VerdictBlocker || len(r.Objects) != 1 {
		t.Fatalf("verdict = %s (%s) objects=%d, want BLOCKER with 1 object", r.Verdict, r.Reason, len(r.Objects))
	}
}

func TestM1ActiveWindowIsMeasuredFromLastRefresh(t *testing.T) {
	// Refreshed 3 days ago; last call 5 days before that (8d before now): 5d <= 7d, still live.
	in := insight(insights.StatusError, usageFS, client("ctl", 1, ago(8*24*time.Hour)))
	in.LastRefresh = now.Add(-3 * 24 * time.Hour)
	r := Reconcile([]insights.Insight{in}, scanned(), now)[0]
	if r.Verdict != VerdictBlocker {
		t.Fatalf("verdict = %s (%s), want BLOCKER", r.Verdict, r.Reason)
	}
	in.Deprecations[0].Clients = []insights.Client{client("ctl", 1, ago(10*24*time.Hour+time.Second))}
	if r := Reconcile([]insights.Insight{in}, scanned(), now)[0]; r.Verdict != VerdictStale {
		t.Fatalf("7d+1s before refresh: verdict = %s (%s), want STALE", r.Verdict, r.Reason)
	}
}

func TestZeroLastRefreshStaleBecomesUnknown(t *testing.T) {
	in := insight(insights.StatusError, usageFS, client("ctl", 1, ago(ActiveWindow+time.Second)))
	in.LastRefresh = time.Time{}
	r := Reconcile([]insights.Insight{in}, scanned(), now)[0]
	if r.Verdict != VerdictUnknown || r.ClearsOn != nil || !strings.Contains(r.Reason, "insight has no refresh time") {
		t.Fatalf("row = %s %q clears=%v, want UNKNOWN no refresh time", r.Verdict, r.Reason, r.ClearsOn)
	}
	// A client within the window of now still blocks (zero refresh falls back to now).
	in.Deprecations[0].Clients = []insights.Client{client("ctl", 1, ago(time.Hour))}
	if r := Reconcile([]insights.Insight{in}, scanned(), now)[0]; r.Verdict != VerdictBlocker {
		t.Fatalf("active client: verdict = %s (%s), want BLOCKER", r.Verdict, r.Reason)
	}
}

func TestPassingNeedsFreshInsightData(t *testing.T) {
	cases := []struct {
		name    string
		refresh time.Time
		want    Verdict
		reason  string
	}{
		{"fresh", now.Add(-time.Hour), VerdictOK, ""},
		{"zero", time.Time{}, VerdictUnknown, "insight has no refresh time"},
		{"too old", now.Add(-ActiveWindow - time.Minute), VerdictUnknown, "insight data too old"},
		{"future", now.Add(time.Minute), VerdictUnknown, "insight refresh time is in the future"},
	}
	for _, c := range cases {
		in := insight(insights.StatusPassing, usageFS)
		in.LastRefresh = c.refresh
		r := Reconcile([]insights.Insight{in}, scanned(), now)[0]
		if r.Verdict != c.want || !strings.Contains(r.Reason, c.reason) {
			t.Errorf("%s: row = %s %q, want %s", c.name, r.Verdict, r.Reason, c.want)
		}
	}
	// Live objects still block a PASSING insight regardless of freshness.
	in := insight(insights.StatusPassing, usageFS)
	in.LastRefresh = time.Time{}
	if r := Reconcile([]insights.Insight{in}, scanned(fsObject), now)[0]; r.Verdict != VerdictBlocker {
		t.Fatalf("live object: verdict = %s, want BLOCKER", r.Verdict)
	}
}

func TestM1CallAfterRefreshIsActive(t *testing.T) {
	in := insight(insights.StatusError, usageFS, client("ctl", 1, ago(time.Minute)))
	in.LastRefresh = now.Add(-30 * 24 * time.Hour)
	if r := Reconcile([]insights.Insight{in}, scanned(), now)[0]; r.Verdict != VerdictBlocker {
		t.Fatalf("verdict = %s (%s), want BLOCKER", r.Verdict, r.Reason)
	}
}

func TestM2UnverifiedObjectsTurnStaleIntoUnknown(t *testing.T) {
	res := scanned()
	res.Listed[0].Unverified = 3
	in := insight(insights.StatusError, usageFS, client("ctl", 1, ago(10*24*time.Hour)))
	r := Reconcile([]insights.Insight{in}, res, now)[0]
	if r.Verdict != VerdictUnknown || r.ClearsOn != nil || !strings.Contains(r.Reason, "3 objects of this resource had no apiVersion evidence") {
		t.Fatalf("row = %s %q clears=%v", r.Verdict, r.Reason, r.ClearsOn)
	}
	// An active client still blocks.
	active := insight(insights.StatusError, usageFS, client("ctl", 1, ago(time.Hour)))
	if r := Reconcile([]insights.Insight{active}, res, now)[0]; r.Verdict != VerdictBlocker {
		t.Fatalf("active: verdict = %s", r.Verdict)
	}
	// A PASSING insight stays OK even with unverified objects.
	passing := insight(insights.StatusPassing, usageFS)
	if r := Reconcile([]insights.Insight{passing}, res, now)[0]; r.Verdict != VerdictOK {
		t.Fatalf("passing: verdict = %s (%s), want OK", r.Verdict, r.Reason)
	}
}

func TestStaleNeedsFreshInsightData(t *testing.T) {
	cases := []struct {
		name    string
		refresh time.Time
		want    Verdict
		reason  string
	}{
		{"fresh", now.Add(-time.Hour), VerdictStale, ""},
		{"too old", now.Add(-ActiveWindow - time.Minute), VerdictUnknown, "insight data too old"},
		{"future", now.Add(time.Minute), VerdictUnknown, "insight refresh time is in the future"},
	}
	for _, c := range cases {
		callTime := c.refresh.Add(-10 * 24 * time.Hour)
		in := insight(insights.StatusError, usageFS, client("ctl", 1, &callTime))
		in.LastRefresh = c.refresh
		r := Reconcile([]insights.Insight{in}, scanned(), now)[0]
		if r.Verdict != c.want || !strings.Contains(r.Reason, c.reason) || (c.want == VerdictUnknown && r.ClearsOn != nil) {
			t.Errorf("%s: row = %s %q clears=%v", c.name, r.Verdict, r.Reason, r.ClearsOn)
		}
	}
}

func TestIsDeprecatedInsightIsCaseInsensitive(t *testing.T) {
	for name, want := range map[string]bool{
		"Deprecated APIs removed in Kubernetes v1.32": true,
		"deprecated apis removed in kubernetes v1.32": true,
		"Kubelet version skew":                        false,
		"":                                            false,
	} {
		if got := IsDeprecatedInsight(insights.Insight{Name: name}); got != want {
			t.Errorf("%q: got %v, want %v", name, got, want)
		}
	}
}

func TestMissingInsightRow(t *testing.T) {
	r := MissingInsight("1.32")
	if r.Verdict != VerdictUnknown || r.Reason != "no deprecated-API insight returned for target 1.32" {
		t.Fatalf("row = %+v", r)
	}
}

func TestH2DetaillessDeprecatedAPIInsightIsUnknown(t *testing.T) {
	for _, st := range []insights.Status{insights.StatusError, insights.StatusWarning, insights.StatusUnknown} {
		in := insights.Insight{ID: "d", Name: deprecatedInsightName, Status: st, DetailsUnavailable: true, LastRefresh: now}
		rows := Reconcile([]insights.Insight{in}, scanned(), now)
		if len(rows) != 1 || rows[0].Verdict != VerdictUnknown || rows[0].AWSStatus != string(st) ||
			!strings.Contains(rows[0].Reason, "details unavailable") || !strings.Contains(rows[0].Reason, string(st)) {
			t.Fatalf("%s: rows = %+v", st, rows)
		}
	}
	empty := insights.Insight{ID: "d", Name: deprecatedInsightName, Status: insights.StatusError}
	if rows := Reconcile([]insights.Insight{empty}, scanned(), now); len(rows) != 1 || !strings.Contains(rows[0].Reason, "no API details") {
		t.Fatalf("empty summary: rows = %+v", rows)
	}
}

func TestH2OtherInsightsAndPassingProduceNoRow(t *testing.T) {
	for _, in := range []insights.Insight{
		{ID: "k", Name: "Kubelet version skew", Status: insights.StatusError, DetailsUnavailable: true},
		{ID: "a", Name: "Add-on compatibility", Status: insights.StatusWarning},
		{ID: "p", Name: deprecatedInsightName, Status: insights.StatusPassing, DetailsUnavailable: true},
	} {
		if rows := Reconcile([]insights.Insight{in}, scanned(), now); len(rows) != 0 {
			t.Fatalf("%s: rows = %+v, want none", in.Name, rows)
		}
	}
}
