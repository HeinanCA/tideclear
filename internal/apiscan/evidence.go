package apiscan

import (
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// evidence returns one Object per distinct apiVersion u was written with:
// the last-applied annotation, and the newest main-resource managedFields
// entry (status/scale entries are ignored so a controller's later status
// write cannot mask the apply; ties all count, a missing time is oldest).
func evidence(u *unstructured.Unstructured, kind, resource string) []Object {
	var out []Object
	seen := map[string]bool{}
	add := func(apiVersion, origin string) {
		if apiVersion == "" || seen[apiVersion] {
			return
		}
		seen[apiVersion] = true
		out = append(out, Object{APIVersion: apiVersion, Kind: kind, Namespace: u.GetNamespace(), Name: u.GetName(), Origin: origin, Resource: resource})
	}
	add(lastAppliedAPIVersion(u), originLastApplied)
	for _, v := range newestMainAPIVersions(u.GetManagedFields()) {
		add(v, originManagedFields)
	}
	return out
}

func lastAppliedAPIVersion(u *unstructured.Unstructured) string {
	raw, ok := u.GetAnnotations()[lastAppliedAnnotation]
	if !ok {
		return ""
	}
	var cfg struct {
		APIVersion string `json:"apiVersion"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return ""
	}
	return cfg.APIVersion
}

func newestMainAPIVersions(entries []metav1.ManagedFieldsEntry) []string {
	var newest []string
	var best time.Time // zero = missing time = oldest
	for _, e := range entries {
		if e.Subresource != "" || e.APIVersion == "" {
			continue
		}
		var t time.Time
		if e.Time != nil {
			t = e.Time.Time
		}
		switch {
		case len(newest) == 0 || t.After(best):
			newest, best = []string{e.APIVersion}, t
		case t.Equal(best):
			newest = append(newest, e.APIVersion)
		}
	}
	return newest
}
