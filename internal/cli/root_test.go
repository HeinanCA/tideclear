package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRunHelpExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"--help"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "tideclear") {
		t.Fatalf("help output missing program name: %q", out.String())
	}
}

func TestRunUnknownFlagExitsTwoWithOneLine(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"--nope"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, errOut.String())
}

// assertOneLine fails unless s is "tideclear: <msg>\n" with no other newline.
func assertOneLine(t *testing.T, s string) {
	t.Helper()
	if !strings.HasPrefix(s, "tideclear: ") || !strings.HasSuffix(s, "\n") ||
		strings.Count(s, "\n") != 1 || strings.Contains(s, "\r") {
		t.Fatalf("stderr is not exactly one 'tideclear: ' line: %q", s)
	}
}

func TestRunPositionalArgExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"foo"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, errOut.String())
}

// runWith runs a root whose RunE returns err (or panics with pv).
func runWith(err error, pv any) (int, string) {
	var out, errOut bytes.Buffer
	code := runCmd(func() error {
		if pv != nil {
			panic(pv)
		}
		return err
	}, &out, &errOut)
	return code, errOut.String()
}

func TestRunJoinedToolErrorAndBlockers(t *testing.T) {
	code, se := runWith(errors.Join(&ToolError{Err: errors.New("boom")}, ErrBlockers), nil)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, se)
}

func TestRunPureBlockersPrintsNothing(t *testing.T) {
	code, se := runWith(fmt.Errorf("scan: %w", ErrBlockers), nil)
	if code != 1 || se != "" {
		t.Fatalf("exit=%d stderr=%q, want 1 and empty", code, se)
	}
}

func TestRunNilToolErrInnerDoesNotPanic(t *testing.T) {
	code, se := runWith(&ToolError{}, nil)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, se)
}

func TestRunRecoversPanic(t *testing.T) {
	code, se := runWith(nil, "kaboom")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, se)
	if !strings.Contains(se, "internal error: kaboom") {
		t.Fatalf("stderr = %q, want internal error: kaboom", se)
	}
}

func TestRunMultilineErrorCollapsed(t *testing.T) {
	code, se := runWith(errors.New("a\nb\r\nc\n"), nil)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, se)
	if se != "tideclear: a; b; c\n" {
		t.Fatalf("stderr = %q", se)
	}
}

func TestExitCode(t *testing.T) {
	cause := errors.New("boom")
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil is clean", nil, 0},
		{"blockers sentinel", ErrBlockers, 1},
		{"wrapped blockers", fmt.Errorf("scan: %w", ErrBlockers), 1},
		{"tool error", &ToolError{Err: cause}, 2},
		{"wrapped tool error", fmt.Errorf("x: %w", &ToolError{Err: cause}), 2},
		{"plain error is a tool failure", cause, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Fatalf("exitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestToolErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("boom")
	te := &ToolError{Err: cause}
	if !errors.Is(te, cause) {
		t.Fatal("ToolError does not unwrap to its cause")
	}
	if te.Error() != "boom" {
		t.Fatalf("Error() = %q, want %q", te.Error(), "boom")
	}
}
