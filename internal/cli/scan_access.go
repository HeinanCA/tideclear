package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

// permissionPreflight reports every missing list permission up front, in one
// stderr line, before the scan's own per-kind skips; the scan itself runs
// unchanged afterwards. It is bounded by extrasTimeout, and a --path scan has
// no cluster to ask. A check that cannot finish degrades to a warning plus a
// "permissions" entry in the returned skipped checks (the denials it did find
// are still returned); a 401 or a parent cancel or timeout is returned and
// exits 2.
func permissionPreflight(ctx context.Context, stderr io.Writer, fl scanFlags, disc discovery.DiscoveryInterface, dyn dynamic.Interface, set *rules.Set) ([]apiscan.Permission, []report.CheckSkipped, error) {
	if fl.pathSet {
		return nil, nil, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, extrasTimeout)
	defer cancel()
	missing, gaps, err := apiscan.CheckAccess(callCtx, disc, dyn, set)
	if err != nil && (eksinfo.IsCredentialFailure(err) || apierrors.IsUnauthorized(err) || ctx.Err() != nil) {
		return nil, nil, fmt.Errorf("check permissions: %w", err)
	}
	if len(missing) > 0 {
		names := make([]string, len(missing))
		for i, p := range missing {
			names[i] = p.String()
		}
		_, _ = fmt.Fprintf(stderr, "tideclear: warning: missing permissions: %s\n", strings.Join(names, ", "))
	}
	reasons := slices.Clone(gaps)
	if err != nil {
		reasons = append(reasons, err.Error())
	}
	if len(reasons) == 0 {
		return missing, nil, nil
	}
	reason := oneLine(strings.Join(reasons, "; "))
	state := "incomplete" // some of the check ran; it was not all clear
	if len(missing) == 0 && len(gaps) == 0 {
		state = "skipped"
	}
	_, _ = fmt.Fprintf(stderr, "tideclear: warning: permission check %s: %s\n", state, reason)
	return missing, []report.CheckSkipped{{Check: "permissions", Reason: reason}}, nil
}
