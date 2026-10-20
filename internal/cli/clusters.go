package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/spf13/cobra"
)

func newClustersCmd(deps Deps) *cobra.Command {
	var profile, region, cluster, output string
	var allRegions bool
	cmd := &cobra.Command{
		Use:   "clusters",
		Short: "List EKS clusters with Kubernetes versions and support end dates",
		Long: "List EKS clusters with Kubernetes versions and support end dates.\n\n" +
			"Read-only. Credentials come from the default AWS chain (AWS_PROFILE and\n" +
			"AWS_REGION are honored). Extended support is flagged, never an error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClusters(cmd, deps, profile, region, cluster, output, allRegions)
		},
	}
	f := cmd.Flags()
	f.StringVar(&profile, "profile", "", "AWS shared-config profile")
	f.StringVar(&region, "region", "", "AWS region (default: from AWS_REGION or profile)")
	f.BoolVar(&allRegions, "all-regions", false, "scan every EKS region (skips regions that reject the credentials)")
	f.StringVar(&cluster, "cluster", "", "show only this cluster")
	f.StringVar(&output, "output", "text", "output format: text|json (table is an alias of text)")
	return cmd
}

func runClusters(cmd *cobra.Command, deps Deps, profile, region, cluster, output string, allRegions bool) error {
	format, err := parseOutput(output)
	if err != nil {
		return &ToolError{Err: err}
	}
	regions, err := resolveRegions(deps, profile, region, allRegions)
	if err != nil {
		return &ToolError{Err: err}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	if allRegions {
		// Prime the per-profile config with a fixed home region so credential
		// providers (STS) have one; a load error is cached and resurfaces below.
		_, _ = deps.EKS(profile, homeRegion)
	}
	lister := eksinfo.Lister{
		Now:     now,
		Clients: func(r string) (eksinfo.EKSAPI, error) { return deps.EKS(profile, r) },
	}
	cs, skipped, err := lister.List(cmd.Context(), regions, cluster)
	for _, s := range skipped {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "tideclear: warning: skipped region %s: %s\n", s.Region, oneLine(s.Err.Error()))
	}
	if err != nil {
		return &ToolError{Err: err}
	}
	if format == outputJSON {
		err = report.ClustersJSON(cmd.OutOrStdout(), cs, skipped)
	} else {
		err = report.ClustersText(cmd.OutOrStdout(), cs)
	}
	if err != nil {
		return &ToolError{Err: err}
	}
	return nil
}

func resolveRegions(deps Deps, profile, region string, all bool) ([]string, error) {
	switch {
	case all && region != "":
		return nil, errors.New("--all-regions and --region are mutually exclusive")
	case all:
		return eksinfo.AllRegions(), nil
	case region != "":
		return []string{region}, nil
	}
	r, err := deps.DefaultRegion(profile)
	if err != nil {
		return nil, err
	}
	if r == "" {
		return nil, errors.New("no AWS region: set --region or AWS_REGION")
	}
	return []string{r}, nil
}
