package insights

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// fakeAPI serves summaries in pages keyed by nextToken and details by id.
type fakeAPI struct {
	pages     map[string]*eks.ListInsightsOutput // "" is the first page
	details   map[string]*types.Insight
	listIn    []*eks.ListInsightsInput
	describes []string
	listErr   error
	descErr   error
}

func (f *fakeAPI) ListInsights(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
	f.listIn = append(f.listIn, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.pages[aws.ToString(in.NextToken)], nil
}

func (f *fakeAPI) DescribeInsight(_ context.Context, in *eks.DescribeInsightInput, _ ...func(*eks.Options)) (*eks.DescribeInsightOutput, error) {
	f.describes = append(f.describes, aws.ToString(in.Id))
	if f.descErr != nil {
		return nil, f.descErr
	}
	return &eks.DescribeInsightOutput{Insight: f.details[aws.ToString(in.Id)]}, nil
}

var refreshed = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

func summary(id, name, ver string, st types.InsightStatusValue) types.InsightSummary {
	return types.InsightSummary{
		Id: aws.String(id), Name: aws.String(name), KubernetesVersion: aws.String(ver),
		Category:        types.CategoryUpgradeReadiness,
		InsightStatus:   &types.InsightStatus{Status: st, Reason: aws.String("why " + id)},
		LastRefreshTime: &refreshed,
	}
}

func twoPages() *fakeAPI {
	return &fakeAPI{pages: map[string]*eks.ListInsightsOutput{
		"": {Insights: []types.InsightSummary{summary("i1", "Deprecated API usage", "1.32", types.InsightStatusValueError)}, NextToken: aws.String("t2")},
		"t2": {Insights: []types.InsightSummary{
			summary("i2", "Add-on compatibility", "1.32", types.InsightStatusValuePassing),
			summary("i3", "Kubelet version skew", "1.32", types.InsightStatusValueUnknown),
		}},
	}}
}

func TestListFollowsPagination(t *testing.T) {
	api := twoPages()
	got, err := List(context.Background(), api, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "i1" || got[2].ID != "i3" {
		t.Fatalf("got %+v", got)
	}
	if len(api.listIn) != 2 || aws.ToString(api.listIn[1].NextToken) != "t2" {
		t.Fatalf("calls = %d, want 2 with second token t2", len(api.listIn))
	}
	if len(api.describes) != 0 {
		t.Fatalf("List must not describe, got %v", api.describes)
	}
}

func TestListFilter(t *testing.T) {
	cases := []struct {
		name   string
		target *rules.Version
		want   []string
	}{
		{"no target", nil, nil},
		{"target", &rules.Version{Major: 1, Minor: 32}, []string{"1.32"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAPI{pages: map[string]*eks.ListInsightsOutput{"": {}}}
			if _, err := List(context.Background(), api, "demo", c.target); err != nil {
				t.Fatal(err)
			}
			in := api.listIn[0]
			if aws.ToString(in.ClusterName) != "demo" {
				t.Errorf("cluster = %q", aws.ToString(in.ClusterName))
			}
			f := in.Filter
			if f == nil || !reflect.DeepEqual(f.Categories, []types.Category{types.CategoryUpgradeReadiness}) {
				t.Fatalf("filter categories = %+v", f)
			}
			if !reflect.DeepEqual(f.KubernetesVersions, c.want) {
				t.Errorf("kubernetesVersions = %v, want %v", f.KubernetesVersions, c.want)
			}
			if len(f.Statuses) != 0 {
				t.Errorf("statuses must not be filtered: %v", f.Statuses)
			}
		})
	}
}

func TestListStatusMapping(t *testing.T) {
	cases := map[types.InsightStatusValue]Status{
		types.InsightStatusValuePassing: StatusPassing,
		types.InsightStatusValueWarning: StatusWarning,
		types.InsightStatusValueError:   StatusError,
		types.InsightStatusValueUnknown: StatusUnknown,
		"SOMETHING_NEW":                 StatusUnknown,
		"":                              StatusUnknown,
	}
	for in, want := range cases {
		api := &fakeAPI{pages: map[string]*eks.ListInsightsOutput{"": {Insights: []types.InsightSummary{summary("i", "n", "1.32", in)}}}}
		got, err := List(context.Background(), api, "demo", nil)
		if err != nil || len(got) != 1 {
			t.Fatalf("%q: %v %v", in, got, err)
		}
		if got[0].Status != want {
			t.Errorf("status %q -> %q, want %q", in, got[0].Status, want)
		}
	}
}

func TestListCopiesSummaryFields(t *testing.T) {
	api := twoPages()
	got, _ := List(context.Background(), api, "demo", nil)
	want := Insight{ID: "i1", Name: "Deprecated API usage", Target: "1.32", Status: StatusError, Reason: "why i1", LastRefresh: refreshed}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %+v\nwant %+v", got[0], want)
	}
}

func TestListNilPointerFields(t *testing.T) {
	api := &fakeAPI{pages: map[string]*eks.ListInsightsOutput{"": {Insights: []types.InsightSummary{{}}}}}
	got, err := List(context.Background(), api, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Insight{Status: StatusUnknown}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestListError(t *testing.T) {
	boom := errors.New("boom")
	_, err := List(context.Background(), &fakeAPI{listErr: boom}, "demo", nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// documentedDetail mirrors the DescribeInsight shape: deprecationDetails with
// clientStats, and addonCompatibilityDetails.
func documentedDetail() *types.Insight {
	last := time.Date(2026, 10, 18, 8, 30, 0, 0, time.UTC)
	return &types.Insight{
		Id: aws.String("i1"), Name: aws.String("Deprecated API usage"), KubernetesVersion: aws.String("1.32"),
		InsightStatus:   &types.InsightStatus{Status: types.InsightStatusValueError, Reason: aws.String("deprecated APIs in use")},
		LastRefreshTime: &refreshed,
		CategorySpecificSummary: &types.InsightCategorySpecificSummary{
			DeprecationDetails: []types.DeprecationDetail{{
				Usage:                          aws.String("/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas"),
				ReplacedWith:                   aws.String("/apis/flowcontrol.apiserver.k8s.io/v1/flowschemas"),
				StopServingVersion:             aws.String("1.32"),
				StartServingReplacementVersion: aws.String("1.29"),
				ClientStats: []types.ClientStat{
					{UserAgent: aws.String("kubectl/v1.31.0"), NumberOfRequestsLast30Days: 12, LastRequestTime: &last},
					{UserAgent: aws.String("legacy-controller"), NumberOfRequestsLast30Days: 3}, // nil time
				},
			}, {}}, // all-nil detail
			AddonCompatibilityDetails: []types.AddonCompatibilityDetail{
				{Name: aws.String("vpc-cni"), CompatibleVersions: []string{"v1.19.2-eksbuild.1", "v1.20.0-eksbuild.1"}},
				{},
			},
		},
	}
}

func TestLoadDescribesEachInsight(t *testing.T) {
	api := twoPages()
	api.details = map[string]*types.Insight{"i1": documentedDetail()}
	got, err := Load(context.Background(), api, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.describes, []string{"i1", "i2", "i3"}) {
		t.Fatalf("describes = %v", api.describes)
	}
	last := time.Date(2026, 10, 18, 8, 30, 0, 0, time.UTC)
	wantDep := []Deprecation{{
		Usage:                   "/apis/flowcontrol.apiserver.k8s.io/v1beta3/flowschemas",
		ReplacedWith:            "/apis/flowcontrol.apiserver.k8s.io/v1/flowschemas",
		StopServing:             "1.32",
		StartServingReplacement: "1.29",
		Clients: []Client{
			{UserAgent: "kubectl/v1.31.0", Requests: 12, LastRequest: &last},
			{UserAgent: "legacy-controller", Requests: 3, LastRequest: nil},
		},
	}, {}}
	if !reflect.DeepEqual(got[0].Deprecations, wantDep) {
		t.Errorf("deprecations\n got %+v\nwant %+v", got[0].Deprecations, wantDep)
	}
	wantCompat := []AddonCompat{{Name: "vpc-cni", Versions: []string{"v1.19.2-eksbuild.1", "v1.20.0-eksbuild.1"}}, {}}
	if !reflect.DeepEqual(got[0].AddonCompat, wantCompat) {
		t.Errorf("addon compat\n got %+v\nwant %+v", got[0].AddonCompat, wantCompat)
	}
	// i2 has no detail in the fake (nil Insight): summary fields survive.
	if got[1].ID != "i2" || got[1].Status != StatusPassing || got[1].Deprecations != nil {
		t.Errorf("summary fallback: %+v", got[1])
	}
}

func TestLoadMarksInsightsWithoutDetails(t *testing.T) {
	api := twoPages()
	api.details = map[string]*types.Insight{"i1": documentedDetail()}
	got, err := Load(context.Background(), api, "demo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].DetailsUnavailable || !got[1].DetailsUnavailable {
		t.Fatalf("DetailsUnavailable i1=%v i2=%v, want false and true", got[0].DetailsUnavailable, got[1].DetailsUnavailable)
	}
}

func TestLoadDescribeError(t *testing.T) {
	boom := errors.New("describe failed")
	api := twoPages()
	api.descErr = boom
	if _, err := Load(context.Background(), api, "demo", nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIHasNoWriteOperations(t *testing.T) {
	typ := reflect.TypeOf((*API)(nil)).Elem()
	var names []string
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	if !reflect.DeepEqual(names, []string{"DescribeInsight", "ListInsights"}) {
		t.Fatalf("API methods = %v, want only DescribeInsight and ListInsights", names)
	}
}
