package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const readmeMaxWidth = 100

// readmeSamples maps the command line of every "$ tideclear ..." sample in a
// README console fence to the golden its output must equal, byte for byte. A
// sample with no entry here fails TestReadmeSamplesMatchGoldens. Console fences
// that start with no "$ " line (the add-ons table) are not output samples of one
// command and are checked for width only.
var readmeSamples = map[string]string{
	"tideclear scan --path ./k8s --target 1.37":  "scan-path-readme.txt",
	"tideclear clusters --all-regions":           "clusters.txt",
	"tideclear scan":                             "scan-live.txt",
	"tideclear insights --cluster demo-a":        "insights.txt",
	"tideclear scan --cluster demo-a --insights": "scan-insights.txt",
}

type consoleBlock struct {
	line  int // 1-based line of the opening fence
	lines []string
}

func readmeConsoleBlocks(t *testing.T) []consoleBlock {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []consoleBlock
	var cur *consoleBlock
	for i, l := range strings.Split(string(raw), "\n") {
		switch {
		case cur == nil && strings.HasPrefix(l, "```console"):
			cur = &consoleBlock{line: i + 1}
		case cur != nil && strings.HasPrefix(l, "```"):
			blocks = append(blocks, *cur)
			cur = nil
		case cur != nil:
			cur.lines = append(cur.lines, l)
		}
	}
	if cur != nil {
		t.Fatalf("README.md:%d: console fence is never closed", cur.line)
	}
	if len(blocks) == 0 {
		t.Fatal("README.md has no console fences")
	}
	return blocks
}

func TestReadmeSamplesMatchGoldens(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range readmeConsoleBlocks(t) {
		cmd, ok := strings.CutPrefix(b.lines[0], "$ ")
		if !ok {
			continue
		}
		name, ok := readmeSamples[cmd]
		if !ok {
			t.Errorf("README.md:%d: sample %q has no golden; add it to readmeSamples", b.line, cmd)
			continue
		}
		seen[cmd] = true
		want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(b.lines[1:], "\n") + "\n"
		if got != string(want) {
			t.Errorf("README.md:%d: sample %q differs from %s\n--- README ---\n%s\n--- golden ---\n%s", b.line, cmd, name, got, want)
		}
	}
	for cmd := range readmeSamples {
		if !seen[cmd] {
			t.Errorf("readmeSamples lists %q but the README has no such sample", cmd)
		}
	}
}

func TestReadmeConsoleLinesFitWidth(t *testing.T) {
	for _, b := range readmeConsoleBlocks(t) {
		for i, l := range b.lines {
			if n := utf8.RuneCountInString(l); n > readmeMaxWidth {
				t.Errorf("README.md:%d: console line is %d columns, max %d: %q", b.line+1+i, n, readmeMaxWidth, l)
			}
		}
	}
}
