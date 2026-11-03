package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/rules"
)

func fieldRule(t *testing.T) rules.FieldRule {
	t.Helper()
	set, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.FieldRules() {
		if r.Path == "spec.externalIPs" {
			return r
		}
	}
	t.Fatal("no externalIPs rule")
	return rules.FieldRule{}
}

func fieldResult(t *testing.T, path string) apiscan.Result {
	r := fieldRule(t)
	origin := "live"
	if path != "" {
		origin = "svc.yaml:0"
	}
	return apiscan.Result{
		Target: rules.Version{Major: 1, Minor: 36},
		Path:   path,
		FieldFindings: []apiscan.FieldFinding{{
			Hit:      apiscan.FieldHit{Kind: "Service", Namespace: "demo", Name: "web", Origin: origin, Rule: r},
			Severity: rules.SeverityWarning,
		}},
	}
}

func TestScanTextFieldFindings(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, fieldResult(t, ""), Extras{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, w := range []string{"KIND", "FIELD", "DEPRECATED IN", "REMOVED IN", "HINT", "SEVERITY",
		"Service", "demo", "web", "spec.externalIPs", "1.36", "LoadBalancer", "WARNING"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "no findings") {
		t.Errorf("field finding reported as clean:\n%s", out)
	}
	if strings.Contains(out, "ORIGIN") {
		t.Errorf("live scan shows ORIGIN:\n%s", out)
	}
}

func TestScanTextFieldFindingsPathShowsOrigin(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, fieldResult(t, "m/"), Extras{}); err != nil {
		t.Fatal(err)
	}
	if out := b.String(); !strings.Contains(out, "ORIGIN") || !strings.Contains(out, "svc.yaml:0") {
		t.Errorf("path scan lacks origin:\n%s", out)
	}
}

func TestScanJSONFieldFindings(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, fieldResult(t, ""), Extras{}); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Findings      []any `json:"findings"`
		FieldFindings []struct {
			Kind, Namespace, Name, Field, Replacement, Severity, Source, Origin string
			DeprecatedIn                                                        *string
			RemovedIn                                                           *string
		} `json:"fieldFindings"`
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.FieldFindings) != 1 || len(doc.Findings) != 0 {
		t.Fatalf("doc = %s", b.String())
	}
	f := doc.FieldFindings[0]
	if f.Kind != "Service" || f.Namespace != "demo" || f.Name != "web" || f.Field != "spec.externalIPs" || f.Severity != "WARNING" ||
		f.DeprecatedIn == nil || *f.DeprecatedIn != "1.36" || f.RemovedIn != nil || f.Origin != "live" ||
		f.Source != "https://www.kubernetes.dev/resources/keps/5707/" || !strings.Contains(f.Replacement, "LoadBalancer") {
		t.Fatalf("finding = %+v", f)
	}
}

func TestScanJSONFieldFindingsAlwaysAnArray(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, apiscan.Result{Target: rules.Version{Major: 1, Minor: 36}}, Extras{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"fieldFindings": []`) {
		t.Fatalf("no empty fieldFindings array:\n%s", b.String())
	}
}
