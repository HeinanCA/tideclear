package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

var fixedNow = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

func dt(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

// fakeEKS serves a fixed set of clusters in one region.
type fakeEKS struct {
	clusters map[string]string
	err      error
}

func (f fakeEKS) ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &eks.ListClustersOutput{}
	for n := range f.clusters {
		out.Clusters = append(out.Clusters, n)
	}
	return out, nil
}

func (f fakeEKS) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.clusters[*in.Name]
	if !ok {
		return nil, &types.ResourceNotFoundException{Message: aws.String("nope")}
	}
	return &eks.DescribeClusterOutput{Cluster: &types.Cluster{Name: in.Name, Version: aws.String(v)}}, nil
}

func (f fakeEKS) DescribeClusterVersions(context.Context, *eks.DescribeClusterVersionsInput, ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &eks.DescribeClusterVersionsOutput{ClusterVersions: []types.ClusterVersionInformation{
		{ClusterVersion: aws.String("1.35"), VersionStatus: types.VersionStatusStandardSupport,
			EndOfStandardSupportDate: dt(2027, 3, 27), EndOfExtendedSupportDate: dt(2028, 3, 27)},
		{ClusterVersion: aws.String("1.31"), VersionStatus: types.VersionStatusExtendedSupport,
			EndOfStandardSupportDate: dt(2025, 11, 26), EndOfExtendedSupportDate: dt(2026, 11, 26)},
		{ClusterVersion: aws.String("1.30"), // past standard end, empty status
			EndOfStandardSupportDate: dt(2026, 6, 1), EndOfExtendedSupportDate: dt(2027, 6, 1)},
	}}, nil
}

func authFail() error {
	return &smithy.GenericAPIError{Code: "UnrecognizedClientException", Message: "bad token"}
}

// depsFor serves per-region fakes; EKS errors for unknown regions.
func depsFor(m map[string]fakeEKS) Deps {
	return Deps{
		Now:           func() time.Time { return fixedNow },
		DefaultRegion: func(string) (string, error) { return "r1", nil },
		EKS: func(_, region string) (eksinfo.EKSAPI, error) {
			f, ok := m[region]
			if !ok {
				return nil, errors.New("no fake region " + region)
			}
			return f, nil
		},
	}
}

func run(t *testing.T, deps Deps, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runDeps(deps, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestClustersExtendedSupportExitsZeroAndFlags(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"ext": "1.31", "past": "1.30", "std": "1.35"}}})
	code, out, errOut := run(t, d, "clusters")
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	for _, name := range []string{"ext", "past"} {
		if !lineWith(out, name, "EXTENDED") {
			t.Errorf("row %s not flagged EXTENDED:\n%s", name, out)
		}
	}
	if lineWith(out, "std", "EXTENDED") {
		t.Errorf("std row wrongly flagged:\n%s", out)
	}
	code, out, _ = run(t, d, "clusters", "--output", "json")
	if code != 0 || strings.Count(out, `"onExtended": true`) != 2 {
		t.Fatalf("json exit=%d out=%s", code, out)
	}
}

func lineWith(out, a, b string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, a) && strings.Contains(l, b) {
			return true
		}
	}
	return false
}

func TestClustersZeroClusters(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {}})
	code, out, _ := run(t, d, "clusters")
	if code != 0 || out != "no clusters found\n" {
		t.Fatalf("exit=%d out=%q", code, out)
	}
}

func TestClustersClusterFlag(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"a": "1.35", "b": "1.31"}}})
	code, out, _ := run(t, d, "clusters", "--cluster", "b")
	if code != 0 || strings.Contains(out, "1.35") || !strings.Contains(out, "b ") {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	code, _, errOut := run(t, d, "clusters", "--cluster", "missing")
	if code != 2 {
		t.Fatalf("not found exit=%d", code)
	}
	assertOneLine(t, errOut)
}

func TestClustersAPIErrorExitsTwoOneLine(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {err: errors.New("boom\nsecond line")}})
	code, _, errOut := run(t, d, "clusters")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	assertOneLine(t, errOut)
}

func TestClustersSingleRegionAuthErrorExitsTwo(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {err: authFail()}})
	code, _, errOut := run(t, d, "clusters", "--region", "r1")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	assertOneLine(t, errOut)
}

func TestClustersRegionFlagPassedToDeps(t *testing.T) {
	var gotProfile, gotRegion string
	d := depsFor(map[string]fakeEKS{"eu-x": {}})
	inner := d.EKS
	d.EKS = func(p, r string) (eksinfo.EKSAPI, error) { gotProfile, gotRegion = p, r; return inner(p, r) }
	if code, _, e := run(t, d, "clusters", "--profile", "prof", "--region", "eu-x"); code != 0 {
		t.Fatalf("exit=%d %s", code, e)
	}
	if gotProfile != "prof" || gotRegion != "eu-x" {
		t.Fatalf("profile=%q region=%q", gotProfile, gotRegion)
	}
}

// allRegionsDeps: only us-east-1 lists; every other region fails auth.
func allRegionsDeps(good map[string]fakeEKS) Deps {
	d := depsFor(nil)
	d.EKS = func(_, region string) (eksinfo.EKSAPI, error) {
		if f, ok := good[region]; ok {
			return f, nil
		}
		return fakeEKS{err: authFail()}, nil
	}
	return d
}

func TestClustersAllRegionsOneAuthFailed(t *testing.T) {
	d := allRegionsDeps(map[string]fakeEKS{"us-east-1": {clusters: map[string]string{"demo-a": "1.35"}}})
	code, out, errOut := run(t, d, "clusters", "--all-regions")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "demo-a") {
		t.Errorf("missing listed cluster: %s", out)
	}
	lines := strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")
	if len(lines) < 1 || !strings.Contains(lines[0], "skipped region") {
		t.Fatalf("stderr = %q", errOut)
	}
	for _, l := range lines {
		assertOneLine(t, l+"\n")
	}
	_, jout, _ := run(t, d, "clusters", "--all-regions", "--output", "json")
	if !strings.Contains(jout, `"skippedRegions": [`) || strings.Contains(jout, `"skippedRegions": []`) {
		t.Errorf("json lacks skipped regions: %s", jout)
	}
}

func TestClustersAllRegionsAllFailExitsTwo(t *testing.T) {
	d := allRegionsDeps(nil)
	code, _, errOut := run(t, d, "clusters", "--all-regions")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	assertOneLine(t, errOut)
}

func TestClustersBadOutputFlag(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {}})
	code, _, errOut := run(t, d, "clusters", "--output", "yaml")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	assertOneLine(t, errOut)
}

func TestClustersOutputFormats(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {}})
	for _, o := range []string{"text", "table", "json"} {
		if code, _, errOut := run(t, d, "clusters", "--output", o); code != 0 {
			t.Errorf("--output %s: exit=%d stderr=%q", o, code, errOut)
		}
	}
	code, _, errOut := run(t, d, "clusters")
	if code != 0 {
		t.Errorf("default output: exit=%d stderr=%q", code, errOut)
	}
	code, _, errOut = run(t, d, "clusters", "--output", "x")
	if code != 2 || !strings.Contains(errOut, `unknown --output "x" (want text or json)`) {
		t.Errorf("bad output: exit=%d stderr=%q", code, errOut)
	}
}

func TestClustersAllRegionsConfigHomeRegionFirst(t *testing.T) {
	var first string
	d := depsFor(nil)
	d.EKS = func(_, r string) (eksinfo.EKSAPI, error) {
		if first == "" {
			first = r
		}
		return fakeEKS{}, nil
	}
	if code, _, _ := run(t, d, "clusters", "--all-regions"); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if first != "us-east-1" {
		t.Errorf("first client region = %q, want us-east-1", first)
	}
}

func TestClustersNoRegionResolved(t *testing.T) {
	d := depsFor(nil)
	d.DefaultRegion = func(string) (string, error) { return "", nil }
	code, _, errOut := run(t, d, "clusters")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	assertOneLine(t, errOut)
}

func TestClustersNotFoundPrintsSkipWarningsFirst(t *testing.T) {
	d := allRegionsDeps(map[string]fakeEKS{"us-east-1": {clusters: map[string]string{"other": "1.35"}}})
	code, _, errOut := run(t, d, "clusters", "--all-regions", "--cluster", "zzz")
	if code != 2 {
		t.Fatalf("exit=%d", code)
	}
	lines := strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "skipped region af-south-1") {
		t.Fatalf("stderr should start with skip warnings: %q", errOut)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "cluster not found: zzz") {
		t.Fatalf("last line = %q", last)
	}
}

func TestClustersSingleRegionTimeoutExitsTwoNamingRegion(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {err: context.DeadlineExceeded}})
	code, _, errOut := run(t, d, "clusters", "--region", "r1")
	if code != 2 || !strings.Contains(errOut, "r1") {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	assertOneLine(t, errOut)
}

func TestClustersConfigErrorNotRegionPrefixed(t *testing.T) {
	d := depsFor(nil)
	d.EKS = func(string, string) (eksinfo.EKSAPI, error) { return nil, errors.New("load config: boom") }
	code, _, errOut := run(t, d, "clusters", "--region", "r1")
	if code != 2 || !strings.Contains(errOut, "load config: boom") || strings.Contains(errOut, "region") {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
}

func TestClustersJSONSupportStates(t *testing.T) {
	d := depsFor(map[string]fakeEKS{"r1": {clusters: map[string]string{"ext": "1.31", "std": "1.35", "mystery": "9.99"}}})
	code, out, _ := run(t, d, "clusters", "--output", "json")
	if code != 0 || !strings.Contains(out, `"support": "unknown"`) || !strings.Contains(out, `"daysLeft": null`) ||
		!strings.Contains(out, `"support": "extended"`) || !strings.Contains(out, `"support": "standard"`) {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}
