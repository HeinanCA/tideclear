// Command gen writes data/k8s-lifecycle.yaml from the prerelease-lifecycle methods
// that k8s.io/api generates on every type. Run via `go generate ./internal/rules`.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const apiModule = "k8s.io/api"

type entry struct {
	Version, Kind, DeprecatedIn, RemovedIn, Replacement, ReplacementIn string
}

type introduced interface{ APILifecycleIntroduced() (int, int) }
type deprecated interface{ APILifecycleDeprecated() (int, int) }
type removed interface{ APILifecycleRemoved() (int, int) }
type replaced interface {
	APILifecycleReplacement() schema.GroupVersionKind
}

func fmtVer(major, minor int) string { return fmt.Sprintf("v%d.%d.0", major, minor) }

func apiVersion(gvk schema.GroupVersionKind) string { return gvk.GroupVersion().String() }

// entries returns one entry per beta kind with a scheduled removal; alpha kinds and *List kinds are skipped.
func entries(s *runtime.Scheme) []entry {
	var out []entry
	for gvk := range s.AllKnownTypes() {
		if !strings.Contains(gvk.Version, "beta") || strings.HasSuffix(gvk.Kind, "List") {
			continue
		}
		obj, err := s.New(gvk)
		if err != nil {
			continue
		}
		rm, ok := obj.(removed)
		if !ok {
			continue
		}
		e := entry{Version: apiVersion(gvk), Kind: gvk.Kind}
		e.RemovedIn = fmtVer(rm.APILifecycleRemoved())
		if d, ok := obj.(deprecated); ok {
			e.DeprecatedIn = fmtVer(d.APILifecycleDeprecated())
		}
		e.Replacement, e.ReplacementIn = replacement(s, obj, gvk)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// replacement returns the replacement apiVersion and the minor it became available in.
// When the type names none it falls back to the same kind at a stable version (no alpha/beta)
// registered in s; never another beta, which is itself scheduled for removal.
func replacement(s *runtime.Scheme, obj runtime.Object, gvk schema.GroupVersionKind) (api, availableIn string) {
	var repl schema.GroupVersionKind
	if r, ok := obj.(replaced); ok {
		repl = r.APILifecycleReplacement()
	}
	if repl.Kind == "" {
		for known := range s.AllKnownTypes() {
			stable := !strings.Contains(known.Version, "alpha") && !strings.Contains(known.Version, "beta")
			// Highest stable version wins so map order cannot change the output.
			if known.Group == gvk.Group && known.Kind == gvk.Kind && stable && known.Version > repl.Version {
				repl = known
			}
		}
	}
	if repl.Kind == "" {
		return "", ""
	}
	if ro, err := s.New(repl); err == nil {
		if in, ok := ro.(introduced); ok {
			availableIn = fmtVer(in.APILifecycleIntroduced())
		}
	}
	return apiVersion(repl), availableIn
}

// render emits the Pluto-shaped yaml. apiVer is the module version, e.g. "v0.37.1".
func render(es []entry, apiVer string) []byte {
	var b bytes.Buffer
	minor := strings.Split(strings.TrimPrefix(apiVer, "v"), ".")[1]
	fmt.Fprintf(&b, "# Generated from %s %s (Kubernetes 1.%s); DO NOT EDIT.\n", apiModule, apiVer, minor)
	b.WriteString("# Regenerate: go generate ./internal/rules\ndeprecated-versions:\n")
	for _, e := range es {
		fmt.Fprintf(&b, "  - version: %s\n    kind: %s\n", e.Version, e.Kind)
		fmt.Fprintf(&b, "    deprecated-in: %s\n    removed-in: %s\n", e.DeprecatedIn, e.RemovedIn)
		fmt.Fprintf(&b, "    replacement-api: %s\n", strconv.Quote(e.Replacement))
		if e.ReplacementIn != "" {
			fmt.Fprintf(&b, "    replacement-available-in: %s\n", e.ReplacementIn)
		}
		b.WriteString("    component: k8s\n")
	}
	return b.Bytes()
}

func moduleVersion() (string, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("no build info")
	}
	for _, d := range bi.Deps {
		if d.Path == apiModule {
			return d.Version, nil
		}
	}
	return "", fmt.Errorf("%s not in build deps", apiModule)
}

func run(outPath string) error {
	s := runtime.NewScheme()
	for _, add := range addToSchemes {
		if err := add(s); err != nil {
			return fmt.Errorf("register scheme: %w", err)
		}
	}
	ver, err := moduleVersion()
	if err != nil {
		return err
	}
	es := entries(s)
	if len(es) == 0 {
		return errors.New("no lifecycle entries found")
	}
	return os.WriteFile(outPath, render(es, ver), 0o644)
}

func main() {
	out := flag.String("o", "data/k8s-lifecycle.yaml", "output path")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
