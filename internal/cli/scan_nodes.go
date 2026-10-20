package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"
)

// nodegroupState is what the node list needs from the nodegroup check: whether
// it ran (managed nodes are then covered by their nodegroup row) and the
// groups it judged, name to amiType (CUSTOM or unreported ones keep their
// nodes in the node list).
type nodegroupState struct {
	ran    bool
	groups map[string]string
}

// nodegroupExtras adds the managed-nodegroup rows, bounded by extrasTimeout.
// Classification uses the parent ctx, so only a parent cancel or timeout (or a
// credential failure) is fatal; anything else degrades to a skipped check.
func nodegroupExtras(ctx context.Context, stderr io.Writer, api ClusterAPI, fl scanFlags, current, target rules.Version, nr *rules.NodeRules, ex report.Extras) (report.Extras, nodegroupState, error) {
	callCtx, cancel := context.WithTimeout(ctx, extrasTimeout)
	defer cancel()
	rows, groups, err := nodes.CheckNodegroups(callCtx, api, fl.cluster, current, target, nr)
	switch {
	case err == nil:
		ex.Nodes = append(ex.Nodes, rows...)
		return ex, nodegroupState{ran: true, groups: groups}, nil
	case eksinfo.IsCredentialFailure(err), ctx.Err() != nil:
		return ex, nodegroupState{}, fmt.Errorf("read nodegroups for %s: %w", fl.cluster, err)
	}
	return skipCheck(stderr, ex, "nodegroups", err), nodegroupState{}, nil
}

// nodesExtras adds the node rows from the Kubernetes API, bounded by
// extrasTimeout. A failed list degrades to a skipped check, a 403 included; a
// 401 means the cluster credentials are rejected and is fatal.
func nodesExtras(ctx context.Context, stderr io.Writer, dyn dynamic.Interface, current, target rules.Version, nr *rules.NodeRules, ng nodegroupState, ex report.Extras) (report.Extras, error) {
	callCtx, cancel := context.WithTimeout(ctx, extrasTimeout)
	defer cancel()
	rows, err := nodes.CheckNodes(callCtx, dyn, current, target, nr, ng.ran, ng.groups)
	switch {
	case err == nil:
		ex.Nodes = append(ex.Nodes, rows...)
		return ex, nil
	case eksinfo.IsCredentialFailure(err), apierrors.IsUnauthorized(err), ctx.Err() != nil:
		return ex, fmt.Errorf("read nodes: %w", err)
	}
	return skipCheck(stderr, ex, "nodes", err), nil
}

// skipCheck records a check that did not run: a skipped entry and one stderr line.
func skipCheck(stderr io.Writer, ex report.Extras, check string, err error) report.Extras {
	reason := oneLine(err.Error())
	ex.Skipped = append(ex.Skipped, report.CheckSkipped{Check: check, Reason: reason})
	_, _ = fmt.Fprintf(stderr, "tideclear: warning: %s check skipped: %s\n", check, reason)
	return ex
}
