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

//go:embed data/fields.yaml
var rawFields []byte

// FieldRule is one deprecated field of a kind. A nil RemovedIn means no removal is scheduled.
type FieldRule struct {
	Group, Kind, Path string
	DeprecatedIn      Version
	RemovedIn         *Version
	Replacement       string
	Source            string
}

type rawFieldRule struct {
	Group        string `json:"group"`
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	DeprecatedIn string `json:"deprecated-in"`
	RemovedIn    string `json:"removed-in"`
	Replacement  string `json:"replacement"`
	Source       string `json:"source"`
}

// parseFields strictly decodes b: an unknown key, a missing required field,
// a bad version, removed-in before deprecated-in, or a duplicate rule is an error.
func parseFields(b []byte) ([]FieldRule, error) {
	j, err := yaml.ToJSON(b)
	if err != nil {
		return nil, fmt.Errorf("convert yaml: %w", err)
	}
	var raw struct {
		Fields []rawFieldRule `json:"fields"`
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw.Fields) == 0 {
		return nil, errors.New("at least one field rule is required")
	}
	seen := map[string]bool{}
	out := make([]FieldRule, 0, len(raw.Fields))
	for i, rr := range raw.Fields {
		r, err := toFieldRule(rr)
		if err != nil {
			return nil, fmt.Errorf("field rule %d (%s %s): %w", i, rr.Kind, rr.Path, err)
		}
		k := r.Group + "/" + r.Kind + "/" + r.Path
		if seen[k] {
			return nil, fmt.Errorf("field rule %d (%s %s): duplicate rule", i, rr.Kind, rr.Path)
		}
		seen[k] = true
		out = append(out, r)
	}
	return out, nil
}

func toFieldRule(rr rawFieldRule) (FieldRule, error) {
	switch {
	case rr.Kind == "":
		return FieldRule{}, errors.New("kind: must not be empty")
	case rr.Path == "" || strings.Contains(rr.Path, "..") || strings.HasPrefix(rr.Path, ".") || strings.HasSuffix(rr.Path, "."):
		return FieldRule{}, errors.New("path: must be dotted names with no empty segment")
	case rr.Source == "":
		return FieldRule{}, errors.New("source: must not be empty")
	case rr.DeprecatedIn == "":
		return FieldRule{}, errors.New("deprecated-in: is required")
	}
	dep, err := ParseVersion(rr.DeprecatedIn)
	if err != nil {
		return FieldRule{}, fmt.Errorf("deprecated-in: %w", err)
	}
	r := FieldRule{Group: rr.Group, Kind: rr.Kind, Path: rr.Path, DeprecatedIn: dep, Replacement: rr.Replacement, Source: rr.Source}
	if rr.RemovedIn != "" {
		rem, err := ParseVersion(rr.RemovedIn)
		if err != nil {
			return FieldRule{}, fmt.Errorf("removed-in: %w", err)
		}
		if rem.before(dep) {
			return FieldRule{}, fmt.Errorf("removed-in %s: is before deprecated-in %s", rem, dep)
		}
		r.RemovedIn = &rem
	}
	return r, nil
}

// Severity ranks the rule at target: BLOCKER once removed, WARNING once only
// deprecated, and false before deprecated-in (the rule does not apply yet).
func (r FieldRule) Severity(target Version) (Severity, bool) {
	switch {
	case r.RemovedIn != nil && !target.before(*r.RemovedIn):
		return SeverityBlocker, true
	case !target.before(r.DeprecatedIn):
		return SeverityWarning, true
	}
	return "", false
}

// FieldRules returns the field-level rules. They are separate from Kinds():
// a field rule says nothing about an apiVersion.
func (s *Set) FieldRules() []FieldRule { return s.fields }
