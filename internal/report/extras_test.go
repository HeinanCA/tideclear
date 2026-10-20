package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/reconcile"
)

func insightsSample() Extras {
	last := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	clears := last.Add(reconcile.AuditWindow)
	return Extras{
		InsightsRan: true,
		Insights: []reconcile.Row{
			{Usage: "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas", APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "FlowSchema",
				Resource: "flowschemas", AWSStatus: "ERROR", Verdict: reconcile.VerdictBlocker, Reason: "live objects use this API version",
				Objects: []apiscan.Object{{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "FlowSchema", Name: "web", Origin: "managedFields", Resource: "flowschemas"}}},
			{Usage: "/apis/networking.k8s.io/v1beta1/ingresses", APIVersion: "networking.k8s.io/v1beta1", Kind: "Ingress", Resource: "ingresses",
				AWSStatus: "ERROR", Verdict: reconcile.VerdictBlocker, Reason: "a client still calls this API",
				Clients: []insights.Client{{UserAgent: "demo-controller/1.2", Requests: 42, LastRequest: &recent}}},
			{Usage: "/apis/apps/v1beta2/deployments", APIVersion: "apps/v1beta2", Kind: "Deployment", Resource: "deployments",
				AWSStatus: "WARNING", Verdict: reconcile.VerdictStale, ClearsOn: &clears, Reason: "last call is older than 7 days",
				Clients: []insights.Client{{UserAgent: "kubectl/1.28", Requests: 3, LastRequest: &last}}},
			{Usage: "/apis/batch/v1beta1/cronjobs", APIVersion: "batch/v1beta1", Resource: "cronjobs", AWSStatus: "ERROR",
				Verdict: reconcile.VerdictUnknown, Reason: "not scanned live: the live scan did not read this resource",
				Clients: []insights.Client{{UserAgent: "old-agent", Requests: 1, LastRequest: &last}}},
			{Usage: "/apis/policy/v1beta1/poddisruptionbudgets", APIVersion: "policy/v1beta1", Kind: "PodDisruptionBudget", Resource: "poddisruptionbudgets",
				AWSStatus: "PASSING", Verdict: reconcile.VerdictOK},
		},
	}
}

func TestScanTextInsightsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, scanSample(t), insightsSample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-insights.txt", b.Bytes())
}

func TestScanJSONInsightsGolden(t *testing.T) {
	var b bytes.Buffer
	ex := insightsSample()
	ex.Skipped = []CheckSkipped{{Check: "insights", Reason: "access denied"}}
	if err := ScanJSON(&b, scanSample(t), ex); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-insights.json", b.Bytes())
}

func TestScanTextStaleRendersClearDate(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138}, insightsSample()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "STALE, clears 2026-11-12") {
		t.Fatalf("stale row lacks the clear-by date:\n%s", b.String())
	}
}

func TestScanTextUnknownFooter(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138}, insightsSample()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(b.String(), "result not clean: 1 unknown\n") {
		t.Fatalf("footer missing or not last:\n%s", b.String())
	}
	b.Reset()
	ex := insightsSample()
	ex.Insights = ex.Insights[:3]
	if err := ScanText(&b, apiscan.Result{Target: target138}, ex); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "result not clean") {
		t.Fatalf("no unknown rows, yet a footer:\n%s", b.String())
	}
}

func TestScanTextInsightsEmptyStillShowsSection(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138}, Extras{InsightsRan: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "upgrade insights: no deprecated API usage reported") {
		t.Fatalf("stdout:\n%s", b.String())
	}
}

func TestScanJSONInsightsKeyRules(t *testing.T) {
	decode := func(ex Extras) map[string]json.RawMessage {
		var b bytes.Buffer
		if err := ScanJSON(&b, apiscan.Result{Target: target138}, ex); err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := decode(Extras{}); m["insights"] != nil || m["checksSkipped"] != nil {
		t.Fatalf("zero Extras added keys: %v", m)
	}
	m := decode(Extras{InsightsRan: true})
	if string(m["insights"]) != "[]" || m["checksSkipped"] != nil || string(m["schemaVersion"]) != "1" {
		t.Fatalf("insights ran with no rows: insights=%s checksSkipped=%s schemaVersion=%s", m["insights"], m["checksSkipped"], m["schemaVersion"])
	}
}

type countWrites struct{ n int }

func (c *countWrites) Write(p []byte) (int, error) { c.n++; return len(p), nil }

func TestScanTextInsightsWriteError(t *testing.T) {
	var c countWrites
	if err := ScanText(&c, apiscan.Result{Target: target138}, insightsSample()); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < c.n; n++ {
		if err := ScanText(&failAfter{n: n}, apiscan.Result{Target: target138}, insightsSample()); err == nil {
			t.Errorf("fail after %d of %d writes: want a write error", n, c.n)
		}
	}
}
