package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

const refreshHintText = `use the console "Refresh insights" button`

func TestScanInsightsHintWhenRefreshOlderThan24h(t *testing.T) {
	old := fixedNow.Add(-30 * time.Hour)
	api := &deprecationAWS{status: types.InsightStatusValuePassing, refresh: &old}
	code, out, _ := insightScan(t, api)
	if code != 0 || !strings.Contains(out, refreshHintText) {
		t.Fatalf("exit=%d, want hint in stdout:\n%s", code, out)
	}
}

func TestScanInsightsNoHintWhenFresh(t *testing.T) {
	_, out, _ := insightScan(t, &deprecationAWS{status: types.InsightStatusValuePassing})
	if strings.Contains(out, "Refresh insights") {
		t.Fatalf("unexpected hint for a 1h-old refresh:\n%s", out)
	}
}

func TestScanHelpStrictMatchesBehavior(t *testing.T) {
	_, out, _ := run(t, insightDeps(t, cleanCluster(), &deprecationAWS{}), "scan", "--help")
	if !strings.Contains(out, "UNKNOWN add-on rows") {
		t.Fatalf("--strict help does not mention add-on rows:\n%s", out)
	}
}
