package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/rules"
)

var target138 = rules.Version{Major: 1, Minor: 38}

func item(t *testing.T, apiVersion, kind, ns, name, origin string) apiscan.ResultItem {
	t.Helper()
	set, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	f, ok := set.Match(apiVersion, kind, target138)
	if !ok {
		t.Fatalf("no rule for %s %s at %s", apiVersion, kind, target138)
	}
	return apiscan.ResultItem{
		Object:  apiscan.Object{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name, Origin: origin},
		Finding: f,
	}
}

func scanSample(t *testing.T) apiscan.Result {
	return apiscan.Result{
		Target:     target138,
		Cluster:    apiscan.ClusterInfo{Context: "demo-ctx", Server: "api.example.test", EKSCluster: "demo-cluster"},
		Unverified: 3,
		Findings: []apiscan.ResultItem{
			item(t, "resource.k8s.io/v1beta1", "DeviceClass", "", "tideclear-probe", "managedFields"),
			item(t, "flowcontrol.apiserver.k8s.io/v1beta3", "FlowSchema", "", "web", "last-applied"),
			item(t, "extensions/v1beta1", "Ingress", "demo", "web", "managedFields"),
			item(t, "coordination.k8s.io/v1beta1", "LeaseCandidate", "demo", "api", "managedFields"),
		},
		Skipped: []apiscan.Skipped{
			{Group: "metrics.example.dev", Reason: "503 Service Unavailable"},
			{Group: "networking.k8s.io", Kind: "Ingress", Reason: "forbidden"},
		},
	}
}

func TestScanTextGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, scanSample(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan.txt", b.Bytes())
}

func TestScanTextEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138}, Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-empty.txt", b.Bytes())
}

func TestScanTextEmptyWithSkipsIsNotCleanLine(t *testing.T) {
	var b bytes.Buffer
	r := apiscan.Result{Target: target138, Skipped: scanSample(t).Skipped}
	if err := ScanText(&b, r, Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-empty-skipped.txt", b.Bytes())
}

func TestScanTextEmptyWithUnverifiedStillNotesThem(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138, Unverified: 1}, Extras{}); err != nil {
		t.Fatal(err)
	}
	want := "no findings for target 1.38\n1 objects had no apiVersion evidence and were not checked\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestScanJSONGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, scanSample(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan.json", b.Bytes())
}

func TestScanJSONEmptyArrays(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, apiscan.Result{Target: target138}, Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-empty.json", b.Bytes())
	var doc map[string]any
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if u, ok := doc["unverified"].(float64); !ok || u != 0 {
		t.Errorf("unverified = %#v, want 0 always present", doc["unverified"])
	}
	for _, k := range []string{"findings", "skipped"} {
		if a, ok := doc[k].([]any); !ok || len(a) != 0 {
			t.Errorf("%s = %#v, want empty array", k, doc[k])
		}
	}
}

func pathSample(t *testing.T) apiscan.Result {
	return apiscan.Result{
		Target:       target138,
		Path:         "deploy/manifests",
		FilesScanned: 4,
		Findings: []apiscan.ResultItem{
			item(t, "extensions/v1beta1", "Ingress", "demo", "web", "web/ingress.yaml:1"),
		},
		Skipped: []apiscan.Skipped{{Group: "web/broken.yaml", Reason: "malformed YAML: line 5"}},
	}
}

func TestScanTextPathHeader(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, pathSample(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.SplitN(b.String(), "\n", 2)[0]; got != "path: deploy/manifests" {
		t.Errorf("first line = %q", got)
	}
	if strings.Contains(b.String(), "cluster:") {
		t.Errorf("path scan printed a cluster header:\n%s", b.String())
	}
}

func TestScanJSONPathGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, pathSample(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-path.json", b.Bytes())
	var doc map[string]any
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["cluster"]; ok {
		t.Errorf("cluster present in path scan: %v", doc["cluster"])
	}
	if doc["schemaVersion"] != float64(1) || doc["path"] != "deploy/manifests" {
		t.Errorf("schemaVersion/path = %v / %v", doc["schemaVersion"], doc["path"])
	}
}

func TestScanJSONLiveHasNoPathField(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, scanSample(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `"path"`) {
		t.Errorf("live scan JSON has path field:\n%s", b.String())
	}
}

func TestScanTextPathGolden(t *testing.T) {
	var b bytes.Buffer
	r := pathSample(t)
	r.Unverified = 1
	if err := ScanText(&b, r, Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-path.txt", b.Bytes())
}

func TestScanJSONPathEmptyKeepsFilesScannedZero(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, apiscan.Result{Target: target138, Path: "deploy/empty"}, Extras{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"filesScanned": 0`) {
		t.Errorf("path scan with zero files omits filesScanned:\n%s", b.String())
	}
}

func TestScanJSONLiveOmitsFilesScanned(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, scanSample(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "filesScanned") || strings.Contains(b.String(), "ORIGIN") {
		t.Errorf("live output has path-only fields:\n%s", b.String())
	}
}
