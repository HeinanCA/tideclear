package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	clienttesting "k8s.io/client-go/testing"
)

const (
	al2Image    = "Amazon Linux 2"
	al2023Image = "Amazon Linux 2023.6.20250115"
	kubelet134  = "v1.34.1-eks-5e0fdde"
	nodeCluster = "demo-cluster"
)

// nodeDeps is a clean cluster at the given minor version (the EKS fake agrees) holding the nodes.
func nodeDeps(t *testing.T, minor string, api ClusterAPI, nodes ...*unstructured.Unstructured) (Deps, cluster) {
	t.Helper()
	c := cleanCluster()
	c.disc.FakedServerVersion = &version.Info{Major: "1", Minor: minor}
	for _, n := range nodes {
		if _, err := c.dyn.Resource(nodeGVR).Create(context.Background(), n, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{nodeCluster: "1." + minor}}})
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		return c.disc, c.dyn, nil
	}
	d.Cluster = func(string, string) (ClusterAPI, error) { return api, nil }
	return d, c
}

func nodesDoc(t *testing.T, js string) (rows []struct {
	Source, Name, Severity string
	Reasons                []string
}, skipped []string) {
	t.Helper()
	var doc struct {
		Nodes []struct {
			Source, Name, Severity string
			Reasons                []string
		}
		ChecksSkipped []struct{ Check string }
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatalf("%v\n%s", err, js)
	}
	for _, s := range doc.ChecksSkipped {
		skipped = append(skipped, s.Check)
	}
	return doc.Nodes, skipped
}

func TestScanAL2NodegroupSeverityFollowsTarget(t *testing.T) {
	api := &fakeAWS{nodegroupFake: nodegroupFake{groups: []types.Nodegroup{fakeNodegroup("workers", types.AMITypesAl2X8664, "1.33", "1.33.0-20250101")}}}
	d, _ := nodeDeps(t, "33", api) // target 1.34: WARNING
	code, out, se := run(t, d, "scan", "--cluster", nodeCluster, "--strict")
	if code != 0 || se != "" || !strings.Contains(out, "workers") || !strings.Contains(out, "AL2_x86_64") || !strings.Contains(out, "WARNING") {
		t.Fatalf("1.34: exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
	api.groups[0] = fakeNodegroup("workers", types.AMITypesAl2X8664, "1.34", "1.34.0-20250101")
	d, _ = nodeDeps(t, "34", api) // target 1.35: BLOCKER
	code, out, se = run(t, d, "scan", "--cluster", nodeCluster)
	if code != 1 || se != "" || !strings.Contains(out, "BLOCKER") || !strings.Contains(out, "nodes:") {
		t.Fatalf("1.35: exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
}

func TestScanSelfManagedAL2OnPlainLiveScanNeedsNoCluster(t *testing.T) {
	d, _ := nodeDeps(t, "34", &fakeAWS{}, fakeNode("ip-10-0-0-1", nil, kubelet134, al2Image), fakeNode("ok", nil, kubelet134, al2023Image))
	code, out, se := run(t, d, "scan")
	if code != 1 || se != "" {
		t.Fatalf("exit=%d stderr=%q, want 1 (AL2 at target 1.35) and empty stderr", code, se)
	}
	if !strings.Contains(out, "ip-10-0-0-1") || strings.Contains(out, "\nnode       ok") || !strings.Contains(out, "check skipped: nodegroups: needs --cluster") {
		t.Fatalf("stdout:\n%s", out)
	}
	_, js, _ := run(t, d, "scan", "--output", "json")
	rows, skipped := nodesDoc(t, js)
	if len(rows) != 1 || rows[0].Source != "node" || rows[0].Severity != "BLOCKER" || len(skipped) != 2 {
		t.Fatalf("rows=%+v skipped=%v", rows, skipped)
	}
}

func TestScanManagedNodesAreNotDuplicatedAndCustomKeepsOSCheck(t *testing.T) {
	managed := map[string]string{"eks.amazonaws.com/nodegroup": "workers"}
	custom := map[string]string{"eks.amazonaws.com/nodegroup": "mine"}
	api := &fakeAWS{nodegroupFake: nodegroupFake{groups: []types.Nodegroup{
		fakeNodegroup("workers", types.AMITypesAl2X8664, "1.33", "r"),
		fakeNodegroup("mine", types.AMITypesCustom, "1.33", "ami-0abc123def4567890"),
	}}}
	d, _ := nodeDeps(t, "33", api,
		fakeNode("m1", managed, "v1.33.0-eks-1", al2Image),
		fakeNode("c1", custom, "v1.33.0-eks-1", al2Image),
		fakeNode("karpenter-1", map[string]string{"karpenter.sh/nodepool": "default"}, "v1.33.0-eks-1", al2Image),
	)
	_, js, se := run(t, d, "scan", "--cluster", nodeCluster, "--output", "json")
	if se != "" {
		t.Fatalf("stderr = %q", se)
	}
	rows, _ := nodesDoc(t, js)
	var got []string
	for _, r := range rows {
		got = append(got, r.Source+":"+r.Name)
	}
	want := []string{"node:c1", "node:karpenter-1", "nodegroup:workers"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v (managed node m1 not duplicated; CUSTOM group row absent: no version warning for an AMI ID)", got, want)
	}
	for _, r := range rows {
		for _, reason := range r.Reasons {
			if strings.Contains(reason, "version unknown") {
				t.Errorf("%s: unexpected %q", r.Name, reason)
			}
		}
	}
}

func TestScanUnparseableKubeletIsWarningNeverExitTwo(t *testing.T) {
	d, _ := nodeDeps(t, "33", &fakeAWS{}, fakeNode("odd", nil, "not-a-version", al2023Image))
	for _, args := range [][]string{{"scan"}, {"scan", "--strict"}} {
		code, out, se := run(t, d, args...)
		if code != 0 || se != "" || !strings.Contains(out, "version unknown") || !strings.Contains(out, "WARNING") {
			t.Fatalf("%v: exit=%d stderr=%q stdout:\n%s", args, code, se, out)
		}
	}
}

func TestScanRealKubeletStringIsNotAnError(t *testing.T) {
	d, _ := nodeDeps(t, "34", &fakeAWS{}, fakeNode("fine", nil, "v1.34.1-eks-5e0fdde", al2023Image))
	code, out, se := run(t, d, "scan", "--strict")
	if code != 0 || se != "" || strings.Contains(out, "nodes:") {
		t.Fatalf("exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
}

func TestScanZeroNodesLeavesOutputUnchanged(t *testing.T) {
	d, _ := nodeDeps(t, "33", &fakeAWS{})
	_, out, _ := run(t, d, "scan", "--cluster", nodeCluster)
	_, js, _ := run(t, d, "scan", "--cluster", nodeCluster, "--output", "json")
	if strings.Contains(out, "nodes") || strings.Contains(js, `"nodes"`) {
		t.Fatalf("zero nodes produced a section:\n%s\n%s", out, js)
	}
}

func TestScanNodeList403DegradesRestIntact(t *testing.T) {
	d, c := nodeDeps(t, "33", &fakeAWS{})
	c.dyn.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", nil)
	})
	code, out, se := run(t, d, "scan", "--cluster", nodeCluster, "--strict")
	if code != 0 || !strings.Contains(out, "no findings") {
		t.Fatalf("exit=%d, want 0 with the report; stdout:\n%s", code, out)
	}
	assertOneLine(t, se)
	if !strings.Contains(se, "nodes check skipped") || !strings.Contains(out, "check skipped: nodes") {
		t.Errorf("stderr=%q stdout:\n%s", se, out)
	}
	_, js, _ := run(t, d, "scan", "--cluster", nodeCluster, "--output", "json")
	if _, skipped := nodesDoc(t, js); !slices.Contains(skipped, "nodes") {
		t.Errorf("checksSkipped = %v", skipped)
	}
}

func TestScanNodegroupDenialDegradesAndNodesAreNotHidden(t *testing.T) {
	denied := &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListNodegroups",
		Err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "nope\nsecond"}}
	api := &fakeAWS{nodegroupFake: nodegroupFake{ngErr: denied}}
	d, _ := nodeDeps(t, "33", api, fakeNode("m1", map[string]string{"eks.amazonaws.com/nodegroup": "workers"}, "v1.33.0-eks-1", al2Image))
	code, out, se := run(t, d, "scan", "--cluster", nodeCluster, "--strict")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
	assertOneLine(t, se)
	if !strings.Contains(se, "nodegroups check skipped") || !strings.Contains(out, "check skipped: nodegroups") {
		t.Errorf("stderr=%q stdout:\n%s", se, out)
	}
	if !strings.Contains(out, "m1") { // the nodegroup check did not run, so the managed node is judged by its osImage
		t.Errorf("managed node hidden although the nodegroup check was skipped:\n%s", out)
	}
}

func TestScanNodegroupCredentialFailureExitsTwo(t *testing.T) {
	bad := &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListNodegroups",
		Err: &smithy.GenericAPIError{Code: "UnrecognizedClientException", Message: "bad token"}}
	d, _ := nodeDeps(t, "33", &fakeAWS{nodegroupFake: nodegroupFake{ngErr: bad}})
	code, out, se := run(t, d, "scan", "--cluster", nodeCluster)
	if code != 2 || out != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
	}
	assertOneLine(t, se)
}

func TestScanNodegroupHangDegrades(t *testing.T) {
	shortExtrasTimeout(t)
	d, _ := nodeDeps(t, "33", &fakeAWS{nodegroupFake: nodegroupFake{ngHang: true}})
	code, out, se := run(t, d, "scan", "--cluster", nodeCluster)
	if code != 0 || out == "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
	}
	assertOneLine(t, se)
	if !strings.Contains(se, "nodegroups check skipped") {
		t.Errorf("stderr=%q", se)
	}
}

func TestScanPathNeverTouchesNodes(t *testing.T) {
	d, c := nodeDeps(t, "34", &fakeAWS{}, fakeNode("ip-1", nil, kubelet134, al2Image))
	var listed bool
	c.dyn.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		listed = true
		return false, nil, nil
	})
	code, out, _ := run(t, d, "scan", "--path", t.TempDir(), "--target", "1.35")
	if code != 0 || listed || strings.Contains(out, "nodes") {
		t.Fatalf("exit=%d listed=%v stdout:\n%s", code, listed, out)
	}
}

// A parent scan timeout during an extras call exits 2 with "scan timed out"; it
// is never degraded to a skipped check (extrasTimeout is large, scanTimeout small).
func parentTimeout(t *testing.T) {
	t.Helper()
	old, oldScan := extrasTimeout, scanTimeout
	extrasTimeout, scanTimeout = time.Minute, 100*time.Millisecond
	t.Cleanup(func() { extrasTimeout, scanTimeout = old, oldScan })
}

func TestScanParentTimeoutDuringExtrasExitsTwo(t *testing.T) {
	cases := map[string]struct {
		api  ClusterAPI
		args []string
	}{
		"add-ons":    {&fakeAWS{addonFake: addonFake{hang: true}}, []string{"scan", "--cluster", nodeCluster}},
		"insights":   {&hangInsightsAWS{}, []string{"scan", "--cluster", nodeCluster, "--insights"}},
		"nodegroups": {&fakeAWS{nodegroupFake: nodegroupFake{ngHang: true}}, []string{"scan", "--cluster", nodeCluster}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parentTimeout(t)
			d, _ := nodeDeps(t, "33", tc.api)
			code, out, se := run(t, d, tc.args...)
			if code != 2 || out != "" || !strings.Contains(se, "scan timed out") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
			}
			assertOneLine(t, se)
		})
	}
}
