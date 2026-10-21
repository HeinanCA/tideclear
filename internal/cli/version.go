package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// buildVersion is overridden at link time via -ldflags "-X <module>/internal/cli.buildVersion=...".
var buildVersion = "dev"

func versionLine(v string) string { return "tideclear version " + v + "\n" }

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tideclear version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), versionLine(buildVersion))
			return err
		},
	}
}
