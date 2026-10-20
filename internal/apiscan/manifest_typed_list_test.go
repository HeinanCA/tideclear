package apiscan

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestTypedListItemsInheritListTypeMeta(t *testing.T) {
	res, _ := scanManifests(t, filepath.Join(manifestDir("list"), "typed.yaml"), t137)
	want := "PodDisruptionBudget policy/v1beta1 api BLOCKER typed.yaml:0/0"
	if got := strings.Join(hits(res), "|"); got != want || res.Unverified != 0 {
		t.Fatalf("got %q unverified=%d, want %q", got, res.Unverified, want)
	}
}

func TestManifestTypedListKeepsItemsOwnTypeMeta(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "l.yaml"), "apiVersion: v1\nkind: PodList\nitems:\n  - apiVersion: policy/v1beta1\n    kind: PodDisruptionBudget\n    metadata:\n      name: web\n")
	res, _ := scanManifests(t, dir, t137)
	if len(res.Findings) != 1 || res.Findings[0].Object.Name != "web" {
		t.Fatalf("findings = %v", hits(res))
	}
}
