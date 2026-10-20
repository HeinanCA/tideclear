package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/nodes"
	"github.com/HeinanCA/tideclear/internal/reconcile"
)

const hostile = "kube\x1b[2J\x1b]0;owned\x07ctl\r\nforged line"

func assertNoControl(t *testing.T, out string) {
	t.Helper()
	for _, r := range strings.TrimSuffix(out, "\n") {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f) {
			t.Fatalf("control character %q in text output:\n%q", r, out)
		}
	}
}

func TestEvidenceTextStripsControlCharsFromUserAgent(t *testing.T) {
	row := reconcile.Row{Clients: []insights.Client{{UserAgent: hostile, Requests: 3}}}
	out := evidenceText(row)
	assertNoControl(t, out)
	if strings.Contains(out, "\n") {
		t.Fatalf("user agent forged a line break: %q", out)
	}
	if !strings.Contains(out, "kube") || !strings.Contains(out, "forged line") {
		t.Fatalf("printable user agent text was lost: %q", out)
	}
}

func TestNodesSectionStripsControlCharsFromNodeStrings(t *testing.T) {
	rows := []nodes.Row{{Source: "node", Name: hostile, OSImage: hostile, Severity: "WARNING"}}
	var b bytes.Buffer
	if err := nodesSection(&b, rows); err != nil {
		t.Fatal(err)
	}
	assertNoControl(t, b.String())
	if got := strings.Count(b.String(), "\n"); got != 4 { // blank line, "nodes:", header, one row
		t.Fatalf("a node string changed the line count to %d:\n%q", got, b.String())
	}
}
