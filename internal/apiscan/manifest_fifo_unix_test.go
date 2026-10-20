//go:build unix

package apiscan

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestManifestFIFONeverOpened(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.yaml"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan Result, 1)
	go func() {
		res, _ := scanManifests(t, dir, t137)
		done <- res
	}()
	res := <-done // a blocking open would hang here until the test timeout
	if skipReasons(res) != "pipe.yaml: not a regular file" || res.FilesScanned != 0 {
		t.Fatalf("skipped=%s files=%d", skipReasons(res), res.FilesScanned)
	}
}
