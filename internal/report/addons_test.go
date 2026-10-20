package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/addons"
)

func TestScanTextAddonsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, w2Scan(t), addonsSample(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-addons.txt", b.Bytes())
}

func TestScanJSONAddonsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, w2Scan(t), addonsSample(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-addons.json", b.Bytes())
}

func TestAddonsOmittedWhenEmpty(t *testing.T) {
	var txt, js bytes.Buffer
	ex := Extras{Addons: []addons.Row{}}
	if err := ScanText(&txt, w2Scan(t), ex); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, w2Scan(t), ex); err != nil {
		t.Fatal(err)
	}
	var plainTxt, plainJS bytes.Buffer
	_ = ScanText(&plainTxt, w2Scan(t), Extras{})
	_ = ScanJSON(&plainJS, w2Scan(t), Extras{})
	if !bytes.Equal(txt.Bytes(), plainTxt.Bytes()) || !bytes.Equal(js.Bytes(), plainJS.Bytes()) {
		t.Errorf("empty add-ons changed the output:\n%s\n%s", txt.String(), js.String())
	}
}

func TestAddonsRanWithNoneSaysSo(t *testing.T) {
	var txt, js bytes.Buffer
	ex := Extras{AddonsRan: true}
	if err := ScanText(&txt, w2Scan(t), ex); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, w2Scan(t), ex); err != nil {
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
