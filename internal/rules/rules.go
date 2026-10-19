package rules

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/yaml"
)

//go:generate go run ./gen

//go:embed data/versions.yaml
var rawVersions []byte

//go:embed data/k8s-lifecycle.yaml
var rawLifecycle []byte

const componentK8s = "k8s"

// Severity ranks a finding.
type Severity string

const (
	SeverityBlocker Severity = "BLOCKER"
	SeverityWarning Severity = "WARNING"
)

// Rule is one deprecated/removed API. A nil RemovedIn means not yet scheduled for removal.
type Rule struct {
	APIVersion    string
	Kind          string
	DeprecatedIn  *Version
	RemovedIn     *Version
	Replacement   string
	ReplacementIn *Version
	Component     string
	Source        string
}

// Finding is a Rule that applies to a target version.
type Finding struct {
	Rule     Rule
	Severity Severity
	// Replacement is Rule.Replacement followed through any replacement that is
	// itself removed at the target version.
	Replacement string
}

// GroupKind identifies a resource kind within an API group ("" is the core group).
type GroupKind struct{ Group, Kind string }

// Set is an immutable collection of rules.
type Set struct{ rules []Rule }

type rawRule struct {
	Version       string `json:"version"`
	Kind          string `json:"kind"`
	DeprecatedIn  string `json:"deprecated-in"`
	RemovedIn     string `json:"removed-in"`
	Replacement   string `json:"replacement-api"`
	ReplacementIn string `json:"replacement-available-in"`
	Component     string `json:"component"`
}

type rawFile struct {
	Deprecated []rawRule `json:"deprecated-versions"`
	// Pluto's per-component target versions; present upstream, unused here.
	TargetVersions json.RawMessage `json:"target-versions"`
}

// Load returns the embedded rules for component "k8s": Pluto data merged with the
// k8s.io/api lifecycle data, the latter winning per apiVersion+kind.
func Load() (*Set, error) {
	pluto, err := parseSource(rawVersions, SourcePluto)
	if err != nil {
		return nil, fmt.Errorf("pluto rules: %w", err)
	}
	life, err := parseSource(rawLifecycle, SourceK8sLifecycle)
	if err != nil {
		return nil, fmt.Errorf("k8s lifecycle rules: %w", err)
	}
	if _, err := LifecycleSourceVersion(); err != nil {
		return nil, fmt.Errorf("k8s lifecycle rules: %w", err)
	}
	return &Set{rules: merge(pluto, life)}, nil
}

// parse decodes Pluto-shaped yaml and keeps component "k8s".
func parse(b []byte) (*Set, error) {
	rs, err := parseSource(b, SourcePluto)
	if err != nil {
		return nil, err
	}
	return &Set{rules: rs}, nil
}

// parseSource strictly decodes b, keeps component "k8s" rules, and errors when none remain.
func parseSource(b []byte, source string) ([]Rule, error) {
	all, err := parseAll(b)
	if err != nil {
		return nil, err
	}
	var kept []Rule
	for _, r := range all {
		if r.Component == componentK8s {
			r.Source = source
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("no k8s rules found")
	}
	return kept, nil
}

func parseAll(b []byte) ([]Rule, error) {
	j, err := yaml.ToJSON(b)
	if err != nil {
		return nil, fmt.Errorf("convert rules yaml: %w", err)
	}
	var f rawFile
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode rules: %w", err)
	}
	out := make([]Rule, 0, len(f.Deprecated))
	for i, rr := range f.Deprecated {
		r, err := toRule(rr)
		if err != nil {
			return nil, fmt.Errorf("rule %d (%s %s): %w", i, rr.Version, rr.Kind, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func toRule(rr rawRule) (Rule, error) {
	r := Rule{APIVersion: rr.Version, Kind: rr.Kind, Replacement: rr.Replacement, Component: rr.Component}
	for _, f := range []struct {
		name string
		in   string
		dst  **Version
	}{
		{"deprecated-in", rr.DeprecatedIn, &r.DeprecatedIn},
		{"removed-in", rr.RemovedIn, &r.RemovedIn},
		{"replacement-available-in", rr.ReplacementIn, &r.ReplacementIn},
	} {
		if f.in == "" {
			continue
		}
		v, err := ParseVersion(f.in)
		if err != nil {
			return Rule{}, fmt.Errorf("%s: %w", f.name, err)
		}
		*f.dst = &v
	}
	if r.Component == componentK8s {
		if err := validateK8s(r); err != nil {
			return Rule{}, err
		}
	}
	return r, nil
}

func validateK8s(r Rule) error {
	switch {
	case r.APIVersion == "":
		return errors.New("version: must not be empty")
	case r.Kind == "":
		return errors.New("kind: must not be empty")
	case r.DeprecatedIn == nil && r.RemovedIn == nil:
		return errors.New("deprecated-in and removed-in: at least one is required")
	case r.DeprecatedIn != nil && r.RemovedIn != nil && r.RemovedIn.before(*r.DeprecatedIn):
		return fmt.Errorf("removed-in %s: is before deprecated-in %s", r.RemovedIn, r.DeprecatedIn)
	}
	return nil
}

// Match reports the most severe finding for apiVersion/kind at target, if any.
// removed-in <= target is a BLOCKER; deprecated-in <= target (and not removed) is a WARNING.
func (s *Set) Match(apiVersion, kind string, target Version) (Finding, bool) {
	var best Finding
	found := false
	for _, r := range s.rules {
		if r.APIVersion != apiVersion || r.Kind != kind {
			continue
		}
		var sev Severity
		switch {
		case r.RemovedIn != nil && !target.before(*r.RemovedIn) && !r.Projected():
			sev = SeverityBlocker
		case r.RemovedIn != nil && !target.before(*r.RemovedIn):
			sev = SeverityWarning
		case r.DeprecatedIn != nil && !target.before(*r.DeprecatedIn):
			sev = SeverityWarning
		default:
			continue
		}
		if !found || (sev == SeverityBlocker && best.Severity != SeverityBlocker) {
			best, found = Finding{Rule: r, Severity: sev, Replacement: s.resolveReplacement(r, target)}, true
		}
	}
	return best, found
}

const maxReplacementHops = 8

// resolveReplacement follows r's replacement while that apiVersion+kind is
// itself removed at target, so a v1beta1 object is not pointed at a v1beta3
// that no longer exists.
func (s *Set) resolveReplacement(r Rule, target Version) string {
	repl := r.Replacement
	seen := map[string]bool{r.APIVersion: true}
	for hop := 0; hop < maxReplacementHops && repl != "" && !seen[repl]; hop++ {
		seen[repl] = true
		next := ""
		for _, c := range s.rules {
			if c.APIVersion == repl && c.Kind == r.Kind && c.RemovedIn != nil && !target.before(*c.RemovedIn) && c.Replacement != "" {
				next = c.Replacement
				break
			}
		}
		if next == "" {
			break
		}
		repl = next
	}
	return repl
}

// Kinds returns the distinct group/kind pairs covered by the rules and by
// their replacements (same kind, replacement group), in first-seen order. The
// replacement pairs let objects of a removed group be listed through the
// current group and matched on their last-applied/managedFields evidence.
func (s *Set) Kinds() []GroupKind {
	seen := map[GroupKind]bool{}
	var out []GroupKind
	add := func(apiVersion, kind string) {
		group := ""
		if g, _, ok := strings.Cut(apiVersion, "/"); ok {
			group = g
		}
		gk := GroupKind{Group: group, Kind: kind}
		if !seen[gk] {
			seen[gk] = true
			out = append(out, gk)
		}
	}
	for _, r := range s.rules {
		add(r.APIVersion, r.Kind)
		if r.Replacement != "" {
			add(r.Replacement, r.Kind)
		}
	}
	return out
}
