package apiscan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/rules"
)

var t137 = rules.Version{Major: 1, Minor: 37}

func manifestDir(name string) string { return filepath.Join("..", "..", "testdata", "manifests", name) }

func scanManifests(t *testing.T, root string, target rules.Version) (Result, string) {
	t.Helper()
	set, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	res, err := Scan(context.Background(), NewManifestSource(root, &warn), set, target)
	if err != nil {
		t.Fatal(err)
	}
	return res, warn.String()
}

func hits(res Result) []string {
	var out []string
	for _, it := range res.Findings {
		out = append(out, it.Object.Kind+" "+it.Object.APIVersion+" "+it.Object.Name+" "+string(it.Finding.Severity)+" "+it.Object.Origin)
	}
	return out
}

func TestManifestGoodDirIsClean(t *testing.T) {
	res, warn := scanManifests(t, manifestDir("good"), t137)
	if len(res.Findings) != 0 || len(res.Skipped) != 0 || warn != "" {
		t.Fatalf("findings=%v skipped=%v warn=%q", hits(res), res.Skipped, warn)
	}
}

func TestManifestBadDirBlockerAndWarning(t *testing.T) {
	res, _ := scanManifests(t, manifestDir("bad"), t137)
	want := []string{
		"ClusterTrustBundle certificates.k8s.io/v1beta1 api WARNING nested/bundle.yaml:0",
		"IPAddress networking.k8s.io/v1beta1 192.0.2.10 BLOCKER address.yml:0",
		"PodDisruptionBudget policy/v1beta1 web BLOCKER pdb.yaml:0",
	}
	got := hits(res)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r := res.Findings[2].Finding.Rule.Replacement; r != "policy/v1" {
		t.Errorf("replacement = %q", r)
	}
}

func TestManifestMultiDocPerDocumentOrigins(t *testing.T) {
	res, _ := scanManifests(t, manifestDir("multidoc"), t137)
	want := "IPAddress networking.k8s.io/v1beta1 api BLOCKER stack.yaml:4|PodDisruptionBudget policy/v1beta1 web BLOCKER stack.yaml:2"
	if got := strings.Join(hits(res), "|"); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestManifestCRDReportedInstancesIgnored(t *testing.T) {
	res, _ := scanManifests(t, manifestDir("crd"), rules.Version{Major: 1, Minor: 30})
	want := "CustomResourceDefinition apiextensions.k8s.io/v1beta1 widgets.example.test BLOCKER crd.yaml:0"
	if got := strings.Join(hits(res), "|"); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestManifestMalformedWarnsSkipsAndContinues(t *testing.T) {
	res, warn := scanManifests(t, manifestDir("malformed"), t137)
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %v", hits(res))
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Group != "broken.yaml" || !strings.HasPrefix(res.Skipped[0].Reason, "malformed YAML in document 0: ") ||
		strings.ContainsAny(res.Skipped[0].Reason, "\r\n") {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	if strings.Count(warn, "\n") != 1 || !strings.Contains(warn, "broken.yaml") || !strings.HasPrefix(warn, "tideclear: warning: ") {
		t.Fatalf("warn = %q", warn)
	}
}

func TestManifestSingleFileAndNonYAMLIgnored(t *testing.T) {
	res, _ := scanManifests(t, filepath.Join(manifestDir("bad"), "pdb.yaml"), t137)
	if got := hits(res); len(got) != 1 || !strings.HasSuffix(got[0], "pdb.yaml:0") {
		t.Fatalf("single file: %v", got)
	}
	// notes.txt holds a removed apiVersion but must never be read.
	res, _ = scanManifests(t, manifestDir("bad"), t137)
	for _, h := range hits(res) {
		if strings.Contains(h, "notes.txt") || strings.Contains(h, " ignored ") {
			t.Fatalf("non-YAML read: %s", h)
		}
	}
}

func TestManifestEmptyDirClean(t *testing.T) {
	res, warn := scanManifests(t, t.TempDir(), t137)
	if len(res.Findings) != 0 || len(res.Skipped) != 0 || warn != "" {
		t.Fatalf("res=%+v warn=%q", res, warn)
	}
}

func TestManifestMissingPathIsError(t *testing.T) {
	_, err := NewManifestSource(filepath.Join(t.TempDir(), "nope"), &bytes.Buffer{}).Objects(context.Background())
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestJSONDocumentAndYMLWalk(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"apiVersion":"policy/v1beta1","kind":"PodDisruptionBudget","metadata":{"name":"web"}}`
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "x.yml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := scanManifests(t, dir, t137)
	if got := hits(res); len(got) != 1 || !strings.HasSuffix(got[0], filepath.Join("a", "b", "x.yml")+":0") {
		t.Fatalf("got %v", got)
	}
}

func TestManifestServerVersionUnknown(t *testing.T) {
	v, known, err := NewManifestSource(".", &bytes.Buffer{}).ServerVersion(context.Background())
	if known || err != nil || v != (rules.Version{}) {
		t.Fatalf("v=%v known=%v err=%v", v, known, err)
	}
}
