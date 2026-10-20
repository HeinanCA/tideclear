package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/rules"
)

func addonsSample() Extras {
	return Extras{
		Addons: []addons.Row{
			{Name: "demo-dns", Current: "v1.1.0-eksbuild.1", Status: addons.StatusOK},
			{Name: "demo-net", Current: "v1.0.0-eksbuild.1", MinCompatible: "v1.2.0-eksbuild.1", Status: addons.StatusIncompatible,
				Severity: rules.SeverityWarning, InsightNote: "insight lists this version as compatible; direct check says not"},
			{Name: "demo-agent", Current: "v3.0.0", Status: addons.StatusUnknown, Reason: "no version of this add-on lists Kubernetes 1.32 as compatible"},
		},
		Skipped: []CheckSkipped{{Check: "nodes", Reason: "access denied"}},
	}
}

func TestScanTextAddonsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, scanSample(t), addonsSample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-addons.txt", b.Bytes())
}

func TestScanJSONAddonsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, scanSample(t), addonsSample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-addons.json", b.Bytes())
}

func TestAddonsOmittedWhenEmpty(t *testing.T) {
	var txt, js bytes.Buffer
	ex := Extras{Addons: []addons.Row{}}
	if err := ScanText(&txt, scanSample(t), ex); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, scanSample(t), ex); err != nil {
		t.Fatal(err)
	}
	var plainTxt, plainJS bytes.Buffer
	_ = ScanText(&plainTxt, scanSample(t), Extras{})
	_ = ScanJSON(&plainJS, scanSample(t), Extras{})
	if !bytes.Equal(txt.Bytes(), plainTxt.Bytes()) || !bytes.Equal(js.Bytes(), plainJS.Bytes()) {
		t.Errorf("empty add-ons changed the output:\n%s\n%s", txt.String(), js.String())
	}
}

func TestAddonsRanWithNoneSaysSo(t *testing.T) {
	var txt, js bytes.Buffer
	ex := Extras{AddonsRan: true}
	if err := ScanText(&txt, scanSample(t), ex); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, scanSample(t), ex); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt.String(), "add-ons: none managed by EKS (self-managed add-ons are not checked)\n") {
		t.Errorf("text lacks the none line:\n%s", txt.String())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(js.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if string(m["addons"]) != "[]" {
		t.Errorf("addons = %s, want []", m["addons"])
	}
}
