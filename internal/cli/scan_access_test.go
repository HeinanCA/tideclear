package cli

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	clienttesting "k8s.io/client-go/testing"
)

// preflightDiscoveryCalls is how many resource-list discovery calls the permission
// preflight makes against newCluster (one per group) before the scan reads any.
const preflightDiscoveryCalls = 3

// answerSSAR makes the fake API server answer SelfSubjectAccessReviews: allowed
// unless the reviewed resource is in deny. A reactor added later wins.
func answerSSAR(c cluster, deny ...string) {
	c.dyn.PrependReactor("create", "selfsubjectaccessreviews", func(a clienttesting.Action) (bool, runtime.Object, error) {
		obj := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		res, _, _ := unstructured.NestedString(obj.Object, "spec", "resourceAttributes", "resource")
		out := obj.DeepCopy()
		allowed := true
		for _, d := range deny {
			allowed = allowed && d != res
		}
		_ = unstructured.SetNestedField(out.Object, allowed, "status", "allowed")
		return true, out, nil
	})
}

func failSSAR(c cluster, err error) {
	c.dyn.PrependReactor("create", "selfsubjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

// denyThenFail makes the fake answer reviews: the resources in deny are denied,
// the resource failOn errors, the rest are allowed.
func denyThenFail(c cluster, failOn string, err error, deny ...string) {
	c.dyn.PrependReactor("create", "selfsubjectaccessreviews", func(a clienttesting.Action) (bool, runtime.Object, error) {
		obj := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		res, _, _ := unstructured.NestedString(obj.Object, "spec", "resourceAttributes", "resource")
		if res == failOn {
			return true, nil, err
		}
		out := obj.DeepCopy()
		_ = unstructured.SetNestedField(out.Object, !slices.Contains(deny, res), "status", "allowed")
		return true, out, nil
	})
}

var (
	skippedPermissionsLine = regexp.MustCompile(`check skipped: permissions: [^\n]*\n`)
	checksSkippedFooter    = regexp.MustCompile(`3 checks skipped`)
)

func TestScanPermissionPreflight(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(c cluster)
		wantStderr  []string // stderr lines, each matched as a prefix
		wantMissing []string // resources reported denied, in order
		wantSkipped bool     // JSON checksSkipped carries a "permissions" entry
	}{
		{name: "all allowed", setup: func(cluster) {}},
		{
			name:        "some denied",
			setup:       func(c cluster) { answerSSAR(c, "ingresses", "nodes") },
			wantStderr:  []string{"tideclear: warning: missing permissions: list nodes, list ingresses.networking.k8s.io"},
			wantMissing: []string{"nodes", "ingresses"},
		},
		{
			name:        "review cannot be created",
			setup:       func(c cluster) { failSSAR(c, errors.New("reviews\nare off")) },
			wantStderr:  []string{"tideclear: warning: permission check skipped: check list nodes: reviews; are off"},
			wantSkipped: true,
		},
		{
			name: "forbidden to create reviews",
			setup: func(c cluster) {
				failSSAR(c, apierrors.NewForbidden(schema.GroupResource{Resource: "selfsubjectaccessreviews"}, "", errors.New("no")))
			},
			wantStderr:  []string{"tideclear: warning: permission check skipped: "},
			wantSkipped: true,
		},
		{
			name: "no usable status is skipped, not denied",
			setup: func(c cluster) {
				c.dyn.PrependReactor("create", "selfsubjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, &unstructured.Unstructured{Object: map[string]any{}}, nil
				})
			},
			wantStderr:  []string{"tideclear: warning: permission check skipped: check list nodes: access review returned no usable status"},
			wantSkipped: true,
		},
		{
			name:  "later review fails: known gaps kept, check incomplete",
			setup: func(c cluster) { denyThenFail(c, "ingresses", errors.New("flaky\nreview"), "nodes") },
			wantStderr: []string{
				"tideclear: warning: missing permissions: list nodes",
				"tideclear: warning: permission check incomplete: check list ingresses.networking.k8s.io: flaky; review",
			},
			wantMissing: []string{"nodes"},
			wantSkipped: true,
		},
		{
			name: "one group version cannot be discovered: check incomplete",
			setup: func(c cluster) {
				calls := 0
				c.disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
					calls++
					if calls == 2 { // the preflight reads core, then flowcontrol
						return true, nil, errors.New("aggregated\napi down")
					}
					return false, nil, nil
				})
			},
			wantStderr:  []string{"tideclear: warning: permission check incomplete: discover flowcontrol.apiserver.k8s.io/v1: aggregated; api down"},
			wantSkipped: true,
		},
	}
	base := func() string {
		_, out, _ := run(t, kubeDeps(t, newCluster("1", "30")), "scan")
		return out
	}()
	mk := func(setup func(cluster)) cluster { c := newCluster("1", "30"); setup(c); return c }
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, se := run(t, kubeDeps(t, mk(tc.setup)), "scan")
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, se)
			}
			got := lines(se)
			if se == "" {
				got = nil
			}
			if len(got) != len(tc.wantStderr) {
				t.Fatalf("stderr = %q, want %d lines", se, len(tc.wantStderr))
			}
			for i, w := range tc.wantStderr {
				if !strings.HasPrefix(got[i], w) {
					t.Errorf("stderr line %d = %q, want prefix %q", i, got[i], w)
				}
			}
			full := out
			const end = `verbs: ["list"]` + "\n"
			if i := strings.Index(out, "\nmissing permissions"); i >= 0 {
				out = out[:i] + out[strings.LastIndex(out, end)+len(end):] // the rest of the report is unchanged
			}
			if tc.wantSkipped {
				if !skippedPermissionsLine.MatchString(out) {
					t.Errorf("text report lacks the skipped permissions line:\n%s", out)
				}
				out = checksSkippedFooter.ReplaceAllString(skippedPermissionsLine.ReplaceAllString(out, ""), "2 checks skipped")
			}
			if out != base {
				t.Errorf("scan output changed:\n%s\nbase:\n%s", out, base)
			}
			if got := strings.Contains(full, "suggested ClusterRole:"); got != (len(tc.wantMissing) > 0) {
				t.Errorf("ClusterRole section present=%v, want %v:\n%s", got, len(tc.wantMissing) > 0, full)
			}
			_, jout, _ := run(t, kubeDeps(t, mk(tc.setup)), "scan", "--output", "json")
			var doc struct {
				Missing []map[string]string `json:"missingPermissions"`
				Skipped []map[string]string `json:"checksSkipped"`
			}
			if err := json.Unmarshal([]byte(jout), &doc); err != nil {
				t.Fatal(err)
			}
			var res []string
			for _, m := range doc.Missing {
				res = append(res, m["resource"])
			}
			if !slices.Equal(res, tc.wantMissing) {
				t.Errorf("missingPermissions = %v, want %v", res, tc.wantMissing)
			}
			hasPerm := slices.ContainsFunc(doc.Skipped, func(m map[string]string) bool { return m["check"] == "permissions" && m["reason"] != "" })
			if hasPerm != tc.wantSkipped {
				t.Errorf("checksSkipped = %v, want a permissions entry: %v", doc.Skipped, tc.wantSkipped)
			}
		})
	}
}

func TestScanPermissionPreflightSkippedDoesNotChangeStrictExit(t *testing.T) {
	c := newCluster("1", "30")
	failSSAR(c, errors.New("reviews off"))
	code, _, _ := run(t, kubeDeps(t, c), "scan", "--strict")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: a skipped check never makes --strict exit 1", code)
	}
}

func TestScanPermissionPreflightDeniedDoesNotChangeExit(t *testing.T) {
	c := newCluster("1", "30") // FlowSchema at v1beta3 is only a warning for target 1.31
	answerSSAR(c, "flowschemas")
	code, _, _ := run(t, kubeDeps(t, c), "scan", "--strict")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: a missing permission is not a --strict blocker", code)
	}
}

func TestScanPermissionPreflightUnauthorizedExitsTwo(t *testing.T) {
	c := newCluster("1", "30")
	failSSAR(c, apierrors.NewUnauthorized("token expired"))
	code, out, se := run(t, kubeDeps(t, c), "scan")
	if code != 2 || out != "" || !strings.Contains(se, "token expired") {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, se)
	}
	assertOneLine(t, se)
}

// hangDisc blocks the first ServerGroups call (the preflight's) until the test
// ends; later calls pass through. It wraps the fake because a blocked reactor
// would hold the fake's lock and stall every other call.
type hangDisc struct {
	discovery.DiscoveryInterface
	release chan struct{}
	calls   atomic.Int32
}

func (h *hangDisc) ServerGroups() (*metav1.APIGroupList, error) {
	if h.calls.Add(1) == 1 {
		<-h.release
	}
	return h.DiscoveryInterface.ServerGroups()
}

func hangFirstDiscovery(t *testing.T, c cluster) Deps {
	t.Helper()
	h := &hangDisc{DiscoveryInterface: c.disc, release: make(chan struct{})}
	t.Cleanup(func() { close(h.release) })
	d := kubeDeps(t, c)
	d.Kube = func(string, string) (discovery.DiscoveryInterface, dynamic.Interface, error) { return h, c.dyn, nil }
	return d
}

func TestScanPermissionPreflightOwnTimeoutDegrades(t *testing.T) {
	old := extrasTimeout
	extrasTimeout = 30 * time.Millisecond
	t.Cleanup(func() { extrasTimeout = old })
	c := newCluster("1", "30")
	code, _, se := run(t, hangFirstDiscovery(t, c), "scan")
	if code != 0 || !strings.HasPrefix(se, "tideclear: warning: permission check skipped: ") || len(lines(se)) != 1 {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}

func TestScanPermissionPreflightScanTimeoutIsFatal(t *testing.T) {
	old := scanTimeout
	scanTimeout = 30 * time.Millisecond
	t.Cleanup(func() { scanTimeout = old })
	c := newCluster("1", "30")
	code, _, se := run(t, hangFirstDiscovery(t, c), "scan")
	if code != 2 || !strings.Contains(se, "scan timed out") {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}

func TestScanPathNeverRunsPermissionPreflight(t *testing.T) {
	d, _ := pathDeps(t) // fails the test if Deps.Kube is called
	code, out, se := run(t, d, "scan", "--path", manifests("good"), "--target", "1.37")
	if code != 0 || se != "" || strings.Contains(out, "missing permissions") {
		t.Fatalf("exit=%d stderr=%q out=%q", code, se, out)
	}
}
