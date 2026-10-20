package cli

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/klog/v2"
)

func TestKlogErrorsNeverReachStderr(t *testing.T) {
	if _, err := kubeRestConfig(writeKubeconfig(t), ""); err != nil { // runs the silencing path
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	klog.Error("leaked-error")
	klog.Warning("leaked-warning")
	klog.Flush()
	os.Stderr = orig
	_ = w.Close()
	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	_ = r.Close()
	if n != 0 {
		t.Fatalf("klog wrote to stderr: %q", buf[:n])
	}
}

func TestScanAllSkippedIsNotTheCleanLine(t *testing.T) {
	c := newCluster("1", "31")
	c.disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.NewServiceUnavailable("down")
	})
	code, out, se := run(t, kubeDeps(t, c), "scan")
	if code != 0 || len(lines(se)) != 2 {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
	if out == "no findings for target 1.32\n" || !strings.Contains(out, "2 skipped, results are partial") {
		t.Fatalf("stdout = %q, must say results are partial", out)
	}
}

func TestScanPartialFooterWithFindings(t *testing.T) {
	c := newCluster("1", "31")
	calls := 0
	c.disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 2 {
			return true, nil, errors.NewServiceUnavailable("down")
		}
		return false, nil, nil
	})
	_, out, _ := run(t, kubeDeps(t, c), "scan")
	if !strings.Contains(out, "FlowSchema") || !strings.Contains(out, "warning: 1 groups/kinds skipped (see stderr); results are partial") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestScanSkippedReasonIsOneLineInJSON(t *testing.T) {
	c := newCluster("1", "31")
	calls := 0
	c.disc.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 2 {
			return true, nil, errors.NewServiceUnavailable("line one\nline two")
		}
		return false, nil, nil
	})
	_, out, _ := run(t, kubeDeps(t, c), "scan", "--output", "json")
	var doc struct{ Skipped []struct{ Reason string } }
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Skipped) != 1 {
		t.Fatalf("err=%v out=%q", err, out)
	}
	if strings.ContainsAny(doc.Skipped[0].Reason, "\r\n") {
		t.Fatalf("reason = %q, want one line", doc.Skipped[0].Reason)
	}
}

func TestScanUnverifiedReported(t *testing.T) {
	c := newCluster("1", "30")
	bare := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": fsGV, "kind": "FlowSchema", "metadata": map[string]any{"name": "bare"}}}
	bare.SetManagedFields([]metav1.ManagedFieldsEntry{})
	if _, err := c.dyn.Resource(fsGVR).Create(t.Context(), bare, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, out, _ := run(t, kubeDeps(t, c), "scan")
	if !strings.Contains(out, "1 objects had no apiVersion evidence and were not checked") {
		t.Fatalf("stdout = %q", out)
	}
	_, out, _ = run(t, kubeDeps(t, c), "scan", "--output", "json")
	if !strings.Contains(out, `"unverified": 1`) {
		t.Fatalf("json = %q", out)
	}
}

func TestScanClusterIdentityInTextAndJSON(t *testing.T) {
	d := kubeDeps(t, newCluster("1", "30"))
	d.KubeIdentity = func(kubeconfig, kctx string) (string, string) { return "demo-ctx", "api.example.test" }
	_, out, _ := run(t, d, "scan", "--cluster", "demo-cluster", "--region", "r1")
	if !strings.HasPrefix(out, "cluster: demo-ctx (api.example.test) [EKS: demo-cluster]\n") {
		t.Fatalf("stdout = %q", out)
	}
	_, out, _ = run(t, d, "scan", "--output", "json")
	var doc struct {
		Cluster    struct{ Context, Server string }
		EKSCluster *string `json:"eksCluster"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Cluster.Context != "demo-ctx" || doc.Cluster.Server != "api.example.test" || doc.EKSCluster != nil {
		t.Fatalf("doc = %+v, want cluster identity and no eksCluster", doc)
	}
}

func TestScanTimeoutExitsTwo(t *testing.T) {
	old := scanTimeout
	scanTimeout = 20 * time.Millisecond
	t.Cleanup(func() { scanTimeout = old })
	c := newCluster("1", "31")
	c.dyn.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		time.Sleep(100 * time.Millisecond)
		return false, nil, nil
	})
	code, _, se := run(t, kubeDeps(t, c), "scan")
	if code != 2 || se != "tideclear: scan timed out after 20ms\n" {
		t.Fatalf("exit=%d stderr=%q", code, se)
	}
}

func TestScanTimeoutMessageUsesWholeMinutes(t *testing.T) {
	if got := durationText(10 * time.Minute); got != "10m" {
		t.Fatalf("got %q, want 10m", got)
	}
}

func TestKubeIdentityFromKubeconfig(t *testing.T) {
	name, server := kubeIdentity(writeKubeconfig(t), "")
	if name != "demo" || server != "127.0.0.1:1" {
		t.Fatalf("got (%q, %q), want (demo, 127.0.0.1:1)", name, server)
	}
	if name, _ := kubeIdentity(filepath.Join(t.TempDir(), "absent"), ""); name != "" {
		t.Fatalf("missing kubeconfig gave name %q, want empty", name)
	}
}

func TestInterruptedOrMapsContextErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	other := stderrors.New("boom")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"canceled", cancelled, "scan interrupted"},
		{"deadline", expired, "scan timed out after 10m"},
		{"live", context.Background(), "boom"},
	} {
		if got := interruptedOr(tc.ctx, other).Error(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestReleaseSignalAfterFirstInterrupt(t *testing.T) {
	sigCtx, cancel := context.WithCancel(context.Background())
	released := make(chan struct{})
	releaseSignalOnDone(sigCtx, func() { close(released) })
	select {
	case <-released:
		t.Fatal("stop called before any signal")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("stop not called after first interrupt")
	}
}
