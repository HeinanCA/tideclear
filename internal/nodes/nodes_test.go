package nodes

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedyn "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func mustRules(t *testing.T) *rules.NodeRules {
	t.Helper()
	nr, err := rules.LoadNodes()
	if err != nil {
		t.Fatal(err)
	}
	return nr
}

// fakeNG serves nodegroups one per page, in name order.
type fakeNG struct {
	groups      []types.Nodegroup
	listErr     error
	describeErr map[string]error // by group name
	pages       int
}

func (f *fakeNG) ListNodegroups(_ context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	f.pages++
	if f.listErr != nil {
		return nil, f.listErr
	}
	i := 0
	if in.NextToken != nil {
		i, _ = strconv.Atoi(*in.NextToken)
	}
	out := &eks.ListNodegroupsOutput{Nodegroups: []string{aws.ToString(f.groups[i].NodegroupName)}}
	if i+1 < len(f.groups) {
		out.NextToken = aws.String(strconv.Itoa(i + 1))
	}
	return out, nil
}

func (f *fakeNG) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	if err := f.describeErr[aws.ToString(in.NodegroupName)]; err != nil {
		return nil, err
	}
	for _, g := range f.groups {
		if aws.ToString(g.NodegroupName) == aws.ToString(in.NodegroupName) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &g}, nil
		}
	}
	return nil, errors.New("no such nodegroup")
}

func ng(name string, ami types.AMITypes, version, release string) types.Nodegroup {
	return types.Nodegroup{NodegroupName: aws.String(name), AmiType: ami, Version: aws.String(version), ReleaseVersion: aws.String(release)}
}

func v(minor int) rules.Version { return rules.Version{Major: 1, Minor: minor} }

func TestAL2ManagedNodegroupSeverityByTarget(t *testing.T) {
	nr := mustRules(t)
	api := &fakeNG{groups: []types.Nodegroup{ng("old", types.AMITypesAl2X8664, "1.33", "1.33.0-20250101")}}
	for _, tc := range []struct {
		current, target int
		want            rules.Severity
	}{{33, 34, rules.SeverityWarning}, {34, 35, rules.SeverityBlocker}} {
		rows, _, err := CheckNodegroups(context.Background(), api, "demo", v(tc.current), v(tc.target), nr)
		if err != nil || len(rows) != 1 {
			t.Fatalf("target %d: rows=%v err=%v", tc.target, rows, err)
		}
		r := rows[0]
		if r.Severity != tc.want || r.Source != "nodegroup" || r.Name != "old" || r.AMIType != "AL2_x86_64" || r.ReleaseVersion != "1.33.0-20250101" || r.Version != "1.33" {
			t.Errorf("target %d: row = %+v", tc.target, r)
		}
		if !strings.Contains(strings.Join(r.Reasons, ";"), "Amazon Linux 2") {
			t.Errorf("reasons = %v", r.Reasons)
		}
	}
}

func TestCleanNodegroupsProduceNoRows(t *testing.T) {
	api := &fakeNG{groups: []types.Nodegroup{
		ng("al2023", types.AMITypesAl2023X8664Standard, "1.31", "1.31.4-20250101"),
		ng("br", types.AMITypesBottlerocketX8664, "1.31", "1.31.4-v1.30.0"),
	}}
	rows, groups, err := CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t))
	if err != nil || len(rows) != 0 || len(groups) != 2 {
		t.Fatalf("rows=%v groups=%v err=%v", rows, groups, err)
	}
}

func TestCustomNodegroupReleaseVersionIsNeverParsed(t *testing.T) {
	api := &fakeNG{groups: []types.Nodegroup{ng("mine", types.AMITypesCustom, "1.31", "ami-0abc123def4567890")}}
	rows, groups, err := CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t))
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v, want none (no version warning for an AMI ID)", rows, err)
	}
	if groups["mine"] != "CUSTOM" || len(groups) != 1 {
		t.Errorf("groups = %v, want {mine: CUSTOM}", groups)
	}
}

func TestNodegroupSkew(t *testing.T) {
	nr := mustRules(t)
	cases := []struct {
		name, version string
		current       int
		want          rules.Severity
		reason        string
	}{
		{"three behind ok", "1.28", 31, "", ""},
		{"four behind", "1.27", 31, rules.SeverityWarning, "behind"},
		{"newer than control plane", "1.32", 31, rules.SeverityWarning, "newer"},
		{"unparseable", "weird", 31, rules.SeverityWarning, "version unknown"},
		{"real kubelet shape", "v1.31.4-eks-5e0fdde", 31, "", ""},
	}
	for _, tc := range cases {
		api := &fakeNG{groups: []types.Nodegroup{ng("g", types.AMITypesAl2023X8664Standard, tc.version, "r")}}
		// target == current so only the control-plane comparison applies.
		rows, _, err := CheckNodegroups(context.Background(), api, "demo", v(tc.current), v(tc.current), nr)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case tc.want == "" && len(rows) != 0:
			t.Errorf("%s: rows = %+v, want none", tc.name, rows)
		case tc.want != "" && (len(rows) != 1 || rows[0].Severity != tc.want || !strings.Contains(strings.Join(rows[0].Reasons, ";"), tc.reason)):
			t.Errorf("%s: rows = %+v", tc.name, rows)
		}
	}
}

func TestNodegroupSkewAgainstTarget(t *testing.T) {
	// 1.28 is 3 behind current 1.31 but 4 behind target 1.32.
	api := &fakeNG{groups: []types.Nodegroup{ng("g", types.AMITypesAl2023X8664Standard, "1.28", "r")}}
	rows, _, err := CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t))
	if err != nil || len(rows) != 1 || rows[0].Severity != rules.SeverityWarning || !strings.Contains(strings.Join(rows[0].Reasons, ";"), "1.32") {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestNodegroupPaginationAndError(t *testing.T) {
	api := &fakeNG{groups: []types.Nodegroup{
		ng("b", types.AMITypesAl2X8664, "1.31", "r"), ng("a", types.AMITypesAl2Arm64, "1.31", "r"),
	}}
	rows, _, err := CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t))
	if err != nil || len(rows) != 2 || rows[0].Name != "a" || rows[1].Name != "b" || api.pages != 2 {
		t.Fatalf("rows=%+v pages=%d err=%v", rows, api.pages, err)
	}
	boom := errors.New("boom")
	_, _, err = CheckNodegroups(context.Background(), &fakeNG{listErr: boom}, "demo", v(31), v(32), mustRules(t))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
}

func node(name string, labels map[string]string, kubelet, osImage string) *unstructured.Unstructured {
	n := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": name},
		"status": map[string]any{"nodeInfo": map[string]any{"kubeletVersion": kubelet, "osImage": osImage}},
	}}
	n.SetLabels(labels)
	return n
}

func dynWith(objs ...runtime.Object) *fakedyn.FakeDynamicClient {
	return fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{nodeGVR: "NodeList"}, objs...)
}

const (
	al2OS    = "Amazon Linux 2"
	al2023OS = "Amazon Linux 2023.6.20250115"
	kubelet  = "v1.31.4-eks-5e0fdde"
)

func checkNodes(t *testing.T, target int, skipManaged bool, groups map[string]string, objs ...runtime.Object) []Row {
	t.Helper()
	rows, err := CheckNodes(context.Background(), dynWith(objs...), v(31), v(target), mustRules(t), skipManaged, groups)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestSelfManagedAL2ViaOSImage(t *testing.T) {
	for _, tc := range []struct {
		target int
		want   rules.Severity
	}{{31, rules.SeverityWarning}, {35, rules.SeverityBlocker}} {
		rows := checkNodes(t, tc.target, true, nil, node("n1", nil, kubelet, al2OS))
		if len(rows) != 1 || rows[0].Severity != tc.want || rows[0].Source != "node" || rows[0].Name != "n1" || rows[0].OSImage != al2OS || rows[0].Version != kubelet {
			t.Errorf("target %d: rows = %+v", tc.target, rows)
		}
	}
}

func TestCleanNodesProduceNoRows(t *testing.T) {
	rows := checkNodes(t, 32, true, nil,
		node("al2023", nil, kubelet, al2023OS),
		node("br", nil, kubelet, "Bottlerocket OS 1.30.0 (aws-k8s-1.31)"),
		node("fargate", map[string]string{"eks.amazonaws.com/compute-type": "fargate"}, "v1.31.0-eks-1", al2OS),
	)
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none (Fargate skipped, AL2023 and Bottlerocket clean)", rows)
	}
}

func TestManagedNodesNotDuplicatedUnlessCustomOrNotChecked(t *testing.T) {
	managed := node("m1", map[string]string{"eks.amazonaws.com/nodegroup": "grp"}, kubelet, al2OS)
	judged := map[string]string{"grp": "AL2_x86_64"}
	if rows := checkNodes(t, 32, true, judged, managed); len(rows) != 0 {
		t.Errorf("managed node duplicated: %+v", rows)
	}
	if rows := checkNodes(t, 32, false, judged, managed); len(rows) != 1 {
		t.Errorf("nodegroup check did not run: want the node reported, got %+v", rows)
	}
	custom := map[string]string{"grp": "CUSTOM"}
	if rows := checkNodes(t, 32, true, custom, managed); len(rows) != 1 || rows[0].Name != "m1" {
		t.Errorf("CUSTOM nodegroup node must keep its osImage check: %+v", rows)
	}
	clean := node("m2", map[string]string{"eks.amazonaws.com/nodegroup": "grp"}, kubelet, al2023OS)
	if rows := checkNodes(t, 32, true, custom, clean); len(rows) != 0 {
		t.Errorf("clean CUSTOM node reported: %+v", rows)
	}
}

// A nodegroup label is a claim by the node, not proof the nodegroup check judged that group.
func TestNodeOfUnjudgedGroupIsNotSkipped(t *testing.T) {
	n := node("m1", map[string]string{"eks.amazonaws.com/nodegroup": "ghost"}, kubelet, al2OS)
	if rows := checkNodes(t, 32, true, map[string]string{"other": "AL2_x86_64"}, n); len(rows) != 1 || rows[0].Name != "m1" {
		t.Fatalf("node labelled with a group the API did not return was skipped: %+v", rows)
	}
}

func TestNodeOfUnreportedAMITypeGroupIsNotSkipped(t *testing.T) {
	n := node("m1", map[string]string{"eks.amazonaws.com/nodegroup": "grp"}, kubelet, al2OS)
	if rows := checkNodes(t, 32, true, map[string]string{"grp": ""}, n); len(rows) != 1 {
		t.Fatalf("node of a group with no amiType was skipped: %+v", rows)
	}
}

func TestEmptyAMITypeNodegroupIsWarning(t *testing.T) {
	api := &fakeNG{groups: []types.Nodegroup{ng("blank", "", "1.31", "r")}}
	rows, groups, err := CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t))
	if err != nil || len(rows) != 1 || rows[0].Severity != rules.SeverityWarning || !slices.Contains(rows[0].Reasons, "AMI type not reported") {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if ami, ok := groups["blank"]; !ok || ami != "" {
		t.Errorf("groups = %v, want blank judged with empty type", groups)
	}
}

func TestNodegroupDeletedBetweenListAndDescribeIsSkipped(t *testing.T) {
	api := &fakeNG{groups: []types.Nodegroup{ng("a", types.AMITypesAl2Arm64, "1.31", "r"), ng("gone", types.AMITypesAl2X8664, "1.31", "r")},
		describeErr: map[string]error{"gone": &types.ResourceNotFoundException{Message: aws.String("deleted")}}}
	rows, groups, err := CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t))
	if err != nil || len(rows) != 1 || rows[0].Name != "a" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, ok := groups["gone"]; ok {
		t.Errorf("deleted group reported as judged: %v", groups)
	}
	boom := errors.New("boom")
	api.describeErr["gone"] = boom
	if _, _, err = CheckNodegroups(context.Background(), api, "demo", v(31), v(32), mustRules(t)); !errors.Is(err, boom) {
		t.Errorf("other describe errors must abort: %v", err)
	}
}

func TestAL2ReasonExplainsTheBlocker(t *testing.T) {
	rows := checkNodes(t, 35, true, nil, node("n1", nil, kubelet, al2OS))
	want := "Amazon Linux 2 (osImage Amazon Linux 2) is cgroup v1: kubelet 1.35+ will not start on it and EKS AL2 AMIs stop at 1.32, so these nodes cannot follow the control plane to 1.35"
	if len(rows) != 1 || !slices.Contains(rows[0].Reasons, want) {
		t.Fatalf("rows = %+v, want reason %q", rows, want)
	}
}

func TestKarpenterStyleNodeReported(t *testing.T) {
	rows := checkNodes(t, 32, true, nil, node("ip-10-0-0-1", map[string]string{"karpenter.sh/nodepool": "default"}, kubelet, al2OS))
	if len(rows) != 1 || rows[0].Name != "ip-10-0-0-1" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestNodeKubeletSkew(t *testing.T) {
	for _, tc := range []struct {
		name, kubelet string
		want          rules.Severity
		reason        string
	}{
		{"three behind", "v1.28.5-eks-1", "", ""},
		{"four behind", "v1.27.5-eks-1", rules.SeverityWarning, "behind"},
		{"newer than control plane", "v1.32.0-eks-1", rules.SeverityWarning, "newer"},
		{"real string", kubelet, "", ""},
		{"unparseable", "weird", rules.SeverityWarning, "version unknown"},
		{"missing", "", rules.SeverityWarning, "version unknown"},
	} {
		rows, err := CheckNodes(context.Background(), dynWith(node("n", nil, tc.kubelet, al2023OS)), v(31), v(31), mustRules(t), true, nil)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case tc.want == "" && len(rows) != 0:
			t.Errorf("%s: rows = %+v", tc.name, rows)
		case tc.want != "" && (len(rows) != 1 || rows[0].Severity != tc.want || !strings.Contains(strings.Join(rows[0].Reasons, ";"), tc.reason)):
			t.Errorf("%s: rows = %+v", tc.name, rows)
		}
	}
}

func TestNodeRowsSortedAndBlockerWinsOverSkewWarning(t *testing.T) {
	rows := checkNodes(t, 35, true, nil, node("b", nil, "v1.27.0-eks-1", al2OS), node("a", nil, kubelet, al2OS))
	names := []string{rows[0].Name, rows[1].Name}
	if !slices.Equal(names, []string{"a", "b"}) || rows[1].Severity != rules.SeverityBlocker || len(rows[1].Reasons) < 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCheckNodesListError(t *testing.T) {
	c := dynWith()
	c.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("no"))
	})
	_, err := CheckNodes(context.Background(), c, v(31), v(32), mustRules(t), true, nil)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want a wrapped Forbidden", err)
	}
}
