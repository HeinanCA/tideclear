package apiscan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/util/yaml"
)

// decodeManifest decodes every document in data. Documents decoded before a
// malformed one are kept as evidence; the error names the first bad document
// (0-based, like Origin) and means the rest of the file was not read.
// unverified counts documents that claim to be objects but lack usable
// apiVersion/kind evidence.
func decodeManifest(data []byte, rel string) (objs []Object, unverified int, err error) {
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for i := 0; ; i++ {
		var doc any
		if derr := dec.Decode(&doc); derr != nil {
			if errors.Is(derr, io.EOF) {
				return objs, unverified, nil
			}
			return objs, unverified, fmt.Errorf("malformed YAML in document %d: %s", i, oneLine(derr.Error()))
		}
		extract(doc, fmt.Sprintf("%s:%d", rel, i), &objs, &unverified)
	}
}

// extract turns one decoded document (or List item) into evidence. A top-level
// sequence is expanded item by item (origin "<doc>/<j>"). Non-map items and maps
// with neither apiVersion nor kind are not objects and are ignored; a map with
// only one of them, or a non-string value, is unverified.
func extract(doc any, origin string, objs *[]Object, unverified *int) {
	if seq, ok := doc.([]any); ok {
		for j, item := range seq {
			extract(item, fmt.Sprintf("%s/%d", origin, j), objs, unverified)
		}
		return
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return
	}
	avRaw, hasAV := m["apiVersion"]
	kindRaw, hasKind := m["kind"]
	if !hasAV && !hasKind {
		return
	}
	apiVersion, _ := avRaw.(string)
	kind, _ := kindRaw.(string)
	if items, isList := m["items"].([]any); isList && strings.HasSuffix(kind, "List") {
		for j, item := range items {
			extract(inheritTypeMeta(item, apiVersion, kind), fmt.Sprintf("%s/%d", origin, j), objs, unverified)
		}
		return
	}
	if apiVersion == "" || kind == "" {
		*unverified++
		return
	}
	meta, _ := m["metadata"].(map[string]any)
	ns, _ := meta["namespace"].(string)
	name, _ := meta["name"].(string)
	*objs = append(*objs, Object{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name, Origin: origin})
}

// inheritTypeMeta fills a typed list item's missing apiVersion and kind from
// the list (a PodDisruptionBudgetList item is a PodDisruptionBudget). A
// generic "List" says nothing about its items. The item is copied, not mutated.
func inheritTypeMeta(item any, listAPIVersion, listKind string) any {
	m, ok := item.(map[string]any)
	if !ok || listKind == "List" {
		return item
	}
	out := make(map[string]any, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	if _, has := out["apiVersion"]; !has && listAPIVersion != "" {
		out["apiVersion"] = listAPIVersion
	}
	if _, has := out["kind"]; !has {
		out["kind"] = strings.TrimSuffix(listKind, "List")
	}
	return out
}
