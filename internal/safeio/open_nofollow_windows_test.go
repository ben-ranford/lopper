//go:build windows

package safeio

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileNoFollowSupportsVerifiedOpenOnWindows(t *testing.T) {
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected windows no-follow support to be enabled")
	}

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	const want = "{\"module\":\"lodash/map\"}\n"
	if err := os.WriteFile(tracePath, []byte(want), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", tracePath, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close file: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if string(data) != want {
		t.Fatalf("unexpected trace data: got %q want %q", string(data), want)
	}
}
