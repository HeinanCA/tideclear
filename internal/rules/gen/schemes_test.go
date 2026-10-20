package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemesCoverModule fails when k8s.io/api gains a group/version package that addToSchemes lacks.
func TestSchemesCoverModule(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}\t{{.ImportPath}}", "k8s.io/api/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dir, _, _ := strings.Cut(line, "\t")
		if _, err := os.Stat(filepath.Join(dir, "register.go")); err == nil {
			n++
		}
	}
	if n != len(addToSchemes) {
		t.Errorf("k8s.io/api has %d registering packages, addToSchemes has %d", n, len(addToSchemes))
	}
}
