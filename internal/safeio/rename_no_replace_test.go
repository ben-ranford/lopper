package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type renameNoReplaceUnsupportedRoot struct {
	Root
}

func TestRenameNoReplaceMovesDirectoryWhenTargetMissing(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	root := openTestRoot(t, rootDir)

	if err := RenameNoReplace(root, "source", "target"); err != nil {
		t.Fatalf("rename no-replace: %v", err)
	}

	assertRenameNoReplacePathAbsent(t, sourcePath)
	assertRenameNoReplaceSameFile(t, targetPath, sourceInfo)
}

func TestRenameNoReplacePreservesOccupiedDirectory(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
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
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("rename no-replace error = %v, want existing target", err)
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplaceSameFile(t, targetPath, targetInfo)
}

func TestRenameNoReplaceMovesBetweenNestedDirectories(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir := t.TempDir()
	sourceParent := filepath.Join(rootDir, "from")
	targetParent := filepath.Join(rootDir, "to")
	sourcePath := filepath.Join(sourceParent, "source")
	targetPath := filepath.Join(targetParent, "target")
	if err := os.MkdirAll(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.MkdirAll(targetParent, 0o750); err != nil {
		t.Fatalf("create target parent: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	root := openTestRoot(t, rootDir)

	if err := RenameNoReplace(root, filepath.Join("from", "source"), filepath.Join("to", "target")); err != nil {
		t.Fatalf("rename no-replace between nested dirs: %v", err)
	}

	assertRenameNoReplacePathAbsent(t, sourcePath)
	assertRenameNoReplaceSameFile(t, targetPath, sourceInfo)
}

func TestRenameNoReplaceRejectsSymlinkSourceParent(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideSourcePath := filepath.Join(outsideDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Mkdir(outsideSourcePath, 0o750); err != nil {
		t.Fatalf("create outside source: %v", err)
	}
	outsideSourceInfo := statTestPath(t, outsideSourcePath)
	makeRenameNoReplaceSymlink(t, outsideDir, filepath.Join(rootDir, "link"))
	root := openTestRoot(t, rootDir)

	err := RenameNoReplace(root, filepath.Join("link", "source"), "target")
	if err == nil {
		t.Fatal("rename no-replace through symlink source parent succeeded")
	}

	assertRenameNoReplaceSameFile(t, outsideSourcePath, outsideSourceInfo)
	assertRenameNoReplacePathAbsent(t, targetPath)
}

func TestRenameNoReplaceRejectsSymlinkTargetParent(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	outsideTargetPath := filepath.Join(outsideDir, "target")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	makeRenameNoReplaceSymlink(t, outsideDir, filepath.Join(rootDir, "link"))
	root := openTestRoot(t, rootDir)

	err := RenameNoReplace(root, "source", filepath.Join("link", "target"))
	if err == nil {
		t.Fatal("rename no-replace through symlink target parent succeeded")
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplacePathAbsent(t, outsideTargetPath)
}

func TestRenameNoReplaceRejectsEscapingSource(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	err := RenameNoReplace(root, filepath.Join("..", "source"), "target")
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("rename no-replace escaping source error = %v, want path escape", err)
	}
}

func TestRenameNoReplaceRejectsEscapingTarget(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	err := RenameNoReplace(root, "source", filepath.Join("..", "target"))
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("rename no-replace escaping target error = %v, want path escape", err)
	}
}

func TestRenameNoReplaceFailsClosedWithoutSupport(t *testing.T) {
	root := renameNoReplaceUnsupportedRoot{Root: openTestRoot(t, t.TempDir())}
	err := RenameNoReplace(root, "source", "target")
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("rename no-replace unsupported root error = %v, want invalid", err)
	}
}

func TestRenameNoReplaceDispatchesDedicatedNoReplaceMethod(t *testing.T) {
	expectedErr := fs.ErrExist
	renameCalled := false
	root := &fakeRoot{
		Root: openTestRoot(t, t.TempDir()),
		rename: func(_, _ string) error {
			renameCalled = true
			return nil
		},
		renameNoReplace: func(oldName, newName string) error {
			if oldName != "source" || newName != "target" {
				t.Fatalf("RenameNoReplace args = %q, %q; want source, target", oldName, newName)
			}
			return expectedErr
		},
	}

	err := RenameNoReplace(root, "source", "target")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("rename no-replace error = %v, want %v", err, expectedErr)
	}
	if renameCalled {
		t.Fatal("RenameNoReplace delegated to replace-capable Rename")
	}
}

func setupRenameNoReplaceIntoFixture(t *testing.T) (rootDir, destDir, sourcePath string, sourceInfo os.FileInfo, root, destRoot Root) {
	t.Helper()
	rootDir = t.TempDir()
	destDir = filepath.Join(rootDir, "dest")
	sourcePath = filepath.Join(rootDir, "source")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.Mkdir(destDir, 0o750); err != nil {
		t.Fatalf("create dest: %v", err)
	}
	sourceInfo = statTestPath(t, sourcePath)
	root = openTestRoot(t, rootDir)
	destRoot = openTestRoot(t, destDir)
	return rootDir, destDir, sourcePath, sourceInfo, root, destRoot
}

func TestRenameNoReplaceIntoMovesDirectoryIntoPinnedRoot(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	_, destDir, sourcePath, sourceInfo, root, destRoot := setupRenameNoReplaceIntoFixture(t)

	if err := RenameNoReplaceInto(root, "source", destRoot, "target"); err != nil {
		t.Fatalf("rename no-replace into: %v", err)
	}

	assertRenameNoReplacePathAbsent(t, sourcePath)
	assertRenameNoReplaceSameFile(t, filepath.Join(destDir, "target"), sourceInfo)
}

func TestRenameNoReplaceIntoPreservesOccupiedDestination(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	_, destDir, sourcePath, sourceInfo, root, destRoot := setupRenameNoReplaceIntoFixture(t)
	targetPath := filepath.Join(destDir, "target")
	if err := os.Mkdir(targetPath, 0o750); err != nil {
		t.Fatalf("create occupied target: %v", err)
	}
	targetInfo := statTestPath(t, targetPath)

	err := RenameNoReplaceInto(root, "source", destRoot, "target")
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("rename no-replace into error = %v, want existing target", err)
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplaceSameFile(t, targetPath, targetInfo)
}

func TestRenameNoReplaceIntoRejectsAbsoluteDestinationEscapingPinnedRoot(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	_, destDir, sourcePath, sourceInfo, root, destRoot := setupRenameNoReplaceIntoFixture(t)
	outsideDir := t.TempDir()
	escapeTarget := filepath.Join(outsideDir, "escaped")

	err := RenameNoReplaceInto(root, "source", destRoot, escapeTarget)
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("rename no-replace into absolute destination error = %v, want path escape", err)
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplacePathAbsent(t, escapeTarget)
	assertRenameNoReplacePathAbsent(t, filepath.Join(destDir, "escaped"))
}

func TestRenameNoReplaceIntoRejectsDestinationEscapingWithDotDot(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir, _, sourcePath, sourceInfo, root, destRoot := setupRenameNoReplaceIntoFixture(t)

	err := RenameNoReplaceInto(root, "source", destRoot, filepath.Join("..", "escaped"))
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("rename no-replace into dot-dot destination error = %v, want path escape", err)
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplacePathAbsent(t, filepath.Join(rootDir, "escaped"))
}

func TestRenameNoReplaceIntoRejectsSymlinkDestinationParent(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	_, destDir, sourcePath, sourceInfo, root, destRoot := setupRenameNoReplaceIntoFixture(t)
	outsideDir := t.TempDir()
	makeRenameNoReplaceSymlink(t, outsideDir, filepath.Join(destDir, "link"))
	outsideTargetPath := filepath.Join(outsideDir, "target")

	err := RenameNoReplaceInto(root, "source", destRoot, filepath.Join("link", "target"))
	if err == nil {
		t.Fatal("rename no-replace into through symlink destination parent succeeded")
	}

	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplacePathAbsent(t, outsideTargetPath)
}

func TestRenameNoReplaceIntoRejectsNonOsRootDestination(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "source"), 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	root := openTestRoot(t, rootDir)
	destRoot := &fakeRoot{Root: openTestRoot(t, t.TempDir())}

	err := RenameNoReplaceInto(root, "source", destRoot, "target")
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("rename no-replace into non-osRoot destination = %v, want invalid", err)
	}
}

func TestRenameNoReplaceIntoFailsClosedWithoutSupport(t *testing.T) {
	root := renameNoReplaceUnsupportedRoot{Root: openTestRoot(t, t.TempDir())}
	destRoot := openTestRoot(t, t.TempDir())
	err := RenameNoReplaceInto(root, "source", destRoot, "target")
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("rename no-replace into unsupported root error = %v, want invalid", err)
	}
}

func TestRenameNoReplaceIntoDispatchesDedicatedMethod(t *testing.T) {
	expectedErr := fs.ErrExist
	root := &fakeRoot{
		Root: openTestRoot(t, t.TempDir()),
		renameNoReplaceInto: func(oldName string, newRoot Root, newName string) error {
			if oldName != "source" || newName != "target" {
				t.Fatalf("RenameNoReplaceInto args = %q, %q; want source, target", oldName, newName)
			}
			return expectedErr
		},
	}
	destRoot := openTestRoot(t, t.TempDir())

	err := RenameNoReplaceInto(root, "source", destRoot, "target")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("rename no-replace into error = %v, want %v", err, expectedErr)
	}
}

func TestRenameNoReplaceRejectsNULNames(t *testing.T) {
	skipRenameNoReplaceUnsupportedPlatform(t)
	root := openTestRoot(t, t.TempDir())
	for _, tc := range []struct {
		name    string
		oldName string
		newName string
	}{
		{name: "source", oldName: "source\x00bad", newName: "target"},
		{name: "target", oldName: "source", newName: "target\x00bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RenameNoReplace(root, tc.oldName, tc.newName)
			if err == nil || !strings.Contains(err.Error(), "invalid argument") {
				t.Fatalf("rename no-replace NUL name error = %v, want invalid argument", err)
			}
		})
	}
}

func makeRenameNoReplaceSymlink(t *testing.T, oldName, newName string) {
	t.Helper()
	if err := os.Symlink(oldName, newName); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func skipRenameNoReplaceUnsupportedPlatform(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
	default:
		t.Skipf("RenameNoReplace is not supported on %s", runtime.GOOS)
	}
}

func assertRenameNoReplacePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to remain absent, stat err=%v", path, err)
	}
}

func assertRenameNoReplaceSameFile(t *testing.T, path string, want os.FileInfo) {
	t.Helper()
	got, err := os.Stat(path)
	if err != nil || !os.SameFile(got, want) {
		t.Fatalf("expected %q to keep identity, got=%#v want=%#v err=%v", path, got, want, err)
	}
}
