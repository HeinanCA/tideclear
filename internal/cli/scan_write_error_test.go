package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestScanReportWriteErrorExitsTwo(t *testing.T) {
	var se bytes.Buffer
	code := runDeps(kubeDeps(t, newCluster("1", "31+")), []string{"scan"}, brokenWriter{}, &se)
	if code != 2 || strings.Count(se.String(), "\n") != 1 || !strings.Contains(se.String(), "broken pipe") {
		t.Fatalf("exit=%d stderr=%q, want 2 and one line naming the write error", code, se.String())
	}
}
