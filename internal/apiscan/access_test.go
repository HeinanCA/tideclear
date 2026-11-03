package apiscan

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
)

// ssarRecorder answers SelfSubjectAccessReviews: allowed unless the
// "group/resource" key is in deny. It records every attribute set it saw.
type ssarRecorder struct {
	mu   sync.Mutex
	seen []map[string]any
}

func (r *ssarRecorder) reactor(deny map[string]bool) clienttesting.ReactionFunc {
	return func(a clienttesting.Action) (bool, runtime.Object, error) {
		obj := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		attrs, _, _ := unstructured.NestedMap(obj.Object, "spec", "resourceAttributes")
		r.mu.Lock()
		r.seen = append(r.seen, attrs)
		r.mu.Unlock()
		key := attrs["group"].(string) + "/" + attrs["resource"].(string)
		out := obj.DeepCopy()
		_ = unstructured.SetNestedField(out.Object, !deny[key], "status", "allowed")
		return true, out, nil
	}
}

func TestCheckAccess(t *testing.T) {
	multi := newDisc(
		resList(ingNew, map[string]string{"ingresses": "Ingress"}),
		resList("flowcontrol.apiserver.k8s.io/v1", map[string]string{"flowschemas": "FlowSchema"}),
		resList("flowcontrol.apiserver.k8s.io/v1beta3", map[string]string{"flowschemas": "FlowSchema"}),
		verbList("authentication.k8s.io/v1", "tokenreviews", "TokenReview", "create"),
	)
	tests := []struct {
		name     string
		deny     map[string]bool
		wantSeen []string // group/resource, in the order asked
		want     []Permission
	}{
		{
			name:     "all allowed",
			wantSeen: []string{"/nodes", "flowcontrol.apiserver.k8s.io/flowschemas", "networking.k8s.io/ingresses"},
		},
		{
			name:     "some denied, sorted, nodes (core group) asked first",
			deny:     map[string]bool{"networking.k8s.io/ingresses": true, "/nodes": true},
			wantSeen: []string{"/nodes", "flowcontrol.apiserver.k8s.io/flowschemas", "networking.k8s.io/ingresses"},
			want:     []Permission{{"", "nodes", "list"}, {"networking.k8s.io", "ingresses", "list"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dyn := newDyn(nil)
			rec := &ssarRecorder{}
			dyn.PrependReactor("create", "selfsubjectaccessreviews", rec.reactor(tc.deny))
			got, _, err := CheckAccess(context.Background(), multi, dyn, mustSet(t))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("missing = %+v, want %+v", got, tc.want)
			}
			var seen []string
			for _, a := range rec.seen {
				seen = append(seen, a["group"].(string)+"/"+a["resource"].(string))
				if a["verb"] != "list" {
					t.Errorf("verb = %v, want list", a["verb"])
				}
				if _, has := a["namespace"]; has {
					t.Errorf("namespace set in %v, want cluster-wide", a)
				}
			}
			if !reflect.DeepEqual(seen, tc.wantSeen) {
				t.Errorf("asked %v, want %v", seen, tc.wantSeen)
			}
		})
	}
}

func TestCheckAccessErrors(t *testing.T) {
	t.Run("SSAR create fails", func(t *testing.T) {
		dyn := newDyn(nil)
		dyn.PrependReactor("create", "selfsubjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
		_, _, err := CheckAccess(context.Background(), newDisc(resList(ingNew, map[string]string{"ingresses": "Ingress"})), dyn, mustSet(t))
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want a wrapped create error", err)
		}
	})
	t.Run("every version's discovery fails: nodes is still checked", func(t *testing.T) {
		disc := newDisc(
			resList(ingNew, map[string]string{"ingresses": "Ingress"}),
			resList("flowcontrol.apiserver.k8s.io/v1", map[string]string{"flowschemas": "FlowSchema"}))
		disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("aggregated api down")
		})
		dyn := newDyn(nil)
		rec := &ssarRecorder{}
		dyn.PrependReactor("create", "selfsubjectaccessreviews", rec.reactor(nil))
		got, gaps, err := CheckAccess(context.Background(), disc, dyn, mustSet(t))
		if err != nil || got != nil || len(rec.seen) != 1 {
			t.Fatalf("missing=%v err=%v asked=%v, want only nodes asked", got, err, rec.seen)
		}
		if len(gaps) != 3 || !strings.Contains(gaps[0], "aggregated api down") || !strings.Contains(strings.Join(gaps, " "), "flowcontrol.apiserver.k8s.io/v1") {
			t.Errorf("gaps = %v, want each failed version named with its reason", gaps)
		}
	})
	t.Run("group discovery fails", func(t *testing.T) {
		disc := newDisc(resList(ingNew, map[string]string{"ingresses": "Ingress"}))
		disc.PrependReactor("get", "group", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("disc down")
		})
		dyn := newDyn(nil)
		dyn.PrependReactor("create", "selfsubjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
			t.Error("SSAR asked although discovery failed")
			return true, nil, nil
		})
		if _, _, err := CheckAccess(context.Background(), disc, dyn, mustSet(t)); err == nil {
			t.Fatal("want discovery error")
		}
	})
}

// statusReactor answers every review with the given status (nil: no status key).
func statusReactor(status map[string]any) clienttesting.ReactionFunc {
	return func(a clienttesting.Action) (bool, runtime.Object, error) {
		out := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		if status != nil {
			out.Object["status"] = status
		}
		return true, out, nil
	}
}

func TestCheckAccessUnusableStatusIsError(t *testing.T) {
	tests := []struct {
		name   string
		status map[string]any
	}{
		{"no status", nil},
		{"status without allowed", map[string]any{"denied": false}},
		{"allowed is not a bool", map[string]any{"allowed": "yes"}},
		{"denied with evaluationError", map[string]any{"allowed": false, "evaluationError": "webhook down"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dyn := newDyn(nil)
			dyn.PrependReactor("create", "selfsubjectaccessreviews", statusReactor(tc.status))
			missing, _, err := CheckAccess(context.Background(), newDisc(), dyn, mustSet(t))
			if err == nil || len(missing) != 0 {
				t.Fatalf("missing=%v err=%v, want an error and no denial", missing, err)
			}
		})
	}
	t.Run("allowed with evaluationError is allowed", func(t *testing.T) {
		dyn := newDyn(nil)
		dyn.PrependReactor("create", "selfsubjectaccessreviews", statusReactor(map[string]any{"allowed": true, "evaluationError": "noise"}))
		missing, _, err := CheckAccess(context.Background(), newDisc(), dyn, mustSet(t))
		if err != nil || len(missing) != 0 {
			t.Fatalf("missing=%v err=%v", missing, err)
		}
	})
}

func TestCheckAccessKeepsDenialsWhenLaterReviewFails(t *testing.T) {
	disc := newDisc(
		resList(ingNew, map[string]string{"ingresses": "Ingress"}),
		resList("flowcontrol.apiserver.k8s.io/v1", map[string]string{"flowschemas": "FlowSchema"}))
	dyn := newDyn(nil)
	dyn.PrependReactor("create", "selfsubjectaccessreviews", func(a clienttesting.Action) (bool, runtime.Object, error) {
		out := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		attrs, _, _ := unstructured.NestedMap(out.Object, "spec", "resourceAttributes")
		switch attrs["resource"] {
		case "nodes":
			_ = unstructured.SetNestedField(out.Object, false, "status", "allowed")
		case "ingresses":
			return true, nil, errors.New("timeout")
		default:
			_ = unstructured.SetNestedField(out.Object, true, "status", "allowed")
		}
		return true, out, nil
	})
	missing, _, err := CheckAccess(context.Background(), disc, dyn, mustSet(t))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want the review error", err)
	}
	if !reflect.DeepEqual(missing, []Permission{{"", "nodes", "list"}}) {
		t.Errorf("missing = %+v, want the denial found before the failure", missing)
	}
}
