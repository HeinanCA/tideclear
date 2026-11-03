package apiscan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/HeinanCA/tideclear/internal/rules"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

var ssarGVR = schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}

// Permission is one Kubernetes access the scan needs. An empty Group is the core group.
type Permission struct{ Group, Resource, Verb string }

// String renders "list deployments.apps"; the core group is a bare resource.
func (p Permission) String() string {
	name := p.Resource
	if p.Group != "" {
		name += "." + p.Group
	}
	return p.Verb + " " + name
}

// CheckAccess asks the API server, once per resource, whether the caller may
// list what the live scan reads (and the nodes the node check reads), and
// returns the denied ones sorted by group then resource. The scan lists
// cluster-wide, so the reviews carry no namespace. An error means the check
// could not finish, not that access is denied; missing then holds the denials
// found before it. gaps names the group versions whose discovery failed, so
// their resources were not checked.
func CheckAccess(ctx context.Context, disc discovery.DiscoveryInterface, dyn dynamic.Interface, set *rules.Set) (missing []Permission, gaps []string, err error) {
	needed, gaps, err := neededPermissions(ctx, disc, set)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range needed {
		if err := ctx.Err(); err != nil {
			return missing, gaps, err
		}
		allowed, err := selfCanList(ctx, dyn, p)
		if err != nil {
			return missing, gaps, fmt.Errorf("check %s: %w", p, withStatus(err))
		}
		if !allowed {
			missing = append(missing, p)
		}
	}
	return missing, gaps, nil
}

// neededPermissions lists the resources Objects would read, picked the way
// readGroup picks them (list-capable, no subresource, wanted kind), across
// every served version of each wanted group, plus core nodes. A version whose
// discovery fails is left out and named in gaps.
func neededPermissions(ctx context.Context, disc discovery.DiscoveryInterface, set *rules.Set) (out []Permission, gaps []string, err error) {
	groups, err := callCtx(ctx, disc.ServerGroups)
	if err != nil {
		return nil, nil, fmt.Errorf("discover API groups: %w", withStatus(err))
	}
	wanted, wantedGroup := wantedKinds(set)
	need := map[Permission]bool{{Resource: "nodes", Verb: "list"}: true}
	// Sorted like Objects, so gaps come out in a stable order.
	sort.Slice(groups.Groups, func(i, j int) bool { return groups.Groups[i].Name < groups.Groups[j].Name })
	for _, g := range groups.Groups {
		if !wantedGroup[g.Name] {
			continue
		}
		for _, v := range g.Versions {
			gv := v.GroupVersion
			list, err := callCtx(ctx, func() (*metav1.APIResourceList, error) { return disc.ServerResourcesForGroupVersion(gv) })
			if err != nil {
				if cerr := ctx.Err(); cerr != nil {
					return nil, nil, cerr
				}
				gaps = append(gaps, fmt.Sprintf("discover %s: %s", gv, withStatus(err)))
				continue
			}
			for _, r := range list.APIResources {
				if !listable(r, g.Name, wanted) {
					continue
				}
				need[Permission{Group: g.Name, Resource: r.Name, Verb: "list"}] = true
			}
		}
	}
	out = make([]Permission, 0, len(need))
	for p := range need {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Permission) int {
		return strings.Compare(a.Group+"/"+a.Resource, b.Group+"/"+b.Resource)
	})
	return out, gaps, nil
}

func selfCanList(ctx context.Context, dyn dynamic.Interface, p Permission) (bool, error) {
	review := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SelfSubjectAccessReview",
		"spec": map[string]any{"resourceAttributes": map[string]any{
			"verb": p.Verb, "group": p.Group, "resource": p.Resource,
		}},
	}}
	got, err := dyn.Resource(ssarGVR).Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	allowed, found, err := unstructured.NestedBool(got.Object, "status", "allowed")
	if err != nil || !found {
		return false, errors.New("access review returned no usable status")
	}
	// Denied with an evaluationError means the authorizer could not decide.
	if evalErr, _, _ := unstructured.NestedString(got.Object, "status", "evaluationError"); !allowed && evalErr != "" {
		return false, fmt.Errorf("access review could not be evaluated: %s", evalErr)
	}
	return allowed, nil
}
