package eksinfo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

var now = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

func day(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

// fakeAPI is an in-memory EKS region with 2-item pages.
type fakeAPI struct {
	clusters map[string]string // name -> version
	order    []string
	versions []types.ClusterVersionInformation
	err      error // returned by every call
	block    bool  // calls wait for ctx to end, then return its error
	nilBody  map[string]bool
	calls    map[string]int
}

func (f *fakeAPI) wait(ctx context.Context) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeAPI) count(op string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[op]++
}

func (f *fakeAPI) ListClusters(ctx context.Context, in *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	f.count("ListClusters")
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+2, len(f.order))
	out := &eks.ListClustersOutput{Clusters: f.order[start:end]}
	if end < len(f.order) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

func (f *fakeAPI) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	f.count("DescribeCluster")
	if f.err != nil {
		return nil, f.err
	}
	if f.nilBody[*in.Name] {
		return &eks.DescribeClusterOutput{}, nil
	}
	v, ok := f.clusters[*in.Name]
	if !ok {
		return nil, &types.ResourceNotFoundException{Message: aws.String("no such cluster")}
	}
	return &eks.DescribeClusterOutput{Cluster: &types.Cluster{Name: in.Name, Version: aws.String(v)}}, nil
}

func (f *fakeAPI) DescribeClusterVersions(_ context.Context, in *eks.DescribeClusterVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	f.count("DescribeClusterVersions")
	if f.err != nil {
		return nil, f.err
	}
	// The real API rejects more than one of these filters in a single request.
	filters := 0
	for _, set := range []bool{in.DefaultOnly != nil, len(in.ClusterVersions) > 0, in.IncludeAll != nil, in.VersionStatus != ""} {
		if set {
			filters++
		}
	}
	if filters > 1 {
		return nil, &types.InvalidParameterException{Message: aws.String("Only one of the defaultOnly, clusterVersions, includeAll or status request parameters is accepted at a time.")}
	}
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+1, len(f.versions))
	out := &eks.DescribeClusterVersionsOutput{ClusterVersions: f.versions[start:end]}
	if end < len(f.versions) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

func versionTable() []types.ClusterVersionInformation {
	return []types.ClusterVersionInformation{
		{ClusterVersion: aws.String("1.35"), VersionStatus: types.VersionStatusStandardSupport,
			EndOfStandardSupportDate: day(2027, 3, 27), EndOfExtendedSupportDate: day(2028, 3, 27)},
		{ClusterVersion: aws.String("1.31"), VersionStatus: types.VersionStatusExtendedSupport,
			EndOfStandardSupportDate: day(2025, 11, 26), EndOfExtendedSupportDate: day(2026, 11, 26)},
		{ClusterVersion: aws.String("1.30"), // empty status: fallback on dates
			EndOfStandardSupportDate: day(2026, 6, 1), EndOfExtendedSupportDate: day(2027, 6, 1)},
		{ClusterVersion: aws.String("1.29"), VersionStatus: types.VersionStatusStandardSupport,
			EndOfStandardSupportDate: day(2026, 12, 1), EndOfExtendedSupportDate: day(2027, 12, 1)},
		{ClusterVersion: aws.String("1.25"), VersionStatus: types.VersionStatusUnsupported,
			EndOfStandardSupportDate: day(2024, 5, 1), EndOfExtendedSupportDate: day(2025, 5, 1)},
		{ClusterVersion: aws.String("1.24"), // empty status, past extended end
			EndOfStandardSupportDate: day(2024, 1, 1), EndOfExtendedSupportDate: day(2025, 1, 1)},
	}
}

func region(clusters map[string]string, order ...string) *fakeAPI {
	return &fakeAPI{clusters: clusters, order: order, versions: versionTable()}
}

func lister(m map[string]*fakeAPI) Lister {
	return Lister{
		Now: func() time.Time { return now },
		Clients: func(r string) (EKSAPI, error) {
			f, ok := m[r]
			if !ok {
				return nil, errors.New("no fake for " + r)
			}
			return f, nil
		},
	}
}

func authErr(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: "bad creds"}
}

func TestListSupportPhases(t *testing.T) {
	f := region(map[string]string{"a": "1.35", "b": "1.31", "c": "1.30", "d": "1.35", "e": "1.29",
		"f": "1.25", "g": "1.24", "h": "9.99"},
		"a", "b", "c", "d", "e", "f", "g", "h") // 4 pages of clusters
	cs, sk, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "")
	if err != nil || len(sk) != 0 {
		t.Fatalf("err=%v skipped=%v", err, sk)
	}
	want := []struct {
		name    string
		support Support
		days    *int // nil = unknown
		stdY    int
		stdM    int
	}{
		{"a", SupportStandard, ip(159), 2027, 3},
		{"b", SupportExtended, ip(38), 2025, 11},
		{"c", SupportExtended, ip(225), 2026, 6},
		{"d", SupportStandard, ip(159), 2027, 3},
		{"e", SupportStandard, ip(43), 2026, 12},
		{"f", SupportUnsupported, ip(-536), 2024, 5},
		{"g", SupportUnsupported, ip(-656), 2024, 1},
		{"h", SupportUnknown, nil, 0, 0},
	}
	if len(cs) != len(want) {
		t.Fatalf("got %d clusters: %+v", len(cs), cs)
	}
	for i, w := range want {
		c := cs[i]
		if c.Name != w.name || c.Region != "r1" || c.Support != w.support || !sameInt(c.DaysLeft, w.days) ||
			c.StandardEnd.Year() != w.stdY && w.stdY != 0 || int(c.StandardEnd.Month()) != w.stdM && w.stdM != 0 {
			t.Errorf("cluster %d = %+v, want %+v", i, c, w)
		}
	}
	// One paginated query per region: the fake serves 6 versions in 6 pages.
	if f.calls["DescribeClusterVersions"] != 6 {
		t.Errorf("DescribeClusterVersions calls = %d, want 6 pages", f.calls["DescribeClusterVersions"])
	}
}

func ip(n int) *int { return &n }

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestListEmptyVersionIsUnknown(t *testing.T) {
	f := region(map[string]string{"a": ""}, "a")
	cs, _, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "")
	if err != nil || len(cs) != 1 || cs[0].Support != SupportUnknown || cs[0].DaysLeft != nil {
		t.Fatalf("cs=%+v err=%v", cs, err)
	}
}

func TestListNameFilterUsesDescribeOnly(t *testing.T) {
	f := region(map[string]string{"a": "1.35", "b": "1.31"}, "a", "b")
	cs, _, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "b")
	if err != nil || len(cs) != 1 || cs[0].Name != "b" {
		t.Fatalf("cs=%+v err=%v", cs, err)
	}
	if f.calls["ListClusters"] != 0 || f.calls["DescribeCluster"] != 1 {
		t.Errorf("calls = %v, want DescribeCluster only", f.calls)
	}
}

func TestListNameNotFound(t *testing.T) {
	f := region(map[string]string{"a": "1.35"}, "a")
	_, _, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "zzz")
	if !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("err = %v, want ErrClusterNotFound", err)
	}
}

func TestListAPIErrorIsPlain(t *testing.T) {
	f := region(nil)
	f.err = errors.New("boom")
	_, _, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "")
	if err == nil || err.Error() == "" || errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListMultiRegionSkipsAuthErrorRegion(t *testing.T) {
	bad := region(nil)
	bad.err = authErr("UnrecognizedClientException")
	good := region(map[string]string{"x": "1.35"}, "x")
	cs, sk, err := lister(map[string]*fakeAPI{"r-bad": bad, "r-ok": good}).
		List(context.Background(), []string{"r-bad", "r-ok"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Region != "r-ok" {
		t.Fatalf("cs = %+v", cs)
	}
	if len(sk) != 1 || sk[0].Region != "r-bad" || sk[0].Err == nil {
		t.Fatalf("skipped = %+v", sk)
	}
}

func TestListAuthCodesSkippable(t *testing.T) {
	for _, code := range []string{"UnrecognizedClientException", "InvalidClientTokenId", "AccessDeniedException", "AccessDenied", "AuthFailure", "OptInRequired"} {
		bad := region(nil)
		bad.err = authErr(code)
		good := region(map[string]string{"x": "1.35"}, "x")
		_, sk, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
		if err != nil || len(sk) != 1 {
			t.Errorf("code %s: err=%v skipped=%v", code, err, sk)
		}
	}
}

func TestListMultiRegionNonAuthErrorFails(t *testing.T) {
	bad := region(nil)
	bad.err = errors.New("throttled")
	good := region(map[string]string{"x": "1.35"}, "x")
	_, _, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
	if err == nil {
		t.Fatal("want error for non-auth failure")
	}
}

func TestListAllRegionsFailSummarizes(t *testing.T) {
	m := map[string]*fakeAPI{}
	var rs []string
	for _, n := range []string{"ra", "rb", "rc", "rd"} {
		f := region(nil)
		f.err = authErr("UnrecognizedClientException")
		m[n] = f
		rs = append(rs, n)
	}
	_, _, err := lister(m).List(context.Background(), rs, "")
	if err == nil {
		t.Fatal("want error when every region fails")
	}
	msg := err.Error()
	if !strings.Contains(msg, "all 4 regions failed") || !strings.Contains(msg, "first: ra:") ||
		strings.Count(msg, "UnrecognizedClientException") != 1 {
		t.Fatalf("err = %q, want count + first error only", msg)
	}
}

func TestListSingleRegionAuthErrorIsPlainError(t *testing.T) {
	a := region(nil)
	a.err = authErr("UnrecognizedClientException")
	cs, sk, err := lister(map[string]*fakeAPI{"a": a}).List(context.Background(), []string{"a"}, "")
	if err == nil || len(sk) != 0 || len(cs) != 0 {
		t.Fatalf("cs=%v sk=%v err=%v", cs, sk, err)
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		t.Errorf("error should keep the API cause: %v", err)
	}
}

func TestListNameAcrossRegions(t *testing.T) {
	a := region(map[string]string{"p": "1.35"}, "p")
	b := region(map[string]string{"q": "1.31"}, "q")
	cs, _, err := lister(map[string]*fakeAPI{"a": a, "b": b}).List(context.Background(), []string{"a", "b"}, "q")
	if err != nil || len(cs) != 1 || cs[0].Region != "b" {
		t.Fatalf("cs=%+v err=%v", cs, err)
	}
}

func TestAllRegionsStaticList(t *testing.T) {
	r := AllRegions()
	if len(r) < 20 {
		t.Fatalf("only %d regions", len(r))
	}
	r[0] = "mutated"
	if AllRegions()[0] == "mutated" {
		t.Fatal("AllRegions must return a copy")
	}
}

// *eks.Client must satisfy EKSAPI unwrapped.
var _ EKSAPI = (*eks.Client)(nil)

func TestListNotFoundWithSkippedReturnsSkipped(t *testing.T) {
	bad := region(nil)
	bad.err = authErr("UnrecognizedClientException")
	other := region(map[string]string{"p": "1.35"}, "p")
	_, sk, err := lister(map[string]*fakeAPI{"A": bad, "B": other}).List(context.Background(), []string{"A", "B"}, "zzz")
	if !errors.Is(err, ErrClusterNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(sk) != 1 || sk[0].Region != "A" {
		t.Fatalf("skipped = %+v, want region A", sk)
	}
}

func TestListMultiRegionTimeoutSkipsRegion(t *testing.T) {
	slow := region(nil)
	slow.block = true
	good := region(map[string]string{"x": "1.35"}, "x")
	l := lister(map[string]*fakeAPI{"slow": slow, "ok": good})
	l.RegionTimeout = 20 * time.Millisecond
	cs, sk, err := l.List(context.Background(), []string{"slow", "ok"}, "")
	if err != nil || len(cs) != 1 || len(sk) != 1 || sk[0].Region != "slow" {
		t.Fatalf("cs=%+v sk=%+v err=%v", cs, sk, err)
	}
}

type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return false }

func TestListTransportTimeoutSkippable(t *testing.T) {
	var _ net.Error = timeoutNetErr{}
	bad := region(nil)
	bad.err = timeoutNetErr{}
	good := region(map[string]string{"x": "1.35"}, "x")
	cs, sk, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
	if err != nil || len(cs) != 1 || len(sk) != 1 {
		t.Fatalf("cs=%+v sk=%+v err=%v", cs, sk, err)
	}
}

func TestListAllRegionsTimeoutIsError(t *testing.T) {
	a, b := region(nil), region(nil)
	a.block, b.block = true, true
	l := lister(map[string]*fakeAPI{"a": a, "b": b})
	l.RegionTimeout = 10 * time.Millisecond
	if _, _, err := l.List(context.Background(), []string{"a", "b"}, ""); err == nil {
		t.Fatal("want error when every region times out")
	}
}

func TestListSingleRegionBlockedEndpointIsBoundedAndNamesRegion(t *testing.T) {
	a := region(nil)
	a.block = true
	l := lister(map[string]*fakeAPI{"only": a})
	l.RegionTimeout = 20 * time.Millisecond
	start := time.Now()
	_, sk, err := l.List(context.Background(), []string{"only"}, "")
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v, want prompt return", time.Since(start))
	}
	if err == nil || len(sk) != 0 || !strings.Contains(err.Error(), "only") {
		t.Fatalf("sk=%v err=%v", sk, err)
	}
}

func TestListSingleRegionTimeoutIsErrorNamingRegion(t *testing.T) {
	a := region(nil)
	a.err = context.DeadlineExceeded
	_, sk, err := lister(map[string]*fakeAPI{"only": a}).List(context.Background(), []string{"only"}, "")
	if err == nil || len(sk) != 0 || !strings.Contains(err.Error(), "only") {
		t.Fatalf("sk=%v err=%v", sk, err)
	}
}

func TestListDeletedMidRunClusterIsSkipped(t *testing.T) {
	// "gone" is listed but DescribeCluster says not found.
	f := region(map[string]string{"a": "1.35"}, "a", "gone")
	cs, _, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "")
	if err != nil || len(cs) != 1 || cs[0].Name != "a" {
		t.Fatalf("cs=%+v err=%v", cs, err)
	}
}

func TestListNilClusterBodyIsSkippedNoPanic(t *testing.T) {
	f := region(map[string]string{"a": "1.35", "b": "1.35"}, "a", "b")
	f.nilBody = map[string]bool{"b": true}
	cs, _, err := lister(map[string]*fakeAPI{"r1": f}).List(context.Background(), []string{"r1"}, "")
	if err != nil || len(cs) != 1 || cs[0].Name != "a" {
		t.Fatalf("cs=%+v err=%v", cs, err)
	}
}

func TestListNilNowDefaultsToWallClock(t *testing.T) {
	f := region(map[string]string{"a": "1.35"}, "a")
	l := lister(map[string]*fakeAPI{"r1": f})
	l.Now = nil
	cs, _, err := l.List(context.Background(), []string{"r1"}, "")
	if err != nil || len(cs) != 1 {
		t.Fatalf("cs=%+v err=%v", cs, err)
	}
}

// credErr mimics the SDK: an EKS call fails because STS AssumeRole (credential
// retrieval) was denied; the AccessDenied API error sits deeper in the chain.
func credErr() error {
	sts := &smithy.OperationError{ServiceID: "STS", OperationName: "AssumeRole",
		Err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to assume role"}}
	return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListClusters",
		Err: fmt.Errorf("get identity: get credentials: failed to refresh cached credentials, %w", sts)}
}

func TestListCredentialRetrievalErrorIsFatalNotSkipped(t *testing.T) {
	bad := region(nil)
	bad.err = credErr()
	good := region(map[string]string{"x": "1.35"}, "x")
	_, sk, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
	if err == nil || len(sk) != 0 {
		t.Fatalf("sk=%v err=%v, want fatal error", sk, err)
	}
}

func TestListEKSAccessDeniedStillSkippable(t *testing.T) {
	bad := region(nil)
	bad.err = &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListClusters",
		Err: &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}}
	good := region(map[string]string{"x": "1.35"}, "x")
	_, sk, err := lister(map[string]*fakeAPI{"b": bad, "g": good}).List(context.Background(), []string{"b", "g"}, "")
	if err != nil || len(sk) != 1 {
		t.Fatalf("sk=%v err=%v", sk, err)
	}
}

func TestListClientConstructionErrorNotRegionPrefixed(t *testing.T) {
	l := Lister{Now: func() time.Time { return now },
		Clients: func(string) (EKSAPI, error) { return nil, errors.New("load config: bad profile") }}
	_, _, err := l.List(context.Background(), []string{"r1"}, "")
	if err == nil || err.Error() != "load config: bad profile" {
		t.Fatalf("err = %v", err)
	}
}
