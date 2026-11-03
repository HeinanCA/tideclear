package apiscan

import (
	"fmt"
	"io"
	"strings"

	"github.com/HeinanCA/tideclear/internal/rules"
)

// FieldHit is an object that sets a field a rule deprecates.
type FieldHit struct {
	Group, Kind, Namespace, Name string
	// Origin is "live" for a cluster read, "path:docIndex" for a manifest.
	Origin string
	Rule   rules.FieldRule
}

// FieldFinding is a FieldHit that applies at the scan target.
type FieldFinding struct {
	Hit      FieldHit
	Severity rules.Severity
}

// fieldHits returns one hit per rule for group/kind whose field is set on obj,
// and a note per rule it could not check because the path runs through a non-map.
func fieldHits(rs []rules.FieldRule, group, kind, ns, name, origin string, obj map[string]any) (hits []FieldHit, unchecked []string) {
	for _, r := range rs {
		if r.Group != group || r.Kind != kind {
			continue
		}
		set, readable := fieldSet(obj, r.Path)
		if !readable {
			unchecked = append(unchecked, fmt.Sprintf("%s: %s %s: %s not checked: the path runs through a value that is not a map", origin, kind, objRef(ns, name), r.Path))
		}
		if set {
			hits = append(hits, FieldHit{Group: group, Kind: kind, Namespace: ns, Name: name, Origin: origin, Rule: r})
		}
	}
	return hits, unchecked
}

// objRef is "ns/name", or just "name" for a cluster-scoped object.
func objRef(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

// fieldSet reports whether the dotted path holds a non-empty value. nil, "",
// and an empty list or map count as unset. readable is false when the path
// runs through a non-map, non-null value: whether the field is set is unknown.
func fieldSet(obj map[string]any, path string) (set, readable bool) {
	var cur any = obj
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false, cur == nil
		}
		if cur, ok = m[seg]; !ok {
			return false, true
		}
	}
	switch v := cur.(type) {
	case nil:
		return false, true
	case string:
		return v != "", true
	case []any:
		return len(v) > 0, true
	case map[string]any:
		return len(v) > 0, true
	}
	return true, true
}

// NewManifestSourceWithRules is NewManifestSource that also reports the
// fields set.FieldRules() deprecates.
func NewManifestSourceWithRules(root string, warn io.Writer, set *rules.Set) Source {
	return manifestSource{root: root, warn: warn, fields: set.FieldRules()}
}
