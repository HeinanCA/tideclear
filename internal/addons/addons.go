// Package addons checks that each installed EKS add-on has a version that
// supports the target Kubernetes version. Read-only: list and describe only.
package addons

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/HeinanCA/tideclear/internal/insights"
	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// API is the read-only EKS surface the add-on check needs.
type API interface {
	ListAddons(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddon(context.Context, *eks.DescribeAddonInput, ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
	DescribeAddonVersions(context.Context, *eks.DescribeAddonVersionsInput, ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error)
}

// Status is the add-on verdict.
type Status string

// Add-on verdicts.
const (
	StatusOK           Status = "OK"
	StatusIncompatible Status = "INCOMPATIBLE"
	StatusUnknown      Status = "UNKNOWN"
)

// SeverityIncompatible is how an incompatible add-on ranks. The brief only
// says "flag", so it is a WARNING (exit 0); change here to make it a BLOCKER.
const SeverityIncompatible = rules.SeverityWarning

// Row is one installed add-on.
type Row struct {
	Name, Current, MinCompatible string
	Status                       Status
	Severity                     rules.Severity // set only when Status is INCOMPATIBLE
	Reason                       string         // why the status is UNKNOWN
	InsightNote                  string         // set when the insight disagrees with the direct check
}

// Check reads every add-on on the cluster and judges its current version
// against target. Any API error aborts the check (wrapped, so credential
// failures stay classifiable); nothing partial is returned.
func Check(ctx context.Context, api API, cluster string, target rules.Version) ([]Row, error) {
	var rows []Row
	pages := eks.NewListAddonsPaginator(api, &eks.ListAddonsInput{ClusterName: aws.String(cluster)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list add-ons for %s: %w", cluster, err)
		}
		for _, name := range page.Addons {
			row, err := checkOne(ctx, api, cluster, name, target)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func checkOne(ctx context.Context, api API, cluster, name string, target rules.Version) (Row, error) {
	d, err := api.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(cluster), AddonName: aws.String(name)})
	if err != nil {
		return Row{}, fmt.Errorf("describe add-on %s: %w", name, err)
	}
	current := ""
	if d != nil && d.Addon != nil {
		current = aws.ToString(d.Addon.AddonVersion)
	}
	supported, err := supportedVersions(ctx, api, name, target)
	if err != nil {
		return Row{}, err
	}
	return judge(name, current, supported, target), nil
}

// supportedVersions lists the add-on versions whose compatibilities include target.
func supportedVersions(ctx context.Context, api API, name string, target rules.Version) ([]string, error) {
	var out []string
	pages := eks.NewDescribeAddonVersionsPaginator(api, &eks.DescribeAddonVersionsInput{
		AddonName: aws.String(name), KubernetesVersion: aws.String(target.String())})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe versions of add-on %s: %w", name, err)
		}
		for _, info := range page.Addons {
			if aws.ToString(info.AddonName) != name {
				continue
			}
			for _, v := range info.AddonVersions {
				if slices.ContainsFunc(v.Compatibilities, func(c types.Compatibility) bool {
					return aws.ToString(c.ClusterVersion) == target.String()
				}) {
					out = append(out, aws.ToString(v.AddonVersion))
				}
			}
		}
	}
	return out, nil
}

// judge never reads OK without proof: no listed version for the target, or
// an unknown current version, is UNKNOWN.
func judge(name, current string, supported []string, target rules.Version) Row {
	row := Row{Name: name, Current: current}
	switch {
	case len(supported) == 0:
		row.Status = StatusUnknown
		row.Reason = fmt.Sprintf("no version of this add-on lists Kubernetes %s as compatible", target)
	case current == "":
		row.Status = StatusUnknown
		row.Reason = "current version not reported"
	case slices.Contains(supported, current):
		row.Status = StatusOK
	default:
		row.Status = StatusIncompatible
		row.Severity = SeverityIncompatible
		row.MinCompatible = slices.MaxFunc(supported, func(a, b string) int { return -compareVersion(a, b) })
	}
	return row
}

// CrossCheck notes where the upgrade insight disagrees with the direct
// check. The direct check wins: statuses never change, and the input is not modified.
func CrossCheck(rows []Row, compat []insights.AddonCompat) []Row {
	out := slices.Clone(rows)
	for i, r := range out {
		if r.Status == StatusUnknown {
			continue
		}
		c := slices.IndexFunc(compat, func(a insights.AddonCompat) bool { return a.Name == r.Name })
		if c < 0 {
			continue
		}
		insightOK := slices.Contains(compat[c].Versions, r.Current)
		switch {
		case insightOK && r.Status == StatusIncompatible:
			out[i].InsightNote = "insight lists this version as compatible; direct check says not"
		case !insightOK && r.Status == StatusOK:
			out[i].InsightNote = "insight does not list this version as compatible; direct check says it is"
		}
	}
	return out
}

// compareVersion orders add-on versions such as "v1.19.2-eksbuild.1" by
// their numeric parts, so v1.9 < v1.10 and eksbuild.2 < eksbuild.10. A
// version that is a prefix of another sorts first.
func compareVersion(a, b string) int {
	x, y := numericParts(a), numericParts(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if c := compareNumber(x[i], y[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(x), len(y))
}

// numericParts is the digit runs of s, without leading zeros.
func numericParts(s string) []string {
	runs := strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	for i, r := range runs {
		runs[i] = strings.TrimLeft(r, "0")
	}
	return runs
}

// compareNumber compares two digit strings without overflow.
func compareNumber(a, b string) int {
	if len(a) != len(b) {
		return cmp.Compare(len(a), len(b))
	}
	return strings.Compare(a, b)
}
