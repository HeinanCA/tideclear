package addons

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

var target = rules.Version{Major: 1, Minor: 32}

// fakeAPI serves installed add-ons (name -> current version) and, per
// add-on, pages of DescribeAddonVersions keyed by nextToken ("" first).
type fakeAPI struct {
	listPages   map[string]*eks.ListAddonsOutput
	current     map[string]string
	versions    map[string]map[string]*eks.DescribeAddonVersionsOutput
	versionIn   []*eks.DescribeAddonVersionsInput
	listErr     error
	descErr     error
	descFail    map[string]error // per add-on DescribeAddon failure
	versionsErr error
}

func (f *fakeAPI) ListAddons(_ context.Context, in *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listPages[aws.ToString(in.NextToken)], nil
}

func (f *fakeAPI) DescribeAddon(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	if f.descErr != nil {
		return nil, f.descErr
	}
	name := aws.ToString(in.AddonName)
	if err := f.descFail[name]; err != nil {
		return nil, err
	}
	return &eks.DescribeAddonOutput{Addon: &types.Addon{AddonName: &name, AddonVersion: aws.String(f.current[name])}}, nil
}

func (f *fakeAPI) DescribeAddonVersions(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
	f.versionIn = append(f.versionIn, in)
	if f.versionsErr != nil {
		return nil, f.versionsErr
	}
	out := f.versions[aws.ToString(in.AddonName)][aws.ToString(in.NextToken)]
	if out == nil {
		return &eks.DescribeAddonVersionsOutput{}, nil
	}
	return out, nil
}

// info builds an AddonInfo whose listed versions each support the given cluster versions.
func info(name string, versions map[string][]string) types.AddonInfo {
	ai := types.AddonInfo{AddonName: aws.String(name)}
	for _, v := range []string{"v1.0.0-eksbuild.1", "v1.1.0-eksbuild.1", "v1.10.0-eksbuild.2", "v1.2.0-eksbuild.1"} {
		cl, ok := versions[v]
		if !ok {
			continue
		}
		vi := types.AddonVersionInfo{AddonVersion: aws.String(v)}
		for _, c := range cl {
			vi.Compatibilities = append(vi.Compatibilities, types.Compatibility{ClusterVersion: aws.String(c)})
		}
		ai.AddonVersions = append(ai.AddonVersions, vi)
	}
	return ai
}

func oneAddon(name, current string, versions map[string][]string) *fakeAPI {
	return &fakeAPI{
		listPages: map[string]*eks.ListAddonsOutput{"": {Addons: []string{name}}},
		current:   map[string]string{name: current},
		versions: map[string]map[string]*eks.DescribeAddonVersionsOutput{
			name: {"": {Addons: []types.AddonInfo{info(name, versions)}}},
		},
	}
}

func TestCheckCompatibleIsOK(t *testing.T) {
	api := oneAddon("demo-dns", "v1.1.0-eksbuild.1", map[string][]string{
		"v1.1.0-eksbuild.1": {"1.31", "1.32"}, "v1.2.0-eksbuild.1": {"1.32"}})
	rows, err := Check(context.Background(), api, "demo", target)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{Name: "demo-dns", Current: "v1.1.0-eksbuild.1", Status: StatusOK}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
	in := api.versionIn[0]
	if aws.ToString(in.KubernetesVersion) != "1.32" || aws.ToString(in.AddonName) != "demo-dns" {
		t.Fatalf("DescribeAddonVersions asked %+v", in)
	}
}

func TestCheckIncompatibleSuggestsLowestMinimum(t *testing.T) {
	// v1.10 sorts after v1.2 numerically (not as strings); v1.0 lacks 1.32.
	api := oneAddon("demo-dns", "v1.0.0-eksbuild.1", map[string][]string{
		"v1.0.0-eksbuild.1": {"1.30", "1.31"}, "v1.10.0-eksbuild.2": {"1.32"}, "v1.2.0-eksbuild.1": {"1.31", "1.32"}})
	rows, err := Check(context.Background(), api, "demo", target)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{{Name: "demo-dns", Current: "v1.0.0-eksbuild.1", MinCompatible: "v1.2.0-eksbuild.1",
		Status: StatusIncompatible, Severity: rules.SeverityWarning}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
}

func TestCheckAbsentOrNoVersionForTargetIsUnknown(t *testing.T) {
	cases := map[string]*fakeAPI{
		"absent":           oneAddon("demo-dns", "v1.0.0-eksbuild.1", nil),
		"none list target": oneAddon("demo-dns", "v1.0.0-eksbuild.1", map[string][]string{"v1.0.0-eksbuild.1": {"1.31"}}),
	}
	cases["absent"].versions = map[string]map[string]*eks.DescribeAddonVersionsOutput{} // add-on not in the catalog
	for name, api := range cases {
		rows, err := Check(context.Background(), api, "demo", target)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Status != StatusUnknown || rows[0].Reason == "" || rows[0].MinCompatible != "" || rows[0].Severity != "" {
			t.Errorf("%s: rows = %+v, want one UNKNOWN row with a reason", name, rows)
		}
	}
}

func TestCheckPaginates(t *testing.T) {
	api := &fakeAPI{
		listPages: map[string]*eks.ListAddonsOutput{
			"":   {Addons: []string{"a-one"}, NextToken: aws.String("p2")},
			"p2": {Addons: []string{"b-two"}},
		},
		current: map[string]string{"a-one": "v1.2.0-eksbuild.1", "b-two": "v1.0.0-eksbuild.1"},
		versions: map[string]map[string]*eks.DescribeAddonVersionsOutput{
			"a-one": {"": {Addons: []types.AddonInfo{info("a-one", map[string][]string{"v1.0.0-eksbuild.1": {"1.31"}})}, NextToken: aws.String("v2")},
				"v2": {Addons: []types.AddonInfo{info("a-one", map[string][]string{"v1.2.0-eksbuild.1": {"1.32"}})}}},
			"b-two": {"": {Addons: []types.AddonInfo{info("b-two", map[string][]string{"v1.1.0-eksbuild.1": {"1.32"}})}}},
		},
	}
	rows, err := Check(context.Background(), api, "demo", target)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, r := range rows {
		got[r.Name] = r.Status
	}
	want := map[string]Status{"a-one": StatusOK, "b-two": StatusIncompatible}
	if !reflect.DeepEqual(got, want) || len(rows) != 2 {
		t.Fatalf("rows = %+v, want statuses %v", rows, want)
	}
}

func TestCheckListErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Check(context.Background(), &fakeAPI{listErr: boom}, "demo", target); !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}

// verInfo is one catalog entry: an add-on version and the cluster versions it supports.
func verInfo(v *string, clusters ...string) types.AddonVersionInfo {
	vi := types.AddonVersionInfo{AddonVersion: v}
	for _, c := range clusters {
		vi.Compatibilities = append(vi.Compatibilities, types.Compatibility{ClusterVersion: aws.String(c)})
	}
	return vi
}

func catalogAPI(name, current string, versions ...types.AddonVersionInfo) *fakeAPI {
	return &fakeAPI{
		listPages: map[string]*eks.ListAddonsOutput{"": {Addons: []string{name}}},
		current:   map[string]string{name: current},
		versions: map[string]map[string]*eks.DescribeAddonVersionsOutput{
			name: {"": {Addons: []types.AddonInfo{{AddonName: aws.String(name), AddonVersions: versions}}}},
		},
	}
}

func TestCheckSuggestsOnlyUpgrades(t *testing.T) {
	cases := []struct {
		name, current string
		listed        []string
		wantMin, why  string
	}{
		{"older listed version is never suggested", "v1.11.1-eksbuild.10",
			[]string{"v1.11.1-eksbuild.9", "v1.11.3-eksbuild.1", "v1.11.4-eksbuild.1"}, "v1.11.3-eksbuild.1", ""},
		{"lowest strictly newer wins", "v1.0.0-eksbuild.1", []string{"v1.10.0-eksbuild.2", "v1.2.0-eksbuild.1"}, "v1.2.0-eksbuild.1", ""},
		{"nothing newer", "v1.12.0-eksbuild.1", []string{"v1.9.0-eksbuild.1", "v1.11.3-eksbuild.1"}, "",
			"no compatible version newer than v1.12.0-eksbuild.1; newest for 1.32 is v1.11.3-eksbuild.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var vs []types.AddonVersionInfo
			for _, v := range c.listed {
				vs = append(vs, verInfo(aws.String(v), "1.32"))
			}
			rows, err := Check(context.Background(), catalogAPI("demo-dns", c.current, vs...), "demo", target)
			if err != nil {
				t.Fatal(err)
			}
			want := []Row{{Name: "demo-dns", Current: c.current, MinCompatible: c.wantMin, Status: StatusIncompatible,
				Severity: rules.SeverityWarning, Reason: c.why}}
			if !reflect.DeepEqual(rows, want) {
				t.Fatalf("rows = %+v, want %+v", rows, want)
			}
		})
	}
}

func TestCheckSkipsEmptyCatalogVersions(t *testing.T) {
	api := catalogAPI("demo-dns", "v1.0.0-eksbuild.1", verInfo(nil, "1.32"), verInfo(aws.String(""), "1.32"))
	rows, err := Check(context.Background(), api, "demo", target)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != StatusUnknown || rows[0].MinCompatible != "" {
		t.Fatalf("rows = %+v, want one UNKNOWN row with no suggestion", rows)
	}
}

func TestCheckDescribeFailureIsPerAddonUnknown(t *testing.T) {
	api := &fakeAPI{
		listPages: map[string]*eks.ListAddonsOutput{"": {Addons: []string{"a-one", "b-two"}}},
		current:   map[string]string{"a-one": "v1.0.0-eksbuild.1", "b-two": "v1.1.0-eksbuild.1"},
		descFail:  map[string]error{"a-one": errors.New("denied\nsecond line")},
		versions: map[string]map[string]*eks.DescribeAddonVersionsOutput{
			"b-two": {"": {Addons: []types.AddonInfo{info("b-two", map[string][]string{"v1.1.0-eksbuild.1": {"1.32"}})}}}},
	}
	rows, err := Check(context.Background(), api, "demo", target)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "a-one" || rows[0].Status != StatusUnknown || rows[1].Status != StatusOK {
		t.Fatalf("rows = %+v", rows)
	}
	if want := "describe add-on a-one: denied; second line"; rows[0].Reason != want {
		t.Errorf("reason = %q, want %q", rows[0].Reason, want)
	}
}

func TestCheckVersionsFailureIsPerAddonUnknown(t *testing.T) {
	api := oneAddon("demo-dns", "v1.0.0-eksbuild.1", nil)
	api.versionsErr = errors.New("throttled")
	rows, err := Check(context.Background(), api, "demo", target)
	if err != nil || len(rows) != 1 || rows[0].Status != StatusUnknown || !strings.Contains(rows[0].Reason, "throttled") {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
}

func TestCheckCredentialAndContextErrorsStayFatal(t *testing.T) {
	cred := &smithy.GenericAPIError{Code: "ExpiredTokenException", Message: "expired"}
	for name, f := range map[string]func() (*fakeAPI, context.Context){
		"credential on describe": func() (*fakeAPI, context.Context) {
			a := oneAddon("x", "v1", nil)
			a.descErr = cred
			return a, context.Background()
		},
		"credential on versions": func() (*fakeAPI, context.Context) {
			a := oneAddon("x", "v1", nil)
			a.versionsErr = cred
			return a, context.Background()
		},
		"cancelled context": func() (*fakeAPI, context.Context) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			a := oneAddon("x", "v1", nil)
			a.descErr = context.Canceled
			return a, ctx
		},
	} {
		api, ctx := f()
		rows, err := Check(ctx, api, "demo", target)
		if err == nil || rows != nil {
			t.Errorf("%s: rows=%+v err=%v, want a fatal error and no rows", name, rows, err)
		}
	}
}

func TestCrossCheckNotesDisagreementOnly(t *testing.T) {
	rows := []Row{
		{Name: "agrees", Current: "v1.0.0", Status: StatusOK},
		{Name: "says-ok", Current: "v1.0.0", Status: StatusOK},
		{Name: "says-bad", Current: "v1.0.0", MinCompatible: "v1.1.0", Status: StatusIncompatible, Severity: rules.SeverityWarning},
		{Name: "no-entry", Current: "v1.0.0", Status: StatusOK},
		{Name: "unknown", Current: "v1.0.0", Status: StatusUnknown},
	}
	compat := []insights.AddonCompat{
		{Name: "agrees", Versions: []string{"v1.0.0"}},
		{Name: "says-ok", Versions: []string{"v0.9.0"}},  // insight: current not compatible
		{Name: "says-bad", Versions: []string{"v1.0.0"}}, // insight: current compatible
		{Name: "unknown", Versions: []string{"v1.0.0"}},
	}
	got := CrossCheck(rows, compat)
	if len(got) != len(rows) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range rows {
		if got[i].Status != rows[i].Status || got[i].Severity != rows[i].Severity || got[i].MinCompatible != rows[i].MinCompatible {
			t.Errorf("%s: verdict changed: %+v -> %+v", rows[i].Name, rows[i], got[i])
		}
	}
	notes := map[string]bool{}
	for _, r := range got {
		notes[r.Name] = r.InsightNote != ""
	}
	want := map[string]bool{"agrees": false, "says-ok": true, "says-bad": true, "no-entry": false, "unknown": false}
	if !reflect.DeepEqual(notes, want) {
		t.Fatalf("notes = %v, want %v", notes, want)
	}
	if rows[1].InsightNote != "" {
		t.Error("CrossCheck mutated its input")
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.19.2-eksbuild.1", "v1.19.2-eksbuild.1", 0},
		{"v1.19.2-eksbuild.1", "v1.19.2-eksbuild.2", -1},
		{"v1.19.2-eksbuild.10", "v1.19.2-eksbuild.2", 1},
		{"v1.9.0-eksbuild.1", "v1.10.0-eksbuild.1", -1},
		{"v1.19.2", "v1.19.2-eksbuild.1", -1},
		{"v1.20.0-eksbuild.1", "v1.19.9-eksbuild.9", 1},
		{"1.2.3", "v1.2.3", 0},
		{"v1.2", "v1.2.0", -1},
	}
	for _, c := range cases {
		if got := compareVersion(c.a, c.b); got != c.want {
			t.Errorf("compareVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := compareVersion(c.b, c.a); got != -c.want {
			t.Errorf("compareVersion(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}
