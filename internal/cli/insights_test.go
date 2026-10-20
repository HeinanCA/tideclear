package cli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

// fakeAWS serves insight summaries in pages keyed by nextToken ("" first).
type fakeAWS struct {
	addonFake
	nodegroupFake
	pages   map[string]*eks.ListInsightsOutput
	listIn  []*eks.ListInsightsInput
	listErr error
}

func (f *fakeAWS) ListInsights(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
	f.listIn = append(f.listIn, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.pages[aws.ToString(in.NextToken)], nil
}

func (f *fakeAWS) DescribeInsight(context.Context, *eks.DescribeInsightInput, ...func(*eks.Options)) (*eks.DescribeInsightOutput, error) {
	return &eks.DescribeInsightOutput{}, nil
}

func insightSummary(id, name string, st types.InsightStatusValue, at time.Time) types.InsightSummary {
	return types.InsightSummary{
		Id: aws.String(id), Name: aws.String(name), KubernetesVersion: aws.String("1.32"),
		InsightStatus:   &types.InsightStatus{Status: st, Reason: aws.String("r")},
		LastRefreshTime: &at,
	}
}

func twoPageAWS(at time.Time) *fakeAWS {
	return &fakeAWS{pages: map[string]*eks.ListInsightsOutput{
		"":   {Insights: []types.InsightSummary{insightSummary("i1", "Deprecated API usage", types.InsightStatusValueError, at)}, NextToken: aws.String("t2")},
		"t2": {Insights: []types.InsightSummary{insightSummary("i2", "Add-on compatibility", types.InsightStatusValuePassing, at)}},
	}}
}

// clusterDeps wires f in as the cluster client and records the profile and region asked for.
func clusterDeps(f *fakeAWS, asked *[2]string) Deps {
	d := depsFor(map[string]fakeEKS{"r1": {}})
	d.Cluster = func(profile, region string) (ClusterAPI, error) {
		if asked != nil {
			*asked = [2]string{profile, region}
		}
		return f, nil
	}
	return d
}

func TestInsightsTextShowsFreshness(t *testing.T) {
	f := twoPageAWS(fixedNow.Add(-14 * time.Hour))
	var asked [2]string
	code, out, se := run(t, clusterDeps(f, &asked), "insights", "--cluster", "demo", "--profile", "p", "--region", "eu-x")
	if code != 0 || se != "" {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	for _, want := range []string{"NAME", "Deprecated API usage", "Add-on compatibility", "ERROR", "PASSING", "14h ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Refresh insights") {
		t.Errorf("fresh data must not carry the refresh hint:\n%s", out)
	}
	if asked != [2]string{"p", "eu-x"} {
		t.Errorf("Cluster asked for %v, want [p eu-x]", asked)
	}
	if len(f.listIn) != 2 {
		t.Errorf("ListInsights calls = %d, want 2 (pagination)", len(f.listIn))
	}
}

func TestInsightsOldDataHintsAtConsoleRefresh(t *testing.T) {
	_, out, _ := run(t, clusterDeps(twoPageAWS(fixedNow.Add(-30*time.Hour)), nil), "insights", "--cluster", "demo")
	if !strings.Contains(out, "30h ago") || !strings.Contains(out, "Refresh insights") {
		t.Fatalf("stdout:\n%s", out)
	}
}

func TestInsightsJSON(t *testing.T) {
	code, out, se := run(t, clusterDeps(twoPageAWS(fixedNow.Add(-time.Hour)), nil), "insights", "--cluster", "demo", "--output", "json")
	if code != 0 || se != "" {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	var doc struct {
		SchemaVersion int `json:"schemaVersion"`
		Insights      []struct{ ID, Status string }
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if doc.SchemaVersion != 1 || len(doc.Insights) != 2 || doc.Insights[1].Status != "PASSING" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestInsightsTargetFiltersVersion(t *testing.T) {
	f := twoPageAWS(fixedNow)
	run(t, clusterDeps(f, nil), "insights", "--cluster", "demo", "--target", "1.32")
	flt := f.listIn[0].Filter
	if !reflect.DeepEqual(flt.KubernetesVersions, []string{"1.32"}) || !reflect.DeepEqual(flt.Categories, []types.Category{types.CategoryUpgradeReadiness}) {
		t.Fatalf("filter = %+v", flt)
	}
	f = twoPageAWS(fixedNow)
	run(t, clusterDeps(f, nil), "insights", "--cluster", "demo")
	flt = f.listIn[0].Filter
	if len(flt.KubernetesVersions) != 0 || !reflect.DeepEqual(flt.Categories, []types.Category{types.CategoryUpgradeReadiness}) {
		t.Fatalf("filter without --target = %+v", flt)
	}
}

func TestInsightsUsesDefaultRegion(t *testing.T) {
	var asked [2]string
	run(t, clusterDeps(twoPageAWS(fixedNow), &asked), "insights", "--cluster", "demo")
	if asked != [2]string{"", "r1"} {
		t.Fatalf("asked %v, want [ r1]", asked)
	}
}

func TestInsightsErrorsExitTwoWithOneLine(t *testing.T) {
	denied := &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListInsights",
		Err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized"}}
	cases := []struct {
		name string
		deps func() Deps
		args []string
	}{
		{"denied", func() Deps { return clusterDeps(&fakeAWS{listErr: denied}, nil) }, []string{"--cluster", "demo"}},
		{"throttled", func() Deps {
			return clusterDeps(&fakeAWS{listErr: &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow\ndown"}}, nil)
		}, []string{"--cluster", "demo"}},
		{"client build fails", func() Deps {
			d := clusterDeps(nil, nil)
			d.Cluster = func(string, string) (ClusterAPI, error) { return nil, errors.New("bad profile") }
			return d
		}, []string{"--cluster", "demo"}},
		{"cluster missing", func() Deps { return clusterDeps(&fakeAWS{}, nil) }, nil},
		{"bad target", func() Deps { return clusterDeps(&fakeAWS{}, nil) }, []string{"--cluster", "demo", "--target", "x"}},
		{"bad output", func() Deps { return clusterDeps(&fakeAWS{}, nil) }, []string{"--cluster", "demo", "--output", "yaml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, se := run(t, c.deps(), append([]string{"insights"}, c.args...)...)
			if code != 2 || out != "" {
				t.Fatalf("exit=%d stdout=%q", code, out)
			}
			assertOneLine(t, se)
		})
	}
}

func TestInsightsEmptyResult(t *testing.T) {
	f := &fakeAWS{pages: map[string]*eks.ListInsightsOutput{"": {}}}
	code, out, _ := run(t, clusterDeps(f, nil), "insights", "--cluster", "demo")
	if code != 0 || out != "no insights found\n" {
		t.Fatalf("exit=%d out=%q", code, out)
	}
}

// compile-time: the cluster client satisfies insights.API and eksinfo is unaffected.
var (
	_ insights.API   = ClusterAPI(nil)
	_ eksinfo.EKSAPI = (*eks.Client)(nil)
)
