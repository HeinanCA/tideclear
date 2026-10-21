package cli

import (
	"bytes"
	"testing"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := buildVersion
	buildVersion = v
	t.Cleanup(func() { buildVersion = old })
}

func TestVersionDefaultsToDev(t *testing.T) {
	if buildVersion != "dev" {
		t.Fatalf("buildVersion = %q, want %q", buildVersion, "dev")
	}
}

func TestVersionOutput(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		t.Run(args[0], func(t *testing.T) {
			withVersion(t, "1.2.3")
			var out, errOut bytes.Buffer
			code := Run(args, &out, &errOut)
			if code != 0 {
				t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
			}
			if want := "tideclear version 1.2.3\n"; out.String() != want {
				t.Fatalf("stdout = %q, want %q", out.String(), want)
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", errOut.String())
			}
		})
	}
}

func TestVersionOutputDefault(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "tideclear version dev\n"; out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestVersionCommandRejectsArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"version", "extra"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	assertOneLine(t, errOut.String())
}
