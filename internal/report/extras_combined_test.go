package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/reconcile"
)

func fullSample() Extras {
	ex := insightsSample()
	ex.Addons, ex.AddonsRan = addonsSample().Addons, true
	ex.Nodes = nodesSample().Nodes
	return ex
}

// degradedSample is a --cluster --insights scan where every extra read was denied.
func degradedSample() Extras {
	const denied = "access denied"
	return Extras{
		InsightsRan: true,
		Insights:    []reconcile.Row{reconcile.Unavailable(denied)},
		Skipped: []CheckSkipped{
			{Check: "insights", Reason: denied}, {Check: "addons", Reason: denied},
			{Check: "nodegroups", Reason: denied}, {Check: "nodes", Reason: denied},
		},
	}
}

func TestScanFullGolden(t *testing.T) {
	var txt, js bytes.Buffer
	if err := ScanText(&txt, scanSample(t), fullSample()); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, scanSample(t), fullSample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-full.txt", txt.Bytes())
	golden(t, "scan-full.json", js.Bytes())
}

func TestScanDegradedGolden(t *testing.T) {
	var txt, js bytes.Buffer
	if err := ScanText(&txt, scanSample(t), degradedSample()); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, scanSample(t), degradedSample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-degraded.txt", txt.Bytes())
	golden(t, "scan-degraded.json", js.Bytes())
}

func TestScanTextSectionOrderAndSpacing(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, scanSample(t), fullSample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	last := -1
	for _, head := range []string{"KIND ", "\n\nupgrade insights:\n", "\n\nadd-ons:\n", "\n\nnodes:\n", "result not clean"} {
		i := strings.Index(out, head)
		if i <= last {
			t.Fatalf("%q out of order or missing (at %d, previous %d):\n%s", head, i, last, out)
		}
		last = i
	}
	b.Reset()
	if err := ScanText(&b, scanSample(t), Extras{}); err != nil || strings.Contains(b.String(), "\n\n") {
		t.Fatalf("plain scan must have no blank line: err=%v\n%s", err, b.String())
	}
}

func TestScanTextFooterCounts(t *testing.T) {
	skip := []CheckSkipped{{Check: "a", Reason: "r"}, {Check: "b", Reason: "r"}}
	unknown := []reconcile.Row{{Verdict: reconcile.VerdictUnknown}}
	for _, tc := range []struct {
		name string
		ex   Extras
		want string // empty: no footer
	}{
		{"unknown only", Extras{InsightsRan: true, Insights: unknown}, "result not clean: 1 unknown\n"},
		{"skipped only", Extras{Skipped: skip}, "result not clean: 2 checks skipped\n"},
		{"both", Extras{InsightsRan: true, Insights: unknown, Skipped: skip}, "result not clean: 1 unknown, 2 checks skipped\n"},
		{"neither", Extras{InsightsRan: true}, ""},
	} {
		var b bytes.Buffer
		if err := ScanText(&b, scanSample(t), tc.ex); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if tc.want == "" && strings.Contains(out, "result not clean") {
			t.Errorf("%s: unexpected footer:\n%s", tc.name, out)
		}
		if tc.want != "" && !strings.HasSuffix(out, tc.want) {
			t.Errorf("%s: want final line %q:\n%s", tc.name, tc.want, out)
		}
		if tc.name == "both" && strings.Count(out, "check skipped: ") != 2 {
			t.Errorf("per-check lines must stay:\n%s", out)
		}
	}
}

func TestScanJSONKeyPresence(t *testing.T) {
	keys := func(ex Extras) string {
		var b bytes.Buffer
		if err := ScanJSON(&b, scanSample(t), ex); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	ran := keys(Extras{InsightsRan: true, AddonsRan: true})
	for _, k := range []string{`"insights": []`, `"addons": []`, `"schemaVersion": 1`} {
		if !strings.Contains(ran, k) {
			t.Errorf("checks ran empty: missing %s", k)
		}
	}
	for _, k := range []string{`"nodes"`, `"checksSkipped"`} {
		if strings.Contains(ran, k) {
			t.Errorf("empty %s must be omitted", k)
		}
	}
	plain := keys(Extras{})
	for _, k := range []string{`"insights"`, `"addons"`, `"nodes"`, `"checksSkipped"`} {
		if strings.Contains(plain, k) {
			t.Errorf("zero Extras added %s", k)
		}
	}
}
