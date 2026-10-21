package demo

import (
	"context"

	"github.com/HeinanCA/tideclear/internal/apiscan"
	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// sampleSource is a read-only apiscan.Source holding the sample's observations.
type sampleSource struct {
	current rules.Version
	obs     apiscan.Observed
}

func (s sampleSource) Objects(context.Context) (apiscan.Observed, error) { return s.obs, nil }

func (s sampleSource) ServerVersion(context.Context) (rules.Version, bool, error) {
	return s.current, true, nil
}

// sampleAddons is a read-only add-on API over fixed data.
type sampleAddons struct {
	names     []string
	installed map[string]string
	supported map[string][]string // add-on name to versions compatible with the target
}

func (s sampleAddons) ListAddons(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	return &eks.ListAddonsOutput{Addons: s.names}, nil
}

func (s sampleAddons) DescribeAddon(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	return &eks.DescribeAddonOutput{Addon: &types.Addon{AddonVersion: aws.String(s.installed[aws.ToString(in.AddonName)])}}, nil
}

func (s sampleAddons) DescribeAddonVersions(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
	info := types.AddonInfo{AddonName: in.AddonName}
	for _, v := range s.supported[aws.ToString(in.AddonName)] {
		info.AddonVersions = append(info.AddonVersions, types.AddonVersionInfo{AddonVersion: aws.String(v),
			Compatibilities: []types.Compatibility{{ClusterVersion: in.KubernetesVersion}}})
	}
	return &eks.DescribeAddonVersionsOutput{Addons: []types.AddonInfo{info}}, nil
}

// sampleNodegroups is a read-only nodegroup API with one Amazon Linux 2 group.
type sampleNodegroups struct{ name, version, release string }

func (s sampleNodegroups) ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return &eks.ListNodegroupsOutput{Nodegroups: []string{s.name}}, nil
}

func (s sampleNodegroups) DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	return &eks.DescribeNodegroupOutput{Nodegroup: &types.Nodegroup{NodegroupName: aws.String(s.name), AmiType: types.AMITypesAl2X8664,
		Version: aws.String(s.version), ReleaseVersion: aws.String(s.release)}}, nil
}
