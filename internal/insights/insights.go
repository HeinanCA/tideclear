// Package insights reads EKS Upgrade Insights. It is read-only: the API
// interface has no refresh operation, so a refresh cannot be started by accident.
package insights

import (
	"context"
	"fmt"
	"time"

	"github.com/HeinanCA/tideclear/internal/rules"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// API is the read-only subset of the SDK client used here; *eks.Client satisfies it.
type API interface {
	ListInsights(ctx context.Context, in *eks.ListInsightsInput, optFns ...func(*eks.Options)) (*eks.ListInsightsOutput, error)
	DescribeInsight(ctx context.Context, in *eks.DescribeInsightInput, optFns ...func(*eks.Options)) (*eks.DescribeInsightOutput, error)
}

// Status is an insight's status; anything the API sends that is not one of
// the four known values is StatusUnknown.
type Status string

// Insight statuses.
const (
	StatusPassing Status = "PASSING"
	StatusWarning Status = "WARNING"
	StatusError   Status = "ERROR"
	StatusUnknown Status = "UNKNOWN"
)

// Insight is one upgrade-readiness insight. List fills the summary fields;
// Load also fills Deprecations and AddonCompat.
type Insight struct {
	ID, Name, Target string
	Status           Status
	Reason           string
	LastRefresh      time.Time // zero when EKS reported none
	Deprecations     []Deprecation
	AddonCompat      []AddonCompat
	// DetailsUnavailable is set by Load when DescribeInsight returned no
	// category summary, so the empty Deprecations means "unknown", not "none".
	DetailsUnavailable bool
}

// Deprecation is one deprecated API the insight saw in use.
type Deprecation struct {
	Usage, ReplacedWith, StopServing, StartServingReplacement string
	Clients                                                   []Client
}

// Client is one Kubernetes client seen calling a deprecated API.
type Client struct {
	UserAgent   string
	Requests    int32      // last 30 days
	LastRequest *time.Time // nil when EKS reported none
}

// AddonCompat lists the add-on versions the insight calls compatible with the target.
type AddonCompat struct {
	Name     string
	Versions []string
}

// List returns the cluster's UPGRADE_READINESS insights (summaries only),
// restricted to target when it is non-nil.
func List(ctx context.Context, api API, cluster string, target *rules.Version) ([]Insight, error) {
	filter := &types.InsightsFilter{Categories: []types.Category{types.CategoryUpgradeReadiness}}
	if target != nil {
		filter.KubernetesVersions = []string{target.String()}
	}
	p := eks.NewListInsightsPaginator(api, &eks.ListInsightsInput{ClusterName: aws.String(cluster), Filter: filter})
	var out []Insight
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Insights {
			out = append(out, Insight{
				ID: aws.ToString(s.Id), Name: aws.ToString(s.Name), Target: aws.ToString(s.KubernetesVersion),
				Status: status(s.InsightStatus), Reason: reason(s.InsightStatus), LastRefresh: aws.ToTime(s.LastRefreshTime),
			})
		}
	}
	return out, nil
}

// Load is List plus a DescribeInsight call per insight for the details.
func Load(ctx context.Context, api API, cluster string, target *rules.Version) ([]Insight, error) {
	out, err := List(ctx, api, cluster, target)
	if err != nil {
		return nil, err
	}
	for i, in := range out {
		d, err := api.DescribeInsight(ctx, &eks.DescribeInsightInput{ClusterName: aws.String(cluster), Id: aws.String(in.ID)})
		if err != nil {
			return nil, fmt.Errorf("describe insight %s: %w", in.ID, err)
		}
		if d == nil || d.Insight == nil || d.Insight.CategorySpecificSummary == nil {
			out[i].DetailsUnavailable = true
			continue
		}
		sum := d.Insight.CategorySpecificSummary
		out[i].Deprecations = deprecations(sum.DeprecationDetails)
		out[i].AddonCompat = addonCompat(sum.AddonCompatibilityDetails)
	}
	return out, nil
}

func status(s *types.InsightStatus) Status {
	if s != nil {
		switch Status(s.Status) {
		case StatusPassing, StatusWarning, StatusError:
			return Status(s.Status)
		}
	}
	return StatusUnknown
}

func reason(s *types.InsightStatus) string {
	if s == nil {
		return ""
	}
	return aws.ToString(s.Reason)
}

func deprecations(ds []types.DeprecationDetail) []Deprecation {
	var out []Deprecation
	for _, d := range ds {
		dep := Deprecation{
			Usage: aws.ToString(d.Usage), ReplacedWith: aws.ToString(d.ReplacedWith),
			StopServing: aws.ToString(d.StopServingVersion), StartServingReplacement: aws.ToString(d.StartServingReplacementVersion),
		}
		for _, c := range d.ClientStats {
			dep.Clients = append(dep.Clients, Client{
				UserAgent: aws.ToString(c.UserAgent), Requests: c.NumberOfRequestsLast30Days, LastRequest: c.LastRequestTime,
			})
		}
		out = append(out, dep)
	}
	return out
}

func addonCompat(as []types.AddonCompatibilityDetail) []AddonCompat {
	var out []AddonCompat
	for _, a := range as {
		out = append(out, AddonCompat{Name: aws.ToString(a.Name), Versions: a.CompatibleVersions})
	}
	return out
}
