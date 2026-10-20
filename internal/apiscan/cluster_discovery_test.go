package apiscan

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

func verbList(gv, name, kind string, verbs ...string) *metav1.APIResourceList {
	return &metav1.APIResourceList{GroupVersion: gv, APIResources: []metav1.APIResource{
		{Name: name, Kind: kind, Verbs: verbs},
	}}
}

func TestCreateOnlyResourceIsNeverListed(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "authentication.k8s.io", Version: "v1beta1", Resource: "tokenreviews"}
	disc := newDisc(verbList("authentication.k8s.io/v1beta1", "tokenreviews", "TokenReview", "create"))
	dyn := newDyn(map[schema.GroupVersionResource]string{gvr: "TokenReviewList"})
	dyn.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewMethodNotSupported(gvr.GroupResource(), "list")
	})
	res := scan(t, disc, dyn, ver(1, 37))
	if len(dyn.Actions()) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("actions=%v skipped=%v, want none", dyn.Actions(), res.Skipped)
	}
}

func TestCreateOnlyVersionFallsThroughToListableVersion(t *testing.T) {
	disc := newDisc(
		verbList(vacNew, "volumeattributesclasses", "VolumeAttributesClass", "create"),
		verbList(vacOld, "volumeattributesclasses", "VolumeAttributesClass", "get", "list"))
	vacNewGVR := schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "volumeattributesclasses"}
	dyn := newDyn(map[schema.GroupVersionResource]string{vacOldGVR: "VolumeAttributesClassList", vacNewGVR: "VolumeAttributesClassList"}, vacObject("fast"))
	dyn.PrependReactor("list", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetResource().Version == "v1" {
			return true, nil, apierrors.NewMethodNotSupported(vacNewGVR.GroupResource(), "list")
		}
		return false, nil, nil
	})
	res := scan(t, disc, dyn, ver(1, 37))
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want the v1beta1 VolumeAttributesClass", res.Findings)
	}
}

func TestDiscoveryCallsHonourCancelledContext(t *testing.T) {
	calls := map[string]func(c *clusterSource, ctx context.Context) error{
		"ServerVersion": func(c *clusterSource, ctx context.Context) error { _, _, err := c.ServerVersion(ctx); return err },
		"Objects":       func(c *clusterSource, ctx context.Context) error { _, err := c.Objects(ctx); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			disc := newDisc(resList(ingNew, map[string]string{"ingresses": "Ingress"}))
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			disc.PrependReactor("*", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
				<-release
				return true, nil, errors.New("released")
			})
			c := NewClusterSource(disc, newDyn(nil), mustSet(t)).(*clusterSource)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- call(c, ctx) }()
			time.AfterFunc(50*time.Millisecond, cancel)
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("call did not return after cancel")
			}
		})
	}
}

func TestRemovedGroupObjectFoundViaCurrentGroup(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	np := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": "deny", "namespace": "demo"},
	}}
	np.SetAnnotations(map[string]string{lastAppliedAnnotation: `{"apiVersion":"extensions/v1beta1","kind":"NetworkPolicy"}`})
	disc := newDisc(resList("networking.k8s.io/v1", map[string]string{"networkpolicies": "NetworkPolicy"}))
	dyn := newDyn(map[schema.GroupVersionResource]string{gvr: "NetworkPolicyList"}, np)
	res := scan(t, disc, dyn, ver(1, 37))
	if len(res.Findings) != 1 || res.Findings[0].Finding.Severity != "BLOCKER" || res.Findings[0].Object.APIVersion != "extensions/v1beta1" {
		t.Fatalf("findings = %+v, want one BLOCKER at extensions/v1beta1", res.Findings)
	}
}
