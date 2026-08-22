//go:build darwin || linux

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathPublishesTarget(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "reports")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	target := filepath.Join(parent, "profile.yaml")

	if err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("thresholds: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyIfAbsentUnderCanonicalPath returned error: %v", err)
	}
	assertFileContent(t, target, "thresholds: {}\n")
	if entries, err := os.ReadDir(parent); err != nil {
		t.Fatalf("read reports: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != "profile.yaml" {
		t.Fatalf("expected only target to remain, got %v", entries)
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsExistingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, writeTestFileName)
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	assertFileContent(t, target, "before")
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsParentSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	target := filepath.Join(workspace, "reports", writeTestFileName)
	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "directory contains symlink") {
		t.Fatalf("expected parent symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside target to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsDanglingTargetSymlink(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(parent, writeTestFileName)
	outsideTarget := filepath.Join(outside, "missing", writeTestFileName)
	if err := os.Symlink(outsideTarget, target); err != nil {
		t.Fatalf("create dangling target symlink: %v", err)
	}

	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected dangling symlink target to be treated as existing, got %v", err)
	}
	if _, statErr := os.Stat(outsideTarget); !os.IsNotExist(statErr) {
		t.Fatalf("expected symlink target to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsInvalidTargets(t *testing.T) {
	t.Run("invalid target path", func(t *testing.T) {
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(string([]byte{'b', 'a', 'd', 0}), []byte("after"), 0o600)
		if err == nil {
			t.Fatal("expected invalid target path to fail")
		}
	})

	t.Run("relative target", func(t *testing.T) {
		workspace := t.TempDir()
		withWorkingDir(t, workspace)
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600)
		if err != nil {
			t.Fatalf("expected relative target under missing parent to be created, got %v", err)
		}
		assertFileContent(t, filepath.Join(workspace, "reports", writeTestFileName), "after")
	})

	t.Run("missing parent", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "missing", writeTestFileName)
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
		if err != nil {
			t.Fatalf("expected missing parent to be created, got %v", err)
		}
		assertFileContent(t, target, "after")
	})

	t.Run("parent file", func(t *testing.T) {
		assertCanonicalWriterRejectsParentFile(t, WriteFileAtomicallyIfAbsentUnderCanonicalPath)
	})

	t.Run("unsearchable parent", func(t *testing.T) {
		if syscall.Geteuid() == 0 {
			t.Skip("effective privileges bypass parent permission checks")
		}
		parent := filepath.Join(t.TempDir(), "unsearchable")
		if err := os.Mkdir(parent, 0o666); err != nil {
			t.Fatalf("mkdir unsearchable parent: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(parent, 0o755); err != nil && !os.IsNotExist(err) {
				t.Errorf("restore unsearchable parent permissions: %v", err)
			}
		})
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(filepath.Join(parent, writeTestFileName), []byte("after"), 0o600)
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("expected unsearchable parent permission error, got %v", err)
		}
	})

	t.Run("invalid directory path", func(t *testing.T) {
		_, _, err := openSearchOnlyCanonicalDirectory(string([]byte{'b', 'a', 'd', 0}))
		if err == nil {
			t.Fatal("expected invalid directory path to fail")
		}
	})
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsRacedIntermediateSymlink(t *testing.T) {
	workspace := t.TempDir()
	racedParent := filepath.Join(workspace, "race")
	nestedParent := filepath.Join(racedParent, "nested")
	if err := os.MkdirAll(nestedParent, 0o755); err != nil {
		t.Fatalf("mkdir nested parent: %v", err)
	}
	outside := t.TempDir()

	originalHook := afterOpenSearchAncestorFn
	t.Cleanup(func() {
		afterOpenSearchAncestorFn = originalHook
	})
	triggerPath := canonicalSearchDirectoryPath(workspace)
	swapped := false
	afterOpenSearchAncestorFn = func(path string) error {
		if swapped || filepath.Clean(path) != filepath.Clean(triggerPath) {
			return nil
		}
		swapped = true
		if err := os.RemoveAll(racedParent); err != nil {
			return err
		}
		return os.Symlink(outside, racedParent)
	}

	target := filepath.Join(nestedParent, writeTestFileName)
	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "directory contains symlink") {
		t.Fatalf("expected raced intermediate symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested", writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside target to remain absent, got err=%v", statErr)
	}
}

func TestOpenSearchOnlyCanonicalDirectoryOpensVolumeRoot(t *testing.T) {
	rootPath := filepath.VolumeName(filepath.Clean(t.TempDir())) + string(os.PathSeparator)
	root, fd, err := openSearchOnlyCanonicalDirectory(rootPath)
	if err != nil {
		t.Fatalf("openSearchOnlyCanonicalDirectory returned error: %v", err)
	}
	if fd < 0 {
		t.Fatalf("expected valid root descriptor, got %d", fd)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close root descriptor: %v", err)
	}
}

func TestOpenCanonicalSearchOnlyWriteRootOnlyExposesDescriptorFallback(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenCanonicalSearchOnlyWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenCanonicalSearchOnlyWriteRoot returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close search-only write root: %v", closeErr)
		}
	}()
	searchRoot, ok := root.root.(*searchOnlyWriteRoot)
	if !ok {
		t.Fatalf("expected search-only root implementation, got %T", root.root)
	}

	for _, tc := range []struct {
		name      string
		wantError string
		run       func() error
	}{
		{name: "Open", wantError: "descriptor fallback", run: func() error {
			_, err := searchRoot.Open(writeTestFileName)
			return err
		}},
		{name: "OpenRoot", wantError: "descriptor fallback", run: func() error {
			_, err := searchRoot.OpenRoot(".")
			return err
		}},
		{name: "Mkdir", wantError: "descriptor fallback", run: func() error { return searchRoot.Mkdir("child", 0o750) }},
		{name: "Chmod", wantError: "descriptor fallback", run: func() error { return searchRoot.Chmod("child", 0o700) }},
		{name: "MkdirAll", wantError: "descriptor fallback", run: func() error { return searchRoot.MkdirAll("child", 0o750) }},
		{name: "Link", wantError: "descriptor fallback", run: func() error { return searchRoot.Link("old", "new") }},
		{name: "Rename", wantError: "descriptor fallback", run: func() error { return searchRoot.Rename("old", "new") }},
		{name: "Remove", wantError: "descriptor fallback", run: func() error { return searchRoot.Remove("child") }},
		{name: "OpenFile child", wantError: "root descriptor access", run: func() error {
			_, err := searchRoot.OpenFile("child", os.O_RDONLY, 0)
			return err
		}},
		{name: "Lstat child", wantError: "root descriptor stat", run: func() error {
			_, err := searchRoot.Lstat("child")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected descriptor fallback error, got %v", err)
			}
		})
	}

	file, err := searchRoot.OpenFile(".", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile root descriptor returned error: %v", err)
	}
	if info, err := file.Stat(); err != nil {
		t.Fatalf("stat duplicated root descriptor: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected duplicated root descriptor to stat as directory, got mode %v", info.Mode())
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close duplicated root descriptor: %v", err)
	}

	if info, err := searchRoot.Lstat("."); err != nil {
		t.Fatalf("Lstat root descriptor returned error: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected root descriptor stat to be directory, got mode %v", info.Mode())
	}
}

func TestOpenSearchOnlyDirectoryPartsPropagatesHookError(t *testing.T) {
	rootPath := filepath.VolumeName(filepath.Clean(t.TempDir())) + string(os.PathSeparator)
	root, err := openSearchOnlyDirectory(rootPath)
	if err != nil {
		t.Fatalf("open root descriptor: %v", err)
	}

	originalHook := afterOpenSearchAncestorFn
	t.Cleanup(func() {
		afterOpenSearchAncestorFn = originalHook
	})
	afterOpenSearchAncestorFn = func(string) error {
		return errors.New("ancestor hook failed")
	}

	opened, fd, err := openSearchOnlyDirectoryParts(root, rootPath, []string{"tmp"}, false, 0)
	if err == nil || !strings.Contains(err.Error(), "ancestor hook failed") {
		if opened != nil {
			if closeErr := opened.Close(); closeErr != nil {
				t.Fatalf("close unexpected opened descriptor: %v", closeErr)
			}
		}
		t.Fatalf("expected hook error, got fd=%d err=%v", fd, err)
	}
}

func TestOpenSearchOnlyChildDirectoryValidationBranches(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	_, parentFD := openCanonicalParentForTest(t, parent)

	originalOpenAt := openSearchOnlyDirectoryAtFn
	originalStat := descriptorStatFn
	t.Cleanup(func() {
		openSearchOnlyDirectoryAtFn = originalOpenAt
		descriptorStatFn = originalStat
	})

	for _, tc := range []struct {
		name      string
		wantError string
		configure func()
	}{
		{
			name:      "open failure after lstat",
			wantError: "openat failed",
			configure: func() {
				openSearchOnlyDirectoryAtFn = func(int, string) (*os.File, error) {
					return nil, errors.New("openat failed")
				}
				descriptorStatFn = originalStat
			},
		},
		{
			name:      "stat failure after open",
			wantError: "descriptor stat failed",
			configure: func() {
				openSearchOnlyDirectoryAtFn = originalOpenAt
				descriptorStatFn = func(int) (descriptorFileInfo, error) {
					return descriptorFileInfo{}, errors.New("descriptor stat failed")
				}
			},
		},
		{
			name:      "changed after open",
			wantError: "directory changed while opening",
			configure: func() {
				openSearchOnlyDirectoryAtFn = originalOpenAt
				descriptorStatFn = func(int) (descriptorFileInfo, error) {
					return descriptorFileInfo{dev: "changed", ino: "changed", mode: unix.S_IFDIR}, nil
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.configure()
			file, err := openSearchOnlyChildDirectory(parentFD, "child", "child")
			assertOpenSearchOnlyChildDirectoryError(t, file, err, tc.wantError)
		})
	}
}

func assertOpenSearchOnlyChildDirectoryError(t *testing.T, file *os.File, err error, want string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), want) {
		return
	}
	closeUnexpectedChildDescriptor(t, file)
	t.Fatalf("expected %s failure, got %v", want, err)
}

func closeUnexpectedChildDescriptor(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		return
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close unexpected child descriptor: %v", err)
	}
}

func openCanonicalParentForTest(t *testing.T, parent string) (*os.File, int) {
	t.Helper()
	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
	if err != nil {
		t.Fatalf("open parent descriptor: %v", err)
	}
	t.Cleanup(func() {
		if err := parentFile.Close(); err != nil {
			t.Errorf("close parent descriptor: %v", err)
		}
	})
	return parentFile, parentFD
}

func TestCanonicalSearchDirectoryPathHandlesTrustedAliases(t *testing.T) {
	originalAliasTarget := searchDirectoryAliasTargetFn
	t.Cleanup(func() {
		searchDirectoryAliasTargetFn = originalAliasTarget
	})

	separator := string(os.PathSeparator)
	tmpAlias := filepath.Join(separator, "tmp")
	tmpTarget := filepath.Join(separator, "private", "tmp")
	varAlias := filepath.Join(separator, "var")
	varTarget := filepath.Join(separator, "private", "var")
	searchDirectoryAliasTargetFn = func(path string) (string, bool) {
		switch path {
		case tmpAlias:
			return tmpTarget, true
		case varAlias:
			return varTarget, true
		default:
			return "", false
		}
	}

	if got := canonicalSearchDirectoryPath(tmpAlias); got != tmpTarget {
		t.Fatalf("expected trusted tmp alias target %q, got %q", tmpTarget, got)
	}
	nested := filepath.Join(tmpAlias, "lopper", "profile")
	wantNested := filepath.Join(tmpTarget, "lopper", "profile")
	if got := canonicalSearchDirectoryPath(nested); got != wantNested {
		t.Fatalf("expected nested trusted tmp alias target %q, got %q", wantNested, got)
	}
	if got := canonicalSearchDirectoryPath(varAlias); got != varTarget {
		t.Fatalf("expected trusted var alias target %q, got %q", varTarget, got)
	}

	regular := filepath.Join(separator, "usr", "local")
	if got := canonicalSearchDirectoryPath(regular); got != regular {
		t.Fatalf("expected regular path to remain unchanged, got %q", got)
	}
}

func TestCanonicalSearchDirectoryPathUsesResolvedRootLevelAliases(t *testing.T) {
	originalAliasTarget := searchDirectoryAliasTargetFn
	t.Cleanup(func() {
		searchDirectoryAliasTargetFn = originalAliasTarget
	})

	searchDirectoryAliasTargetFn = func(path string) (string, bool) {
		if path != filepath.Join(string(os.PathSeparator), "alias") {
			return "", false
		}
		return filepath.Join(string(os.PathSeparator), "resolved-alias"), true
	}

	if got := canonicalSearchDirectoryPath(filepath.Join(string(os.PathSeparator), "alias", "reports", "profile.yaml")); got != filepath.Join(string(os.PathSeparator), "resolved-alias", "reports", "profile.yaml") {
		t.Fatalf("expected synthetic root-level alias to resolve, got %q", got)
	}
}

func TestSearchDirectoryAliasTargetRequiresSymlinkForTrustedAlias(t *testing.T) {
	originalGOOS := runtimeGOOS
	originalLstat := searchDirectoryLstatFn
	originalStat := searchDirectoryStatFn
	t.Cleanup(func() {
		runtimeGOOS = originalGOOS
		searchDirectoryLstatFn = originalLstat
		searchDirectoryStatFn = originalStat
	})

	runtimeGOOS = "darwin"
	dirInfo := statTestPath(t, t.TempDir())
	searchDirectoryStatFn = func(string) (fs.FileInfo, error) {
		return dirInfo, nil
	}
	searchDirectoryLstatFn = func(string) (fs.FileInfo, error) {
		return &modeOverrideFileInfo{FileInfo: dirInfo, mode: os.ModeDir | 0o755}, nil
	}

	tmpAlias := filepath.Join(string(os.PathSeparator), "tmp")
	if target, ok := searchDirectoryAliasTarget(tmpAlias); ok {
		t.Fatalf("expected ordinary trusted alias directory to remain unchanged, got target=%q", target)
	}

	searchDirectoryLstatFn = func(string) (fs.FileInfo, error) {
		return &modeOverrideFileInfo{FileInfo: dirInfo, mode: os.ModeSymlink | 0o777}, nil
	}
	want := filepath.Join(string(os.PathSeparator), "private", "tmp")
	if target, ok := searchDirectoryAliasTarget(tmpAlias); !ok || target != want {
		t.Fatalf("expected symlinked trusted alias target %q, got target=%q ok=%v", want, target, ok)
	}
}

func TestSearchDirectoryRootLevelAliasRejectionBranches(t *testing.T) {
	originalLstat := searchDirectoryLstatFn
	originalStat := searchDirectoryStatFn
	t.Cleanup(func() {
		searchDirectoryLstatFn = originalLstat
		searchDirectoryStatFn = originalStat
	})

	if isSearchDirectoryRootLevelAlias("relative") {
		t.Fatal("relative path must not be a root-level alias")
	}
	if isSearchDirectoryRootLevelAlias(filepath.Join(string(os.PathSeparator), "alias", "nested")) {
		t.Fatal("nested path must not be a root-level alias")
	}

	dirInfo := statTestPath(t, t.TempDir())
	rootAlias := filepath.Join(string(os.PathSeparator), "alias")
	searchDirectoryLstatFn = func(string) (fs.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	if isSearchDirectoryRootLevelAlias(rootAlias) {
		t.Fatal("missing alias path must not be accepted")
	}

	searchDirectoryLstatFn = func(string) (fs.FileInfo, error) {
		return &modeOverrideFileInfo{FileInfo: dirInfo, mode: os.ModeDir | 0o755}, nil
	}
	if isSearchDirectoryRootLevelAlias(rootAlias) {
		t.Fatal("ordinary directory must not be accepted as alias")
	}

	searchDirectoryLstatFn = func(string) (fs.FileInfo, error) {
		return &modeOverrideFileInfo{FileInfo: dirInfo, mode: os.ModeSymlink | 0o777}, nil
	}
	searchDirectoryStatFn = func(string) (fs.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	if isSearchDirectoryRootLevelAlias(rootAlias) {
		t.Fatal("symlink with missing target must not be accepted")
	}

	fileTarget := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(fileTarget, []byte("target"), 0o600); err != nil {
		t.Fatalf("write regular alias target: %v", err)
	}
	fileInfo := statTestPath(t, fileTarget)
	searchDirectoryStatFn = func(string) (fs.FileInfo, error) {
		return fileInfo, nil
	}
	if isSearchDirectoryRootLevelAlias(rootAlias) {
		t.Fatal("symlink to a regular file must not be accepted")
	}
}

func TestOpenSearchOnlyDirectoryRejectsMissingPath(t *testing.T) {
	file, err := openSearchOnlyDirectory(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected missing directory descriptor: %v", closeErr)
		}
		t.Fatal("expected missing directory open to fail")
	}
}

func TestOpenCanonicalSearchOnlyWriteRootRejectsInvalidRootPath(t *testing.T) {
	if root, err := OpenCanonicalSearchOnlyWriteRoot(string([]byte{'b', 'a', 'd', 0})); err == nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected invalid root: %v", closeErr)
		}
		t.Fatal("expected invalid root path to fail")
	}
}

func TestOpenSearchOnlyChildDirectoryPropagatesMkdirError(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)
	expectedErr := errors.New("mkdirat failed")
	withDescriptorOperationHooks(t, descriptorOperationHooks{
		mkdirat: func(int, string, uint32) error {
			return expectedErr
		},
	})

	file, err := openSearchOnlyChildDirectoryWithOptions(parentFD, "missing", filepath.Join(parent, "missing"), true, 0o750)
	closeUnexpectedChildDescriptor(t, file)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected mkdirat error, got %v", err)
	}
}

func TestDescriptorStatRejectsInvalidDescriptor(t *testing.T) {
	if _, err := descriptorStat(-1); err == nil {
		t.Fatal("expected invalid descriptor stat to fail")
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathWritesSearchOnlyParent(t *testing.T) {
	parent := setupSearchOnlyParentForTest(t)
	target := filepath.Join(parent, writeTestFileName)

	if err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyIfAbsentUnderCanonicalPath returned error: %v", err)
	}
	assertFileContent(t, target, "after")
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("restore dropbox permissions before listing: %v", err)
	}
	if entries, err := os.ReadDir(parent); err != nil {
		t.Fatalf("read dropbox: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected only target to remain, got %v", entries)
	}
}

func TestWriteFileAtomicallyReplacingUnderCanonicalPathRejectsExistingTargetInSearchOnlyParent(t *testing.T) {
	parent := setupSearchOnlyParentWithExistingTargetForTest(t)
	target := filepath.Join(parent, writeTestFileName)

	err := WriteFileAtomicallyReplacingUnderCanonicalPath(target, []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "existing target cannot be safely replaced under descriptor fallback") {
		t.Fatalf("expected fail-closed existing target error, got %v", err)
	}
	assertFileContent(t, target, "before")
	if info, err := os.Stat(target); err != nil {
		t.Fatalf("stat target: %v", err)
	} else if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected existing target mode to remain 0640, got %#o", info.Mode().Perm())
	}
}

func TestDescriptorReplacingPathPreservesConcurrentReplacement(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)

	targetPath := filepath.Join(parent, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	replacementPath := filepath.Join(parent, "replacement")
	if err := os.WriteFile(replacementPath, []byte("concurrent"), 0o600); err != nil {
		t.Fatalf("seed concurrent replacement: %v", err)
	}

	originalDescriptorLstatFn := descriptorLstatFn
	t.Cleanup(func() {
		descriptorLstatFn = originalDescriptorLstatFn
	})
	replaced := false
	descriptorLstatFn = func(fd int, name string) (descriptorFileInfo, error) {
		info, err := originalDescriptorLstatFn(fd, name)
		if err == nil && name == writeTestFileName && !replaced {
			replaced = true
			if renameErr := os.Rename(replacementPath, targetPath); renameErr != nil {
				t.Fatalf("replace target after descriptor lookup: %v", renameErr)
			}
		}
		return info, err
	}

	err := writeFileAtomicallyReplacingUnderDescriptorPath(parentFD, writeTestFileName, []byte("forced"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "existing target cannot be safely replaced under descriptor fallback") {
		t.Fatalf("expected fail-closed existing target error, got %v", err)
	}
	assertFileContent(t, targetPath, "concurrent")
}

func TestWriteFileAtomicallyReplacingUnderCanonicalPathCreatesAbsentTarget(t *testing.T) {
	parent := setupSearchOnlyParentForTest(t)
	target := filepath.Join(parent, writeTestFileName)

	if err := WriteFileAtomicallyReplacingUnderCanonicalPath(target, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyReplacingUnderCanonicalPath returned error: %v", err)
	}
	assertFileContent(t, target, "after")
}

func TestWriteFileAtomicallyReplacingUnderCanonicalPathRejectsExistingSymlink(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(parent, writeTestFileName)
	outsideTarget := filepath.Join(outside, writeTestFileName)
	if err := os.WriteFile(outsideTarget, []byte("outside"), 0o600); err != nil {
		t.Fatalf("seed outside target: %v", err)
	}
	if err := os.Symlink(outsideTarget, target); err != nil {
		t.Fatalf("create target symlink: %v", err)
	}

	err := WriteFileAtomicallyReplacingUnderCanonicalPath(target, []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("expected target symlink rejection, got %v", err)
	}
	assertFileContent(t, outsideTarget, "outside")
}

func TestWriteFileAtomicallyReplacingUnderCanonicalPathRejectsInvalidTargets(t *testing.T) {
	t.Run("invalid target path", func(t *testing.T) {
		err := WriteFileAtomicallyReplacingUnderCanonicalPath(string([]byte{'b', 'a', 'd', 0}), []byte("after"), 0o600)
		if err == nil {
			t.Fatal("expected invalid target path to fail")
		}
	})

	t.Run("parent file", func(t *testing.T) {
		assertCanonicalWriterRejectsParentFile(t, WriteFileAtomicallyReplacingUnderCanonicalPath)
	})

	t.Run("directory target", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), writeTestFileName)
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("seed directory target: %v", err)
		}
		err := WriteFileAtomicallyReplacingUnderCanonicalPath(target, []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "target path is not a regular file") {
			t.Fatalf("expected directory target rejection, got %v", err)
		}
	})
}

func TestWriteRootPinnedCanonicalPathWriters(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	if err := root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(filepath.Join("reports", "profile.yaml"), []byte("thresholds: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyIfAbsentUnderPinnedRoot returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "reports", "profile.yaml"), "thresholds: {}\n")

	if err := root.WriteFileAtomicallyReplacingUnderPinnedRoot(filepath.Join("reports", "created.txt"), []byte("created"), 0o640); err != nil {
		t.Fatalf("WriteFileAtomicallyReplacingUnderPinnedRoot returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "reports", "created.txt"), "created")

	err := root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(filepath.Join("reports", "profile.yaml"), []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "reports", "profile.yaml"), "thresholds: {}\n")

	err = root.WriteFileAtomicallyReplacingUnderPinnedRoot(filepath.Join("reports", "profile.yaml"), []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "existing target cannot be safely replaced under descriptor fallback") {
		t.Fatalf("expected fail-closed existing target error, got %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "reports", "profile.yaml"), "thresholds: {}\n")
}

func TestWriteRootPinnedCanonicalPathWritersRejectUnsafeTargets(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)
	outside := t.TempDir()

	err := root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(filepath.Join("..", filepath.Base(outside), writeTestFileName), []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf("expected root escape rejection, got %v", err)
	}
	err = root.WriteFileAtomicallyReplacingUnderPinnedRoot(filepath.Join("..", filepath.Base(outside), writeTestFileName), []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf("expected replacing root escape rejection, got %v", err)
	}

	if err := os.Symlink(outside, filepath.Join(rootDir, "link")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}
	err = root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(filepath.Join("link", writeTestFileName), []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "directory contains symlink") {
		t.Fatalf("expected symlink parent rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside target to remain absent, got err=%v", statErr)
	}

	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(rootDir, "target-link")); err != nil {
		t.Fatalf("create target symlink: %v", err)
	}
	err = root.WriteFileAtomicallyReplacingUnderPinnedRoot("target-link", []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("expected target symlink rejection, got %v", err)
	}
}

func TestWriteRootPinnedCanonicalPathWritersPropagateRootDescriptorErrors(t *testing.T) {
	expectedErr := errors.New("root descriptor open failed")
	root := &WriteRoot{
		root: &fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return nil, expectedErr
			},
		},
		rootAbs: string(os.PathSeparator),
	}

	if err := root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(writeTestFileName, []byte("after"), 0o600); !errors.Is(err, expectedErr) {
		t.Fatalf("expected root descriptor open error, got %v", err)
	}
	if err := root.WriteFileAtomicallyReplacingUnderPinnedRoot(writeTestFileName, []byte("after"), 0o600); !errors.Is(err, expectedErr) {
		t.Fatalf("expected root descriptor open error, got %v", err)
	}

	root.root = &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{close: func() error { return nil }}, nil
		},
	}
	err := root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(writeTestFileName, []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "pinned root does not expose a file descriptor") {
		t.Fatalf("expected non-descriptor root error, got %v", err)
	}
}

func TestWriteRootPinnedCanonicalPathWritersJoinRootDescriptorCloseError(t *testing.T) {
	root := &WriteRoot{
		root: &fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return os.NewFile(^uintptr(0), "invalid-root-descriptor"), nil
			},
		},
		rootAbs: string(os.PathSeparator),
	}

	for _, tc := range []struct {
		name  string
		write func() error
	}{
		{
			name: "if absent",
			write: func() error {
				return root.WriteFileAtomicallyIfAbsentUnderPinnedRoot(writeTestFileName, []byte("after"), 0o600)
			},
		},
		{
			name: "replacing",
			write: func() error {
				return root.WriteFileAtomicallyReplacingUnderPinnedRoot(writeTestFileName, []byte("after"), 0o600)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.write()
			if err == nil {
				t.Fatal("expected descriptor and close failure")
			}
			if !strings.Contains(err.Error(), "bad file descriptor") {
				t.Fatalf("expected joined invalid descriptor error, got %v", err)
			}
		})
	}
}

func TestCanonicalPathWritersRejectAbsolutePathResolutionFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(string, []byte, os.FileMode) error
	}{
		{
			name:  "if absent",
			write: WriteFileAtomicallyIfAbsentUnderCanonicalPath,
		},
		{
			name:  "replacing",
			write: WriteFileAtomicallyReplacingUnderCanonicalPath,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFileSystem(t, &fakeFileSystem{abs: func(string) (string, error) {
				return "", errors.New("abs failed")
			}})
			assertErrorContains(t, tc.write(writeTestFileName, []byte("after"), 0o600), "abs failed")
		})
	}
}

func assertCanonicalWriterRejectsParentFile(t *testing.T, write func(string, []byte, os.FileMode) error) {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatalf("seed parent file: %v", err)
	}
	assertErrorContains(t, write(filepath.Join(parent, writeTestFileName), []byte("after"), 0o600), "directory is not a directory")
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathCreatesNestedParentInSearchOnlyParent(t *testing.T) {
	parent := setupSearchOnlyParentForTest(t)
	target := filepath.Join(parent, "reports", writeTestFileName)

	if err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyIfAbsentUnderCanonicalPath returned error: %v", err)
	}
	assertFileContent(t, target, "after")
	if info, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Fatalf("stat created nested parent: %v", err)
	} else if info.Mode().Perm() != canonicalPathParentPerm {
		t.Fatalf("nested parent mode = %#o, want %#o", info.Mode().Perm(), canonicalPathParentPerm)
	}
}

func TestDescriptorPathHelpersRejectInvalidDescriptorAndTempNames(t *testing.T) {
	err := rejectExistingDescriptorPath(-1, writeTestFileName)
	if err == nil {
		t.Fatal("expected invalid descriptor error")
	}
	if file, fd, err := openSearchOnlyCanonicalDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected missing directory descriptor: %v", closeErr)
		}
		t.Fatalf("expected missing directory open to fail, got fd=%d", fd)
	}
	notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0o600); err != nil {
		t.Fatalf("seed non-directory: %v", err)
	}
	if file, fd, err := openSearchOnlyCanonicalDirectory(notDirectory); err == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected non-directory descriptor: %v", closeErr)
		}
		t.Fatalf("expected non-directory open to fail, got fd=%d", fd)
	}

	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)

	originalRandomTempNameFn := randomTempNameFn
	t.Cleanup(func() {
		randomTempNameFn = originalRandomTempNameFn
	})
	randomTempNameFn = func() (string, error) { return "", errors.New("boom") }
	if name, file, err := createDescriptorTempFile(parentFD, 0o600); err == nil {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected random-name temp file: %v", closeErr)
			}
		}
		t.Fatalf("expected random name error, got name=%q", name)
	}

	collidingName := ".safeio-collision"
	if err := os.WriteFile(filepath.Join(parent, collidingName), []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed colliding temp file: %v", err)
	}
	randomTempNameFn = func() (string, error) { return collidingName, nil }
	if name, file, err := createDescriptorTempFile(parentFD, 0o600); err == nil || !strings.Contains(err.Error(), "too many collisions") {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected colliding temp file: %v", closeErr)
			}
		}
		t.Fatalf("expected collision exhaustion, got name=%q err=%v", name, err)
	}
	if name, file, err := createDescriptorTempFile(-1, 0o600); err == nil {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected invalid-descriptor temp file: %v", closeErr)
			}
		}
		t.Fatalf("expected invalid descriptor temp creation to fail, got name=%q", name)
	}
}

func TestDescriptorReplacingPathRejectsInvalidDescriptor(t *testing.T) {
	err := writeFileAtomicallyReplacingUnderDescriptorPath(-1, writeTestFileName, []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected invalid descriptor replacement to fail")
	}
}

func TestDescriptorPathWriteRejectsExistingTargetAndCleansTemp(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)

	if err := os.WriteFile(filepath.Join(parent, writeTestFileName), []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, writeTestFileName, []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	assertFileContent(t, filepath.Join(parent, writeTestFileName), "before")
	if entries, readErr := os.ReadDir(parent); readErr != nil {
		t.Fatalf("read parent: %v", readErr)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected failed existing-target publish to clean temp file, got %v", entries)
	}
}

func TestDescriptorPathWriteRejectsInvalidDescriptorBeforeTemp(t *testing.T) {
	err := writeFileAtomicallyIfAbsentUnderDescriptorPath(-1, writeTestFileName, []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected invalid descriptor write to fail")
	}
}

func TestDescriptorPathWritePropagatesTempFileOperationErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hooks descriptorOperationHooks
	}{
		{
			name: "write error",
			hooks: descriptorOperationHooks{
				write: func(*os.File, []byte) (int, error) {
					return 0, errors.New("descriptor write failed")
				},
			},
		},
		{
			name: "chmod error",
			hooks: descriptorOperationHooks{
				chmod: func(*os.File, os.FileMode) error {
					return errors.New("descriptor chmod failed")
				},
			},
		},
		{
			name: "stat error",
			hooks: descriptorOperationHooks{
				stat: func(*os.File) (os.FileInfo, error) {
					return nil, errors.New("descriptor stat failed")
				},
			},
		},
		{
			name: "close error",
			hooks: descriptorOperationHooks{
				close: func(*os.File) error {
					return errors.New("descriptor close failed")
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			_, parentFD := openCanonicalParentForTest(t, parent)
			withDescriptorOperationHooks(t, tc.hooks)

			err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, writeTestFileName, []byte("after"), 0o600)
			if err == nil || !strings.Contains(err.Error(), "descriptor") {
				t.Fatalf("expected descriptor operation error, got %v", err)
			}
			if _, statErr := os.Lstat(filepath.Join(parent, writeTestFileName)); !os.IsNotExist(statErr) {
				t.Fatalf("expected target to remain absent, got %v", statErr)
			}
		})
	}
}

func TestDescriptorPathWriteRejectsChangedTargetValidation(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)

	originalDescriptorLstatFn := descriptorLstatFn
	t.Cleanup(func() {
		descriptorLstatFn = originalDescriptorLstatFn
	})

	t.Run("missing after publish", func(t *testing.T) {
		descriptorLstatFn = func(int, string) (descriptorFileInfo, error) {
			return descriptorFileInfo{}, os.ErrNotExist
		}
		err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "missing-after-publish.txt", []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "changed before validation") {
			t.Fatalf("expected changed target validation error, got %v", err)
		}
	})

	t.Run("symlink after publish", func(t *testing.T) {
		descriptorLstatFn = func(int, string) (descriptorFileInfo, error) {
			return descriptorFileInfo{mode: unix.S_IFLNK}, nil
		}
		err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "symlink-after-publish.txt", []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "became a symlink") {
			t.Fatalf("expected symlink validation error, got %v", err)
		}
	})

	t.Run("different regular file after publish", func(t *testing.T) {
		descriptorLstatFn = func(int, string) (descriptorFileInfo, error) {
			return descriptorFileInfo{mode: unix.S_IFREG, dev: "different", ino: "different"}, nil
		}
		err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "different-after-publish.txt", []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "changed before validation") {
			t.Fatalf("expected different file validation error, got %v", err)
		}
	})
}

func TestDescriptorPathWriteCleansTempOnPublishFailure(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)

	if err := os.Mkdir(filepath.Join(parent, "missing-dir-target"), 0o755); err != nil {
		t.Fatalf("seed directory target: %v", err)
	}
	err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "", []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected publish failure for empty descriptor-relative target")
	}
	if entries, readErr := os.ReadDir(parent); readErr != nil {
		t.Fatalf("read parent: %v", readErr)
	} else if len(entries) != 1 || entries[0].Name() != "missing-dir-target" {
		t.Fatalf("expected failed publish to clean temp file, got %v", entries)
	}
}

func TestDescriptorPathWritePropagatesTempUnlinkErrorAfterPublish(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)
	expectedErr := errors.New("descriptor unlink failed")
	withDescriptorOperationHooks(t, descriptorOperationHooks{
		unlinkat: func(int, string, int) error {
			return expectedErr
		},
	})

	err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, writeTestFileName, []byte("after"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected unlink error, got %v", err)
	}
	assertFileContent(t, filepath.Join(parent, writeTestFileName), "after")
}

func TestCleanupDescriptorTempFileClosesAndRemovesTemp(t *testing.T) {
	parent := t.TempDir()
	_, parentFD := openCanonicalParentForTest(t, parent)

	tempName, tempFile, err := createDescriptorTempFile(parentFD, 0o600)
	if err != nil {
		t.Fatalf("create descriptor temp: %v", err)
	}
	if err := cleanupDescriptorTempFile(parentFD, tempName, tempFile); err != nil {
		t.Fatalf("cleanup descriptor temp: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(parent, tempName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected temp file removal, got %v", statErr)
	}
	if err := cleanupDescriptorTempFile(parentFD, tempName, nil); err != nil {
		t.Fatalf("cleanup missing descriptor temp should be ignored: %v", err)
	}

	closedTempName, closedTempFile, err := createDescriptorTempFile(parentFD, 0o600)
	if err != nil {
		t.Fatalf("create closed descriptor temp: %v", err)
	}
	if err := closedTempFile.Close(); err != nil {
		t.Fatalf("close descriptor temp before cleanup: %v", err)
	}
	if err := cleanupDescriptorTempFile(parentFD, closedTempName, closedTempFile); err != nil {
		t.Fatalf("cleanup already-closed descriptor temp: %v", err)
	}

	if err := cleanupDescriptorTempFile(-1, "still-present", nil); err == nil {
		t.Fatal("expected invalid descriptor cleanup error")
	}
}

func TestCleanupDescriptorTempFileJoinsCloseAndUnlinkErrors(t *testing.T) {
	closeErr := errors.New("descriptor close failed")
	unlinkErr := errors.New("descriptor unlink failed")
	tempFile, err := os.CreateTemp(t.TempDir(), "descriptor-cleanup-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	withDescriptorOperationHooks(t, descriptorOperationHooks{
		close: func(*os.File) error {
			return closeErr
		},
		unlinkat: func(int, string, int) error {
			return unlinkErr
		},
	})

	err = cleanupDescriptorTempFile(-1, "still-present", tempFile)
	if !errors.Is(err, closeErr) || !errors.Is(err, unlinkErr) {
		t.Fatalf("expected joined close and unlink errors, got %v", err)
	}
}

func TestAtomicIfAbsentErrorBranchesForCoverage(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	for _, tc := range []struct {
		name      string
		wantError string
		run       func() error
	}{
		{
			name:      "temp create failure",
			wantError: "create temp failed",
			run: func() error {
				return writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
					openFile: func(string, int, os.FileMode) (File, error) {
						return nil, errors.New("create temp failed")
					},
				}, writeTestFileName, []byte("after"), 0o600)
			},
		},
		{
			name:      "temp write failure",
			wantError: "write temp failed",
			run: func() error {
				return writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
					openFile: func(string, int, os.FileMode) (File, error) {
						return failedWriteFakeFile("write temp failed"), nil
					},
					remove: func(string) error { return nil },
				}, writeTestFileName, []byte("after"), 0o600)
			},
		},
		{
			name:      "temp stat failure",
			wantError: "stat temp failed",
			run: func() error {
				return writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
					openFile: func(string, int, os.FileMode) (File, error) {
						return writtenRegularFakeFile(nil, errors.New("stat temp failed")), nil
					},
					remove: func(string) error { return nil },
				}, writeTestFileName, []byte("after"), 0o600)
			},
		},
		{
			name:      "remove temp failure after link",
			wantError: "remove temp failed",
			run: func() error {
				return writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
					openFile: func(string, int, os.FileMode) (File, error) {
						return writtenRegularFakeFile(tempInfo, nil), nil
					},
					link:   func(string, string) error { return nil },
					remove: func(string) error { return errors.New("remove temp failed") },
				}, writeTestFileName, []byte("after"), 0o600)
			},
		},
		{
			name:      "pinned replacement temp write failure",
			wantError: "pinned write failed",
			run: func() error {
				return writeAtomicReplacementWithPinnedTarget(&fakeRoot{
					openFile: func(string, int, os.FileMode) (File, error) {
						return failedWriteFakeFile("pinned write failed"), nil
					},
					remove: func(string) error { return nil },
				}, writeTestFileName, []byte("after"), 0o600, nil, false)
			},
		},
		{
			name:      "pinned replacement commit failure",
			wantError: "pinned rename failed",
			run: func() error {
				return writeAtomicReplacementWithPinnedTarget(&fakeRoot{
					openFile: func(string, int, os.FileMode) (File, error) {
						return writtenRegularFakeFile(tempInfo, nil), nil
					},
					rename: func(string, string) error { return errors.New("pinned rename failed") },
					remove: func(string) error { return nil },
				}, writeTestFileName, []byte("after"), 0o600, nil, false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertErrorContains(t, tc.run(), tc.wantError)
		})
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %s failure, got %v", want, err)
	}
}

func failedWriteFakeFile(message string) File {
	return &fakeFile{
		write: func([]byte) (int, error) { return 0, errors.New(message) },
		close: func() error { return nil },
	}
}

func writtenRegularFakeFile(info fs.FileInfo, statErr error) File {
	return &fakeFile{
		write: func(p []byte) (int, error) { return len(p), nil },
		chmod: func(os.FileMode) error { return nil },
		stat: func() (fs.FileInfo, error) {
			if statErr != nil {
				return nil, statErr
			}
			return info, nil
		},
		close: func() error { return nil },
	}
}

func setupSearchOnlyParentForTest(t *testing.T) string {
	t.Helper()
	return setupSearchOnlyParentStateForTest(t, false)
}

func setupSearchOnlyParentWithExistingTargetForTest(t *testing.T) string {
	t.Helper()
	return setupSearchOnlyParentStateForTest(t, true)
}

func setupSearchOnlyParentStateForTest(t *testing.T, seedTarget bool) string {
	t.Helper()
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent permission checks")
	}
	parent := filepath.Join(t.TempDir(), "dropbox")
	if err := os.Mkdir(parent, 0o733); err != nil {
		t.Fatalf("mkdir dropbox: %v", err)
	}
	if seedTarget {
		seedSearchOnlyTargetForTest(t, parent)
	}
	if err := os.Chmod(parent, 0o333); err != nil {
		t.Fatalf("chmod dropbox search-only: %v", err)
	}
	restoreSearchOnlyParentForTest(t, parent)
	requireSearchOnlyParentForTest(t, parent)
	return parent
}

func seedSearchOnlyTargetForTest(t *testing.T, parent string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(parent, writeTestFileName), []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target: %v", err)
	}
}

func restoreSearchOnlyParentForTest(t *testing.T, parent string) {
	t.Helper()
	t.Cleanup(func() { restoreSearchOnlyParentMode(t, parent) })
}

func restoreSearchOnlyParentMode(t *testing.T, parent string) {
	t.Helper()
	err := os.Chmod(parent, 0o755)
	if err != nil && !os.IsNotExist(err) {
		t.Errorf("restore search-only fixture permissions: %v", err)
	}
}

func requireSearchOnlyParentForTest(t *testing.T, parent string) {
	t.Helper()
	if _, err := os.ReadDir(parent); !os.IsPermission(err) {
		t.Skipf("parent read permission semantics are not testable: %v", err)
	}
}

func TestSafeIOHelperCoverageBranches(t *testing.T) {
	cleanName, parts := splitPinnedPath(string(os.PathSeparator) + filepath.Join("absolute", writeTestFileName))
	if cleanName == "." || len(parts) != 2 || parts[0] != "absolute" || parts[1] != writeTestFileName {
		t.Fatalf("unexpected split pinned path: clean=%q parts=%v", cleanName, parts)
	}

	if !arePureSentinelCauses([]error{nil, os.ErrNotExist}, []error{os.ErrNotExist}) {
		t.Fatal("expected nil joined cause to be ignored for pure sentinel matching")
	}
}

type descriptorOperationHooks struct {
	mkdirat  func(int, string, uint32) error
	linkat   func(int, string, int, string, int) error
	unlinkat func(int, string, int) error
	write    func(*os.File, []byte) (int, error)
	chmod    func(*os.File, os.FileMode) error
	stat     func(*os.File) (os.FileInfo, error)
	close    func(*os.File) error
}

func withDescriptorOperationHooks(t *testing.T, hooks descriptorOperationHooks) {
	t.Helper()
	originalMkdirat := descriptorMkdiratFn
	originalLinkat := descriptorLinkatFn
	originalUnlinkat := descriptorUnlinkatFn
	originalWrite := descriptorFileWriteFn
	originalChmod := descriptorFileChmodFn
	originalStat := descriptorFileStatFn
	originalClose := descriptorFileCloseFn
	if hooks.mkdirat != nil {
		descriptorMkdiratFn = hooks.mkdirat
	}
	if hooks.linkat != nil {
		descriptorLinkatFn = hooks.linkat
	}
	if hooks.unlinkat != nil {
		descriptorUnlinkatFn = hooks.unlinkat
	}
	if hooks.write != nil {
		descriptorFileWriteFn = hooks.write
	}
	if hooks.chmod != nil {
		descriptorFileChmodFn = hooks.chmod
	}
	if hooks.stat != nil {
		descriptorFileStatFn = hooks.stat
	}
	if hooks.close != nil {
		descriptorFileCloseFn = hooks.close
	}
	t.Cleanup(func() {
		descriptorMkdiratFn = originalMkdirat
		descriptorLinkatFn = originalLinkat
		descriptorUnlinkatFn = originalUnlinkat
		descriptorFileWriteFn = originalWrite
		descriptorFileChmodFn = originalChmod
		descriptorFileStatFn = originalStat
		descriptorFileCloseFn = originalClose
	})
}
