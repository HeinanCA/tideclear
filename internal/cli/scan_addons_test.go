package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HeinanCA/tideclear/internal/eksinfo"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

// fakeAddon is an installed add-on and the versions its catalog lists for the asked-for cluster version.
type fakeAddon struct {
	Name, Current string
	Supports      []string
	Absent        bool // not in the DescribeAddonVersions catalog at all
}

// addonFake serves add-on reads; its zero value has no add-ons.
type addonFake struct {
	addons   []fakeAddon
	addonErr error // returned by ListAddons
	hang     bool  // ListAddons blocks until its context is done
	asked    []string
}

func (f *addonFake) ListAddons(ctx context.Context, _ *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	if f.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.addonErr != nil {
		return nil, f.addonErr
	}
	var names []string
	for _, a := range f.addons {
		names = append(names, a.Name)
	}
	return &eks.ListAddonsOutput{Addons: names}, nil
}

func (f *addonFake) find(name *string) fakeAddon {
	for _, a := range f.addons {
		if a.Name == aws.ToString(name) {
			return a
		}
	}
	return fakeAddon{}
}

func (f *addonFake) DescribeAddon(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	a := f.find(in.AddonName)
	return &eks.DescribeAddonOutput{Addon: &types.Addon{AddonName: &a.Name, AddonVersion: &a.Current}}, nil
}

func (f *addonFake) DescribeAddonVersions(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
	f.asked = append(f.asked, aws.ToString(in.KubernetesVersion))
	a := f.find(in.AddonName)
	if a.Absent {
		return &eks.DescribeAddonVersionsOutput{}, nil
	}
	info := types.AddonInfo{AddonName: &a.Name}
	for _, v := range a.Supports {
		info.AddonVersions = append(info.AddonVersions, types.AddonVersionInfo{AddonVersion: aws.String(v),
			Compatibilities: []types.Compatibility{{ClusterVersion: in.KubernetesVersion}}})
	}
	return &eks.DescribeAddonVersionsOutput{Addons: []types.AddonInfo{info}}, nil
}

// failingCreds is a credential provider whose failure is tagged like loadAWSConfig's.
func failingCreds() aws.CredentialsProvider {
	return eksinfo.WrapCredentials(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("the SSO session associated with this profile has expired or is otherwise invalid")
	}))
}

func addonScan(t *testing.T, api ClusterAPI, args ...string) (int, string, string) {
	t.Helper()
	return run(t, insightDeps(t, cleanCluster(), api), append([]string{"scan", "--cluster", "demo-cluster"}, args...)...)
}

func okAddon() fakeAddon {
	return fakeAddon{Name: "demo-dns", Current: "v1.1.0-eksbuild.1", Supports: []string{"v1.1.0-eksbuild.1", "v1.2.0-eksbuild.1"}}
}

func badAddon() fakeAddon {
	return fakeAddon{Name: "demo-net", Current: "v1.0.0-eksbuild.1", Supports: []string{"v1.10.0-eksbuild.1", "v1.2.0-eksbuild.1"}}
}

func TestScanAddonsCompatibleIsOK(t *testing.T) {
	api := &fakeAWS{addonFake: addonFake{addons: []fakeAddon{okAddon()}}}
	code, out, se := addonScan(t, api, "--strict")
	if code != 0 || se != "" {
		t.Fatalf("exit=%d stderr=%q stdout:\n%s", code, se, out)
	}
	for _, want := range []string{"NAME", "CURRENT", "MIN COMPATIBLE", "STATUS", "NOTE", "demo-dns", "v1.1.0-eksbuild.1", "OK"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if len(api.asked) != 1 || api.asked[0] != "1.31" {
		t.Errorf("DescribeAddonVersions asked for %v, want [1.31]", api.asked)
	}
}

func TestScanAddonsIncompatibleShowsMinimumAndIsWarning(t *testing.T) {
	api := &fakeAWS{addonFake: addonFake{addons: []fakeAddon{badAddon()}}}
	for _, args := range [][]string{nil, {"--strict"}} {
		code, out, se := addonScan(t, api, args...)
		if code != 0 || se != "" {
			t.Fatalf("%v: exit=%d stderr=%q, want 0 (WARNING)", args, code, se)
		}
		if !strings.Contains(out, "v1.0.0-eksbuild.1") || !strings.Contains(out, "v1.2.0-eksbuild.1") || !strings.Contains(out, "INCOMPATIBLE (WARNING)") {
			t.Fatalf("stdout:\n%s", out)
		}
	}
	_, js, _ := addonScan(t, api, "--output", "json")
	var doc struct {
		Addons []struct{ Name, Current, MinCompatible, Status, Severity string }
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatal(err)
	}
	want := struct{ Name, Current, MinCompatible, Status, Severity string }{"demo-net", "v1.0.0-eksbuild.1", "v1.2.0-eksbuild.1", "INCOMPATIBLE", "WARNING"}
	if len(doc.Addons) != 1 || doc.Addons[0] != want {
		t.Fatalf("addons = %+v", doc.Addons)
	}
}

func TestScanAddonsAbsentFromCatalogIsUnknownNeverOK(t *testing.T) {
	api := &fakeAWS{addonFake: addonFake{addons: []fakeAddon{{Name: "demo-agent", Current: "v3.0.0", Absent: true}}}}
	code, out, _ := addonScan(t, api)
	if code != 0 || !strings.Contains(out, "UNKNOWN") || !strings.Contains(out, "result not clean: 1 unknown") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
	if strings.Contains(out, " OK") {
		t.Errorf("unknown add-on read as OK:\n%s", out)
	}
	if code, _, _ := addonScan(t, api, "--strict"); code != 1 {
		t.Fatalf("--strict exit = %d, want 1 (add-on UNKNOWN counts)", code)
	}
}

func TestScanAddonsInsightDisagreementIsNotedDirectCheckWins(t *testing.T) {
	// Insight says the current version is not compatible; the direct check says it is.
	api := &deprecationAWS{status: types.InsightStatusValuePassing,
		addonCompat: []types.AddonCompatibilityDetail{{Name: aws.String("demo-dns"), CompatibleVersions: []string{"v0.9.0-eksbuild.1"}}}}
	api.addons = []fakeAddon{okAddon()}
	code, out, _ := insightScan(t, api, "--strict")
	if code != 0 || !strings.Contains(out, "insight does not list") || !strings.Contains(out, "OK") {
		t.Fatalf("exit=%d stdout:\n%s", code, out)
	}
	// Without --insights there is nothing to cross-check.
	_, out, _ = addonScan(t, &fakeAWS{addonFake: addonFake{addons: []fakeAddon{okAddon()}}})
	if strings.Contains(out, "insight") {
		t.Errorf("note without --insights:\n%s", out)
	}
}

func TestScanAddonsDegradesOnNonCredentialFailure(t *testing.T) {
	op := func(code string) error {
		return &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListAddons",
			Err: &smithy.GenericAPIError{Code: code, Message: "nope\nsecond line"}}
	}
	for _, code := range []string{"AccessDeniedException", "ThrottlingException", "InternalServerException"} {
		t.Run(code, func(t *testing.T) {
			api := &fakeAWS{addonFake: addonFake{addonErr: op(code)}}
			exit, out, se := addonScan(t, api, "--strict")
			if exit != 0 || strings.Contains(out, "add-ons:") {
				t.Fatalf("exit=%d, want 0 (skipped check never counts); stdout:\n%s", exit, out)
			}
			assertOneLine(t, se)
			if !strings.Contains(se, "addons") {
				t.Errorf("stderr should name the skipped check: %q", se)
			}
			_, js, _ := addonScan(t, api, "--output", "json")
			var doc struct {
				ChecksSkipped []struct{ Check, Reason string }
			}
			if err := json.Unmarshal([]byte(js), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.ChecksSkipped) != 1 || doc.ChecksSkipped[0].Check != "addons" {
				t.Fatalf("json doc = %+v", doc)
			}
		})
	}
}

func TestScanAddonsCredentialFailuresExitTwo(t *testing.T) {
	for _, code := range []string{"ExpiredTokenException", "UnrecognizedClientException", "InvalidClientTokenId"} {
		api := &fakeAWS{addonFake: addonFake{addonErr: &smithy.OperationError{ServiceID: eks.ServiceID, OperationName: "ListAddons",
			Err: &smithy.GenericAPIError{Code: code, Message: "bad"}}}}
		exit, out, se := addonScan(t, api)
		if exit != 2 || out != "" {
			t.Fatalf("%s: exit=%d stdout=%q", code, exit, out)
		}
		assertOneLine(t, se)
	}
}

func TestScanAddonsCredentialProviderFailureExitsTwo(t *testing.T) {
	_, err := eks.New(eks.Options{Region: "us-east-1", Credentials: failingCreds(), HTTPClient: deadHTTP{}, RetryMaxAttempts: 1}).
		ListAddons(context.Background(), &eks.ListAddonsInput{ClusterName: aws.String("demo-cluster")})
	if err == nil {
		t.Fatal("ListAddons succeeded offline")
	}
	exit, out, se := addonScan(t, &fakeAWS{addonFake: addonFake{addonErr: err}})
	if exit != 2 || out != "" || !strings.Contains(se, "SSO session") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out, se)
	}
	assertOneLine(t, se)
}

func TestScanWithoutClusterNotesSkippedAddonsOnStdoutOnly(t *testing.T) {
	code, out, se := run(t, kubeDeps(t, cleanCluster()), "scan")
	if code != 0 || se != "" {
		t.Fatalf("exit=%d stderr=%q, want 0 and empty", code, se)
	}
	if !strings.Contains(out, "check skipped: addons: needs --cluster") {
		t.Fatalf("stdout lacks the footer note:\n%s", out)
	}
	_, js, _ := run(t, kubeDeps(t, cleanCluster()), "scan", "--output", "json")
	var doc struct {
		ChecksSkipped []struct{ Check, Reason string }
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.ChecksSkipped) != 1 || doc.ChecksSkipped[0].Check != "addons" {
		t.Fatalf("json doc = %+v", doc)
	}
}

func TestScanPathNeverTouchesAddons(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a.yaml", []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &fakeAWS{addonFake: addonFake{addons: []fakeAddon{badAddon()}}}
	d := insightDeps(t, cleanCluster(), api)
	for _, args := range [][]string{{"--target", "1.31"}, {"--cluster", "demo-cluster"}} {
		code, out, se := run(t, d, append([]string{"scan", "--path", dir}, args...)...)
		if code != 0 || se != "" || strings.Contains(out, "add-on") || strings.Contains(out, "skipped") {
			t.Fatalf("%v: exit=%d stderr=%q stdout:\n%s", args, code, se, out)
		}
	}
	if len(api.asked) != 0 {
		t.Fatalf("add-on API called during a --path scan: %v", api.asked)
	}
}

func shortExtrasTimeout(t *testing.T) {
	t.Helper()
	old, oldScan := extrasTimeout, scanTimeout
	extrasTimeout, scanTimeout = 30*time.Millisecond, 2*time.Second
	t.Cleanup(func() { extrasTimeout, scanTimeout = old, oldScan })
}

func TestScanAddonsHangDegradesInsteadOfLosingTheReport(t *testing.T) {
	shortExtrasTimeout(t)
	api := &fakeAWS{addonFake: addonFake{hang: true}}
	exit, out, se := addonScan(t, api)
	if exit != 0 || out == "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want 0 with the report", exit, out, se)
	}
	assertOneLine(t, se)
	if !strings.Contains(se, "addons check skipped") || !strings.Contains(out, "check skipped: addons") {
		t.Errorf("stderr=%q stdout:\n%s", se, out)
	}
}

type hangInsightsAWS struct{ deprecationAWS }

func (h *hangInsightsAWS) ListInsights(ctx context.Context, _ *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestScanInsightsHangDegradesToUnavailableRow(t *testing.T) {
	shortExtrasTimeout(t)
	exit, out, se := insightScan(t, &hangInsightsAWS{})
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%q stdout:\n%s", exit, se, out)
	}
	assertOneLine(t, se)
	if !strings.Contains(se, "insights check skipped") || !strings.Contains(out, "UNKNOWN") || !strings.Contains(out, "check skipped: insights") {
		t.Errorf("stderr=%q stdout:\n%s", se, out)
	}
}

func TestScanAddonsNoneManagedSaysSo(t *testing.T) {
	_, out, _ := addonScan(t, &fakeAWS{})
	if !strings.Contains(out, "add-ons: none managed by EKS (self-managed add-ons are not checked)") {
		t.Fatalf("stdout:\n%s", out)
	}
	_, js, _ := addonScan(t, &fakeAWS{}, "--output", "json")
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["addons"]) != "[]" {
		t.Fatalf("addons = %s, want []", doc["addons"])
	}
	_, js, _ = run(t, kubeDeps(t, cleanCluster()), "scan", "--output", "json")
	if strings.Contains(js, `"addons":`) {
		t.Errorf("scan without --cluster has an addons key:\n%s", js)
	}
}
