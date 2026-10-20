package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/insights"
)

var insightsNow = time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)

func sampleInsights(age time.Duration) []insights.Insight {
	at := insightsNow.Add(-age)
	return []insights.Insight{
		{ID: "i1", Name: "Deprecated API usage", Target: "1.32", Status: insights.StatusError, Reason: "deprecated APIs in use", LastRefresh: at},
		{ID: "i2", Name: "Add-on compatibility", Target: "1.32", Status: insights.StatusPassing, LastRefresh: at},
		{ID: "i3", Name: "Kubelet version skew", Target: "1.32", Status: insights.StatusUnknown},
	}
}

func insightsText(t *testing.T, ins []insights.Insight) string {
	t.Helper()
	var b bytes.Buffer
	if err := InsightsText(&b, ins, insightsNow); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestInsightsTextGolden(t *testing.T) {
	golden(t, "insights.txt", []byte(insightsText(t, sampleInsights(14*time.Hour))))
}

func TestInsightsTextStaleGolden(t *testing.T) {
	golden(t, "insights-stale.txt", []byte(insightsText(t, sampleInsights(30*time.Hour))))
}

func TestInsightsTextFreshness(t *testing.T) {
	const hint = "Refresh insights"
	cases := []struct {
		age      time.Duration
		want     string
		wantHint bool
	}{
		{14 * time.Hour, "14h ago", false},
		{24 * time.Hour, "24h ago", false}, // exactly 24h is not yet stale
		{24*time.Hour + time.Minute, "24h ago", true},
		{30 * time.Minute, "30m ago", false},
		{10 * time.Second, "just now", false},
		{72 * time.Hour, "3d ago", true},
	}
	for _, c := range cases {
		out := insightsText(t, sampleInsights(c.age))
		if !strings.Contains(out, c.want) {
			t.Errorf("age %v: want %q in\n%s", c.age, c.want, out)
		}
		if got := strings.Contains(out, hint); got != c.wantHint {
			t.Errorf("age %v: hint present = %v, want %v", c.age, got, c.wantHint)
		}
	}
}

func TestInsightsTextEmpty(t *testing.T) {
	if got := insightsText(t, nil); got != "no insights found\n" {
		t.Fatalf("got %q", got)
	}
}

func TestInsightsJSONGolden(t *testing.T) {
	var b bytes.Buffer
	if err := InsightsJSON(&b, sampleInsights(14*time.Hour)); err != nil {
		t.Fatal(err)
	}
	golden(t, "insights.json", b.Bytes())
}

func TestInsightsJSONEmptyArray(t *testing.T) {
	var b bytes.Buffer
	if err := InsightsJSON(&b, nil); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"schemaVersion\": 1,\n  \"insights\": []\n}\n"
	if b.String() != want {
		t.Fatalf("got %q", b.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestInsightsWriteErrors(t *testing.T) {
	if err := InsightsText(failWriter{}, sampleInsights(time.Hour), insightsNow); err == nil {
		t.Error("InsightsText swallowed write error")
	}
	if err := InsightsJSON(failWriter{}, sampleInsights(time.Hour)); err == nil {
		t.Error("InsightsJSON swallowed write error")
	}
}
