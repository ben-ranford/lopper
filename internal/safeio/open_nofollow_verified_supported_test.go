//go:build darwin || windows

package safeio

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestOpenFileNoFollowRejectsParentReplacementWithSymlink(t *testing.T) {
	assertOpenFileNoFollowRejectsParentReplacementWithSymlink(t, false)
}

func TestOpenFileNoFollowRejectsParentReplacementWithRelativeSymlink(t *testing.T) {
	assertOpenFileNoFollowRejectsParentReplacementWithSymlink(t, true)
}

func assertOpenFileNoFollowRejectsParentReplacementWithSymlink(t *testing.T, relativeTarget bool) {
	t.Helper()

	rootDir := t.TempDir()
	parentPath := filepath.Join(rootDir, "trace-parent")
	replacementPath := filepath.Join(rootDir, "replacement-parent")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatalf("mkdir trace parent: %v", err)
	}
	if err := os.Mkdir(replacementPath, 0o700); err != nil {
		t.Fatalf("mkdir replacement parent: %v", err)
	}

	tracePath := filepath.Join(parentPath, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("pinned parent\n"), 0o600); err != nil {
		t.Fatalf("write pinned trace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replacementPath, filepath.Base(tracePath)), []byte("replacement parent\n"), 0o600); err != nil {
		t.Fatalf("write replacement trace: %v", err)
	}

	withOpenParentRootNoFollowBeforeChildOpen(t, parentPath, func() {
		displacedPath := parentPath + ".original"
		if err := os.Rename(parentPath, displacedPath); err != nil {
			t.Fatalf("rename pinned parent: %v", err)
		}
		symlinkTarget := replacementPath
		if relativeTarget {
			var err error
			symlinkTarget, err = filepath.Rel(filepath.Dir(parentPath), replacementPath)
			if err != nil {
				t.Fatalf("relative replacement path: %v", err)
			}
		}
		if err := os.Symlink(symlinkTarget, parentPath); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
	})

	file, err := OpenFileNoFollow(tracePath)
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected parent replacement by symlink to be rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "path contains symlink") {
		t.Fatalf("expected parent replacement symlink rejection, got %v", err)
	}
}
