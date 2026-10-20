package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
)

// scanTimeout bounds a whole scan (a var so tests can shorten it).
var scanTimeout = 10 * time.Minute

type scanFlags struct {
	kubeconfig, kubeContext, target, cluster, profile, region, output, path string
	pathSet                                                                 bool // --path given (even if empty)
	insights, strict                                                        bool
}

func newScanCmd(deps Deps) *cobra.Command {
	var fl scanFlags
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Find deprecated and removed Kubernetes APIs in a live cluster or in manifests",
		Long: "Find objects in a live cluster that use Kubernetes APIs deprecated or removed\n" +
			"by the target version. Read-only: only get/list requests are sent.\n\n" +
			"With --path, scan .yaml/.yml/.json manifests (a directory, searched recursively, or\n" +
			"one file) instead. No cluster is contacted: give --target, or --cluster to\n" +
			"read the current version from EKS (target defaults to its next minor). --path\n" +
			"cannot be combined with --kubeconfig or --context.\n\n" +
			"The target defaults to the cluster's next minor version. With --cluster the\n" +
			"current version comes from EKS (honoring --profile and --region); otherwise\n" +
			"from the API server. --cluster and --context are independent: they must name\n" +
			"the same cluster.\n\n" +
			"With --insights (and --cluster) the EKS Upgrade Insights are reconciled with the\n" +
			"live scan: BLOCKER (live objects, or a client called the API in the last 7 days),\n" +
			"STALE (last call older than 7 days; the insight clears 30 days after it), OK or\n" +
			"UNKNOWN. If insights cannot be read the rest of the report still prints, with an\n" +
			"UNKNOWN row; expired or invalid credentials exit 2.\n\n" +
			"Exit codes: 0 clean (warnings allowed), 1 blockers (--strict: also STALE and\n" +
			"UNKNOWN insight rows), 2 tool or connection error.\n" +
			"Groups or kinds that cannot be read are reported on stderr and in the JSON\n" +
			"\"skipped\" list; the exit code reflects only what was scanned.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl.pathSet = cmd.Flags().Changed("path")
			return runScan(cmd, deps, fl)
		},
	}
	f := cmd.Flags()
	f.StringVar(&fl.kubeconfig, "kubeconfig", "", "path to a kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	f.StringVar(&fl.kubeContext, "context", "", "kubeconfig context (default: current context)")
	f.StringVar(&fl.target, "target", "", "target Kubernetes version, e.g. 1.33 (default: current minor + 1)")
	f.StringVar(&fl.cluster, "cluster", "", "EKS cluster name: read the current version from EKS")
	f.StringVar(&fl.profile, "profile", "", "AWS shared-config profile (with --cluster)")
	f.StringVar(&fl.region, "region", "", "AWS region (with --cluster)")
	f.StringVar(&fl.path, "path", "", "scan manifests under this file or directory instead of a live cluster")
	f.BoolVar(&fl.insights, "insights", false, "also read EKS Upgrade Insights and reconcile them with the scan (needs --cluster, not --path)")
	f.BoolVar(&fl.strict, "strict", false, "exit 1 on STALE or UNKNOWN reconcile rows as well as blockers")
	f.StringVar(&fl.output, "output", "text", "output format: text|json (table is an alias of text)")
	return cmd
}

func runScan(cmd *cobra.Command, deps Deps, fl scanFlags) error {
	format, target, err := parseScanFlags(fl)
	if err != nil {
		return &ToolError{Err: err}
	}
	set, err := rules.Load()
	if err != nil {
		return &ToolError{Err: err}
	}
	src, dyn, err := newSource(cmd, deps, fl, set)
	if err != nil {
		return &ToolError{Err: err}
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	releaseSignalOnDone(ctx, stop)
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	cur, tv, err := resolveTarget(ctx, deps, fl, src, target)
	if err != nil {
		return &ToolError{Err: interruptedOr(ctx, err)}
	}
	res, err := apiscan.Scan(ctx, src, set, tv)
	if err != nil {
		return &ToolError{Err: interruptedOr(ctx, err)}
	}
	annotate(cmd, deps, fl, &res)
	ex, err := collectExtras(ctx, cmd.ErrOrStderr(), deps, fl, res, cur, tv, dyn)
	if err != nil {
		return &ToolError{Err: interruptedOr(ctx, err)}
	}
	if format == outputJSON {
		err = report.ScanJSON(cmd.OutOrStdout(), res, ex)
	} else {
		err = report.ScanText(cmd.OutOrStdout(), res, ex)
	}
	if err != nil {
		return &ToolError{Err: err}
	}
	if n := strictBlockers(res, ex, fl.strict); n > 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "tideclear: --strict: %d stale/unknown items count as blockers\n", n)
	}
	return exitFor(res, ex, fl.strict)
}

// parseScanFlags validates the flags that need no client: output format,
// --target, and the --path and --insights combinations.
func parseScanFlags(fl scanFlags) (outputFormat, *rules.Version, error) {
	format, err := parseOutput(fl.output)
	if err != nil {
		return 0, nil, err
	}
	var target *rules.Version
	if fl.target != "" {
		t, err := rules.ParseVersion(fl.target)
		if err != nil {
			return 0, nil, fmt.Errorf("--target: %w", err)
		}
		target = &t
	}
	if err := checkInsightsFlags(fl); err != nil {
		return 0, nil, err
	}
	if fl.pathSet {
		if err := checkPathFlags(fl, target); err != nil {
			return 0, nil, err
		}
	}
	return format, target, nil
}

// newSource builds the scan source; the dynamic client is nil for a --path scan.
func newSource(cmd *cobra.Command, deps Deps, fl scanFlags, set *rules.Set) (apiscan.Source, dynamic.Interface, error) {
	if fl.pathSet {
		return apiscan.NewManifestSource(fl.path, cmd.ErrOrStderr()), nil, nil
	}
	disc, dyn, err := deps.Kube(fl.kubeconfig, fl.kubeContext)
	if err != nil {
		return nil, nil, err
	}
	return apiscan.NewClusterSource(disc, dyn, set), dyn, nil
}

// resolveTarget returns the cluster's current version and the version the
// scan checks against: --target, else the current version's next minor.
// A --path scan without --cluster has no current version (zero value).
func resolveTarget(ctx context.Context, deps Deps, fl scanFlags, src apiscan.Source, target *rules.Version) (current, tv rules.Version, err error) {
	if fl.pathSet && fl.cluster == "" {
		return rules.Version{}, *target, nil // checkPathFlags guarantees it; there is no current version to validate against
	}
	current, err = currentVersion(ctx, deps, fl, src)
	if err != nil {
		return rules.Version{}, rules.Version{}, err
	}
	tv = current.Next()
	if target != nil {
		tv = *target
	}
	return current, tv, rules.ValidateTarget(current, tv)
}

// annotate fills the result's cluster identity and echoes skipped groups to stderr.
func annotate(cmd *cobra.Command, deps Deps, fl scanFlags, res *apiscan.Result) {
	res.Cluster = apiscan.ClusterInfo{EKSCluster: fl.cluster}
	res.Path = fl.path
	if !fl.pathSet && deps.KubeIdentity != nil {
		res.Cluster.Context, res.Cluster.Server = deps.KubeIdentity(fl.kubeconfig, fl.kubeContext)
	}
	for i, s := range res.Skipped {
		res.Skipped[i].Reason = oneLine(s.Reason)
		if fl.path == "" { // the manifest source already warned once per file
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "tideclear: warning: %s\n", oneLine(skippedMessage(s)))
		}
	}
}

// checkInsightsFlags: --insights reads the EKS cluster's insights and joins
// them with a live scan, so it needs --cluster and cannot be used with --path.
func checkInsightsFlags(fl scanFlags) error {
	switch {
	case !fl.insights:
		return nil
	case fl.pathSet:
		return errors.New("--insights needs a live scan: it cannot be combined with --path")
	case fl.cluster == "":
		return errors.New("--insights needs --cluster")
	}
	return nil
}

// checkPathFlags rejects --path combinations that make no sense, before any
// client is built.
func checkPathFlags(fl scanFlags, target *rules.Version) error {
	switch {
	case fl.path == "":
		return errors.New("--path must not be empty")
	case fl.kubeconfig != "" || fl.kubeContext != "":
		return errors.New("--path cannot be combined with --kubeconfig or --context")
	case target == nil && fl.cluster == "":
		return errors.New("--path needs --target (or --cluster to read the current version from EKS)")
	}
	if _, err := os.Stat(fl.path); err != nil {
		return fmt.Errorf("--path: %s", oneLine(err.Error()))
	}
	return nil
}

func skippedMessage(s apiscan.Skipped) string {
	if s.Kind == "" {
		return fmt.Sprintf("skipped group %s: %s", s.Group, s.Reason)
	}
	return fmt.Sprintf("skipped kind %s (group %q): %s", s.Kind, s.Group, s.Reason)
}

// currentVersion is the cluster's version: from EKS with --cluster (the API
// server is not asked), else from the API server.
func currentVersion(ctx context.Context, deps Deps, fl scanFlags, src apiscan.Source) (rules.Version, error) {
	if fl.cluster == "" {
		v, known, err := src.ServerVersion(ctx)
		if err != nil {
			return rules.Version{}, fmt.Errorf("cannot reach cluster: %w", err)
		}
		if !known {
			return rules.Version{}, errors.New("cannot determine the cluster version: use --cluster")
		}
		return v, nil
	}
	regions, err := resolveRegions(deps, fl.profile, fl.region, false)
	if err != nil {
		return rules.Version{}, err
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	lister := eksinfo.Lister{
		Now:     now,
		Clients: func(r string) (eksinfo.EKSAPI, error) { return deps.EKS(fl.profile, r) },
	}
	cs, _, err := lister.List(ctx, regions, fl.cluster)
	if err != nil {
		return rules.Version{}, err
	}
	return rules.ParseVersion(cs[0].Version)
}

// interruptedOr turns a deadline or Ctrl-C into a plain message, else err.
func interruptedOr(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("scan timed out after %s", durationText(scanTimeout))
	case errors.Is(ctx.Err(), context.Canceled):
		return errors.New("scan interrupted")
	}
	return err
}

// durationText renders 10m0s as 10m.
func durationText(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		return strings.TrimSuffix(s, "0s")
	}
	return s
}

// releaseSignalOnDone calls stop once ctx is done, so a second Ctrl-C gets
// the default behavior (process exit) instead of being swallowed.
func releaseSignalOnDone(ctx context.Context, stop func()) {
	go func() {
		<-ctx.Done()
		stop()
	}()
}
