package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/spf13/cobra"
)

func newInsightsCmd(deps Deps) *cobra.Command {
	var profile, region, cluster, target, output string
	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Show EKS Upgrade Insights for a cluster",
		Long: "Show the EKS Upgrade Insights (category UPGRADE_READINESS) of one cluster:\n" +
			"name, target Kubernetes version, status and how long ago EKS refreshed each one.\n\n" +
			"Read-only. tideclear never starts an insights refresh: if the data is old, use\n" +
			"the \"Refresh insights\" button in the EKS console. Any AWS error, including a\n" +
			"denied permission, exits 2.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInsights(cmd, deps, profile, region, cluster, target, output)
		},
	}
	f := cmd.Flags()
	f.StringVar(&profile, "profile", "", "AWS shared-config profile")
	f.StringVar(&region, "region", "", "AWS region (default: from AWS_REGION or profile)")
	f.StringVar(&cluster, "cluster", "", "EKS cluster name (required)")
	f.StringVar(&target, "target", "", "only insights for this Kubernetes version, e.g. 1.33 (default: all versions)")
	f.StringVar(&output, "output", "text", "output format: text|json (table is an alias of text)")
	_ = cmd.MarkFlagRequired("cluster")
	return cmd
}

func runInsights(cmd *cobra.Command, deps Deps, profile, region, cluster, target, output string) error {
	format, err := parseOutput(output)
	if err != nil {
		return &ToolError{Err: err}
	}
	var tv *rules.Version
	if target != "" {
		v, err := rules.ParseVersion(target)
		if err != nil {
			return &ToolError{Err: fmt.Errorf("--target: %w", err)}
		}
		tv = &v
	}
	regions, err := resolveRegions(deps, profile, region, false)
	if err != nil {
		return &ToolError{Err: err}
	}
	api, err := deps.Cluster(profile, regions[0])
	if err != nil {
		return &ToolError{Err: err}
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), eksinfo.DefaultRegionTimeout)
	defer cancel()
	ins, err := insights.List(ctx, api, cluster, tv)
	if err != nil {
		return &ToolError{Err: fmt.Errorf("list insights for %s: %w", cluster, err)}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	if format == outputJSON {
		err = report.InsightsJSON(cmd.OutOrStdout(), ins)
	} else {
		err = report.InsightsText(cmd.OutOrStdout(), ins, now())
	}
	if err != nil {
		return &ToolError{Err: err}
	}
	return nil
}
