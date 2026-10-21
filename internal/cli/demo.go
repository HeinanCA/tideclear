package cli

import (
	"fmt"
	"time"

	"github.com/HeinanCA/tideclear/internal/demo"
	"github.com/HeinanCA/tideclear/internal/report"
	"github.com/spf13/cobra"
)

const (
	demoBanner = "SAMPLE DATA: this is a built-in demonstration, not a scan of your cluster.\n\n"
	demoNote   = "\nThis was a built-in demonstration of the report shape. Nothing was read from AWS or Kubernetes,\n" +
		"and it says nothing about any real cluster. Dates are relative to the day you run it.\n" +
		"A real scan with a BLOCKER exits 1; demo always exits 0.\n" +
		"To scan a real cluster: tideclear scan --cluster NAME --insights\n"
)

func newDemoCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "demo",
		Short: "Print a sample report from built-in data (no cluster or credentials needed)",
		Long: "Print a sample upgrade-readiness report for a fictional cluster, built from\n" +
			"data compiled into the binary. Nothing is read from AWS or Kubernetes. It\n" +
			"shows what a report looks like; it does not scan or validate anything, and it\n" +
			"always exits 0.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now
			if deps.Now != nil {
				now = deps.Now
			}
			res, ex, err := demo.Sample(now())
			if err != nil {
				return &ToolError{Err: err}
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprint(out, demoBanner); err != nil {
				return &ToolError{Err: err}
			}
			if err := report.ScanText(out, res, ex); err != nil {
				return &ToolError{Err: err}
			}
			_, err = fmt.Fprint(out, demoNote)
			if err != nil {
				return &ToolError{Err: err}
			}
			return nil
		},
	}
}
