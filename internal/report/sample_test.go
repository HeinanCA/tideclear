package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/reconcile"
	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedyn "k8s.io/client-go/dynamic/fake"
)

// The weekend-2 samples (README quotes their goldens) tell one story: an EKS
// cluster at 1.34 about to move to 1.35. Every node, add-on and insight row is
// what the real judge returns for that input, so a sample can never show a row
// the tool cannot produce.
var (
	current134 = rules.Version{Major: 1, Minor: 34}
	target135  = rules.Version{Major: 1, Minor: 35}
	target132  = rules.Version{Major: 1, Minor: 32}
	sampleNow  = time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)
)

func mustVerdict(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// w2Scan is the live scan behind the weekend-2 samples.
func w2Scan(t *testing.T) apiscan.Result {
	t.Helper()
	set, err := rules.Load()
	mustVerdict(t, err)
	at := func(apiVersion, kind, ns, name, origin, resource string) apiscan.ResultItem {
		f, ok := set.Match(apiVersion, kind, target135)
		if !ok {
			t.Fatalf("no rule for %s %s at %s", apiVersion, kind, target135)
		}
		return apiscan.ResultItem{Finding: f, Object: apiscan.Object{
			APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name, Origin: origin, Resource: resource}}
	}
	res := apiscan.Result{
		Target:     target135,
		Cluster:    apiscan.ClusterInfo{Context: "demo-ctx", Server: "api.example.test", EKSCluster: "demo-cluster"},
		Unverified: 3,
		Findings: []apiscan.ResultItem{
			at("resource.k8s.io/v1beta1", "DeviceClass", "", "tideclear-probe", "managedFields", "deviceclasses"),
			at("flowcontrol.apiserver.k8s.io/v1beta3", "FlowSchema", "", "web", "last-applied", "flowschemas"),
		},
		Skipped: []apiscan.Skipped{
			{Group: "metrics.example.dev", Reason: "503 Service Unavailable"},
			{Group: "batch", Kind: "CronJob", Reason: "forbidden"},
		},
	}
	for _, it := range res.Findings {
		res.Live = append(res.Live, it.Object)
	}
	return res
}

var (
	insightRefresh = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	insightRecent  = time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	insightOld     = time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
)

func removalInsight(id, target string, st insights.Status, usage string, cs ...insights.Client) insights.Insight {
	return insights.Insight{ID: id, Name: "Deprecated APIs removed in Kubernetes v" + target, Target: target, Status: st, LastRefresh: insightRefresh,
		Deprecations: []insights.Deprecation{{Usage: usage, Clients: cs}}}
}

// insightsSample is the 1.35 insight: the one API Kubernetes removes in 1.35
// (apidiscovery v2beta1), still called by a recent client.
func insightsSample(t *testing.T) Extras {
	t.Helper()
	ins := []insights.Insight{
		removalInsight("i1", "1.35", insights.StatusError, "/apis/apidiscovery.k8s.io/v2beta1/apigroupdiscoveries",
			insights.Client{UserAgent: "demo-controller/1.2", Requests: 42, LastRequest: &insightRecent}),
	}
	return Extras{InsightsRan: true, Insights: reconcile.Reconcile(ins, w2Scan(t), sampleNow)}
}

// staleScan is the live scan behind scan-insights: a 1.31 cluster moving to
// 1.32, where flowcontrol v1beta3 is removed.
func staleScan(t *testing.T) apiscan.Result {
	t.Helper()
	set, err := rules.Load()
	mustVerdict(t, err)
	f, ok := set.Match("flowcontrol.apiserver.k8s.io/v1beta3", "FlowSchema", target132)
	if !ok {
		t.Fatal("no rule for FlowSchema v1beta3 at 1.32")
	}
	obj := apiscan.Object{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "FlowSchema", Name: "web", Origin: "last-applied", Resource: "flowschemas"}
	return apiscan.Result{
		Target:   target132,
		Cluster:  apiscan.ClusterInfo{Context: "demo-ctx", Server: "api.example.test", EKSCluster: "demo-cluster"},
		Findings: []apiscan.ResultItem{{Object: obj, Finding: f}},
		Live:     []apiscan.Object{obj},
		Listed: []apiscan.Listed{
			{Group: "flowcontrol.apiserver.k8s.io", Resource: "flowschemas", Kind: "FlowSchema"},
			{Group: "flowcontrol.apiserver.k8s.io", Resource: "prioritylevelconfigurations", Kind: "PriorityLevelConfiguration"},
		},
	}
}

// staleInsightsSample is the 1.32 insight: a BLOCKER backed by a live object
// and a STALE row whose only caller (an old kubectl) last called over 7 days
// before the insight refreshed.
func staleInsightsSample(t *testing.T) Extras {
	t.Helper()
	ins := []insights.Insight{
		removalInsight("i1", "1.32", insights.StatusError, "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas"),
		removalInsight("i2", "1.32", insights.StatusWarning, "/apis/flowcontrol.apiserver.k8s.io/v1beta3/prioritylevelconfigurations",
			insights.Client{UserAgent: "kubectl/1.28", Requests: 3, LastRequest: &insightOld}),
	}
	return Extras{InsightsRan: true, Insights: reconcile.Reconcile(ins, staleScan(t), sampleNow)}
}

type fakeAddonAPI struct {
	current   map[string]string // add-on name to installed version, in list order by names
	names     []string
	supported map[string][]string // add-on name to versions compatible with the target
}

func (f fakeAddonAPI) ListAddons(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	return &eks.ListAddonsOutput{Addons: f.names}, nil
}

func (f fakeAddonAPI) DescribeAddon(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	return &eks.DescribeAddonOutput{Addon: &types.Addon{AddonVersion: aws.String(f.current[aws.ToString(in.AddonName)])}}, nil
}

func (f fakeAddonAPI) DescribeAddonVersions(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
	info := types.AddonInfo{AddonName: in.AddonName}
	for _, v := range f.supported[aws.ToString(in.AddonName)] {
		info.AddonVersions = append(info.AddonVersions, types.AddonVersionInfo{AddonVersion: aws.String(v),
			Compatibilities: []types.Compatibility{{ClusterVersion: in.KubernetesVersion}}})
	}
	return &eks.DescribeAddonVersionsOutput{Addons: []types.AddonInfo{info}}, nil
}

// addonsSample runs the real add-on check and cross-check at 1.35.
func addonsSample(t *testing.T) Extras {
	t.Helper()
	api := fakeAddonAPI{
		names:   []string{"demo-dns", "demo-net", "demo-agent"},
		current: map[string]string{"demo-dns": "v1.1.0-eksbuild.1", "demo-net": "v1.0.0-eksbuild.1", "demo-agent": "v3.0.0"},
		supported: map[string][]string{
			"demo-dns": {"v1.1.0-eksbuild.1", "v1.2.0-eksbuild.1"},
			"demo-net": {"v1.2.0-eksbuild.1", "v1.3.0-eksbuild.1"},
		},
	}
	rows, err := addons.Check(context.Background(), api, "demo-cluster", target135)
	mustVerdict(t, err)
	rows = addons.CrossCheck(rows, []insights.AddonCompat{{Name: "demo-net", Versions: []string{"v1.0.0-eksbuild.1"}}})
	return Extras{Addons: rows, Skipped: []CheckSkipped{{Check: "nodes", Reason: "access denied"}}}
}

type fakeNodegroupAPI struct{}

func (fakeNodegroupAPI) ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers-al2"}}, nil
}

func (fakeNodegroupAPI) DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	return &eks.DescribeNodegroupOutput{Nodegroup: &types.Nodegroup{NodegroupName: aws.String("workers-al2"), AmiType: types.AMITypesAl2X8664,
		Version: aws.String("1.32"), ReleaseVersion: aws.String("1.32.9-20251001")}}, nil
}

func sampleNode(name, kubelet, osImage string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": name},
		"status": map[string]any{"nodeInfo": map[string]any{"kubeletVersion": kubelet, "osImage": osImage}},
	}}
}

// nodesSample runs the real nodegroup and node checks (1.34 to 1.35): the AL2
// nodegroup and a self-managed AL2 node block, an old kubelet and an
// unreadable version warn.
func nodesSample(t *testing.T) Extras {
	t.Helper()
	nr, err := rules.LoadNodes()
	mustVerdict(t, err)
	ctx := context.Background()
	groupRows, judged, _, err := nodes.CheckNodegroups(ctx, fakeNodegroupAPI{}, "demo-cluster", current134, target135, nr)
	mustVerdict(t, err)
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	dyn := fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "NodeList"},
		sampleNode("ip-10-0-0-7", "v1.32.9-eks-5e0fdde", "Amazon Linux 2"),
		sampleNode("ip-10-0-0-8", "v1.30.14-eks-5e0fdde", "Bottlerocket OS 1.30.0"),
		sampleNode("ip-10-0-0-9", "weird", "Bottlerocket OS 1.30.0"))
	nodeRows, err := nodes.CheckNodes(ctx, dyn, current134, target135, nr, true, judged)
	mustVerdict(t, err)
	return Extras{Nodes: append(groupRows, nodeRows...)}
}

// removedAt reports whether the rules data says apiVersion is removed in target.
// The row may carry no Kind (the scan never listed the resource), so every kind
// of the group is tried.
func removedAt(set *rules.Set, apiVersion string, target rules.Version) bool {
	group, _, _ := strings.Cut(apiVersion, "/")
	for _, gk := range set.Kinds() {
		if gk.Group != group {
			continue
		}
		if f, ok := set.Match(apiVersion, gk.Kind, target); ok && f.Rule.RemovedIn != nil && *f.Rule.RemovedIn == target {
			return true
		}
	}
	return false
}

// TestSampleInsightRowsAreRemovedAtTheirTarget keeps the samples honest: a
// "removed in vX" insight can only list APIs that the rules data removes in X.
func TestSampleInsightRowsAreRemovedAtTheirTarget(t *testing.T) {
	set, err := rules.Load()
	mustVerdict(t, err)
	for name, c := range map[string]struct {
		target rules.Version
		ex     Extras
	}{
		"1.34 to 1.35 (full)":     {target135, insightsSample(t)},
		"1.31 to 1.32 (insights)": {target132, staleInsightsSample(t)},
	} {
		if len(c.ex.Insights) == 0 {
			t.Errorf("%s: no insight rows", name)
		}
		for _, r := range c.ex.Insights {
			if !removedAt(set, r.APIVersion, c.target) {
				t.Errorf("%s: %s is not removed in %s per the rules data", name, r.APIVersion, c.target)
			}
		}
	}
}
