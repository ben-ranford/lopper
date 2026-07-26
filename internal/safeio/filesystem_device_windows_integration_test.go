//go:build windows

package safeio

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsOpenRootNoFollowTraversesNestedDirectoryWithRealHandles(t *testing.T) {
	rootDir := t.TempDir()
	nested := filepath.Join(rootDir, "nested", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested path: %v", err)
	}
	targetPath := filepath.Join(nested, "result.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	root, err := OpenRootNoFollow(nested)
	if err != nil {
		t.Fatalf("open nested no-follow root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close nested no-follow root: %v", closeErr)
		}
	}()

	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat nested no-follow root: %v", err)
	}
	if !os.SameFile(openedInfo, statTestPath(t, nested)) {
		t.Fatal("expected nested no-follow root to retain directory identity")
	}

	data, err := ReadFileWithinRoot(root, "result.txt")
	if err != nil {
		t.Fatalf("read nested file through real root handle: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected nested file contents: %q", data)
	}
}

func TestWindowsOpenRelativeWriteRootTraversesNestedDirectoryWithRealHandles(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "existing"), 0o755); err != nil {
		t.Fatalf("mkdir existing path: %v", err)
	}

	root, err := OpenRootNoFollow(rootDir)
	if err != nil {
		t.Fatalf("open parent root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close parent root: %v", closeErr)
		}
	}()

	child, err := OpenRelativeWriteRoot(root, filepath.Join("existing", "created"), true, 0o750)
	if err != nil {
		t.Fatalf("open relative write root: %v", err)
	}
	defer func() {
		if closeErr := child.Close(); closeErr != nil {
			t.Fatalf("close relative write root: %v", closeErr)
		}
	}()

	if err := child.WriteFileExclusiveCreatingParents(filepath.Join("nested", "result.txt"), []byte("bound\n"), 0o600, 0o750); err != nil {
		t.Fatalf("write nested file through relative write root: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rootDir, "existing", "created", "nested", "result.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "bound\n" {
		t.Fatalf("unexpected written file contents: %q", data)
	}
}

func TestWindowsOpenRelativeWriteRootRejectsIdentityErrorsFromRealHandles(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "existing"), 0o755); err != nil {
		t.Fatalf("mkdir existing path: %v", err)
	}

	root, err := OpenRootNoFollow(rootDir)
	if err != nil {
		t.Fatalf("open parent root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close parent root: %v", closeErr)
		}
	}()

	previous := windowsHandleFileIdentityFn
	windowsHandleFileIdentityFn = func(uintptr) (windowsHandleFileIdentity, error) {
		return windowsHandleFileIdentity{}, fs.ErrPermission
	}
	t.Cleanup(func() {
		windowsHandleFileIdentityFn = previous
	})

	child, err := OpenRelativeWriteRoot(root, filepath.Join("existing", "created"), true, 0o750)
	if child != nil {
		_ = child.Close()
		t.Fatal("expected identity lookup rejection to return no child root")
	}
	if err == nil || !strings.Contains(err.Error(), "device boundary") {
		t.Fatalf("expected identity lookup rejection, got %v", err)
	}
}
