// Package apiscan finds objects using deprecated or removed Kubernetes APIs.
package apiscan

import (
	"context"
	"sort"
	"strings"

	"github.com/HeinanCA/tideclear/internal/rules"
)

// Object is one thing that was written with an apiVersion.
type Object struct {
	APIVersion, Kind, Namespace, Name string
	// Origin says where the apiVersion evidence came from.
	Origin string
	// Resource is the plural resource name (a live scan only; manifests leave
	// it empty). It is stable across the versions a group serves, so it joins
	// an object to an Upgrade Insights usage path.
	Resource string
}

// Listed is a kind the scan read successfully, so "no object found" is proof.
type Listed struct {
	Group, Resource, Kind string
	// Unverified counts this kind's objects that had no apiVersion evidence.
	Unverified int
}

// Skipped is a group or kind left out of a scan (Kind empty = whole group).
type Skipped struct{ Group, Kind, Reason string }

// Observed is what a Source read: the objects with apiVersion evidence, the
// groups/kinds it could not read, and how many objects it read but could not
// check for lack of any apiVersion evidence.
type Observed struct {
	Objects    []Object
	Skipped    []Skipped
	Unverified int
	// Listed is the kinds a live scan read in full (manifest sources leave it empty).
	Listed []Listed
	// FilesScanned counts the manifest files a path scan read.
	FilesScanned int
}

// ClusterInfo identifies the cluster a scan ran against.
type ClusterInfo struct{ Context, Server, EKSCluster string }

// Source yields the objects to check. ServerVersion's bool reports whether a
// version is known; an unreachable server is an error.
type Source interface {
	Objects(ctx context.Context) (Observed, error)
	ServerVersion(ctx context.Context) (rules.Version, bool, error)
}

// ResultItem is an Object that hit a rule.
type ResultItem struct {
	Object  Object
	Finding rules.Finding
}

// Result is the outcome of a scan.
type Result struct {
	Target  rules.Version
	Cluster ClusterInfo
	// Path is the manifest root of a --path scan (empty for a live scan).
	Path         string
	Findings     []ResultItem
	Skipped      []Skipped
	Unverified   int
	Listed       []Listed
	FilesScanned int
	// Live is every object with apiVersion evidence, rule-matched or not. Rule 1
	// of the insights reconcile matches against it, so a live object at a version
	// no bundled rule covers is still seen.
	Live []Object
}

// Scan matches every object from src against set at target. Findings are
// sorted by kind, apiVersion, namespace, name; skipped by group, kind.
func Scan(ctx context.Context, src Source, set *rules.Set, target rules.Version) (Result, error) {
	obs, err := src.Objects(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Target: target, Skipped: obs.Skipped, Unverified: obs.Unverified, Listed: obs.Listed, FilesScanned: obs.FilesScanned, Live: obs.Objects}
	for _, o := range obs.Objects {
		if f, ok := set.Match(o.APIVersion, o.Kind, target); ok {
			res.Findings = append(res.Findings, ResultItem{Object: o, Finding: f})
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i].Object, res.Findings[j].Object
		return key(a.Kind, a.APIVersion, a.Namespace, a.Name) < key(b.Kind, b.APIVersion, b.Namespace, b.Name)
	})
	sort.SliceStable(res.Skipped, func(i, j int) bool {
		return key(res.Skipped[i].Group, res.Skipped[i].Kind) < key(res.Skipped[j].Group, res.Skipped[j].Kind)
	})
	return res, nil
}

func key(parts ...string) string { return strings.Join(parts, "\x00") }
