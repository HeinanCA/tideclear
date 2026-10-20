package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/HeinanCA/tideclear/internal/addons"
	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/spf13/cobra"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

// ErrBlockers signals that a scan found removed-API blockers (exit 1).
var ErrBlockers = errors.New("blockers found")

// ToolError wraps a tool, credential or connectivity failure (exit 2).
type ToolError struct{ Err error }

func (e *ToolError) Error() string {
	if e.Err == nil {
		return "tool error"
	}
	return e.Err.Error()
}
func (e *ToolError) Unwrap() error { return e.Err }

// exitCode maps a command error to a process exit code: 0 clean,
// 1 blockers, 2 any other failure.
func exitCode(err error) int {
	var te *ToolError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &te):
		return 2
	case errors.Is(err, ErrBlockers):
		return 1
	default:
		return 2
	}
}

// ClusterAPI is the per-cluster EKS surface commands may call. It is
// read-only by construction; later checks add their own read-only interfaces.
type ClusterAPI interface {
	insights.API
	addons.API
	nodes.NodegroupAPI
}

// Deps holds the external adapters commands use; tests substitute fakes.
type Deps struct {
	// EKS builds an EKS client for a profile and region ("" = default chain).
	EKS func(profile, region string) (eksinfo.EKSAPI, error)
	// Cluster builds a client for per-cluster reads (insights) for a profile
	// and region; it shares the cached AWS config with EKS.
	Cluster func(profile, region string) (ClusterAPI, error)
	// DefaultRegion resolves the region the default chain would use.
	DefaultRegion func(profile string) (string, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Kube builds Kubernetes clients for a kubeconfig path and context
	// ("" = default loading rules and current context).
	Kube func(kubeconfig, kubeContext string) (discovery.DiscoveryInterface, dynamic.Interface, error)
	// KubeIdentity names the kube context and API server host a Kube call
	// would use; nil or "" means unknown.
	KubeIdentity func(kubeconfig, kubeContext string) (name, server string)
}

func newRootCmd(deps Deps, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "tideclear",
		Short:         "tideclear: read-only EKS upgrade readiness checks",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// Runnable so cobra validates Args; bare invocation prints help.
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(newClustersCmd(deps))
	root.AddCommand(newScanCmd(deps))
	root.AddCommand(newInsightsCmd(deps))
	return root
}

// Run executes the CLI and returns the process exit code. A failure prints
// exactly one line to stderr; ErrBlockers prints nothing (the report does).
func Run(args []string, stdout, stderr io.Writer) int {
	return runDeps(realDeps(), args, stdout, stderr)
}

func runDeps(deps Deps, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(deps, stdout, stderr)
	root.SetArgs(args)
	return execute(root, stderr)
}

// runCmd runs a root whose RunE is fn (test seam for error/panic paths).
func runCmd(fn func() error, stdout, stderr io.Writer) int {
	root := newRootCmd(Deps{}, stdout, stderr)
	root.RunE = func(*cobra.Command, []string) error { return fn() }
	root.SetArgs(nil)
	return execute(root, stderr)
}

func execute(root *cobra.Command, stderr io.Writer) (code int) {
	defer func() {
		if v := recover(); v != nil {
			_, _ = fmt.Fprintf(stderr, "tideclear: internal error: %s\n", oneLine(fmt.Sprint(v)))
			code = 2
		}
	}()
	err := root.Execute()
	if err != nil && !isPureBlockers(err) {
		_, _ = fmt.Fprintf(stderr, "tideclear: %s\n", oneLine(err.Error()))
	}
	return exitCode(err)
}

// isPureBlockers reports whether err is a blockers result with no tool failure.
func isPureBlockers(err error) bool {
	var te *ToolError
	return errors.Is(err, ErrBlockers) && !errors.As(err, &te)
}

// oneLine collapses CR/LF runs into "; " so output stays a single line.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }), "; ")
}
