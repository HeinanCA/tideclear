// Package eksinfo lists EKS clusters with their Kubernetes support dates.
package eksinfo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

// ErrClusterNotFound means the requested cluster name matched nothing.
var ErrClusterNotFound = errors.New("cluster not found")

// EKSAPI is the subset of the SDK client used here; *eks.Client satisfies it.
type EKSAPI interface {
	ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	DescribeClusterVersions(ctx context.Context, in *eks.DescribeClusterVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error)
}

// Cluster is one EKS cluster and the support window of its version.
type Cluster struct {
	Name, Region, Version string
	StandardEnd           time.Time
	ExtendedEnd           time.Time
	DaysLeft              *int
	Support               Support
	OnExtended            bool // compatibility: Support == SupportExtended
}

// Support is where a cluster's Kubernetes version sits in its support window.
type Support string

// Support states. SupportUnknown means the version was absent from
// DescribeClusterVersions.
const (
	SupportStandard    Support = "standard"
	SupportExtended    Support = "extended"
	SupportUnsupported Support = "unsupported"
	SupportUnknown     Support = "unknown"
)

// DefaultRegionTimeout bounds each region, single- or multi-region.
const DefaultRegionTimeout = 20 * time.Second

// SkippedRegion is a region left out of a multi-region run.
type SkippedRegion struct {
	Region string
	Err    error
}

// Lister lists clusters across regions.
type Lister struct {
	Clients func(region string) (EKSAPI, error)
	Now     func() time.Time
	// RegionTimeout bounds each region; 0 means DefaultRegionTimeout.
	RegionTimeout time.Duration
}

// List returns clusters sorted by region then name. With name set only that
// cluster is looked up. With several regions, a region failing with an
// auth/opt-in error or a timeout is skipped (a single region's timeout is an
// error naming it); if every region fails the call
// errors. Credential-retrieval failures and client-construction failures are
// never skipped.
func (l Lister) List(ctx context.Context, regions []string, name string) ([]Cluster, []SkippedRegion, error) {
	var all []Cluster
	var skipped []SkippedRegion
	multi := len(regions) > 1
	for _, r := range regions {
		cs, err := l.listRegionBounded(ctx, r, name)
		var ce *clientError
		switch {
		case err == nil:
			all = append(all, cs...)
		case errors.As(err, &ce):
			return nil, nil, ce.err
		case multi && (isAuthError(err) || isTimeout(err)):
			skipped = append(skipped, SkippedRegion{Region: r, Err: err})
		default:
			return nil, nil, fmt.Errorf("region %s: %w", r, err)
		}
	}
	if len(regions) > 0 && len(skipped) == len(regions) {
		return nil, nil, fmt.Errorf("all %d regions failed; first: %s: %w",
			len(skipped), skipped[0].Region, skipped[0].Err)
	}
	if name != "" && len(all) == 0 {
		return nil, skipped, fmt.Errorf("%w: %s", ErrClusterNotFound, name)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Region != all[j].Region {
			return all[i].Region < all[j].Region
		}
		return all[i].Name < all[j].Name
	})
	return all, skipped, nil
}

// clientError marks a failure to build a client (config/profile), which is
// not specific to a region.
type clientError struct{ err error }

func (e *clientError) Error() string { return e.err.Error() }
func (e *clientError) Unwrap() error { return e.err }

func (l Lister) listRegionBounded(ctx context.Context, region, name string) ([]Cluster, error) {
	d := l.RegionTimeout
	if d <= 0 {
		d = DefaultRegionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return l.listRegion(ctx, region, name)
}

func (l Lister) listRegion(ctx context.Context, region, name string) ([]Cluster, error) {
	api, err := l.Clients(region)
	if err != nil {
		return nil, &clientError{err: err}
	}
	versions, err := clusterVersions(ctx, api, name)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, nil
	}
	info, err := versionInfo(ctx, api, versions)
	if err != nil {
		return nil, err
	}
	nowFn := l.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	out := make([]Cluster, 0, len(versions))
	for _, v := range versions {
		vi, ok := info[v.version]
		out = append(out, buildCluster(v, region, vi, ok, now))
	}
	return out, nil
}

type named struct{ name, version string }

// clusterVersions returns each cluster's name and version in one region.
func clusterVersions(ctx context.Context, api EKSAPI, name string) ([]named, error) {
	names := []string{name}
	if name == "" {
		names = nil
		p := eks.NewListClustersPaginator(api, &eks.ListClustersInput{})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			names = append(names, page.Clusters...)
		}
	}
	var out []named
	for _, n := range names {
		d, err := api.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(n)})
		var nf *types.ResourceNotFoundException
		if errors.As(err, &nf) {
			continue // deleted between list and describe
		}
		if err != nil {
			return nil, err
		}
		if d == nil || d.Cluster == nil {
			continue
		}
		out = append(out, named{name: n, version: aws.ToString(d.Cluster.Version)})
	}
	return out, nil
}

// versionInfo fetches support data for the versions in use, keyed by version.
func versionInfo(ctx context.Context, api EKSAPI, cs []named) (map[string]types.ClusterVersionInformation, error) {
	inUse := map[string]bool{}
	for _, c := range cs {
		if c.version != "" {
			inUse[c.version] = true
		}
	}
	// The API accepts only one of defaultOnly, clusterVersions, includeAll or status.
	// includeAll also returns unsupported versions; filter to the ones in use below.
	p := eks.NewDescribeClusterVersionsPaginator(api, &eks.DescribeClusterVersionsInput{
		IncludeAll: aws.Bool(true),
	})
	info := map[string]types.ClusterVersionInformation{}
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range page.ClusterVersions {
			if k := aws.ToString(v.ClusterVersion); inUse[k] {
				info[k] = v
			}
		}
	}
	return info, nil
}

func buildCluster(c named, region string, v types.ClusterVersionInformation, found bool, now time.Time) Cluster {
	out := Cluster{Name: c.name, Region: region, Version: c.version, Support: SupportUnknown}
	if !found {
		return out
	}
	std, ext := aws.ToTime(v.EndOfStandardSupportDate), aws.ToTime(v.EndOfExtendedSupportDate)
	out.StandardEnd, out.ExtendedEnd = std, ext
	out.Support = supportState(v.VersionStatus, std, ext, now)
	end := ext
	if out.Support == SupportStandard {
		end = std
	}
	if !end.IsZero() {
		d := daysUntil(end, now)
		out.DaysLeft = &d
	}
	out.OnExtended = out.Support == SupportExtended
	return out
}

// supportState trusts the API status and derives from dates only when absent.
func supportState(status types.VersionStatus, std, ext, now time.Time) Support {
	switch status {
	case types.VersionStatusStandardSupport:
		return SupportStandard
	case types.VersionStatusExtendedSupport:
		return SupportExtended
	case types.VersionStatusUnsupported:
		return SupportUnsupported
	}
	switch {
	case !ext.IsZero() && now.After(ext):
		return SupportUnsupported
	case !std.IsZero() && now.After(std):
		return SupportExtended
	}
	return SupportStandard
}

// isTimeout reports a per-region deadline or a transport timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// isAuthError reports credential, permission and opt-in failures. The SDK
// models only AccessDeniedException; UnrecognizedClientException,
// InvalidClientTokenId, AuthFailure and OptInRequired arrive as
// smithy.GenericAPIError codes.
func isAuthError(err error) bool {
	if isCredentialError(err) {
		return false
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch code := ae.ErrorCode(); code {
	case "UnrecognizedClientException", "InvalidClientTokenId", "AuthFailure", "OptInRequired":
		return true
	default:
		return strings.HasPrefix(code, "AccessDenied")
	}
}

// IsAccessDenied reports an IAM permission failure (an AccessDenied* code from
// EKS itself). Credential-retrieval errors and bad-credential codes
// (UnrecognizedClientException, InvalidClientTokenId) are not permission failures.
func IsAccessDenied(err error) bool {
	var ae smithy.APIError
	return !isCredentialError(err) && errors.As(err, &ae) && strings.HasPrefix(ae.ErrorCode(), "AccessDenied")
}

// IsCredentialFailure reports that the caller's credentials are unusable
// (missing, expired, or rejected), so no per-check degrade can help: the
// whole run must stop. A plain AccessDenied is a permission gap and is not one.
func IsCredentialFailure(err error) bool {
	if isCredentialError(err) {
		return true
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.ErrorCode() {
	case "UnrecognizedClientException", "InvalidClientTokenId", "ExpiredToken", "ExpiredTokenException",
		"RequestExpired", "InvalidSignatureException", "SignatureDoesNotMatch", "AuthFailure",
		"IncompleteSignature", "MissingAuthenticationToken", "MissingAuthenticationTokenException":
		return true
	}
	return false
}

// isCredentialError reports a failure to obtain credentials: the SDK nests an
// operation error from another service (STS AssumeRole, SSO) under the EKS
// call's own, so an AccessDenied there is not an EKS-region problem.
func isCredentialError(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		var oe *smithy.OperationError
		if errors.As(e, &oe) && oe.ServiceID != "" && oe.ServiceID != eks.ServiceID {
			return true
		}
	}
	return false
}

// daysUntil counts calendar days in UTC from now to end; negative once end has passed.
func daysUntil(end, now time.Time) int {
	day := func(t time.Time) time.Time {
		u := t.UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}
	return int(day(end).Sub(day(now)) / (24 * time.Hour))
}
