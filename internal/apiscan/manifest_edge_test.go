package apiscan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pdbDoc = "apiVersion: policy/v1beta1\nkind: PodDisruptionBudget\nmetadata:\n  name: web\n"

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// denyAccess makes path unreadable until the test ends.
func denyAccess(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

func skipReasons(res Result) string {
	var out []string
	for _, s := range res.Skipped {
		out = append(out, s.Group+": "+s.Reason)
	}
	return strings.Join(out, "|")
}

func TestManifestListKindIsExpanded(t *testing.T) {
	res, _ := scanManifests(t, manifestDir("list"), t137)
	want := "PodDisruptionBudget policy/v1beta1 api BLOCKER typed.yaml:0/0|PodDisruptionBudget policy/v1beta1 web BLOCKER list.yaml:0/0"
	if got := strings.Join(hits(res), "|"); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestManifestTopLevelJSONArrayIsExpanded(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "a.json"), `[{"apiVersion":"policy/v1beta1","kind":"PodDisruptionBudget","metadata":{"name":"web"}}]`)
	res, _ := scanManifests(t, dir, t137)
	want := "PodDisruptionBudget policy/v1beta1 web BLOCKER a.json:0/0"
	if got := strings.Join(hits(res), "|"); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestManifestTopLevelYAMLListIsExpanded(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "b.yaml"), "- apiVersion: policy/v1beta1\n  kind: PodDisruptionBudget\n  metadata:\n    name: web\n")
	res, _ := scanManifests(t, dir, t137)
	want := "PodDisruptionBudget policy/v1beta1 web BLOCKER b.yaml:0/0"
	if got := strings.Join(hits(res), "|"); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestManifestListItemsWithoutEvidenceAreUnverified(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "l.yaml"), "apiVersion: v1\nkind: List\nitems:\n  - kind: Pod\n  - foo: bar\n")
	res, _ := scanManifests(t, dir, t137)
	if res.Unverified != 1 || len(res.Findings) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("unverified=%d findings=%v skipped=%v", res.Unverified, hits(res), res.Skipped)
	}
}

func TestManifestSymlinkedRootIsWalked(t *testing.T) {
	real := t.TempDir()
	put(t, filepath.Join(real, "sub", "pdb.yaml"), pdbDoc)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	res, _ := scanManifests(t, link, t137)
	if got := strings.Join(hits(res), "|"); got != "PodDisruptionBudget policy/v1beta1 web BLOCKER "+filepath.Join("sub", "pdb.yaml")+":0" {
		t.Fatalf("got %q", got)
	}
	if res.FilesScanned != 1 {
		t.Fatalf("files scanned = %d", res.FilesScanned)
	}
}

func TestManifestSymlinkedSingleFileRootKeepsGivenName(t *testing.T) {
	real := t.TempDir()
	put(t, filepath.Join(real, "target.txt"), pdbDoc)
	link := filepath.Join(t.TempDir(), "link.yaml")
	if err := os.Symlink(filepath.Join(real, "target.txt"), link); err != nil {
		t.Fatal(err)
	}
	res, _ := scanManifests(t, link, t137)
	if got := strings.Join(hits(res), "|"); got != "PodDisruptionBudget policy/v1beta1 web BLOCKER link.yaml:0" {
		t.Fatalf("got %q", got)
	}
}

func TestManifestUnreadableFileIsSkippedNotMalformed(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "ok.yaml"), pdbDoc)
	put(t, filepath.Join(dir, "locked.yaml"), pdbDoc)
	denyAccess(t, filepath.Join(dir, "locked.yaml"))
	res, warn := scanManifests(t, dir, t137)
	if len(res.Findings) != 1 || len(res.Skipped) != 1 || !strings.HasPrefix(res.Skipped[0].Reason, "unreadable: ") ||
		strings.Contains(res.Skipped[0].Reason, "malformed") || strings.Count(warn, "\n") != 1 {
		t.Fatalf("findings=%v skipped=%s warn=%q", hits(res), skipReasons(res), warn)
	}
}

func TestManifestUnreadableDirIsSkippedAndWalkContinues(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "ok.yaml"), pdbDoc)
	put(t, filepath.Join(dir, "locked", "x.yaml"), pdbDoc)
	denyAccess(t, filepath.Join(dir, "locked"))
	res, _ := scanManifests(t, dir, t137)
	if len(res.Findings) != 1 || len(res.Skipped) != 1 || res.Skipped[0].Group != "locked" ||
		!strings.HasPrefix(res.Skipped[0].Reason, "unreadable directory: ") {
		t.Fatalf("findings=%v skipped=%s", hits(res), skipReasons(res))
	}
}

func TestManifestUnreadableRootIsError(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "x.yaml"), pdbDoc)
	denyAccess(t, dir)
	if _, err := NewManifestSource(dir, &bytes.Buffer{}).Objects(context.Background()); err == nil {
		t.Fatal("unreadable root did not fail")
	}
}

func TestManifestPartialFileKeepsDecodedDocuments(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "p.yaml"), pdbDoc+"---\nfoo: [unclosed\n bad: x\n")
	res, warn := scanManifests(t, dir, t137)
	if len(res.Findings) != 1 || len(res.Skipped) != 1 || !strings.HasPrefix(res.Skipped[0].Reason, "malformed YAML in document 1: ") ||
		strings.Count(warn, "\n") != 1 {
		t.Fatalf("findings=%v skipped=%s warn=%q", hits(res), skipReasons(res), warn)
	}
}

func TestManifestNonObjectDocumentsAndUnverified(t *testing.T) {
	dir := t.TempDir()
	body := "- a\n- b\n---\nhello\n---\nfoo: bar\n---\n# only a comment\n---\n" +
		"kind: Pod\n---\napiVersion: v1\n---\napiVersion: 5\nkind: Pod\n"
	put(t, filepath.Join(dir, "d.yaml"), body)
	res, _ := scanManifests(t, dir, t137)
	if res.Unverified != 3 || len(res.Findings) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("unverified=%d findings=%v skipped=%s", res.Unverified, hits(res), skipReasons(res))
	}
}

func TestManifestJSONFilesScannedAndCounted(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "a.json"), `{"apiVersion":"policy/v1beta1","kind":"PodDisruptionBudget","metadata":{"name":"web"}}`)
	put(t, filepath.Join(dir, "b.yaml"), "apiVersion: v1\nkind: ConfigMap\n")
	put(t, filepath.Join(dir, "c.YML"), "apiVersion: v1\nkind: ConfigMap\n")
	put(t, filepath.Join(dir, "notes.txt"), pdbDoc)
	res, _ := scanManifests(t, dir, t137)
	if got := strings.Join(hits(res), "|"); got != "PodDisruptionBudget policy/v1beta1 web BLOCKER a.json:0" || res.FilesScanned != 3 {
		t.Fatalf("got %q files=%d", got, res.FilesScanned)
	}
}

func TestManifestSymlinkedDirNotFollowed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	put(t, filepath.Join(outside, "pdb.yaml"), pdbDoc)
	if err := os.Symlink(outside, filepath.Join(dir, "loop")); err != nil {
		t.Fatal(err)
	}
	res, warn := scanManifests(t, dir, t137)
	if len(res.Findings) != 0 || skipReasons(res) != "loop: symlinked directory not followed" || strings.Count(warn, "\n") != 1 {
		t.Fatalf("findings=%v skipped=%s warn=%q", hits(res), skipReasons(res), warn)
	}
}

func TestManifestNonManifestSingleFileIsError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notes.txt")
	put(t, f, pdbDoc)
	_, err := NewManifestSource(f, &bytes.Buffer{}).Objects(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a YAML or JSON file") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
}
