package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
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

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
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
	return root
}

// Run executes the CLI and returns the process exit code. A failure prints
// exactly one line to stderr; ErrBlockers prints nothing (the report does).
func Run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	return execute(root, stderr)
}

// runCmd runs a root whose RunE is fn (test seam for error/panic paths).
func runCmd(fn func() error, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
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
