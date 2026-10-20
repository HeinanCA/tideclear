package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

func TestScanInsightsPassingDetaillessUntrustedRefreshIsUnknown(t *testing.T) {
	zero, future, old := time.Time{}, fixedNow.Add(time.Hour), fixedNow.Add(-8*24*time.Hour)
	for name, at := range map[string]*time.Time{"zero": &zero, "future": &future, "older than 7d": &old} {
		t.Run(name, func(t *testing.T) {
			api := &deprecationAWS{status: types.InsightStatusValuePassing, noSummary: true, refresh: at}
			code, out, _ := insightScan(t, api)
			if code != 0 || !strings.Contains(out, "result not clean: 1 unknown") || strings.Contains(out, "no deprecated API usage reported") {
				t.Fatalf("exit=%d stdout:\n%s", code, out)
			}
			if code, _, _ := insightScan(t, api, "--strict"); code != 1 {
				t.Fatalf("--strict exit = %d, want 1", code)
			}
		})
	}
}
