//go:build darwin || windows

package safeio

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenFileNoFollowSupportsAtomicOpen(t *testing.T) {
	if !OpenFileNoFollowSupported() {
		t.Fatalf("expected %s no-follow support to be enabled", runtime.GOOS)
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

func TestOpenFileNoFollowKeepsParentPinnedAcrossRename(t *testing.T) {
	rootDir := t.TempDir()
	parentPath := filepath.Join(rootDir, "trace-parent")
	replacementParentPath := filepath.Join(rootDir, "replacement-parent")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatalf("mkdir trace parent: %v", err)
	}
	if err := os.Mkdir(replacementParentPath, 0o700); err != nil {
		t.Fatalf("mkdir replacement parent: %v", err)
	}

	const want = "pinned parent\n"
	tracePath := filepath.Join(parentPath, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte(want), 0o600); err != nil {
		t.Fatalf("write pinned trace: %v", err)
	}
	replacementTracePath := filepath.Join(replacementParentPath, filepath.Base(tracePath))
	if err := os.WriteFile(replacementTracePath, []byte("replacement parent\n"), 0o600); err != nil {
		t.Fatalf("write replacement trace: %v", err)
	}

	withOpenFileNoFollowByVerificationBeforeOpen(t, func() {
		displacedParentPath := filepath.Join(rootDir, "displaced-parent")
		if err := os.Rename(parentPath, displacedParentPath); err != nil {
			t.Fatalf("rename pinned parent: %v", err)
		}
		if err := os.Rename(replacementParentPath, parentPath); err != nil {
			t.Fatalf("replace parent path: %v", err)
		}
	})

	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", tracePath, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close pinned trace: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read pinned trace: %v", err)
	}
	if string(data) != want {
		t.Fatalf("opened replacement parent content: got %q want %q", data, want)
	}
}
