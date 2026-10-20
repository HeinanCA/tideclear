package apiscan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/HeinanCA/tideclear/internal/rules"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

const (
	lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
	originLastApplied     = "last-applied"
	originManagedFields   = "managedFields"
	listPageSize          = 500
	maxListPages          = 10000
)

type clusterSource struct {
	disc discovery.DiscoveryInterface
	dyn  dynamic.Interface
	set  *rules.Set
}

// NewClusterSource reads a live cluster using only get/list verbs, listing
// just the kinds set has rules for.
func NewClusterSource(disc discovery.DiscoveryInterface, dyn dynamic.Interface, set *rules.Set) Source {
	return &clusterSource{disc: disc, dyn: dyn, set: set}
}

// callCtx runs a discovery call, which client-go gives no context, and gives
// up on ctx.Done. The abandoned call is still bounded by the 30s rest timeout.
func callCtx[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (c *clusterSource) ServerVersion(ctx context.Context) (rules.Version, bool, error) {
	info, err := callCtx(ctx, c.disc.ServerVersion)
	if err != nil {
		return rules.Version{}, false, err
	}
	v, err := rules.ParseVersion(info.Major + "." + info.Minor)
	if err != nil {
		return rules.Version{}, false, fmt.Errorf("server version: %w", err)
	}
	return v, true, nil
}

// Objects lists every kind the rules cover and emits the apiVersions each
// object was written with. Each group is read through its preferred version
// first, then through its other served versions for the wanted kinds not yet
// found; a kind is listed once, via the first version that serves it.
func (c *clusterSource) Objects(ctx context.Context) (Observed, error) {
	groups, err := callCtx(ctx, c.disc.ServerGroups)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return Observed{}, cerr
		}
		return Observed{}, fmt.Errorf("discover API groups: %w", withStatus(err))
	}
	wanted := map[rules.GroupKind]bool{}
	wantedGroup := map[string]bool{}
	for _, gk := range c.set.Kinds() {
		wanted[gk] = true
		wantedGroup[gk.Group] = true
	}
	sort.Slice(groups.Groups, func(i, j int) bool { return groups.Groups[i].Name < groups.Groups[j].Name })

	var obs Observed
	for _, g := range groups.Groups {
		if !wantedGroup[g.Name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Observed{}, err
		}
		if err := c.readGroup(ctx, g, wanted, &obs); err != nil {
			return Observed{}, err
		}
	}
	return obs, nil
}

func (c *clusterSource) readGroup(ctx context.Context, g metav1.APIGroup, wanted map[rules.GroupKind]bool, obs *Observed) error {
	pending := 0
	for gk := range wanted {
		if gk.Group == g.Name {
			pending++
		}
	}
	preferred := g.PreferredVersion.GroupVersion
	versions := []string{preferred}
	for _, v := range g.Versions {
		if v.GroupVersion != preferred {
			versions = append(versions, v.GroupVersion)
		}
	}
	found := map[string]bool{}
	for i, gv := range versions {
		if pending == len(found) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		reason := func(err error) string {
			if i == 0 {
				return err.Error()
			}
			return fmt.Sprintf("version %s: %s", strings.TrimPrefix(gv, g.Name+"/"), err)
		}
		list, err := callCtx(ctx, func() (*metav1.APIResourceList, error) { return c.disc.ServerResourcesForGroupVersion(gv) })
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			obs.Skipped = append(obs.Skipped, Skipped{Group: g.Name, Reason: reason(err)})
			continue
		}
		parsed, err := schema.ParseGroupVersion(gv)
		if err != nil {
			obs.Skipped = append(obs.Skipped, Skipped{Group: g.Name, Reason: reason(err)})
			continue
		}
		for _, r := range list.APIResources {
			// Create-only resources (TokenReview, SubjectAccessReview, ...) answer list with 405.
			if strings.Contains(r.Name, "/") || !slices.Contains(r.Verbs, "list") || !wanted[rules.GroupKind{Group: g.Name, Kind: r.Kind}] || found[r.Kind] {
				continue
			}
			found[r.Kind] = true
			if err := ctx.Err(); err != nil {
				return err
			}
			got, unverified, err := c.listKind(ctx, parsed.WithResource(r.Name), r.Kind)
			switch {
			case err == nil:
				obs.Objects = append(obs.Objects, got...)
				obs.Unverified += unverified
			case apierrors.IsForbidden(err):
				obs.Skipped = append(obs.Skipped, Skipped{Group: g.Name, Kind: r.Kind, Reason: err.Error()})
			default:
				return fmt.Errorf("list %s: %w", r.Name, withStatus(err))
			}
		}
	}
	return nil
}

// listKind lists one kind, restarting from scratch once if the server
// expires the continue token (410).
func (c *clusterSource) listKind(ctx context.Context, gvr schema.GroupVersionResource, kind string) ([]Object, int, error) {
	out, unverified, err := c.listPages(ctx, gvr, kind)
	if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		out, unverified, err = c.listPages(ctx, gvr, kind)
	}
	return out, unverified, err
}

func (c *clusterSource) listPages(ctx context.Context, gvr schema.GroupVersionResource, kind string) ([]Object, int, error) {
	var out []Object
	unverified := 0
	opts := metav1.ListOptions{Limit: listPageSize}
	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, 0, fmt.Errorf("gave up after %d pages", maxListPages)
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		l, err := c.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, 0, err
		}
		for i := range l.Items {
			ev := evidence(&l.Items[i], kind)
			if len(ev) == 0 {
				unverified++
			}
			out = append(out, ev...)
		}
		next := l.GetContinue()
		if next == "" {
			return out, unverified, nil
		}
		if next == opts.Continue {
			return nil, 0, errors.New("server returned the same continue token twice")
		}
		opts.Continue = next
	}
}

// withStatus appends the HTTP status and reason to an API error, which can
// otherwise render as an empty or opaque message.
func withStatus(err error) error {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		s := st.Status()
		return fmt.Errorf("%w (HTTP %d %s)", err, s.Code, s.Reason)
	}
	return err
}
