// Package nodes checks worker nodes for the target Kubernetes version:
// Amazon Linux 2 images (kubelet 1.35+ needs cgroup v2) and kubelet version
// skew. Managed nodegroups come from the EKS API, every other node from the
// Kubernetes API. Read-only: list and describe only. Rows exist only for
// findings; a clean node or nodegroup has none.
package nodes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// NodegroupAPI is the read-only EKS surface the nodegroup check needs.
type NodegroupAPI interface {
	ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// Source values of Row.
const (
	SourceNodegroup = "nodegroup"
	SourceNode      = "node"
)

const customAMIType = string(types.AMITypesCustom)

// Row is one finding. ReleaseVersion is display only (it can be an AMI ID or
// an OS release) and is never parsed.
type Row struct {
	Source, Name, Version, AMIType, ReleaseVersion, OSImage string
	Severity                                                rules.Severity
	Reasons                                                 []string
}

// CheckNodegroups judges every managed nodegroup: AL2 by amiType prefix, skew
// from the nodegroup Version only. It also returns the groups it judged (name
// to amiType) so CheckNodes skips only nodes of those, and keeps the nodes of
// CUSTOM or unreported-amiType groups, whose OS this check cannot judge. A
// group deleted between list and describe is skipped and its name returned; any other API failure
// aborts the check (wrapped, so it stays classifiable).
func CheckNodegroups(ctx context.Context, api NodegroupAPI, cluster string, current, target rules.Version, nr *rules.NodeRules) ([]Row, map[string]string, []string, error) {
	var rows []Row
	judged := map[string]string{}
	var deleted []string
	pages := eks.NewListNodegroupsPaginator(api, &eks.ListNodegroupsInput{ClusterName: aws.String(cluster)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list nodegroups for %s: %w", cluster, err)
		}
		for _, name := range page.Nodegroups {
			out, err := api.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(cluster), NodegroupName: aws.String(name)})
			var gone *types.ResourceNotFoundException
			if errors.As(err, &gone) {
				deleted = append(deleted, name)
				continue
			}
			if err != nil {
				return nil, nil, nil, fmt.Errorf("describe nodegroup %s: %w", name, err)
			}
			if out.Nodegroup == nil {
				return nil, nil, nil, fmt.Errorf("describe nodegroup %s: empty response", name)
			}
			g := out.Nodegroup
			judged[name] = string(g.AmiType)
			if row, ok := judgeNodegroup(name, g, current, target, nr); ok {
				rows = append(rows, row)
			}
		}
	}
	sortRows(rows)
	return rows, judged, deleted, nil
}

func judgeNodegroup(name string, g *types.Nodegroup, current, target rules.Version, nr *rules.NodeRules) (Row, bool) {
	var j judgement
	if g.AmiType == "" {
		j.add(rules.SeverityWarning, "AMI type not reported")
	}
	if nr.IsAL2AMIType(string(g.AmiType)) {
		j.al2(nr, target, "amiType "+string(g.AmiType))
	}
	j.skew(aws.ToString(g.Version), current, target, nr)
	return Row{Source: SourceNodegroup, Name: name, Version: aws.ToString(g.Version), AMIType: string(g.AmiType),
		ReleaseVersion: aws.ToString(g.ReleaseVersion), Severity: j.severity, Reasons: j.reasons}, j.found()
}

var nodeGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

const nodePageSize = 500

// CheckNodes lists every v1 node and judges it: osImage against the AL2 rule
// and the kubelet version against the control plane and the target. Fargate
// nodes are skipped; so are managed-nodegroup nodes when skipManaged (the
// nodegroup check ran) and their group is in groups, except CUSTOM groups and
// groups with no reported amiType. A failed or unparseable
// kubelet version is a WARNING row, never an error.
func CheckNodes(ctx context.Context, dyn dynamic.Interface, current, target rules.Version, nr *rules.NodeRules, skipManaged bool, groups map[string]string) ([]Row, error) {
	var rows []Row
	opts := metav1.ListOptions{Limit: nodePageSize}
	for {
		list, err := dyn.Resource(nodeGVR).List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		for i := range list.Items {
			n := &list.Items[i]
			if skipNode(n, nr, skipManaged, groups) {
				continue
			}
			if row, ok := judgeNode(n, current, target, nr); ok {
				rows = append(rows, row)
			}
		}
		if list.GetContinue() == "" {
			break
		}
		opts.Continue = list.GetContinue()
	}
	sortRows(rows)
	return rows, nil
}

func skipNode(n *unstructured.Unstructured, nr *rules.NodeRules, skipManaged bool, groups map[string]string) bool {
	labels := n.GetLabels()
	if v, ok := labels[nr.FargateLabelKey]; ok && v == nr.FargateLabelValue {
		return true
	}
	ami := groups[labels[nr.ManagedNodegroupLabel]]
	return skipManaged && ami != "" && ami != customAMIType
}

func judgeNode(n *unstructured.Unstructured, current, target rules.Version, nr *rules.NodeRules) (Row, bool) {
	kubelet, _, _ := unstructured.NestedString(n.Object, "status", "nodeInfo", "kubeletVersion")
	osImage, _, _ := unstructured.NestedString(n.Object, "status", "nodeInfo", "osImage")
	var j judgement
	if nr.IsAL2OSImage(osImage) {
		j.al2(nr, target, "osImage "+osImage)
	}
	j.skew(kubelet, current, target, nr)
	return Row{Source: SourceNode, Name: n.GetName(), Version: kubelet, OSImage: osImage, Severity: j.severity, Reasons: j.reasons}, j.found()
}

func sortRows(rows []Row) {
	slices.SortStableFunc(rows, func(a, b Row) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
}
