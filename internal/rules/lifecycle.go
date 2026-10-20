package rules

import (
	"fmt"
	"regexp"
)

// Rule sources, recorded on Rule.Source for reporting.
const (
	SourcePluto        = "pluto"
	SourceK8sLifecycle = "k8s-lifecycle"
)

// LifecycleSourceVersion is the Kubernetes minor of the k8s.io/api the lifecycle data was generated from.
// Removals after it are projections, not facts; see Rule.Projected.
func LifecycleSourceVersion() (Version, error) { return lifecycleHeaderVersion(rawLifecycle) }

var headerRE = regexp.MustCompile(`(?m)^# Generated from k8s\.io/api \S+ \(Kubernetes (\d+\.\d+)\)`)

func lifecycleHeaderVersion(b []byte) (Version, error) {
	m := headerRE.FindSubmatch(b)
	if m == nil {
		return Version{}, fmt.Errorf("k8s-lifecycle.yaml: missing %q header", "# Generated from k8s.io/api <ver> (Kubernetes X.Y)")
	}
	return ParseVersion(string(m[1]))
}

// Projected reports whether the removal is after the k8s.io/api release the data came from.
// Such dates come from the deprecation policy's default timeline and can slip, so Match
// caps them at WARNING.
// shortcut: every post-release removal counts as projected, because k8s.io/api v0.37.1 sets no
// explicit removed= tag for any of them; read the types.go tags if a future release adds one.
func (r Rule) Projected() bool {
	if r.RemovedIn == nil {
		return false
	}
	// Load rejects data without the header, so the error cannot occur for rules it returned.
	src, err := LifecycleSourceVersion()
	return err == nil && src.before(*r.RemovedIn)
}

// merge returns one rule per apiVersion+kind. For component k8s a lifecycle rule replaces a
// Pluto rule with the same key; within one source the later rule wins. Order is first-seen.
func merge(pluto, lifecycle []Rule) []Rule {
	idx := map[string]int{}
	var out []Rule
	for _, r := range append(append([]Rule{}, pluto...), lifecycle...) {
		k := r.APIVersion + "|" + r.Kind
		if i, ok := idx[k]; ok {
			out[i] = r
			continue
		}
		idx[k] = len(out)
		out = append(out, r)
	}
	return out
}
