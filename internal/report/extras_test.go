package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
)

func TestScanTextInsightsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, staleScan(t), staleInsightsSample(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-insights-stale.txt", b.Bytes())
}

func TestScanJSONInsightsGolden(t *testing.T) {
	var b bytes.Buffer
	ex := staleInsightsSample(t)
	ex.Skipped = []CheckSkipped{{Check: "insights", Reason: "access denied"}}
	if err := ScanJSON(&b, staleScan(t), ex); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-insights-stale.json", b.Bytes())
}

func TestScanTextStaleRendersClearDate(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target132}, staleInsightsSample(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "STALE, clears 2026-11-09") {
		t.Fatalf("stale row lacks the clear-by date:\n%s", b.String())
	}
}

func TestScanTextStaleFooter(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target132}, staleInsightsSample(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(b.String(), "result not clean: 1 stale\n") {
		t.Fatalf("footer missing or not last:\n%s", b.String())
	}
	b.Reset()
	ex := staleInsightsSample(t)
	ex.Insights = ex.Insights[:1] // the BLOCKER row only
	if err := ScanText(&b, apiscan.Result{Target: target132}, ex); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "result not clean") {
		t.Fatalf("no stale or unknown rows, yet a footer:\n%s", b.String())
	}
}

func TestScanTextInsightsEmptyStillShowsSection(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target135}, Extras{InsightsRan: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "upgrade insights: no deprecated API usage reported") {
		t.Fatalf("stdout:\n%s", b.String())
	}
}

func TestScanJSONInsightsKeyRules(t *testing.T) {
	decode := func(ex Extras) map[string]json.RawMessage {
		var b bytes.Buffer
		if err := ScanJSON(&b, apiscan.Result{Target: target135}, ex); err != nil {
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
	if err := ScanText(&c, apiscan.Result{Target: target135}, insightsSample(t)); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < c.n; n++ {
		if err := ScanText(&failAfter{n: n}, apiscan.Result{Target: target135}, insightsSample(t)); err == nil {
			t.Errorf("fail after %d of %d writes: want a write error", n, c.n)
		}
	}
}
