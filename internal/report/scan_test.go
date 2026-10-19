package report

import (
	"bytes"
	"encoding/json"
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
	if err := ScanText(&b, scanSample(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan.txt", b.Bytes())
}

func TestScanTextEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-empty.txt", b.Bytes())
}

func TestScanTextEmptyWithSkipsIsNotCleanLine(t *testing.T) {
	var b bytes.Buffer
	r := apiscan.Result{Target: target138, Skipped: scanSample(t).Skipped}
	if err := ScanText(&b, r); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-empty-skipped.txt", b.Bytes())
}

func TestScanTextEmptyWithUnverifiedStillNotesThem(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, apiscan.Result{Target: target138, Unverified: 1}); err != nil {
		t.Fatal(err)
	}
	want := "no findings for target 1.38\n1 objects had no apiVersion evidence and were not checked\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestScanJSONGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, scanSample(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan.json", b.Bytes())
}

func TestScanJSONEmptyArrays(t *testing.T) {
	var b bytes.Buffer
	if err := ScanJSON(&b, apiscan.Result{Target: target138}); err != nil {
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
