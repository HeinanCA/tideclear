package report

import (
	"bytes"
	"testing"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/rules"
)

// The README story: demo-a, an EKS cluster on 1.36, about to move to 1.37 (the
// default target: current minor plus one). The fleet in clusters.txt lists it,
// and every live-output sample on the README page is this cluster, this target
// and this moment (sampleNow). Its two live objects are the APIs Kubernetes 1.37
// removes from networking.k8s.io, so both insight rows are BLOCKERs.
var target137 = rules.Version{Major: 1, Minor: 37}

func storyScan(t *testing.T) apiscan.Result {
	t.Helper()
	set, err := rules.Load()
	mustVerdict(t, err)
	at := func(kind, name, resource string) apiscan.ResultItem {
		const apiVersion = "networking.k8s.io/v1beta1"
		f, ok := set.Match(apiVersion, kind, target137)
		if !ok {
			t.Fatalf("no rule for %s %s at %s", apiVersion, kind, target137)
		}
		return apiscan.ResultItem{Finding: f, Object: apiscan.Object{
			APIVersion: apiVersion, Kind: kind, Name: name, Origin: "managedFields", Resource: resource}}
	}
	res := apiscan.Result{
		Target:  target137,
		Cluster: apiscan.ClusterInfo{Context: "demo-ctx", Server: "api.example.test"},
		Findings: []apiscan.ResultItem{
			at("IPAddress", "api", "ipaddresses"),
			at("ServiceCIDR", "main", "servicecidrs"),
		},
		Listed: []apiscan.Listed{
			{Group: "networking.k8s.io", Resource: "ipaddresses", Kind: "IPAddress"},
			{Group: "networking.k8s.io", Resource: "servicecidrs", Kind: "ServiceCIDR"},
		},
	}
	for _, it := range res.Findings {
		res.Live = append(res.Live, it.Object)
	}
	return res
}

// storyEKSScan is storyScan run with --cluster demo-a.
func storyEKSScan(t *testing.T) apiscan.Result {
	t.Helper()
	res := storyScan(t)
	res.Cluster.EKSCluster = "demo-a"
	return res
}

func storyInsights(t *testing.T) Extras {
	t.Helper()
	ins := []insights.Insight{{
		ID: "i1", Name: "Deprecated APIs removed in Kubernetes v1.37", Target: "1.37",
		Status: insights.StatusError, LastRefresh: insightRefresh,
		Deprecations: []insights.Deprecation{
			{Usage: "/apis/networking.k8s.io/v1beta1/ipaddresses"},
			{Usage: "/apis/networking.k8s.io/v1beta1/servicecidrs"},
		},
	}}
	return Extras{InsightsRan: true, Insights: reconcile.Reconcile(ins, storyScan(t), sampleNow)}
}

// storyPathScan is `scan --path ./k8s --target 1.37` over a repo that still
// carries two long-removed APIs. It needs no cluster and no AWS credentials.
func storyPathScan(t *testing.T) apiscan.Result {
	t.Helper()
	set, err := rules.Load()
	mustVerdict(t, err)
	at := func(apiVersion, kind, name, origin string) apiscan.ResultItem {
		f, ok := set.Match(apiVersion, kind, target137)
		if !ok {
			t.Fatalf("no rule for %s %s at %s", apiVersion, kind, target137)
		}
		return apiscan.ResultItem{Finding: f, Object: apiscan.Object{
			APIVersion: apiVersion, Kind: kind, Namespace: "demo", Name: name, Origin: origin}}
	}
	return apiscan.Result{
		Target:       target137,
		Path:         "./k8s",
		FilesScanned: 2,
		Findings: []apiscan.ResultItem{
			at("batch/v1beta1", "CronJob", "nightly", "cron.yaml:0"),
			at("apps/v1beta1", "Deployment", "web", "deploy.yaml:0"),
		},
	}
}

func TestScanTextStoryPathGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, storyPathScan(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-path-readme.txt", b.Bytes())
}

func TestScanTextStoryLiveGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, storyScan(t), Extras{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-live.txt", b.Bytes())
}

func TestScanTextStoryInsightsGolden(t *testing.T) {
	var b bytes.Buffer
	if err := ScanText(&b, storyEKSScan(t), storyInsights(t)); err != nil {
		t.Fatal(err)
	}
	golden(t, "scan-insights.txt", b.Bytes())
}
