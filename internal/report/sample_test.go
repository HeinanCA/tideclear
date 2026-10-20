package report

import (
	"context"
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
			at("extensions/v1beta1", "Ingress", "demo", "web", "managedFields", "ingresses"),
		},
		Listed: []apiscan.Listed{
			{Group: "flowcontrol.apiserver.k8s.io", Resource: "flowschemas", Kind: "FlowSchema"},
			{Group: "flowcontrol.apiserver.k8s.io", Resource: "prioritylevelconfigurations", Kind: "PriorityLevelConfiguration"},
			{Group: "apidiscovery.k8s.io", Resource: "apigroupdiscoveries", Kind: "APIGroupDiscovery"},
			{Group: "policy", Resource: "poddisruptionbudgets", Kind: "PodDisruptionBudget"},
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

// insightsSample reconciles four deprecated-API insights against w2Scan.
func insightsSample(t *testing.T) Extras {
	t.Helper()
	old := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	refresh := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	mk := func(id string, st insights.Status, usage string, cs ...insights.Client) insights.Insight {
		return insights.Insight{ID: id, Name: "Deprecated APIs removed in Kubernetes v1.35", Target: "1.35", Status: st, LastRefresh: refresh,
			Deprecations: []insights.Deprecation{{Usage: usage, Clients: cs}}}
	}
	ins := []insights.Insight{
		mk("i1", insights.StatusError, "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas"),
		mk("i2", insights.StatusError, "/apis/apidiscovery.k8s.io/v2beta1/apigroupdiscoveries",
			insights.Client{UserAgent: "demo-controller/1.2", Requests: 42, LastRequest: &recent}),
		mk("i3", insights.StatusWarning, "/apis/flowcontrol.apiserver.k8s.io/v1beta3/prioritylevelconfigurations",
			insights.Client{UserAgent: "kubectl/1.28", Requests: 3, LastRequest: &old}),
		mk("i4", insights.StatusError, "/apis/batch/v1beta1/cronjobs",
			insights.Client{UserAgent: "old-agent", Requests: 1, LastRequest: &old}),
		mk("i5", insights.StatusPassing, "/apis/policy/v1beta1/poddisruptionbudgets"),
	}
	return Extras{InsightsRan: true, Insights: reconcile.Reconcile(ins, w2Scan(t), sampleNow)}
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
