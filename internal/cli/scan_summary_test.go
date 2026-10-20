package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	clienttesting "k8s.io/client-go/testing"
)

func denied(op string) error {
	return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: op,
		Err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "nope"}}
}

func TestScanDeletedNodegroupLeavesStderrTraceAndNoRow(t *testing.T) {
	api := &fakeAWS{nodegroupFake: nodegroupFake{vanished: []string{"gone"},
		groups: []types.Nodegroup{fakeNodegroup("kept", types.AMITypesAl2023X8664Standard, "1.33", "r")}}}
	d, _ := nodeDeps(t, "33", api)
	code, out, se := run(t, d, "scan", "--cluster", nodeCluster, "--strict")
	if code != 0 || strings.Contains(out, "gone") || strings.Contains(out, "nodes:") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
	if se != "tideclear: nodegroup gone was deleted during the scan\n" {
		t.Fatalf("stderr = %q", se)
	}
}

func TestScanStrictSummaryLineNamesTheCount(t *testing.T) {
	api := &deprecationAWS{listErr: denied("ListInsights")} // one UNKNOWN insights row
	code, _, se := insightScan(t, api, "--strict")
	if code != 1 || !strings.HasSuffix(se, "tideclear: --strict: 1 stale/unknown item counts as a blocker\n") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	code, _, se = insightScan(t, api)
	if code != 0 || strings.Contains(se, "--strict") {
		t.Fatalf("without --strict: exit=%d stderr=%q", code, se)
	}
}

func TestScanStrictSummaryOmittedWhenLiveBlockerDecides(t *testing.T) {
	api := &deprecationAWS{listErr: denied("ListInsights")}
	d, _ := blockerDeps(t, api)
	code, _, se := run(t, d, "scan", "--cluster", "demo-cluster", "--insights", "--strict")
	if code != 1 || strings.Contains(se, "--strict:") {
		t.Fatalf("exit=%d stderr=%q: a live BLOCKER decides, no strict summary", code, se)
	}
}

func TestScanDegradedEveryCheckNamedLiveFindingsIntact(t *testing.T) {
	api := &deprecationAWS{listErr: denied("ListInsights"),
		addonFake:     addonFake{addonErr: denied("ListAddons")},
		nodegroupFake: nodegroupFake{ngErr: denied("ListNodegroups")}}
	d, c := blockerDeps(t, api)
	c.dyn.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", nil)
	})
	args := []string{"scan", "--cluster", "demo-cluster", "--insights"}
	code, out, se := run(t, d, args...)
	if code != 1 || !strings.Contains(out, "FlowSchema") || !strings.Contains(out, "BLOCKER") {
		t.Fatalf("exit=%d (live findings decide), stdout:\n%s", code, out)
	}
	for _, check := range []string{"insights", "addons", "nodegroups", "nodes"} {
		if !strings.Contains(out, "check skipped: "+check+": ") || !strings.Contains(se, tideWarn(check)) {
			t.Errorf("check %q not named on stdout and stderr\nstdout:\n%s\nstderr:\n%s", check, out, se)
		}
	}
	if !strings.HasSuffix(out, "result not clean: 1 unknown, 4 checks skipped\n") {
		t.Errorf("footer:\n%s", out)
	}
	_, js, _ := run(t, d, append(args, "--output", "json")...)
	var doc struct {
		Findings      []struct{ Kind string }
		ChecksSkipped []struct{ Check string }
		Nodes, Addons json.RawMessage
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil || len(doc.Findings) == 0 || len(doc.ChecksSkipped) != 4 || doc.Nodes != nil || doc.Addons != nil {
		t.Fatalf("json doc = %+v err=%v", doc, err)
	}
}

// blockerDeps is a 1.31 cluster whose FlowSchema v1beta3 is a BLOCKER at target 1.32.
func blockerDeps(t *testing.T, api ClusterAPI) (Deps, cluster) {
	t.Helper()
	c := newCluster("1", "31")
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"demo-cluster": "1.31"}}})
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		return c.disc, c.dyn, nil
	}
	d.Cluster = func(string, string) (ClusterAPI, error) { return api, nil }
	return d, c
}

func tideWarn(check string) string { return "tideclear: warning: " + check + " check skipped: " }
