//go:build windows

package safeio

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	win "golang.org/x/sys/windows"
)

func TestNewWindowsObjectAttributesUsesCaseInsensitiveLookup(t *testing.T) {
	const root = win.Handle(123)

	attrs, err := newWindowsObjectAttributes(root, "Source")
	if err != nil {
		t.Fatalf("newWindowsObjectAttributes returned error: %v", err)
	}

	if attrs.Length != uint32(unsafe.Sizeof(win.OBJECT_ATTRIBUTES{})) {
		t.Fatalf("object attributes length = %d, want %d", attrs.Length, unsafe.Sizeof(win.OBJECT_ATTRIBUTES{}))
	}
	if attrs.RootDirectory != root {
		t.Fatalf("root handle = %v, want %v", attrs.RootDirectory, root)
	}
	if attrs.Attributes&win.OBJ_CASE_INSENSITIVE == 0 {
		t.Fatalf("object attributes = %#x, want OBJ_CASE_INSENSITIVE", attrs.Attributes)
	}
	if attrs.ObjectName == nil {
		t.Fatal("object name is nil")
	}
	if attrs.ObjectName.Length != uint16(len("Source")*2) {
		t.Fatalf("object name length = %d, want %d", attrs.ObjectName.Length, len("Source")*2)
	}
}

func TestRenameNoReplaceWindowsOpensSourceCaseInsensitively(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "Source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	root := openTestRoot(t, rootDir)

	if err := RenameNoReplace(root, "source", "target"); err != nil {
		t.Fatalf("rename no-replace with case-mismatched source: %v", err)
	}

	assertRenameNoReplacePathAbsent(t, sourcePath)
	assertRenameNoReplaceSameFile(t, targetPath, sourceInfo)
}

func TestRenameNoReplaceWindowsCaseInsensitiveSourcePreservesOccupiedTarget(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "Source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.Mkdir(targetPath, 0o750); err != nil {
		t.Fatalf("create target: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	targetInfo := statTestPath(t, targetPath)
	root := openTestRoot(t, rootDir)

	err := RenameNoReplace(root, "source", "target")
	if !os.IsExist(err) {
		t.Fatalf("rename no-replace error = %v, want existing target", err)
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplaceSameFile(t, targetPath, targetInfo)
}
