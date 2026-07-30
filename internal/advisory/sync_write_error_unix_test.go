//go:build !windows

package advisory

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

const downloadSnapshotWriteErrorChildEnv = "LOPPER_DOWNLOAD_SNAPSHOT_WRITE_ERROR_CHILD"

func TestDownloadSnapshotWriteError(t *testing.T) {
	if os.Getenv(downloadSnapshotWriteErrorChildEnv) == "1" {
		runDownloadSnapshotWriteErrorChild(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDownloadSnapshotWriteError$")
	cmd.Env = append(os.Environ(), downloadSnapshotWriteErrorChildEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run child test process: %v\n%s", err, output)
	}
}

func runDownloadSnapshotWriteErrorChild(t *testing.T) {
	var oldLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &oldLimit); err != nil {
		t.Fatalf("get RLIMIT_FSIZE: %v", err)
	}

	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &oldLimit); err != nil {
			t.Fatalf("restore RLIMIT_FSIZE: %v", err)
		}
	}()
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1, Max: oldLimit.Max}); err != nil {
		t.Fatalf("set RLIMIT_FSIZE: %v", err)
	}

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"vulns":[{"id":"OSV-1"}]}`)),
		}, nil
	})}
	if _, err := downloadSnapshot(context.Background(), "https://example.test/osv.json", client, t.TempDir()); err == nil || !strings.Contains(err.Error(), "write advisory snapshot temp file") {
		t.Fatalf("expected snapshot write error, got %v", err)
	}

	closeErrClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloseReadCloser{Reader: strings.NewReader(`{"vulns":[{"id":"OSV-2"}]}`)},
		}, nil
	})}
	if _, err := downloadSnapshot(context.Background(), "https://example.test/osv.json", closeErrClient, t.TempDir()); err == nil || !strings.Contains(err.Error(), "write advisory snapshot temp file") || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected snapshot write+close error, got %v", err)
	}
}
