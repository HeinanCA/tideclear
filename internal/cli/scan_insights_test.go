package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakedisc "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	fakedyn "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const usageFlowSchema = "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas"

// deprecationAWS serves one deprecated-API insight with the given clients.
type deprecationAWS struct {
	status      types.InsightStatusValue
	clients     []types.ClientStat
	listErr     error
	describeErr error
	name        string     // insight name; default "Deprecated APIs removed in Kubernetes v1.32"
	none        bool       // ListInsights returns no insights at all
	noSummary   bool       // DescribeInsight returns no category summary
	target      string     // insight target version; default echoes the request filter
	refresh     *time.Time // insight LastRefreshTime; default one hour before fixedNow
}

func (f *deprecationAWS) ListInsights(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.none {
		return &eks.ListInsightsOutput{}, nil
	}
	sum := insightSummary("i1", f.insightName(), f.status, fixedNow.Add(-time.Hour))
	if f.refresh != nil {
		sum.LastRefreshTime = f.refresh
	}
	sum.KubernetesVersion = aws.String(f.target)
	if f.target == "" && in.Filter != nil && len(in.Filter.KubernetesVersions) > 0 {
		sum.KubernetesVersion = aws.String(in.Filter.KubernetesVersions[0])
	}
	return &eks.ListInsightsOutput{Insights: []types.InsightSummary{sum}}, nil
}

func (f *deprecationAWS) DescribeInsight(context.Context, *eks.DescribeInsightInput, ...func(*eks.Options)) (*eks.DescribeInsightOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if f.noSummary {
		return &eks.DescribeInsightOutput{Insight: &types.Insight{}}, nil
	}
	return &eks.DescribeInsightOutput{Insight: &types.Insight{CategorySpecificSummary: &types.InsightCategorySpecificSummary{
		DeprecationDetails: []types.DeprecationDetail{{Usage: aws.String(usageFlowSchema), ClientStats: f.clients}},
	}}}, nil
}

func (f *deprecationAWS) insightName() string {
	if f.name != "" {
		return f.name
	}
	return "Deprecated APIs removed in Kubernetes v1.32"
}

func callAt(ua string, n int32, ago time.Duration) types.ClientStat {
	t := fixedNow.Add(-ago)
	return types.ClientStat{UserAgent: aws.String(ua), NumberOfRequestsLast30Days: n, LastRequestTime: &t}
}

// cleanCluster is a fake cluster whose only FlowSchema is written at v1 (fixed).
func cleanCluster() cluster {
	fs := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": fsGV, "kind": "FlowSchema", "metadata": map[string]any{"name": "web"}}}
	fs.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: "Update", APIVersion: fsGV}})
	disc := &fakedisc.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{
		{GroupVersion: fsGV, APIResources: []metav1.APIResource{{Name: "flowschemas", Kind: "FlowSchema", Verbs: metav1.Verbs{"get", "list"}}}},
	}}}
	disc.FakedServerVersion = newCluster("1", "30").disc.FakedServerVersion
	dyn := fakedyn.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{fsGVR: "FlowSchemaList"}, fs)
	return cluster{disc: disc, dyn: dyn}
}

func insightDeps(t *testing.T, c cluster, api ClusterAPI) Deps {
	t.Helper()
	d := kubeDeps(t, c)
	d.Cluster = func(string, string) (ClusterAPI, error) { return api, nil }
	return d
}

func insightScan(t *testing.T, api ClusterAPI, args ...string) (int, string, string) {
	t.Helper()
	return run(t, insightDeps(t, cleanCluster(), api), append([]string{"scan", "--cluster", "demo-cluster", "--insights"}, args...)...)
}

func TestScanInsightsStaleExitsZeroUnlessStrict(t *testing.T) {
	api := &deprecationAWS{status: types.InsightStatusValueError, clients: []types.ClientStat{callAt("kubectl/1.28", 3, 10*24*time.Hour)}}
	code, out, se := insightScan(t, api)
	if code != 0 || se != "" || !strings.Contains(out, "STALE, clears 2026-11-08") {
		t.Fatalf("exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
	if code, _, _ := insightScan(t, api, "--strict"); code != 1 {
		t.Fatalf("--strict exit = %d, want 1 (STALE counts)", code)
	}
}

func TestScanInsightsActiveClientIsBlocker(t *testing.T) {
	api := &deprecationAWS{status: types.InsightStatusValueError, clients: []types.ClientStat{callAt("demo-controller/1.2", 42, 24*time.Hour)}}
	code, out, se := insightScan(t, api)
	if code != 1 || se != "" || !strings.Contains(out, "demo-controller/1.2") || !strings.Contains(out, "BLOCKER") {
		t.Fatalf("exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
}

func TestScanInsightsUnknownCountsOnlyWithStrict(t *testing.T) {
	api := &deprecationAWS{status: types.InsightStatusValueWarning} // no client stats: no date to judge by
	code, out, _ := insightScan(t, api)
	if code != 0 || !strings.Contains(out, "result not clean: 1 unknown") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
	if code, _, _ := insightScan(t, api, "--strict"); code != 1 {
		t.Fatalf("--strict exit = %d, want 1 (UNKNOWN counts)", code)
	}
}

func TestScanInsightsPassingIsClean(t *testing.T) {
	code, out, _ := insightScan(t, &deprecationAWS{status: types.InsightStatusValuePassing}, "--strict")
	if code != 0 || strings.Contains(out, "result not clean") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
}

func TestScanInsightsLiveObjectBlocksRegardlessOfInsight(t *testing.T) {
	c := newCluster("1", "31")
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"demo-cluster": "1.31"}}})
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) {
		return c.disc, c.dyn, nil
	}
	d.Cluster = func(string, string) (ClusterAPI, error) {
		return &deprecationAWS{status: types.InsightStatusValuePassing}, nil
	}
	code, out, _ := run(t, d, "scan", "--cluster", "demo-cluster", "--insights")
	if code != 1 || !strings.Contains(out, "object FlowSchema web") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
}

func TestScanInsightsDegradesOnNonCredentialFailure(t *testing.T) {
	op := func(code string) error {
		return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
			Err: &smithy.GenericAPIError{Code: code, Message: "nope\nsecond line"}}
	}
	cases := map[string]*deprecationAWS{
		"denied":          {listErr: op("AccessDeniedException")},
		"throttled":       {listErr: op("ThrottlingException")},
		"server error":    {listErr: op("InternalServerException")},
		"describe denied": {status: types.InsightStatusValueError, describeErr: op("AccessDeniedException")},
	}
	for name, api := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, se := insightScan(t, api)
			if code != 0 || !strings.Contains(out, "insights unavailable") || !strings.Contains(out, "result not clean: 1 unknown") {
				t.Fatalf("exit=%d stdout:\n%s", code, out)
			}
			assertOneLine(t, se)
			if !strings.Contains(se, "insights") {
				t.Errorf("stderr should name the skipped check: %q", se)
			}
			if code, _, _ := insightScan(t, api, "--strict"); code != 1 {
				t.Errorf("--strict exit = %d, want 1", code)
			}
			_, js, _ := insightScan(t, api, "--output", "json")
			var doc struct {
				Insights      []struct{ Verdict, Reason string }
				ChecksSkipped []struct{ Check, Reason string }
			}
			if err := json.Unmarshal([]byte(js), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Insights) != 1 || doc.Insights[0].Verdict != "UNKNOWN" || len(doc.ChecksSkipped) != 1 || doc.ChecksSkipped[0].Check != "insights" {
				t.Fatalf("json doc = %+v", doc)
			}
		})
	}
}

func TestScanInsightsCredentialFailuresExitTwo(t *testing.T) {
	for _, code := range []string{"ExpiredToken", "ExpiredTokenException", "RequestExpired", "InvalidSignatureException",
		"SignatureDoesNotMatch", "AuthFailure", "UnrecognizedClientException", "InvalidClientTokenId"} {
		t.Run(code, func(t *testing.T) {
			api := &deprecationAWS{listErr: &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
				Err: &smithy.GenericAPIError{Code: code, Message: "bad"}}}
			exit, out, se := insightScan(t, api)
			if exit != 2 || out != "" {
				t.Fatalf("exit=%d stdout=%q", exit, out)
			}
			assertOneLine(t, se)
		})
	}
}

func TestScanInsightsClientBuildFailureExitsTwo(t *testing.T) {
	d := insightDeps(t, cleanCluster(), nil)
	d.Cluster = func(string, string) (ClusterAPI, error) { return nil, errors.New("bad profile") }
	code, _, se := run(t, d, "scan", "--cluster", "demo-cluster", "--insights")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	assertOneLine(t, se)
}

func TestScanInsightsFlagValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a.yaml", []byte("kind: X\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"without --cluster": {"scan", "--insights"},
		"with --path":       {"scan", "--insights", "--path", dir, "--target", "1.32"},
		"path and cluster":  {"scan", "--insights", "--path", dir, "--cluster", "demo-cluster"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, se := run(t, insightDeps(t, cleanCluster(), &deprecationAWS{}), args...)
			if code != 2 || out != "" || !strings.Contains(se, "--insights") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, se)
			}
			assertOneLine(t, se)
		})
	}
}

func TestScanStrictAloneCountsNothingExtra(t *testing.T) {
	code, _, se := run(t, kubeDeps(t, newCluster("1", "30")), "scan", "--strict")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q, want 0 (warnings only, no extra checks)", code, se)
	}
}

func TestScanWithoutInsightsNeverCallsEKSInsights(t *testing.T) {
	d := kubeDeps(t, cleanCluster())
	d.Cluster = func(string, string) (ClusterAPI, error) {
		t.Fatal("Cluster must not be built without --insights")
		return nil, nil
	}
	if code, _, se := run(t, d, "scan", "--cluster", "demo-cluster"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}

func TestScanInsightsDetaillessDeprecatedAPIInsightIsUnknown(t *testing.T) {
	api := &deprecationAWS{status: types.InsightStatusValueError, name: "Deprecated APIs removed in Kubernetes v1.32", noSummary: true}
	code, out, _ := insightScan(t, api)
	if code != 0 || !strings.Contains(out, "result not clean: 1 unknown") || strings.Contains(out, "no deprecated API usage reported") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
	if code, _, _ := insightScan(t, api, "--strict"); code != 1 {
		t.Fatalf("--strict exit = %d, want 1", code)
	}
}

// realClientErr is what a real EKS client returns when the (wrapped) credential provider fails.
func realClientErr(t *testing.T) error {
	t.Helper()
	failing := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("the SSO session associated with this profile has expired or is otherwise invalid")
	})
	c := eks.New(eks.Options{Region: "us-east-1", Credentials: eksinfo.WrapCredentials(failing),
		HTTPClient: deadHTTP{}, RetryMaxAttempts: 1})
	_, err := c.ListInsights(context.Background(), &eks.ListInsightsInput{ClusterName: aws.String("demo-cluster")})
	if err == nil {
		t.Fatal("ListInsights succeeded offline")
	}
	return err
}

type deadHTTP struct{}

func (deadHTTP) Do(*http.Request) (*http.Response, error) { return nil, errors.New("network down") }

func TestScanInsightsCredentialProviderFailureExitsTwo(t *testing.T) {
	api := &deprecationAWS{listErr: realClientErr(t)}
	exit, out, se := insightScan(t, api)
	if exit != 2 || out != "" || !strings.Contains(se, "SSO session") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out, se)
	}
	assertOneLine(t, se)
}

func TestScanInsightsOtherVersionInsightDoesNotSatisfyMissingGuard(t *testing.T) {
	api := &deprecationAWS{status: types.InsightStatusValuePassing, target: "1.32"} // scan target is 1.31
	code, out, _ := insightScan(t, api)
	if code != 0 || !strings.Contains(out, "no deprecated-API insight returned for target 1.31") ||
		!strings.Contains(out, "result not clean") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
}

func TestScanInsightsNoDeprecatedAPIInsightIsUnknown(t *testing.T) {
	cases := map[string]*deprecationAWS{
		"zero insights":            {none: true},
		"only other insights":      {status: types.InsightStatusValueError, name: "Kubelet version skew", noSummary: true},
		"other insight is passing": {status: types.InsightStatusValuePassing, name: "Kubelet version skew"},
	}
	for name, api := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, _ := insightScan(t, api)
			if code != 0 || !strings.Contains(out, "result not clean: 1 unknown") ||
				!strings.Contains(out, "no deprecated-API insight returned for target 1.31") ||
				strings.Contains(out, "no deprecated API usage reported") {
				t.Fatalf("exit=%d stdout:\n%s", code, out)
			}
			if code, _, _ := insightScan(t, api, "--strict"); code != 1 {
				t.Fatalf("--strict exit = %d, want 1", code)
			}
		})
	}
}

func TestScanInsightsDeprecatedNameMatchIsCaseInsensitive(t *testing.T) {
	api := &deprecationAWS{status: types.InsightStatusValuePassing, name: "deprecated apis removed in kubernetes v1.32", noSummary: true}
	code, out, _ := insightScan(t, api, "--strict")
	if code != 0 || strings.Contains(out, "result not clean") || !strings.Contains(out, "no deprecated API usage reported") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
}
