package rules

import (
	"strings"
	"testing"
)

const goodFields = `
fields:
  - group: ""
    kind: Service
    path: spec.externalIPs
    deprecated-in: "1.36"
    removed-in: ""
    replacement: use a LoadBalancer
    source: https://example.test/kep
`

func TestParseFieldsStrict(t *testing.T) {
	rs, err := parseFields([]byte(goodFields))
	if err != nil || len(rs) != 1 {
		t.Fatalf("rules=%v err=%v", rs, err)
	}
	r := rs[0]
	if r.Group != "" || r.Kind != "Service" || r.Path != "spec.externalIPs" || r.DeprecatedIn != (Version{1, 36}) ||
		r.RemovedIn != nil || r.Replacement != "use a LoadBalancer" || r.Source != "https://example.test/kep" {
		t.Fatalf("rule = %+v", r)
	}
	one := func(s string) string { return "fields:\n  - kind: Service\n    path: spec.x\n    source: s\n" + s }
	bad := []struct{ name, in, want string }{
		{"empty file", "", "at least one"},
		{"unknown key", one("    deprecated-in: \"1.36\"\n    bogus: 1\n"), "unknown field"},
		{"no kind", "fields:\n  - path: spec.x\n    deprecated-in: \"1.36\"\n    source: s\n", "kind"},
		{"no path", "fields:\n  - kind: Service\n    deprecated-in: \"1.36\"\n    source: s\n", "path"},
		{"empty path segment", "fields:\n  - kind: Service\n    path: spec..x\n    deprecated-in: \"1.36\"\n    source: s\n", "path"},
		{"no source", "fields:\n  - kind: Service\n    path: spec.x\n    deprecated-in: \"1.36\"\n", "source"},
		{"no deprecated-in", one(""), "deprecated-in"},
		{"bad version", one("    deprecated-in: banana\n"), "deprecated-in"},
		{"bad removed-in", one("    deprecated-in: \"1.36\"\n    removed-in: x\n"), "removed-in"},
		{"removed before deprecated", one("    deprecated-in: \"1.36\"\n    removed-in: \"1.35\"\n"), "before"},
		{"duplicate", one("    deprecated-in: \"1.36\"\n") + "  - kind: Service\n    path: spec.x\n    deprecated-in: \"1.36\"\n    source: s\n", "duplicate"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFields([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestFieldRuleSeverity(t *testing.T) {
	v := func(m int) *Version { return &Version{1, m} }
	cases := []struct {
		name      string
		deprecate Version
		removed   *Version
		target    Version
		sev       Severity
		applies   bool
	}{
		{"below deprecated-in", Version{1, 36}, nil, Version{1, 35}, "", false},
		{"at deprecated-in, no removal", Version{1, 36}, nil, Version{1, 36}, SeverityWarning, true},
		{"above deprecated-in, no removal", Version{1, 36}, nil, Version{1, 40}, SeverityWarning, true},
		{"between deprecated and removed", Version{1, 36}, v(38), Version{1, 37}, SeverityWarning, true},
		{"at removed-in", Version{1, 36}, v(38), Version{1, 38}, SeverityBlocker, true},
		{"next major", Version{1, 36}, nil, Version{2, 0}, SeverityWarning, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sev, ok := FieldRule{DeprecatedIn: tc.deprecate, RemovedIn: tc.removed}.Severity(tc.target)
			if sev != tc.sev || ok != tc.applies {
				t.Fatalf("got %q,%v want %q,%v", sev, ok, tc.sev, tc.applies)
			}
		})
	}
}

func TestLoadIncludesExternalIPsRule(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range s.FieldRules() {
		if r.Group == "" && r.Kind == "Service" && r.Path == "spec.externalIPs" {
			found = true
			if r.DeprecatedIn != (Version{1, 36}) || r.RemovedIn != nil || r.Source != "https://www.kubernetes.dev/resources/keps/5707/" || r.Replacement == "" {
				t.Fatalf("rule = %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("no spec.externalIPs rule in %v", s.FieldRules())
	}
}

func TestFieldKindsAreSeparateFromAPIKinds(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, gk := range s.Kinds() {
		if gk == (GroupKind{Kind: "Service"}) {
			t.Fatal("Kinds() must stay apiVersion-only; Service is a field-rule kind")
		}
	}
}
