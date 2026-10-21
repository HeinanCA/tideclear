package demo

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
)

var fixedNow = time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)

func verdictRows(ex report.Extras, v reconcile.Verdict) []reconcile.Row {
	var out []reconcile.Row
	for _, r := range ex.Insights {
		if r.Verdict == v {
			out = append(out, r)
		}
	}
	return out
}

func TestSampleShape(t *testing.T) {
	res, ex, err := Sample(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Target.String(); got != "1.32" {
		t.Errorf("target = %s, want 1.32", got)
	}
	if res.Cluster.Context != "sample-context" || res.Cluster.EKSCluster != "sample-cluster" || res.Cluster.Server != "" {
		t.Errorf("cluster = %+v, want a fictional sample identity and no server", res.Cluster)
	}
	if len(res.Findings) != 1 || res.Findings[0].Finding.Severity != rules.SeverityBlocker {
		t.Errorf("findings = %+v, want exactly one BLOCKER", res.Findings)
	}
	if got := len(ex.Insights); got != 2 {
		t.Errorf("insight rows = %d, want 2", got)
	}
	if got := len(verdictRows(ex, reconcile.VerdictBlocker)); got != 1 {
		t.Errorf("BLOCKER verdict rows = %d, want exactly 1", got)
	}
	stale := verdictRows(ex, reconcile.VerdictStale)
	if len(stale) != 1 || stale[0].ClearsOn == nil {
		t.Fatalf("STALE rows = %+v, want exactly one with a clears date", stale)
	}
	if got, want := stale[0].ClearsOn.Format("2006-01-02"), "2026-11-09"; got != want {
		t.Errorf("clears = %s, want %s", got, want)
	}
	incompatible := 0
	for _, a := range ex.Addons {
		if a.Status == addons.StatusIncompatible {
			incompatible++
		}
	}
	if len(ex.Addons) != 2 || incompatible != 1 {
		t.Errorf("add-on rows = %+v, want 2 rows with exactly 1 INCOMPATIBLE", ex.Addons)
	}
	if len(ex.Nodes) != 1 {
		t.Errorf("node rows = %+v, want exactly 1", ex.Nodes)
	}
	if len(res.Skipped) != 0 || len(ex.Skipped) != 0 {
		t.Errorf("sample must not show skipped groups or checks: %+v %+v", res.Skipped, ex.Skipped)
	}
}

// The insight verdicts depend on the clock: a refresh older than 7 days turns
// the rows UNKNOWN. The sample is built from offsets, so any run day gives the
// same verdicts.
func TestSampleVerdictsHoldOnAnyRunDay(t *testing.T) {
	for _, offset := range []time.Duration{0, 24 * time.Hour, 400 * 24 * time.Hour, -30 * 24 * time.Hour} {
		now := fixedNow.Add(offset)
		_, ex, err := Sample(now)
		if err != nil {
			t.Fatal(err)
		}
		if len(verdictRows(ex, reconcile.VerdictBlocker)) != 1 || len(verdictRows(ex, reconcile.VerdictStale)) != 1 || len(verdictRows(ex, reconcile.VerdictUnknown)) != 0 {
			t.Errorf("now %s: verdicts changed: %+v", now, ex.Insights)
		}
		stale := verdictRows(ex, reconcile.VerdictStale)[0]
		if !stale.ClearsOn.After(now) {
			t.Errorf("now %s: clears %s is not in the future", now, stale.ClearsOn)
		}
	}
}

func TestSampleIsUTCAndDeterministic(t *testing.T) {
	zone := time.FixedZone("far", 13*3600)
	_, a, err := Sample(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := Sample(fixedNow.In(zone))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := b.Insights[1].ClearsOn.Location(), time.UTC; got != want {
		t.Errorf("clears zone = %s, want UTC", got)
	}
	if !a.Insights[1].ClearsOn.Equal(*b.Insights[1].ClearsOn) {
		t.Errorf("clears differs with the clock's zone: %s vs %s", a.Insights[1].ClearsOn, b.Insights[1].ClearsOn)
	}
}

func removedAt(set *rules.Set, apiVersion string, target rules.Version) bool {
	group, _, _ := strings.Cut(apiVersion, "/")
	for _, gk := range set.Kinds() {
		if gk.Group != group {
			continue
		}
		if f, ok := set.Match(apiVersion, gk.Kind, target); ok && f.Rule.RemovedIn != nil && *f.Rule.RemovedIn == target {
			return true
		}
	}
	return false
}

// A "removed in vX" insight may only list APIs the rules data removes in X, so
// the sample cannot show a row the tool cannot produce.
func TestSampleInsightRowsAreRemovedAtTarget(t *testing.T) {
	set, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	res, ex, err := Sample(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ex.Insights {
		if !removedAt(set, r.APIVersion, res.Target) {
			t.Errorf("%s is not removed in %s per the rules data", r.APIVersion, res.Target)
		}
	}
}

// The demo's Extras flags must be what a real `scan --insights --cluster` sets
// when every check ran: both sections shown, refresh staleness from the
// insights' own refresh times.
func TestSampleExtrasFlagsMatchARealInsightsScan(t *testing.T) {
	_, ex, err := Sample(fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.InsightsRan || !ex.AddonsRan {
		t.Errorf("InsightsRan=%v AddonsRan=%v, a real --insights scan sets both", ex.InsightsRan, ex.AddonsRan)
	}
	if want := report.RefreshStale(sampleInsights(fixedNow), fixedNow); ex.RefreshStale != want {
		t.Errorf("RefreshStale = %v, want %v as report.RefreshStale computes", ex.RefreshStale, want)
	}
}

// The real proof of "no network, AWS or kubeconfig" is the cli test that runs
// the command with fatal Deps, a failing HTTP transport and bogus AWS and
// KUBECONFIG settings. This guard only sees direct imports (nodes already
// links client-go transitively), so it is a tripwire, not a proof.
func TestDemoPackageDoesNotImportClientPackages(t *testing.T) {
	banned := []string{
		"github.com/aws/aws-sdk-go-v2/config",
		"github.com/aws/aws-sdk-go-v2/credentials",
		"net",
		"net/http",
		"os",
		"os/exec",
		"k8s.io/client-go",
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources found: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range banned {
				if path == b || strings.HasPrefix(path, b+"/") {
					t.Errorf("%s imports %s", f, path)
				}
			}
		}
	}
}
