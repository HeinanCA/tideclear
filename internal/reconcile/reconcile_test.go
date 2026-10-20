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

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func insight(st insights.Status, usage string, clients ...insights.Client) insights.Insight {
	return insights.Insight{ID: "i1", Name: "Deprecated API usage", Status: st, LastRefresh: now.Add(-time.Hour),
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
		{"boundary_7d_exactly_is_blocker", insight(insights.StatusError, usageFS, client("ctl", 1, ago(ActiveWindow))), scanned(), VerdictBlocker, nil, 0, 1, ""},
		{"boundary_7d_plus_1s_is_stale", insight(insights.StatusError, usageFS, client("ctl", 1, ago(ActiveWindow+time.Second))), scanned(), VerdictStale, clearsFor(ActiveWindow + time.Second), 0, 1, ""},
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
