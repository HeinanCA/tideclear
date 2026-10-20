package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
)

// failAfter accepts n writes, then fails every write.
type failAfter struct{ n int }

var errWrite = errors.New("write failed")

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errWrite
	}
	f.n--
	return len(p), nil
}

func TestScanTextReturnsFirstWriteError(t *testing.T) {
	cases := map[string]apiscan.Result{
		"header":  {Path: "p", Target: target138},
		"summary": {Path: "p", Target: target138},
		"footer":  {Path: "p", Target: target138, Unverified: 2},
	}
	okWrites := map[string]int{"header": 0, "summary": 1, "footer": 2}
	for name, r := range cases {
		if err := ScanText(&failAfter{n: okWrites[name]}, r, Extras{}); !errors.Is(err, errWrite) {
			t.Errorf("%s: err = %v, want write error", name, err)
		}
	}
}

func TestScanTextSkippedWarningWriteError(t *testing.T) {
	r := apiscan.Result{Path: "p", Target: target138, Skipped: []apiscan.Skipped{{Group: "g"}}}
	if err := ScanText(&failAfter{n: 1}, r, Extras{}); !errors.Is(err, errWrite) {
		t.Fatalf("err = %v, want write error", err)
	}
}

func TestScanReportsResolvedReplacement(t *testing.T) {
	r := apiscan.Result{Path: "p", Target: target138, Findings: []apiscan.ResultItem{
		item(t, "flowcontrol.apiserver.k8s.io/v1beta1", "FlowSchema", "", "web", "f.yaml:0"),
	}}
	var txt, js bytes.Buffer
	if err := ScanText(&txt, r, Extras{}); err != nil {
		t.Fatal(err)
	}
	if err := ScanJSON(&js, r, Extras{}); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": txt.String(), "json": js.String()} {
		if !strings.Contains(out, "flowcontrol.apiserver.k8s.io/v1\n") && !strings.Contains(out, `"flowcontrol.apiserver.k8s.io/v1"`) && !strings.Contains(out, "flowcontrol.apiserver.k8s.io/v1 ") {
			t.Errorf("%s lacks the v1 replacement:\n%s", name, out)
		}
		if strings.Contains(out, "v1beta3") {
			t.Errorf("%s still names v1beta3:\n%s", name, out)
		}
	}
}
