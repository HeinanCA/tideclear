package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

func manifests(name string) string {
	return filepath.Join("..", "..", "testdata", "manifests", name)
}

// pathDeps fails the test if the scan touches Kubernetes at all, and counts
// EKS client requests.
func pathDeps(t *testing.T) (Deps, *atomic.Int32) {
	t.Helper()
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"demo-cluster": "1.30"}}})
	eksCalls := &atomic.Int32{}
	inner := d.EKS
	d.EKS = func(profile, region string) (eksinfo.EKSAPI, error) {
		eksCalls.Add(1)
		return inner(profile, region)
	}
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		t.Error("Deps.Kube called by a --path scan")
		return nil, nil, nil
	}
	return d, eksCalls
}

func TestScanPathGoodIsCleanWithoutAnyClient(t *testing.T) {
	d, eksCalls := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", manifests("good"), "--target", "1.37")
	if code != 0 || se != "" || eksCalls.Load() != 0 {
		t.Fatalf("exit=%d stderr=%q eks calls=%d", code, se, eksCalls.Load())
	}
	if want := "path: " + manifests("good") + "\nfiles scanned: 1\nno findings for target 1.37\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

func TestScanPathBadBlockersExitOne(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", manifests("bad"), "--target", "1.37")
	if code != 1 || se != "" {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	for _, w := range []string{"PodDisruptionBudget", "IPAddress", "BLOCKER", "ClusterTrustBundle", "WARNING", "policy/v1"} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
}

func TestScanPathWarningOnlyExitZero(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, _ := run(t, d, "scan", "--path", filepath.Join(manifests("bad"), "nested"), "--target", "1.37")
	if code != 0 || !strings.Contains(out, "WARNING") {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

func TestScanPathJSONHasPathNoCluster(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, _ := run(t, d, "scan", "--path", manifests("multidoc"), "--target", "1.37", "--output", "json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["cluster"]; code != 1 || ok || doc["path"] != manifests("multidoc") || doc["schemaVersion"] != float64(1) {
		t.Fatalf("exit=%d doc=%v", code, doc)
	}
}

func TestScanPathMalformedWarnsOnceAndIsListed(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", manifests("malformed"), "--target", "1.37")
	if code != 0 || strings.Count(se, "\n") != 1 || !strings.Contains(se, "broken.yaml") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	if !strings.Contains(out, "1 skipped") {
		t.Errorf("stdout lacks skip footer:\n%s", out)
	}
	_, out, _ = run(t, d, "scan", "--path", manifests("malformed"), "--target", "1.37", "--output", "json")
	if !strings.Contains(out, `"group": "broken.yaml"`) || !strings.Contains(out, "malformed YAML in document 0: ") {
		t.Errorf("json lacks skipped file:\n%s", out)
	}
}

func TestScanPathEmptyDirClean(t *testing.T) {
	d, _ := pathDeps(t)
	if code, _, se := run(t, d, "scan", "--path", t.TempDir(), "--target", "1.37"); code != 0 || se != "" {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}

func TestScanPathUsageErrorsExitTwoOneLine(t *testing.T) {
	cases := map[string][]string{
		"missing path":       {"scan", "--path", filepath.Join(t.TempDir(), "nope"), "--target", "1.37"},
		"no target/cluster":  {"scan", "--path", manifests("good")},
		"with kubeconfig":    {"scan", "--path", manifests("good"), "--target", "1.37", "--kubeconfig", "/x"},
		"with context":       {"scan", "--path", manifests("good"), "--target", "1.37", "--context", "c"},
		"bad target":         {"scan", "--path", manifests("good"), "--target", "x"},
		"cluster too far":    {"scan", "--path", manifests("good"), "--cluster", "demo-cluster", "--target", "1.33"},
		"unknown cluster":    {"scan", "--path", manifests("good"), "--cluster", "nope"},
		"cluster, bad right": {"scan", "--path", manifests("good"), "--cluster", "demo-cluster", "--target", "1.29"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			d, _ := pathDeps(t)
			code, out, se := run(t, d, args...)
			if code != 2 || out != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
			}
			assertOneLine(t, se)
		})
	}
}

func TestScanPathWithClusterCallsEKSNotKube(t *testing.T) {
	d, eksCalls := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", manifests("bad"), "--cluster", "demo-cluster")
	if code != 1 || se != "" || eksCalls.Load() != 1 {
		t.Fatalf("exit=%d stderr=%q eks calls=%d", code, se, eksCalls.Load())
	}
	// default target = current 1.30 + 1; PodDisruptionBudget (removed 1.25) blocks.
	if !strings.Contains(out, "PodDisruptionBudget") {
		t.Fatalf("stdout = %s", out)
	}
	code, out, _ = run(t, d, "scan", "--path", manifests("good"), "--cluster", "demo-cluster")
	if code != 0 || !strings.Contains(out, "no findings for target 1.31") {
		t.Fatalf("exit=%d out=%q", code, out)
	}
}

func TestScanPathEmptyValueIsToolError(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", "", "--target", "1.37")
	if code != 2 || out != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
	}
	assertOneLine(t, se)
}

func TestScanPathNonManifestFileIsToolError(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", filepath.Join(manifests("bad"), "notes.txt"), "--target", "1.37")
	if code != 2 || out != "" || !strings.Contains(se, "not a YAML or JSON file") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
	}
	assertOneLine(t, se)
}

func TestScanPathSymlinkedRootKeepsGivenPathAndScans(t *testing.T) {
	abs, err := filepath.Abs(manifests("bad"))
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(abs, link); err != nil {
		t.Fatal(err)
	}
	d, _ := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", link, "--target", "1.37")
	if code != 1 || se != "" || !strings.HasPrefix(out, "path: "+link+"\n") || !strings.Contains(out, "PodDisruptionBudget") {
		t.Fatalf("exit=%d stderr=%q out=%s", code, se, out)
	}
}

func TestScanPathListFixtureBlocks(t *testing.T) {
	d, _ := pathDeps(t)
	code, out, _ := run(t, d, "scan", "--path", manifests("list"), "--target", "1.37")
	if code != 1 || !strings.Contains(out, "list.yaml:0/0") {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

func TestScanPathShowsFilesScannedAndOrigin(t *testing.T) {
	d, _ := pathDeps(t)
	_, out, _ := run(t, d, "scan", "--path", manifests("good"), "--target", "1.37")
	if want := "path: " + manifests("good") + "\nfiles scanned: 1\nno findings for target 1.37\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	_, out, _ = run(t, d, "scan", "--path", manifests("bad"), "--target", "1.37")
	if !strings.Contains(out, "ORIGIN") || !strings.Contains(out, "pdb.yaml:0") || !strings.Contains(out, "files scanned: 3") {
		t.Errorf("text lacks ORIGIN/origin/files scanned:\n%s", out)
	}
	_, out, _ = run(t, d, "scan", "--path", manifests("bad"), "--target", "1.37", "--output", "json")
	if !strings.Contains(out, `"filesScanned": 3`) {
		t.Errorf("json lacks filesScanned:\n%s", out)
	}
}

func TestScanPathUnreadableDirWarnsAndExitsByFindings(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	d, _ := pathDeps(t)
	code, out, se := run(t, d, "scan", "--path", dir, "--target", "1.37")
	if code != 0 || !strings.Contains(se, "unreadable directory") || strings.Count(se, "\n") != 1 || !strings.Contains(out, "results are partial") {
		t.Fatalf("exit=%d stderr=%q out=%s", code, se, out)
	}
}

func TestScanPathFooterSaysFilesSkipped(t *testing.T) {
	d, _ := pathDeps(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("apiVersion: policy/v1beta1\nkind: PodDisruptionBudget\nmetadata:\n  name: w\n---\n{{ bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, d, "scan", "--path", dir, "--target", "1.37")
	if code != 1 || !strings.Contains(out, "warning: 1 files skipped (see stderr); results are partial\n") {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}
