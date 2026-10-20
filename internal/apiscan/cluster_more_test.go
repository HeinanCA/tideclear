package apiscan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

const (
	vacNew = "storage.k8s.io/v1"
	vacOld = "storage.k8s.io/v1beta1"
)

var vacOldGVR = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1beta1", Resource: "volumeattributesclasses"}

func vacObject(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": vacOld, "kind": "VolumeAttributesClass", "metadata": map[string]any{"name": name}}}
	u.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "a", Operation: "Update", APIVersion: vacOld, Time: &metav1.Time{Time: *at(1)}}})
	return u
}

func TestKindOnlyInNonPreferredVersionIsListed(t *testing.T) {
	disc := newDisc(
		resList(vacNew, map[string]string{"frobs": "Frob"}),
		resList(vacOld, map[string]string{"volumeattributesclasses": "VolumeAttributesClass"}))
	dyn := newDyn(map[schema.GroupVersionResource]string{vacOldGVR: "VolumeAttributesClassList"}, vacObject("fast"))
	res := scan(t, disc, dyn, ver(1, 37))
	if len(res.Findings) != 1 || res.Findings[0].Object.Kind != "VolumeAttributesClass" || res.Findings[0].Object.APIVersion != vacOld {
		t.Fatalf("findings = %+v, want the v1beta1 VolumeAttributesClass", res.Findings)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

func TestKindServedByPreferredIsNotListedAgainViaOlderVersion(t *testing.T) {
	disc := newDisc(
		resList(vacNew, map[string]string{"volumeattributesclasses": "VolumeAttributesClass"}),
		resList(vacOld, map[string]string{"volumeattributesclasses": "VolumeAttributesClass"}))
	vacNewGVR := schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "volumeattributesclasses"}
	dyn := newDyn(map[schema.GroupVersionResource]string{vacNewGVR: "VolumeAttributesClassList", vacOldGVR: "VolumeAttributesClassList"})
	scan(t, disc, dyn, ver(1, 37))
	for _, a := range dyn.Actions() {
		if v := a.GetResource().Version; v != "v1" {
			t.Errorf("listed via %s, want preferred v1 only", v)
		}
	}
	if len(dyn.Actions()) != 1 {
		t.Fatalf("actions = %d, want 1", len(dyn.Actions()))
	}
}

func TestNonPreferredDiscoveryFailureIsSkipped(t *testing.T) {
	disc := newDisc(
		resList(vacNew, map[string]string{"frobs": "Frob"}),
		resList(vacOld, map[string]string{"volumeattributesclasses": "VolumeAttributesClass"}))
	calls := 0
	disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 2 {
			return true, nil, apierrors.NewServiceUnavailable("old version down")
		}
		return false, nil, nil
	})
	res := scan(t, disc, newDyn(nil), ver(1, 37))
	if len(res.Skipped) != 1 || res.Skipped[0].Group != "storage.k8s.io" ||
		!strings.Contains(res.Skipped[0].Reason, "v1beta1") || !strings.Contains(res.Skipped[0].Reason, "old version down") {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

func TestUnverifiedObjectsAreCounted(t *testing.T) {
	bad := ingress("badjson", "")
	bad.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{not json"})
	badButManaged := ingress("badjson2", "", mf{ingNew, "a", "", at(1)})
	badButManaged.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{not json"})
	disc, dyn := ingressCluster(ingress("none", ""), bad, badButManaged, ingress("ok", "", mf{ingNew, "a", "", at(1)}))
	res := scan(t, disc, dyn, ver(1, 31))
	if res.Unverified != 2 {
		t.Fatalf("Unverified = %d, want 2 (no evidence, unparseable last-applied)", res.Unverified)
	}
}

func TestEmptyAPIVersionManagedFieldsEntryIsIgnored(t *testing.T) {
	disc, dyn := ingressCluster(ingress("web", "", mf{ingOld, "a", "", at(1)}, mf{"", "b", "", at(9)}))
	res := scan(t, disc, dyn, ver(1, 31))
	if len(res.Findings) != 1 || res.Findings[0].Object.APIVersion != ingOld {
		t.Fatalf("findings = %+v, want the v1beta1 evidence", res.Findings)
	}
	if res.Unverified != 0 {
		t.Fatalf("Unverified = %d", res.Unverified)
	}
}

func pagedList(token string, items ...unstructured.Unstructured) *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{Items: items}
	l.SetContinue(token)
	return l
}

func TestRepeatedContinueTokenIsError(t *testing.T) {
	disc, dyn := ingressCluster()
	dyn.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, pagedList("same"), nil
	})
	set := mustSet(t)
	_, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, ver(1, 31))
	if err == nil || !strings.Contains(err.Error(), "continue token") {
		t.Fatalf("err = %v, want repeated continue token error", err)
	}
}

func TestPageCapIsError(t *testing.T) {
	disc, dyn := ingressCluster()
	n := 0
	dyn.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, runtime.Object, error) {
		n++
		return true, pagedList(fmt.Sprintf("t%d", n)), nil
	})
	set := mustSet(t)
	_, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, ver(1, 31))
	if err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("err = %v, want page cap error", err)
	}
	if n > maxListPages {
		t.Fatalf("listed %d pages, cap is %d", n, maxListPages)
	}
}

func TestExpiredContinueRestartsOnce(t *testing.T) {
	item := ingress("web", "", mf{ingOld, "a", "", at(1)})
	for _, tc := range []struct {
		name    string
		expired int
		wantErr bool
	}{{"one 410", 1, false}, {"two 410", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			disc, dyn := ingressCluster()
			calls := 0
			dyn.PrependReactor("list", "ingresses", func(a clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				switch {
				case calls == 1:
					return true, pagedList("p2", *item), nil
				case calls <= 1+tc.expired:
					return true, nil, apierrors.NewResourceExpired("continue token expired")
				default:
					return true, pagedList("", *item), nil
				}
			})
			set := mustSet(t)
			res, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, ver(1, 31))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && len(res.Findings) != 1 {
				t.Fatalf("findings = %+v, want exactly the restarted list's one (no duplicates from the aborted pass)", res.Findings)
			}
		})
	}
}

func TestGoneAlsoRestarts(t *testing.T) {
	disc, dyn := ingressCluster()
	calls := 0
	dyn.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusGone, Reason: metav1.StatusReasonGone}}
		}
		return true, pagedList(""), nil
	})
	scan(t, disc, dyn, ver(1, 31))
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestCancelledContextStopsScan(t *testing.T) {
	disc, dyn := ingressCluster(ingress("web", ""))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	set := mustSet(t)
	_, err := Scan(ctx, NewClusterSource(disc, dyn, set), set, ver(1, 31))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestDiscoverErrorCarriesStatus(t *testing.T) {
	disc, dyn := ingressCluster()
	disc.PrependReactor("get", "group", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusBadGateway, Reason: metav1.StatusReasonServiceUnavailable}}
	})
	set := mustSet(t)
	_, err := Scan(context.Background(), NewClusterSource(disc, dyn, set), set, ver(1, 31))
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "ServiceUnavailable") {
		t.Fatalf("err = %v, want status code and reason", err)
	}
}

func TestCancelledDuringDiscoveryFailureIsInterruptNotSkipped(t *testing.T) {
	disc := newDisc(
		resList("extensions/v1beta1", map[string]string{"ingresses": "Ingress"}),
		resList(ingNew, map[string]string{"ingresses": "Ingress"}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, nil, apierrors.NewServiceUnavailable("down")
	})
	obs, err := NewClusterSource(disc, newDyn(nil), mustSet(t)).Objects(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(obs.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", obs.Skipped)
	}
}
