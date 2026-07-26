package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	writeTestFileName   = "file.txt"
	openRootErrFmt      = "open root: %v"
	closeRootErrFmt     = "close root: %v"
	closeTempFileErrFmt = "close temp file: %v"
)

type modeOverrideFileInfo struct {
	fs.FileInfo
	mode os.FileMode
}

func (i *modeOverrideFileInfo) Mode() os.FileMode {
	return i.mode
}

type stubFileInfo struct {
	name string
	mode os.FileMode
}

func (i *stubFileInfo) Name() string       { return i.name }
func (i *stubFileInfo) Size() int64        { return 0 }
func (i *stubFileInfo) Mode() os.FileMode  { return i.mode }
func (i *stubFileInfo) ModTime() time.Time { return time.Time{} }
func (i *stubFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *stubFileInfo) Sys() any           { return nil }

type truncatingFakeFile struct {
	*fakeFile
	truncate func(size int64) error
}

type unsafeReplacementPathCase struct {
	name string
	info fs.FileInfo
	want string
}

type mkdirCloseTraversalFixture struct {
	t           *testing.T
	components  []string
	infos       []fs.FileInfo
	closeErrors []error
	events      []string
}

func (f *truncatingFakeFile) Truncate(size int64) error {
	return f.truncate(size)
}

func unsafeReplacementPathCases(info fs.FileInfo) []unsafeReplacementPathCase {
	return []unsafeReplacementPathCase{
		{
			name: "symlink",
			info: &modeOverrideFileInfo{FileInfo: info, mode: os.ModeSymlink | 0o777},
			want: "became a symlink",
		},
		{
			name: "directory",
			info: &modeOverrideFileInfo{FileInfo: info, mode: os.ModeDir | 0o755},
			want: "not a regular file",
		},
	}
}

func newMkdirCloseTraversalFixture(t *testing.T, components ...string) *mkdirCloseTraversalFixture {
	t.Helper()
	fixture := &mkdirCloseTraversalFixture{
		t:           t,
		components:  components,
		infos:       make([]fs.FileInfo, len(components)),
		closeErrors: make([]error, len(components)),
	}
	for index := range components {
		fixture.infos[index] = statTestPath(t, t.TempDir())
	}
	return fixture
}

func (f *mkdirCloseTraversalFixture) rootAt(depth int) Root {
	return &fakeRoot{
		lstat:    func(name string) (fs.FileInfo, error) { return f.lstat(depth, name) },
		openRoot: func(name string) (Root, error) { return f.openRoot(depth, name) },
		close:    func() error { return f.close(depth) },
	}
}

func (f *mkdirCloseTraversalFixture) lstat(depth int, name string) (fs.FileInfo, error) {
	if name == "." {
		f.events = append(f.events, "lstat-opened:"+f.path(depth))
		return f.infos[depth-1], nil
	}
	if depth >= len(f.components) || name != f.components[depth] {
		f.t.Fatalf("unexpected component %q at depth %d", name, depth)
	}
	f.events = append(f.events, "lstat:"+f.path(depth+1))
	return f.infos[depth], nil
}

func (f *mkdirCloseTraversalFixture) openRoot(depth int, name string) (Root, error) {
	if depth >= len(f.components) || name != f.components[depth] {
		f.t.Fatalf("unexpected open component %q at depth %d", name, depth)
	}
	f.events = append(f.events, "open-root:"+f.path(depth+1))
	return f.rootAt(depth + 1), nil
}

func (f *mkdirCloseTraversalFixture) close(depth int) error {
	f.events = append(f.events, "close-root:"+f.path(depth))
	return f.closeErrors[depth-1]
}

func (f *mkdirCloseTraversalFixture) path(depth int) string {
	return filepath.ToSlash(filepath.Join(f.components[:depth]...))
}

func dirFileInfo(name string) fs.FileInfo {
	return &modeOverrideFileInfo{
		FileInfo: &stubFileInfo{name: name, mode: os.ModeDir | 0o755},
		mode:     os.ModeDir | 0o755,
	}
}

func writePinnedTargetInfoPair(t *testing.T) (fs.FileInfo, fs.FileInfo) {
	t.Helper()
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "original")
	changedPath := filepath.Join(dir, "changed")
	if err := os.WriteFile(originalPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("changed"), 0o640); err != nil {
		t.Fatalf("seed changed target: %v", err)
	}
	return statTestPath(t, originalPath), statTestPath(t, changedPath)
}

func assertOverwritePinnedFileRejectsBeforeMutation(t *testing.T, openedInfo fs.FileInfo, lstat func(*testing.T) func(string) (fs.FileInfo, error), beforeRevalidate func(*testing.T) func() error) {
	t.Helper()

	truncateCalls := 0
	writeCalls := 0
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return openedInfo, nil },
			write: func(p []byte) (int, error) {
				writeCalls++
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(int64) error {
			truncateCalls++
			return nil
		},
	}
	root := &fakeRoot{
		lstat: lstat(t),
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), beforeRevalidate(t))
	if err == nil {
		t.Fatal("expected overwrite rejection before mutation")
	}
	if truncateCalls != 0 {
		t.Fatalf("expected no truncation before rejection, got %d calls", truncateCalls)
	}
	if writeCalls != 0 {
		t.Fatalf("expected no write before rejection, got %d calls", writeCalls)
	}
}

func openTestWriteRoot(t *testing.T, rootDir string, open func(string) (*WriteRoot, error)) *WriteRoot {
	t.Helper()
	root, err := open(rootDir)
	if err != nil {
		t.Fatalf("open test write root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close test write root: %v", closeErr)
		}
	})
	return root
}

func changedDirectoryTestRoot(t *testing.T) (*fakeRoot, *bool) {
	t.Helper()
	expectedInfo := statTestPath(t, t.TempDir())
	changedInfo := statTestPath(t, t.TempDir())
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return changedInfo, nil },
		close: func() error {
			childClosed = true
			return nil
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return expectedInfo, nil },
		openRoot: func(string) (Root, error) { return child, nil },
	}
	return root, &childClosed
}

func assertWriteRootRejectsParent(t *testing.T, root *WriteRoot, wantError, failureMessage string) {
	t.Helper()
	err := root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750)
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("%s, got %v", failureMessage, err)
	}
}

func assertPreservedExistingRegularFileMode(t *testing.T, write func(rootDir, targetPath string, data []byte) error, writeName string) {
	t.Helper()
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("chmod target file: %v", err)
	}

	if err := write(rootDir, targetPath, []byte("after")); err != nil {
		t.Fatalf("%s returned error: %v", writeName, err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(data) != "after" {
		t.Fatalf("unexpected content: got %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected existing file mode 0644 to be preserved, got %#o", info.Mode().Perm())
	}
}

func assertRejectsSymlinkedParentEscapingRoot(t *testing.T, write func(rootDir, targetPath string, data []byte) error) {
	t.Helper()
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	outsideDir := filepath.Join(parentDir, "outside")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "outside"), filepath.Join(rootDir, "src")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	targetPath := filepath.Join(rootDir, "src", writeTestFileName)
	err := write(rootDir, targetPath, []byte("secret"))
	if err == nil {
		t.Fatal("expected symlink escape write to fail")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside file to remain absent, got err=%v", statErr)
	}
}

func assertWriteUnderRejectsPathTraversalOutsideRoot(t *testing.T, write func(rootDir, targetPath string, data []byte) error) {
	t.Helper()
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	outsidePath := filepath.Join(parentDir, "secret.txt")
	err := write(rootDir, outsidePath, []byte("secret"))
	if err == nil {
		t.Fatal("expected error for outside path, got nil")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func assertWriteUnderRejectsNonDirectoryRoot(t *testing.T, write func(rootDir, targetPath string, data []byte) error) {
	t.Helper()
	rootDir := t.TempDir()
	rootFile := filepath.Join(rootDir, "root-file")
	if err := os.WriteFile(rootFile, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	err := write(rootFile, filepath.Join(rootFile, "child.txt"), []byte("hello"))
	if err == nil {
		t.Fatal("expected error when root is not a directory")
	}
	if !strings.Contains(err.Error(), "open root") && !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func assertWriteWithinRootRejectsAbsolutePath(t *testing.T, write func(root Root, targetPath string, data []byte, perm os.FileMode) error) {
	t.Helper()
	root := openTestRoot(t, t.TempDir())

	err := write(root, filepath.Join(t.TempDir(), writeTestFileName), []byte("hello"), 0o640)
	if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf("expected absolute path rejection, got %v", err)
	}
}

func TestWriteFileUnderWritesFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}

	if err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFileUnder returned error: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: got %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat written file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected file mode: got %#o", info.Mode().Perm())
	}
}

func TestWriteFileUnderPreservesExistingRegularFileMode(t *testing.T) {
	writeUnder := func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	}
	assertPreservedExistingRegularFileMode(t, writeUnder, "WriteFileUnder")
}

func TestWriteFileReplacingUnderPreservesExistingRegularFileMode(t *testing.T) {
	writeReplacingUnder := func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	}
	assertPreservedExistingRegularFileMode(t, writeReplacingUnder, "WriteFileReplacingUnder")
}

func TestWriteFileUnderRejectsReadOnlyExistingRegularFile(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o400); err != nil {
		t.Fatalf("chmod target file read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	err := WriteFileUnder(rootDir, targetPath, []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected read-only existing file to be rejected")
	}
	if !os.IsPermission(err) {
		t.Fatalf("expected permission error, got %v", err)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("read target file: %v", readErr)
	}
	if string(data) != "before" {
		t.Fatalf("expected read-only target to remain unchanged, got %q", string(data))
	}
}

func TestWriteFileWithinRootWritesRelativeFile(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	if err := WriteFileWithinRoot(root, writeTestFileName, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFileWithinRoot returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rootDir, writeTestFileName))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: got %q", string(data))
	}
}

func TestWriteFileWithinRootRejectsAbsolutePath(t *testing.T) {
	assertWriteWithinRootRejectsAbsolutePath(t, WriteFileWithinRoot)
}

func TestWriteFileWithinRootReturnsTempCreationError(t *testing.T) {
	expectedErr := errors.New("open temp failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) { return nil, expectedErr },
	}

	err := WriteFileWithinRoot(root, writeTestFileName, []byte("hello"), 0o640)
	if err == nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp creation error, got %v", err)
	}
}

func TestWriteFileUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	assertWriteUnderRejectsPathTraversalOutsideRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileUnderRejectsSymlinkedParentEscapingRoot(t *testing.T) {
	assertRejectsSymlinkedParentEscapingRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileReplacingUnderRejectsSymlinkedParentEscapingRoot(t *testing.T) {
	assertRejectsSymlinkedParentEscapingRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileReplacingUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	assertWriteUnderRejectsPathTraversalOutsideRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileReplacingUnderRejectsNonDirectoryRoot(t *testing.T) {
	assertWriteUnderRejectsNonDirectoryRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileUnderRejectsNonDirectoryRoot(t *testing.T) {
	assertWriteUnderRejectsNonDirectoryRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileUnderRootAbsFailureWhenCWDRemoved(t *testing.T) {
	withRemovedWorkingDir(t, "dead-root")

	err := WriteFileUnder(".", writeTestFileName, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") && !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestWriteFileUnderTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	rootDir := t.TempDir()
	withRemovedWorkingDir(t, "dead-target")

	err := WriteFileUnder(rootDir, "relative-target.txt", []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve target path") && !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestWriteFileUnderReturnsErrorForMissingParentDir(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "missing", writeTestFileName)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

func TestWriteRootCreatesMissingParentsAndWritesAtomically(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", "nested", writeTestFileName)
	if err := root.WriteFileCreatingParents(targetPath, []byte("hello"), 0o640, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParents returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rootDir, targetPath))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
	info, err := os.Stat(filepath.Join(rootDir, targetPath))
	if err != nil {
		t.Fatalf("stat written file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected file mode: %#o", info.Mode().Perm())
	}

	if err := root.WriteFileCreatingParents("root-file.txt", []byte("root"), 0o600, 0o750); err != nil {
		t.Fatalf("write root-level file: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootDir, "root-file.txt")); err != nil {
		t.Fatalf("read root-level file: %v", err)
	} else if string(data) != "root" {
		t.Fatalf("unexpected root-level content: %q", string(data))
	}
}

func TestOpenWriteRootPropagatesRootResolutionError(t *testing.T) {
	expectedErr := errors.New("root abs failure")
	withFileSystem(t, &fakeFileSystem{abs: func(string) (string, error) {
		return "", expectedErr
	}})

	root, err := OpenWriteRoot(".")
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected root: %v", closeErr)
		}
	}
	if !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "resolve root path") {
		t.Fatalf("expected root path resolution error, got %v", err)
	}
}

func TestOpenCanonicalWriteRootWritesInsideCanonicalRoot(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "canonical", "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical root: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		t.Fatalf("resolve canonical root: %v", err)
	}

	root := openTestWriteRoot(t, canonicalRoot, OpenCanonicalWriteRoot)

	targetPath := filepath.Join("reports", "nested", writeTestFileName)
	if err := root.WriteFileCreatingParents(targetPath, []byte("canonical"), 0o640, 0o750); err != nil {
		t.Fatalf("write through canonical root: %v", err)
	}
	assertFileContent(t, filepath.Join(canonicalRoot, targetPath), "canonical")
}

func TestWriteRootMkdirAllCreatesConfinedDirectories(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	dirPath := filepath.Join("keys", "nested")
	if err := root.MkdirAll(dirPath, 0o750); err != nil {
		t.Fatalf("mkdir within root: %v", err)
	}
	info, err := os.Stat(filepath.Join(rootDir, dirPath))
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", dirPath)
	}
}

func TestWriteRootMkdirAllDurableRejectsAbsolutePath(t *testing.T) {
	root := &WriteRoot{
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				t.Fatal("expected absolute-path rejection before root lookup")
				return nil, nil
			},
		},
	}

	err := root.MkdirAllDurable(string(os.PathSeparator)+"escape", 0o750)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute-path rejection, got %v", err)
	}
}

func TestMkdirAllDurableReturnsNilForRootTarget(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			t.Fatal("expected root target to return before root lookup")
			return nil, nil
		},
	}
	err := mkdirAllDurable(root, "/root", ".", 0o750)
	if err != nil {
		t.Fatalf("expected root-target durable mkdir to be a no-op, got %v", err)
	}
}

func TestMkdirAllDurableSkipsDotComponents(t *testing.T) {
	infoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(infoRoot, "keys"), 0o755); err != nil {
		t.Fatalf("mkdir info root: %v", err)
	}
	keysInfo := statTestPath(t, filepath.Join(infoRoot, "keys"))
	openRootCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case "keys":
				return keysInfo, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		openRoot: func(name string) (Root, error) {
			openRootCalls++
			if name != "keys" {
				t.Fatalf("unexpected open root part %q", name)
			}
			return &fakeRoot{
				lstat: func(name string) (fs.FileInfo, error) {
					if name != "." {
						t.Fatalf("unexpected child lstat for %q", name)
					}
					return keysInfo, nil
				},
				close: closeWithoutError,
			}, nil
		},
	}

	separator := string(os.PathSeparator)
	rawPath := "." + separator + "keys" + separator + "."
	if err := mkdirAllDurable(root, "/root", rawPath, 0o750); err != nil {
		t.Fatalf("mkdirAllDurable returned error: %v", err)
	}
	if openRootCalls != 1 {
		t.Fatalf("expected one real path component to be opened, got %d", openRootCalls)
	}
}

func TestMkdirAllDurableRejectsSymlinkComponent(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return &stubFileInfo{name: "keys", mode: os.ModeSymlink}, nil
		},
	}
	err := mkdirAllDurable(root, "/root", "keys", 0o750)
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestMkdirAllDurableRejectsNonDirectoryComponent(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return &stubFileInfo{name: "keys", mode: 0o644}, nil
		},
	}
	err := mkdirAllDurable(root, "/root", "keys", 0o750)
	if err == nil || !strings.Contains(err.Error(), "output parent is not a directory") {
		t.Fatalf("expected non-directory rejection, got %v", err)
	}
}

func TestMkdirAllDurablePropagatesOpenRootError(t *testing.T) {
	openErr := errors.New("open child root failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return dirFileInfo("keys"), nil
		},
		openRoot: func(string) (Root, error) {
			return nil, openErr
		},
	}
	err := mkdirAllDurable(root, "/root", "keys", 0o750)
	if !errors.Is(err, openErr) {
		t.Fatalf("expected open child root error, got %v", err)
	}
}

func TestMkdirAllDurableJoinsOpenedRootLookupAndCloseErrors(t *testing.T) {
	lookupErr := errors.New("opened child lookup failure")
	closeErr := errors.New("opened child close failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return dirFileInfo("keys"), nil
		},
		openRoot: func(string) (Root, error) {
			return &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return nil, lookupErr
				},
				close: func() error { return closeErr },
			}, nil
		},
	}
	err := mkdirAllDurable(root, "/root", "keys", 0o750)
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined opened-root lookup and close errors, got %v", err)
	}
}

func TestMkdirAllDurableRejectsChangedOpenedDirectory(t *testing.T) {
	infoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(infoRoot, "checked"), 0o755); err != nil {
		t.Fatalf("mkdir checked dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(infoRoot, "opened"), 0o755); err != nil {
		t.Fatalf("mkdir opened dir: %v", err)
	}
	checkedInfo := statTestPath(t, filepath.Join(infoRoot, "checked"))
	openedInfo := statTestPath(t, filepath.Join(infoRoot, "opened"))
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return checkedInfo, nil
		},
		openRoot: func(string) (Root, error) {
			return &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return openedInfo, nil
				},
				close: closeWithoutError,
			}, nil
		},
	}
	err := mkdirAllDurable(root, "/root", "keys", 0o750)
	if err == nil || !strings.Contains(err.Error(), "output parent changed while opening") {
		t.Fatalf("expected changed-directory rejection, got %v", err)
	}
}

func TestMkdirAllDurableReturnsOwnedRootCloseError(t *testing.T) {
	closeErr := errors.New("close child root failure")
	fixture := newMkdirCloseTraversalFixture(t, "keys")
	fixture.closeErrors[0] = closeErr

	err := mkdirAllDurable(fixture.rootAt(0), "/root", "keys", 0o750)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected owned-root close error, got %v", err)
	}
	wantEvents := []string{"lstat:keys", "open-root:keys", "lstat-opened:keys", "close-root:keys"}
	if !slices.Equal(fixture.events, wantEvents) {
		t.Fatalf("unexpected close traversal: got %#v want %#v", fixture.events, wantEvents)
	}
}

func TestMkdirAllDurableSkipsOnlyDotComponents(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			t.Fatal("expected dot-only durable mkdir to skip root lookups")
			return nil, nil
		},
	}
	err := mkdirAllDurable(root, "/root", "."+string(os.PathSeparator)+".", 0o750)
	if err != nil {
		t.Fatalf("expected dot-only durable mkdir to succeed, got %v", err)
	}
}

func TestMkdirAllDurableReturnsTrackedLookupError(t *testing.T) {
	lookupErr := errors.New("tracked lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, lookupErr
		},
	}
	err := mkdirAllDurable(root, "/root", "keys", 0o750)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected tracked lookup error, got %v", err)
	}
}

func TestMkdirAllDurableReturnsCurrentCloseErrorBetweenComponents(t *testing.T) {
	closeErr := errors.New("close intermediate root failure")
	fixture := newMkdirCloseTraversalFixture(t, "keys", "nested")
	fixture.closeErrors[0] = closeErr

	err := mkdirAllDurable(fixture.rootAt(0), "/root", filepath.Join("keys", "nested"), 0o750)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected intermediate close error, got %v", err)
	}
	wantEvents := []string{
		"lstat:keys",
		"open-root:keys",
		"lstat-opened:keys",
		"lstat:keys/nested",
		"open-root:keys/nested",
		"lstat-opened:keys/nested",
		"close-root:keys",
		"close-root:keys/nested",
	}
	if !slices.Equal(fixture.events, wantEvents) {
		t.Fatalf("unexpected close traversal: got %#v want %#v", fixture.events, wantEvents)
	}
}

func TestLstatOrCreateDirectoryTrackedReturnsLookupError(t *testing.T) {
	lookupErr := errors.New("lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, lookupErr
		},
	}
	_, created, err := lstatOrCreateDirectoryTracked(root, "keys", 0o750)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected lookup error, got %v", err)
	}
	if created {
		t.Fatal("expected lookup failure not to report creation")
	}
}

func TestLstatOrCreateDirectoryTrackedReturnsMkdirError(t *testing.T) {
	mkdirErr := errors.New("mkdir failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		mkdir: func(string, os.FileMode) error {
			return mkdirErr
		},
	}
	_, created, err := lstatOrCreateDirectoryTracked(root, "keys", 0o750)
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("expected mkdir error, got %v", err)
	}
	if created {
		t.Fatal("expected mkdir failure not to report creation")
	}
}

func TestLstatOrCreateDirectoryTrackedHandlesExistingDirectoryRace(t *testing.T) {
	lookupCalls := 0
	info := dirFileInfo("keys")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			lookupCalls++
			if lookupCalls == 1 {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		mkdir: func(string, os.FileMode) error {
			return fs.ErrExist
		},
	}
	got, created, err := lstatOrCreateDirectoryTracked(root, "keys", 0o750)
	if err != nil {
		t.Fatalf("expected existing-directory race to succeed, got %v", err)
	}
	if created {
		t.Fatal("expected raced existing directory not to report creation")
	}
	if got != info {
		t.Fatalf("expected post-race directory info to be returned, got %#v", got)
	}
}

func TestWriteRootMkdirAllRejectsSymlinkedDirectory(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootDir, "keys")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	err = root.MkdirAll(filepath.Join("keys", "nested"), 0o750)
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlinked directory rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(err) {
		t.Fatalf("expected no directory outside root, stat err=%v", err)
	}
}

func TestWriteRootReadRegularFileRejectsFinalSymlink(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	outsideFile := filepath.Join(t.TempDir(), "outside.key")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(rootDir, "key")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	_, _, err = root.ReadRegularFile("key")
	if err == nil || !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("expected final symlink rejection, got %v", err)
	}
}

func TestWriteRootReadRegularFileReportsChangedTarget(t *testing.T) {
	checkedPath := filepath.Join(t.TempDir(), "checked")
	openedPath := filepath.Join(t.TempDir(), "opened")
	if err := os.WriteFile(checkedPath, []byte("checked"), 0o600); err != nil {
		t.Fatalf("write checked file: %v", err)
	}
	if err := os.WriteFile(openedPath, []byte("opened"), 0o600); err != nil {
		t.Fatalf("write opened file: %v", err)
	}
	checkedInfo := statTestPath(t, checkedPath)
	openedInfo := statTestPath(t, openedPath)
	root := &WriteRoot{
		rootAbs: "/root",
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return checkedInfo, nil
			},
			open: func(string) (File, error) {
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return openedInfo, nil },
					close: func() error { return nil },
				}, nil
			},
		},
	}
	if _, _, err := root.ReadRegularFile("key"); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("expected changed-target error, got %v", err)
	}
}

func TestWriteRootReadRegularFileUnderLimitRejectsOversizedFile(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "key"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	if _, _, err := root.ReadRegularFileUnderLimit("key", 4); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestWriteRootReadPinnedRegularFileUnderLimitReportsChangedRoot(t *testing.T) {
	checkedRoot := t.TempDir()
	openedRoot := t.TempDir()
	targetPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(targetPath, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	rootInfo := statTestPath(t, checkedRoot)
	openedRootInfo := statTestPath(t, openedRoot)
	targetInfo := statTestPath(t, targetPath)
	root := &WriteRoot{
		rootAbs: checkedRoot,
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name == "." {
					return openedRootInfo, nil
				}
				return targetInfo, nil
			},
		},
	}

	if _, _, err := root.ReadPinnedRegularFileUnderLimit("key", rootInfo, 16); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("expected changed-root error, got %v", err)
	}
}

func TestWriteRootReadPinnedRegularFileUnderLimitRejectsAbsolutePath(t *testing.T) {
	root := &WriteRoot{
		rootAbs: "/root",
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				t.Fatal("expected absolute-path rejection before root lookup")
				return nil, nil
			},
		},
	}

	_, _, err := root.ReadPinnedRegularFileUnderLimit(string(os.PathSeparator)+"escape", nil, 64)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute-path rejection, got %v", err)
	}
}

func TestWriteRootReadPinnedRegularFileUnderLimitReturnsPinnedRootLookupError(t *testing.T) {
	rootLookupErr := errors.New("root lookup failure")
	root := &WriteRoot{
		rootAbs: "/root",
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "." {
					t.Fatalf("unexpected lstat path %q", name)
				}
				return nil, rootLookupErr
			},
		},
	}

	_, _, err := root.ReadPinnedRegularFileUnderLimit("key", dirFileInfo("."), 64)
	if !errors.Is(err, rootLookupErr) {
		t.Fatalf("expected pinned-root lookup error, got %v", err)
	}
}

func TestWriteRootReadPinnedRegularFileUnderLimitReturnsTargetLookupError(t *testing.T) {
	targetLookupErr := errors.New("target lookup failure")
	rootDir := t.TempDir()
	rootInfo := statTestPath(t, rootDir)
	root := &WriteRoot{
		rootAbs: rootDir,
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name == "." {
					return rootInfo, nil
				}
				return nil, targetLookupErr
			},
		},
	}

	_, _, err := root.ReadPinnedRegularFileUnderLimit("key", rootInfo, 64)
	if !errors.Is(err, targetLookupErr) {
		t.Fatalf("expected target lookup error, got %v", err)
	}
}

func TestWriteRootReadPinnedRegularFileUnderLimitRejectsNonRegularTarget(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if err := os.Mkdir(filepath.Join(rootDir, "key"), 0o755); err != nil {
		t.Fatalf("mkdir key: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	if _, _, err := root.ReadPinnedRegularFileUnderLimit("key", nil, 16); err == nil || !strings.Contains(err.Error(), "target path is not a regular file") {
		t.Fatalf("expected non-regular target rejection, got %v", err)
	}
}

func TestWriteRootReadRegularFileAdditionalBranches(t *testing.T) {
	t.Run("rejects non-regular target", func(t *testing.T) {
		root := &WriteRoot{
			rootAbs: "/root",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return statTestPath(t, t.TempDir()), nil
				},
			},
		}
		_, _, err := root.ReadRegularFile("dir")
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected non-regular file error, got %v", err)
		}
	})

	t.Run("propagates open stat and read errors", func(t *testing.T) {
		statErr := errors.New("stat failure")
		readErr := errors.New("read failure")
		path := filepath.Join(t.TempDir(), writeTestFileName)
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		info := statTestPath(t, path)
		root := &WriteRoot{
			rootAbs: "/root",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return info, nil },
				open: func(string) (File, error) {
					return &fakeFile{
						stat:  func() (fs.FileInfo, error) { return nil, statErr },
						close: closeWithoutError,
					}, nil
				},
			},
		}
		if _, _, err := root.ReadRegularFile("key"); !errors.Is(err, statErr) {
			t.Fatalf("expected stat error, got %v", err)
		}

		root.root = &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return info, nil },
			open: func(string) (File, error) {
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return info, nil },
					read:  func([]byte) (int, error) { return 0, readErr },
					close: closeWithoutError,
				}, nil
			},
		}
		if _, _, err := root.ReadRegularFile("key"); !errors.Is(err, readErr) {
			t.Fatalf("expected read error, got %v", err)
		}
	})
}

func TestWriteRootPathHelpersValidationAndCleanup(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	if err := root.MkdirAll(".", 0o750); err != nil {
		t.Fatalf("expected root mkdirall noop, got %v", err)
	}
	tempPath, tempFile, err := root.CreateTempFile(0o600)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := root.CleanupTempFile(tempPath, tempFile); err != nil {
		t.Fatalf("cleanup temp file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, tempPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected cleaned temp path to be absent, got %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "link", call: func() error { return root.Link(".", "x") }},
		{name: "link-new", call: func() error { return root.Link("x", ".") }},
		{name: "rename", call: func() error { return root.Rename(".", "x") }},
		{name: "rename-new", call: func() error { return root.Rename("x", ".") }},
		{name: "remove", call: func() error { return root.Remove(".") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatalf("expected %s root-target validation error", tc.name)
			}
		})
	}
}

func TestWriteRootPathHelpersPropagateRootErrors(t *testing.T) {
	linkErr := errors.New("link failure")
	renameErr := errors.New("rename failure")
	openErr := errors.New("open failure")
	root := &WriteRoot{
		rootAbs: "/root",
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				path := filepath.Join(t.TempDir(), writeTestFileName)
				if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
					t.Fatalf("write temp file: %v", err)
				}
				return statTestPath(t, path), nil
			},
			link:   func(string, string) error { return linkErr },
			rename: func(string, string) error { return renameErr },
			open:   func(string) (File, error) { return nil, openErr },
		},
	}
	if err := root.MkdirAll("/abs", 0o750); err == nil {
		t.Fatal("expected absolute mkdirall path to fail")
	}
	if err := root.Link("a", "b"); !errors.Is(err, linkErr) {
		t.Fatalf("expected link error, got %v", err)
	}
	if err := root.Rename("a", "b"); !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
	if _, _, err := root.ReadRegularFile("a"); !errors.Is(err, openErr) {
		t.Fatalf("expected open error, got %v", err)
	}
}

func TestOpenCanonicalWriteRootPropagatesResolutionError(t *testing.T) {
	expectedErr := errors.New("canonical abs failure")
	withFileSystem(t, &fakeFileSystem{abs: func(string) (string, error) {
		return "", expectedErr
	}})

	root, err := OpenCanonicalWriteRoot(".")
	if root != nil {
		t.Fatal("expected canonical write root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected canonical resolution error, got %v", err)
	}
}

func TestOpenCanonicalWriteRootWrapsOpenError(t *testing.T) {
	expectedErr := errors.New("canonical open failure")
	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(string) (Root, error) {
		return nil, expectedErr
	}})

	root, err := OpenCanonicalWriteRoot(t.TempDir())
	if root != nil {
		t.Fatal("expected canonical write root to remain nil")
	}
	if !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "open canonical root") {
		t.Fatalf("expected wrapped canonical open error, got %v", err)
	}
}

func TestOpenCanonicalWriteRootReturnsPinnedRoot(t *testing.T) {
	expectedRoot := &fakeRoot{close: closeWithoutError}
	withFileSystem(t, &fakeFileSystem{
		abs: func(path string) (string, error) {
			if path != "relative-root" {
				t.Fatalf("Abs path = %q, want relative-root", path)
			}
			return "/resolved/root", nil
		},
		openRootNoFollow: func(path string) (Root, error) {
			if path != "/resolved/root" {
				t.Fatalf("OpenRootNoFollow path = %q, want /resolved/root", path)
			}
			return expectedRoot, nil
		},
	})

	root, err := OpenCanonicalWriteRoot("relative-root")
	if err != nil {
		t.Fatalf("OpenCanonicalWriteRoot returned error: %v", err)
	}
	if root == nil {
		t.Fatal("expected canonical write root")
	}
	if root.root != expectedRoot {
		t.Fatal("expected returned WriteRoot to pin the opened root")
	}
	if root.rootAbs != "/resolved/root" {
		t.Fatalf("rootAbs = %q, want /resolved/root", root.rootAbs)
	}
}

func TestOpenRootNoFollowOpensVolumeRoot(t *testing.T) {
	volumeRoot := filepath.VolumeName(t.TempDir()) + string(os.PathSeparator)
	root, err := (&osFileSystem{}).OpenRootNoFollow(volumeRoot)
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close volume root: %v", err)
	}
}

func TestOpenRootNoFollowOpensNestedPathWithDotSegments(t *testing.T) {
	parent := t.TempDir()
	nested := filepath.Join(parent, "nested", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested path: %v", err)
	}

	root, err := (&osFileSystem{}).OpenRootNoFollow(filepath.Join(parent, ".", "nested", ".", "child"))
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error for nested path: %v", err)
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
		t.Fatalf("expected nested no-follow root to pin the requested directory")
	}
}

func TestOpenRootNoFollowDelegatesToConfiguredFileSystem(t *testing.T) {
	expected := &fakeRoot{}
	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(name string) (Root, error) {
		if name != "/delegated" {
			t.Fatalf("expected delegated path, got %q", name)
		}
		return expected, nil
	}})

	root, err := OpenRootNoFollow("/delegated")
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	if root != expected {
		t.Fatalf("expected delegated root, got %#v", root)
	}
}

func TestOpenRootNoFollowOpensNestedDirectory(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	nested := filepath.Join(parent, "child", "grandchild")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested root: %v", err)
	}

	root, err := (&osFileSystem{}).OpenRootNoFollow(nested)
	if err != nil {
		t.Fatalf("OpenRootNoFollow(%q): %v", nested, err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close nested root: %v", closeErr)
		}
	}()

	if _, err := root.Lstat("."); err != nil {
		t.Fatalf("Lstat nested root: %v", err)
	}
}

func TestOpenRootNoFollowRejectsInvalidComponents(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test parent: %v", err)
	}
	filePath := filepath.Join(parent, "file")
	if err := os.WriteFile(filePath, []byte("file"), 0o600); err != nil {
		t.Fatalf("write non-directory component: %v", err)
	}
	symlinkPath := filepath.Join(parent, "link")
	if err := os.Symlink(parent, symlinkPath); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: filepath.Join(parent, "missing"), want: "no such file"},
		{name: "file", path: filePath, want: "root is not a directory"},
		{name: "symlink", path: symlinkPath, want: "root contains symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := (&osFileSystem{}).OpenRootNoFollow(tc.path)
			if root != nil {
				if closeErr := root.Close(); closeErr != nil {
					t.Fatalf("close unexpected root: %v", closeErr)
				}
				t.Fatal("expected rejected root to remain nil")
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestOpenRootNoFollowAllowsTrustedTempAlias(t *testing.T) {
	aliasPath, resolvedPath, ok := tempDirAliasPair(t)
	if !ok {
		t.Skip("trusted temp alias unavailable")
	}

	root, err := (&osFileSystem{}).OpenRootNoFollow(aliasPath)
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close trusted alias root: %v", closeErr)
		}
	}()

	aliasInfo, err := os.Stat(resolvedPath)
	if err != nil {
		t.Fatalf("stat resolved trusted alias path: %v", err)
	}
	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat opened trusted alias root: %v", err)
	}
	if !os.SameFile(aliasInfo, openedInfo) {
		t.Fatalf("expected trusted alias root to pin resolved directory identity")
	}
}

func TestOpenRootExistingAncestorNoFollowRejectsUntrustedSymlinkAncestor(t *testing.T) {
	parentDir := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	linkPath := filepath.Join(parentDir, "cache-link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	root, ancestorPath, missingParts, err := OpenRootExistingAncestorNoFollow(filepath.Join(linkPath, "nested", "cache"))
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected root: %v", closeErr)
		}
		t.Fatal("expected untrusted symlink ancestor to be rejected")
	}
	if ancestorPath != "" || len(missingParts) != 0 {
		t.Fatalf("expected rejected ancestor walk to return no path state, got path=%q missing=%v", ancestorPath, missingParts)
	}
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestOpenRootExistingAncestorNoFollowAllowsTrustedAliasAncestor(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trusted aliases are only enabled on darwin")
	}

	requestedPath := uniqueTrustedAliasMissingPath(t, "cache")
	root, ancestorPath, missingParts, err := OpenRootExistingAncestorNoFollow(requestedPath)
	if err != nil {
		t.Fatalf("OpenRootExistingAncestorNoFollow returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close trusted alias ancestor root: %v", closeErr)
		}
	}()

	wantAncestorPath := filepath.Join(string(os.PathSeparator), "private", "tmp")
	if ancestorPath != wantAncestorPath {
		t.Fatalf("expected trusted alias ancestor path %q, got %q", wantAncestorPath, ancestorPath)
	}
	if len(missingParts) != 2 || missingParts[0] == "" || missingParts[1] != "cache" {
		t.Fatalf("unexpected missing parts: %v", missingParts)
	}

	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat trusted alias ancestor root: %v", err)
	}
	targetInfo, err := os.Stat(wantAncestorPath)
	if err != nil {
		t.Fatalf("stat trusted alias target: %v", err)
	}
	if !os.SameFile(openedInfo, targetInfo) {
		t.Fatalf("expected ancestor root to pin %q", wantAncestorPath)
	}
}

func TestTrustedRootAliasTargetGuards(t *testing.T) {
	untrustedTarget, ok := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "tmp", "nested"))
	if ok || untrustedTarget != "" {
		t.Fatalf("expected nested alias path to be rejected, got target=%q ok=%v", untrustedTarget, ok)
	}

	expectedTmpTarget := filepath.Join(string(os.PathSeparator), "private", "tmp")
	tmpTarget, tmpOK := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "tmp"))
	if runtime.GOOS == "darwin" {
		if !tmpOK || tmpTarget != expectedTmpTarget {
			t.Fatalf("expected /tmp alias target %q, got target=%q ok=%v", expectedTmpTarget, tmpTarget, tmpOK)
		}
		expectedVarTarget := filepath.Join(string(os.PathSeparator), "private", "var")
		varTarget, varOK := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "var"))
		if !varOK || varTarget != expectedVarTarget {
			t.Fatalf("expected /var alias target %q, got target=%q ok=%v", expectedVarTarget, varTarget, varOK)
		}
		return
	}
	if tmpOK || tmpTarget != "" {
		t.Fatalf("expected trusted aliases to be disabled on %s, got target=%q ok=%v", runtime.GOOS, tmpTarget, tmpOK)
	}
}

func TestOpenRootChildPinnedBranches(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		close: func() error { return nil },
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) {
			return child, nil
		},
	}

	opened, openedPath, err := (&osFileSystem{}).openRootChildPinned(root, "child", "/root/child")
	if err != nil || opened == nil || openedPath != "/root/child" {
		t.Fatalf("expected non-symlink child to open unchanged, got root=%v path=%q err=%v", opened, openedPath, err)
	}
	if closeErr := opened.Close(); closeErr != nil {
		t.Fatalf("close opened child: %v", closeErr)
	}

	linkDir := t.TempDir()
	linkInfoPath := filepath.Join(linkDir, "link-target")
	testFile := filepath.Join(linkInfoPath, "target")
	if err := os.MkdirAll(linkInfoPath, 0o755); err != nil {
		t.Fatalf("mkdir link target: %v", err)
	}
	linkPath := filepath.Join(linkDir, "link")
	if err := os.Symlink(testFile, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	symlinkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	untrustedRoot := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return symlinkInfo, nil },
	}
	rejected, rejectedPath, err := (&osFileSystem{}).openRootChildPinned(untrustedRoot, "link", filepath.Join(string(os.PathSeparator), "repo", "link"))
	if rejected != nil || rejectedPath != "" || err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected untrusted symlink child rejection, got root=%v path=%q err=%v", rejected, rejectedPath, err)
	}
}

func TestOpenRootChildPinnedPropagatesLookupError(t *testing.T) {
	lookupErr := errors.New("pinned child lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
	}

	opened, openedPath, err := (&osFileSystem{}).openRootChildPinned(root, "child", "/root/child")
	if opened != nil || openedPath != "" {
		t.Fatalf("expected lookup failure to return no child root, got root=%v path=%q", opened, openedPath)
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestOpenRootNoFollowWithPropagatesAbsAndVolumeOpenErrors(t *testing.T) {
	absErr := errors.New("abs path failure")
	absFn := func(string) (string, error) { return "", absErr }
	openRootFn := func(string) (Root, error) {
		t.Fatal("unexpected volume root open")
		return nil, nil
	}
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}
	root, err := openRootNoFollowWith("repo", absFn, filepath.Rel, openRootFn, openChildFn)
	if root != nil || !errors.Is(err, absErr) {
		t.Fatalf("expected abs error, got root=%v err=%v", root, err)
	}

	openErr := errors.New("open volume root")
	absFn = func(string) (string, error) { return filepath.Join(string(os.PathSeparator), "repo"), nil }
	openRootFn = func(string) (Root, error) {
		return nil, openErr
	}
	openChildFn = func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open after volume-root failure")
		return nil, "", nil
	}
	root, err = openRootNoFollowWith("repo", absFn, filepath.Rel, openRootFn, openChildFn)
	if root != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected volume-root open error, got root=%v err=%v", root, err)
	}
}

func TestOpenRootNoFollowWithJoinsOwnedRootCloseError(t *testing.T) {
	closeErr := errors.New("close traversed root")
	childErr := errors.New("open child failure")
	root := &fakeRoot{
		close: func() error { return closeErr },
	}
	absFn := func(string) (string, error) { return filepath.Join(string(os.PathSeparator), "repo", "child"), nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) { return nil, "", childErr }

	opened, err := openRootNoFollowWith(filepath.Join(string(os.PathSeparator), "repo", "child"), absFn, filepath.Rel, openRootFn, openChildFn)
	if opened != nil {
		t.Fatalf("expected traversal failure to return no root, got %#v", opened)
	}
	if !errors.Is(err, childErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined child and close errors, got %v", err)
	}
}

func TestOpenPinnedRootAliasWithPropagatesAliasRootAndStatErrors(t *testing.T) {
	openErr := errors.New("open trusted alias")
	openRootNoFollowFn := func(string) (Root, error) { return nil, openErr }
	opened, path, err := openPinnedRootAliasWith("/private/tmp", "/tmp", openRootNoFollowFn, os.Stat, os.SameFile)
	if opened != nil || path != "" || !errors.Is(err, openErr) {
		t.Fatalf("expected trusted-alias open error, got root=%v path=%q err=%v", opened, path, err)
	}

	statErr := errors.New("stat trusted alias")
	closeErr := errors.New("close trusted alias root")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return statTestPath(t, t.TempDir()), nil },
		close: func() error { return closeErr },
	}
	openRootNoFollowFn = func(string) (Root, error) { return root, nil }
	statFn := func(string) (fs.FileInfo, error) {
		return nil, statErr
	}
	opened, path, err = openPinnedRootAliasWith("/private/tmp", "/tmp", openRootNoFollowFn, statFn, os.SameFile)
	if opened != nil || path != "" {
		t.Fatalf("expected stat failure to return no trusted-alias root, got root=%v path=%q", opened, path)
	}
	if !errors.Is(err, statErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined stat and close errors, got %v", err)
	}
}

func TestOpenPinnedRootAliasWithRejectsChangedDirectoryIdentity(t *testing.T) {
	targetInfo := statTestPath(t, t.TempDir())
	openedInfo := statTestPath(t, t.TempDir())
	closeErr := errors.New("close changed alias root")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return openedInfo, nil },
		close: func() error { return closeErr },
	}
	openRootNoFollowFn := func(string) (Root, error) { return root, nil }
	statFn := func(string) (fs.FileInfo, error) { return targetInfo, nil }
	sameFileFn := func(fs.FileInfo, fs.FileInfo) bool { return false }

	opened, path, err := openPinnedRootAliasWith("/private/tmp", "/tmp", openRootNoFollowFn, statFn, sameFileFn)
	if opened != nil || path != "" {
		t.Fatalf("expected changed trusted-alias root to be rejected, got root=%v path=%q", opened, path)
	}
	if err == nil || !strings.Contains(err.Error(), "root changed while opening") || !errors.Is(err, closeErr) {
		t.Fatalf("expected changed trusted-alias root error with close failure, got %v", err)
	}
}

func TestOpenRootChildPinnedAllowsTrustedAliasTarget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trusted aliases are only enabled on darwin")
	}

	aliasInfo, err := os.Lstat(filepath.Join(string(os.PathSeparator), "tmp"))
	if err != nil {
		t.Fatalf("lstat /tmp alias: %v", err)
	}
	if aliasInfo.Mode()&os.ModeSymlink == 0 {
		t.Skip("/tmp is not exposed as a symlink on this host")
	}

	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return aliasInfo, nil },
	}
	opened, openedPath, err := (&osFileSystem{}).openRootChildPinned(root, "tmp", filepath.Join(string(os.PathSeparator), "tmp"))
	if err != nil {
		t.Fatalf("expected trusted alias child open to succeed, got %v", err)
	}
	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			t.Fatalf("close trusted alias child: %v", closeErr)
		}
	}()

	wantPath := filepath.Join(string(os.PathSeparator), "private", "tmp")
	if openedPath != wantPath {
		t.Fatalf("expected trusted alias target %q, got %q", wantPath, openedPath)
	}
}

func TestWriteRootOpenDirectoryReturnsPinnedRootWhenDirIsRoot(t *testing.T) {
	pinnedRoot := &fakeRoot{close: closeWithoutError}
	root := &WriteRoot{root: pinnedRoot, rootAbs: "/root"}

	dir, owned, err := root.openDirectory(".", false, 0o755)
	if err != nil {
		t.Fatalf("openDirectory returned error: %v", err)
	}
	if dir != pinnedRoot {
		t.Fatal("expected pinned root to be reused for root directory")
	}
	if owned {
		t.Fatal("expected pinned root to remain unowned")
	}
}

func TestWriteRootOpenDirectorySkipsDotComponents(t *testing.T) {
	root, secondRootClosed := newDotComponentTestWriteRoot(t, closeWithoutError, nil)

	separator := string(os.PathSeparator)
	rawPath := "first" + separator + "." + separator + "second" + separator + separator
	dir, owned, err := root.openDirectory(rawPath, false, 0o755)
	if err != nil {
		t.Fatalf("openDirectory returned error: %v", err)
	}
	if dir == nil {
		t.Fatal("expected nested root")
	}
	if !owned {
		t.Fatal("expected nested root to be owned")
	}
	if !*secondRootClosed {
		t.Fatal("expected intermediate root to be closed after opening nested directory")
	}
	if closeErr := dir.Close(); closeErr != nil {
		t.Fatalf("close returned root: %v", closeErr)
	}
}

func TestWriteRootOpenDirectoryJoinsIntermediateAndChildCloseErrors(t *testing.T) {
	intermediateCloseErr := errors.New("intermediate close failure")
	childCloseErr := errors.New("child close failure")
	root, _ := newDotComponentTestWriteRoot(t, func() error { return childCloseErr }, func() error { return intermediateCloseErr })

	dir, owned, err := root.openDirectory(filepath.Join("first", "second"), false, 0o755)
	if dir != nil {
		t.Fatal("expected returned directory to be nil on close failure")
	}
	if owned {
		t.Fatal("expected owned to be false on failure")
	}
	if !errors.Is(err, intermediateCloseErr) || !errors.Is(err, childCloseErr) {
		t.Fatalf("expected joined intermediate and child close errors, got %v", err)
	}
}

func newDotComponentTestWriteRoot(t *testing.T, childClose func() error, intermediateClose func() error) (*WriteRoot, *bool) {
	t.Helper()

	firstInfo := statTestPath(t, t.TempDir())
	secondInfo := statTestPath(t, t.TempDir())
	intermediateClosed := false
	if intermediateClose == nil {
		intermediateClose = func() error {
			intermediateClosed = true
			return nil
		}
	}

	return &WriteRoot{
		rootAbs: "/root",
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "first" {
					t.Fatalf("unexpected root lstat for %q", name)
				}
				return firstInfo, nil
			},
			openRoot: func(name string) (Root, error) {
				if name != "first" {
					t.Fatalf("unexpected root open for %q", name)
				}
				return newDotComponentIntermediateRoot(t, firstInfo, secondInfo, childClose, intermediateClose), nil
			},
		},
	}, &intermediateClosed
}

func newDotComponentIntermediateRoot(t *testing.T, firstInfo, secondInfo fs.FileInfo, childClose func() error, intermediateClose func() error) Root {
	t.Helper()

	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case ".":
				return firstInfo, nil
			case "second":
				return secondInfo, nil
			default:
				t.Fatalf("unexpected child lstat for %q", name)
				return nil, nil
			}
		},
		openRoot: func(name string) (Root, error) {
			if name != "second" {
				t.Fatalf("unexpected child open for %q", name)
			}
			return &fakeRoot{
				lstat: func(name string) (fs.FileInfo, error) {
					if name != "." {
						t.Fatalf("unexpected opened grandchild lstat for %q", name)
					}
					return secondInfo, nil
				},
				close: childClose,
			}, nil
		},
		close: intermediateClose,
	}
}

func TestWriteRootChmodDelegatesToPinnedRoot(t *testing.T) {
	called := false
	root := &WriteRoot{
		root: &fakeRoot{
			chmod: func(name string, perm os.FileMode) error {
				called = true
				if name != "reports/output.txt" {
					t.Fatalf("chmod name = %q, want reports/output.txt", name)
				}
				if perm != 0o640 {
					t.Fatalf("chmod perm = %#o, want 0640", perm)
				}
				return nil
			},
		},
	}

	if err := root.Chmod("reports/output.txt", 0o640); err != nil {
		t.Fatalf("WriteRoot.Chmod returned error: %v", err)
	}
	if !called {
		t.Fatal("expected WriteRoot.Chmod to delegate to pinned root")
	}
}

func TestWriteRootLstatRejectsAbsolutePath(t *testing.T) {
	root := &WriteRoot{
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				t.Fatal("expected absolute-path rejection before root lookup")
				return nil, nil
			},
		},
	}

	_, err := root.Lstat(string(os.PathSeparator) + "escape")
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute-path rejection, got %v", err)
	}
}

func TestWriteRootChmodRejectsAbsolutePath(t *testing.T) {
	root := &WriteRoot{
		root: &fakeRoot{
			chmod: func(string, os.FileMode) error {
				t.Fatal("expected absolute-path rejection before root chmod")
				return nil
			},
		},
	}

	err := root.Chmod(string(os.PathSeparator)+"escape", 0o600)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute-path rejection, got %v", err)
	}
}

func TestWriteRootWriteFileReplacingWritesRelativeFile(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve canonical root: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	if err := root.WriteFileReplacing("output.txt", []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacing returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "output.txt"), "after")
}

func TestWriteRootWriteFileReplacingRejectsAbsolutePath(t *testing.T) {
	root := &WriteRoot{
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				t.Fatal("expected absolute-path rejection before target lookup")
				return nil, nil
			},
		},
	}

	err := root.WriteFileReplacing(string(os.PathSeparator)+"escape", []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute-path rejection, got %v", err)
	}
}

func TestWriteRootWriteFileReplacingWithExactPermissionsRejectsAbsolutePath(t *testing.T) {
	root := &WriteRoot{
		rootAbs: "/root",
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				t.Fatal("expected absolute-path rejection before target lookup")
				return nil, nil
			},
		},
	}

	err := root.WriteFileReplacingWithExactPermissions(string(os.PathSeparator)+"escape", []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute-path rejection, got %v", err)
	}
}

func TestWriteRootReplacementMethodsPropagateLookupError(t *testing.T) {
	tests := []struct {
		name  string
		write func(*WriteRoot, string, []byte, os.FileMode) error
	}{
		{name: "preserve permissions", write: (*WriteRoot).WriteFileReplacing},
		{name: "exact permissions", write: (*WriteRoot).WriteFileReplacingWithExactPermissions},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookupErr := errors.New("target lookup failure")
			root := &WriteRoot{
				rootAbs: "/root",
				root: &fakeRoot{
					lstat: func(string) (fs.FileInfo, error) {
						return nil, lookupErr
					},
				},
			}

			err := tt.write(root, "output.txt", []byte("after"), 0o600)
			if !errors.Is(err, lookupErr) {
				t.Fatalf("expected target lookup error, got %v", err)
			}
		})
	}
}

func TestOpenRootChildNoFollowPropagatesLookupError(t *testing.T) {
	expectedErr := errors.New("child lookup failure")
	root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) {
		return nil, expectedErr
	}}

	child, err := openRootChildNoFollow(root, "child", "/root/child")
	if child != nil {
		t.Fatal("expected child root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected child lookup error, got %v", err)
	}
}

func TestOpenRootChildNoFollowRejectsSymlinkAndNonDirectory(t *testing.T) {
	linkDir := t.TempDir()
	targetPath := filepath.Join(linkDir, "target")
	if err := os.WriteFile(targetPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	linkPath := filepath.Join(linkDir, "link")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	symlinkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) { return symlinkInfo, nil }}
	if _, err := openRootChildNoFollow(root, "link", "/root/link"); err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}

	fileInfo := statTestPath(t, targetPath)
	root = &fakeRoot{lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil }}
	if _, err := openRootChildNoFollow(root, "file", "/root/file"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected non-directory rejection, got %v", err)
	}
}

func TestOpenRootChildNoFollowPropagatesOpenError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("child open failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) {
			return nil, expectedErr
		},
	}

	child, err := openRootChildNoFollow(root, "child", "/root/child")
	if child != nil {
		t.Fatal("expected child root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected child open error, got %v", err)
	}
}

func TestOpenRootChildNoFollowJoinsChildLookupAndCloseErrors(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	lookupErr := errors.New("opened child lookup failure")
	closeErr := errors.New("opened child close failure")
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
		close: func() error {
			childClosed = true
			return closeErr
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return child, nil },
	}

	opened, err := openRootChildNoFollow(root, "child", "/root/child")
	if opened != nil {
		t.Fatal("expected rejected child root to remain nil")
	}
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined lookup and close errors, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected rejected child root to be closed")
	}
}

func TestOpenRootChildNoFollowRejectsChangedDirectory(t *testing.T) {
	root, childClosed := changedDirectoryTestRoot(t)

	opened, err := openRootChildNoFollow(root, "child", "/root/child")
	if opened != nil {
		t.Fatal("expected changed child root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), "root changed while opening") {
		t.Fatalf("expected changed-directory error, got %v", err)
	}
	if !*childClosed {
		t.Fatal("expected changed child root to be closed")
	}
}

func TestOSRootOpenRootReturnsMissingDirectoryError(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	child, err := root.OpenRoot("missing")
	if child != nil {
		if closeErr := child.Close(); closeErr != nil {
			t.Fatalf("close unexpected child root: %v", closeErr)
		}
		t.Fatal("expected missing child root to remain nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing child root error, got %v", err)
	}
}

func TestWriteRootPropagatesParentLookupError(t *testing.T) {
	expectedErr := errors.New("parent lookup failure")
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return nil, expectedErr
			},
			close: func() error {
				return nil
			},
		}, nil
	}})

	root, err := OpenWriteRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	err = root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("hello"), 0o600, 0o750)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected parent lookup error, got %v", err)
	}
}

func TestWriteRootRejectsNonRelativeTargets(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	for _, targetPath := range []string{rootDir, "..", filepath.Join("..", writeTestFileName), "."} {
		err := root.WriteFileCreatingParents(targetPath, []byte("hello"), 0o600, 0o750)
		if err == nil {
			t.Fatalf("expected target %q to be rejected", targetPath)
		}
	}
}

func TestWriteRootRejectsSymlinkedParent(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, writeTestFileName)
	if err := os.WriteFile(outsideTarget, []byte("outside-before"), 0o600); err != nil {
		t.Fatalf("seed outside target: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	assertWriteRootRejectsParent(t, root, "output parent contains symlink", "expected symlinked parent rejection")
	data, readErr := os.ReadFile(outsideTarget)
	if readErr != nil {
		t.Fatalf("read outside target: %v", readErr)
	}
	if string(data) != "outside-before" {
		t.Fatalf("unexpected outside content: %q", string(data))
	}
}

func TestWriteRootRejectsNonDirectoryParent(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "reports"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write non-directory parent: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	assertWriteRootRejectsParent(t, root, "output parent is not a directory", "expected non-directory parent rejection")
}

func TestOpenTargetParentChildPropagatesOpenError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("parent open failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) {
			return nil, expectedErr
		},
	}

	child, err := openTargetParentChild(root, "parent", "/root/parent", false, 0)
	if child != nil {
		t.Fatal("expected parent root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected parent open error, got %v", err)
	}
}

func TestOpenTargetParentChildClosesChildOnLookupError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("opened parent lookup failure")
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
		close: func() error {
			childClosed = true
			return nil
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return child, nil },
	}

	opened, err := openTargetParentChild(root, "parent", "/root/parent", false, 0)
	if opened != nil {
		t.Fatal("expected rejected parent root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected opened parent lookup error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected rejected parent root to be closed")
	}
}

func TestOpenTargetParentChildRejectsChangedDirectory(t *testing.T) {
	root, childClosed := changedDirectoryTestRoot(t)

	opened, err := openTargetParentChild(root, "parent", "/root/parent", false, 0)
	if opened != nil {
		t.Fatal("expected changed parent root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), "output parent changed while opening") {
		t.Fatalf("expected changed parent error, got %v", err)
	}
	if !*childClosed {
		t.Fatal("expected changed parent root to be closed")
	}
}

func TestLstatOrCreateDirectoryPropagatesMkdirError(t *testing.T) {
	expectedErr := errors.New("mkdir parent failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		mkdir: func(string, os.FileMode) error {
			return expectedErr
		},
	}

	info, err := lstatOrCreateDirectory(root, "parent", true, 0o750)
	if info != nil {
		t.Fatal("expected directory info to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected mkdir error, got %v", err)
	}
}

func TestOpenTargetParentClosesOwnedParentAfterDescendantError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("descendant lookup failure")
	ownedClosed := false
	owned := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return dirInfo, nil
			}
			return nil, expectedErr
		},
		close: func() error {
			ownedClosed = true
			return nil
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return owned, nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("first", "second", writeTestFileName)}

	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if parent != nil || closeParent {
		t.Fatal("expected descendant failure to return no parent root")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected descendant lookup error, got %v", err)
	}
	if !ownedClosed {
		t.Fatal("expected owned parent root to be closed")
	}
}

func TestOpenTargetParentReturnsExistingRootForTopLevelTarget(t *testing.T) {
	root := &fakeRoot{}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: writeTestFileName}

	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if err != nil {
		t.Fatalf("expected top-level target parent lookup to succeed, got %v", err)
	}
	if parent != root {
		t.Fatalf("expected top-level target to reuse the write root, got %#v", parent)
	}
	if closeParent {
		t.Fatal("expected top-level target parent to remain caller-owned")
	}
}

func TestOpenTargetParentClosesNextWhenOwnedParentCloseFails(t *testing.T) {
	firstInfo := statTestPath(t, t.TempDir())
	secondInfo := statTestPath(t, t.TempDir())
	closeErr := errors.New("owned parent close failure")
	nextCloseErr := errors.New("next parent close failure")
	nextClosed := false
	next := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return secondInfo, nil },
		close: func() error {
			nextClosed = true
			return nextCloseErr
		},
	}
	owned := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return firstInfo, nil
			}
			return secondInfo, nil
		},
		openRoot: func(string) (Root, error) { return next, nil },
		close:    func() error { return closeErr },
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return firstInfo, nil },
		openRoot: func(string) (Root, error) { return owned, nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("first", "second", writeTestFileName)}

	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if parent != nil || closeParent {
		t.Fatal("expected close failure to return no parent root")
	}
	if !errors.Is(err, closeErr) || !errors.Is(err, nextCloseErr) {
		t.Fatalf("expected joined parent close errors, got %v", err)
	}
	if !nextClosed {
		t.Fatal("expected next parent root to be closed")
	}
}

func TestWriteFileAtTargetJoinsReadyAndParentCloseErrors(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	readyErr := errors.New("parent ready failure")
	closeErr := errors.New("parent close failure")
	parentClosed := false
	parent := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		close: func() error {
			parentClosed = true
			return closeErr
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return parent, nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("parent", writeTestFileName)}
	originalReady := writeFileParentReadyFn
	writeFileParentReadyFn = func() error { return readyErr }
	t.Cleanup(func() {
		writeFileParentReadyFn = originalReady
	})

	err := writeRoot.writeFileAtTarget(target, []byte("data"), 0o600, false, 0)
	if !errors.Is(err, readyErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined ready and close errors, got %v", err)
	}
	if !parentClosed {
		t.Fatal("expected pinned parent root to be closed")
	}
}

func TestWriteFileAtRootReturnsExistingTargetCloseError(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed existing target: %v", err)
	}
	fileInfo := statTestPath(t, targetPath)
	expectedErr := errors.New("existing target close failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{close: func() error { return expectedErr }}, nil
		},
	}
	target := rootedTarget{rel: writeTestFileName, abs: targetPath}

	err := writeFileAtRoot(root, target, []byte("after"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected existing target close error, got %v", err)
	}
}

func TestWriteFileAtRootReturnsAtomicSessionCreationError(t *testing.T) {
	expectedErr := errors.New("atomic temp creation failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, expectedErr
		},
	}
	target := rootedTarget{rel: writeTestFileName, abs: filepath.Join(t.TempDir(), writeTestFileName)}

	err := writeFileAtRoot(root, target, []byte("data"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected atomic session creation error, got %v", err)
	}
}

func TestResolvedWriteFilePermPropagatesLookupError(t *testing.T) {
	expectedErr := errors.New("target lookup failure")
	root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) {
		return nil, expectedErr
	}}
	target := rootedTarget{rel: writeTestFileName, abs: filepath.Join(t.TempDir(), writeTestFileName)}

	perm, existing, err := resolvedWriteFilePerm(root, target, 0o600)
	if perm != 0 || existing != nil {
		t.Fatalf("expected empty permission result, got perm=%#o existing=%v", perm, existing)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected target lookup error, got %v", err)
	}
}

func TestWriteRootDoesNotCreateOutsideAfterMissingParentSwap(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	outsideSentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(outsideSentinel, []byte("outside-before"), 0o600); err != nil {
		t.Fatalf("seed outside sentinel: %v", err)
	}

	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			mkdir: func(path string, perm os.FileMode) error {
				if err := os.Symlink(outside, filepath.Join(rootDir, "reports")); err != nil {
					return err
				}
				return root.Mkdir(path, perm)
			},
		}, nil
	}})

	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	err = root.WriteFileCreatingParents(filepath.Join("reports", "nested", writeTestFileName), []byte("after"), 0o600, 0o750)
	if err == nil {
		t.Fatal("expected swapped parent symlink to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside nested directory to remain absent, got err=%v", statErr)
	}
	data, readErr := os.ReadFile(outsideSentinel)
	if readErr != nil {
		t.Fatalf("read outside sentinel: %v", readErr)
	}
	if string(data) != "outside-before" {
		t.Fatalf("unexpected outside sentinel: %q", string(data))
	}
}

func TestWriteRootPinsParentBeforeInRootSymlinkRetarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "reports")
	relocatedParent := filepath.Join(rootDir, "reports-relocated")
	redirectedParent := filepath.Join(rootDir, "redirected")
	if err := os.MkdirAll(originalParent, 0o755); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}
	if err := os.MkdirAll(redirectedParent, 0o755); err != nil {
		t.Fatalf("mkdir redirected parent: %v", err)
	}
	originalTarget := filepath.Join(originalParent, writeTestFileName)
	redirectedTarget := filepath.Join(redirectedParent, writeTestFileName)
	if err := os.WriteFile(originalTarget, []byte("original-before"), 0o600); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(redirectedTarget, []byte("redirected-before"), 0o600); err != nil {
		t.Fatalf("seed redirected target: %v", err)
	}

	originalReady := writeFileParentReadyFn
	writeFileParentReadyFn = func() error {
		if err := os.Rename(originalParent, relocatedParent); err != nil {
			return err
		}
		return os.Symlink(filepath.Base(redirectedParent), originalParent)
	}
	t.Cleanup(func() {
		writeFileParentReadyFn = originalReady
	})

	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	err = root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750)
	if err != nil {
		t.Fatalf("WriteFileCreatingParents returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(relocatedParent, writeTestFileName), "after")
	assertFileContent(t, redirectedTarget, "redirected-before")
}

func TestWriteFileUnderRejectsRootPathTarget(t *testing.T) {
	rootDir := t.TempDir()
	err := WriteFileUnder(rootDir, rootDir, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root directory target error")
	}
}

func TestWriteFileUnderRejectsExistingDirectoryTarget(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "existing")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("create directory target: %v", err)
	}

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected directory target error")
	}
}

func TestWriteFileUnderRejectsSymlinkTarget(t *testing.T) {
	rootDir := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.Symlink(outsidePath, targetPath); err != nil {
		t.Fatalf("create target symlink: %v", err)
	}

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected symlink target error")
	}
	data, readErr := os.ReadFile(outsidePath)
	if readErr != nil {
		t.Fatalf("read outside file: %v", readErr)
	}
	if string(data) != "secret" {
		t.Fatalf("expected outside file to remain unchanged, got %q", string(data))
	}
	info, statErr := os.Lstat(targetPath)
	if statErr != nil {
		t.Fatalf("lstat target symlink: %v", statErr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected target path to remain a symlink, got mode %v", info.Mode())
	}
}

func TestCreateAtomicTempFileInRootDir(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempRel, tempFile, err := createAtomicTempFile(root, ".", 0o600)
	if err != nil {
		t.Fatalf("createAtomicTempFile returned error: %v", err)
	}
	if strings.Contains(tempRel, string(os.PathSeparator)) {
		t.Fatalf("expected root-relative temp file name, got %q", tempRel)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf(closeTempFileErrFmt, err)
	}
	if err := root.Remove(tempRel); err != nil {
		t.Fatalf("remove temp file: %v", err)
	}
}

func TestCreateAtomicTempFileReturnsErrorForMissingDir(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	_, tempFile, err := createAtomicTempFile(root, "missing", 0o600)
	if tempFile != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			t.Fatalf(closeTempFileErrFmt, closeErr)
		}
	}
	if err == nil {
		t.Fatal("expected missing-dir temp file error")
	}
}

func TestCreateAtomicTempFilePropagatesRandomNameError(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) { return "", errors.New("boom") }
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	_, _, err := createAtomicTempFile(root, ".", 0o600)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected random temp name error, got %v", err)
	}
}

func TestCreateAtomicTempFileFailsAfterRepeatedCollisions(t *testing.T) {
	rootDir := t.TempDir()

	seedFile, err := os.OpenFile(filepath.Join(rootDir, "fixed"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create colliding temp file: %v", err)
	}
	if _, err := seedFile.Write([]byte("x")); err != nil {
		t.Fatalf("seed colliding temp file: %v", err)
	}
	if err := seedFile.Close(); err != nil {
		t.Fatalf("close colliding temp file: %v", err)
	}

	root := openTestRoot(t, rootDir)

	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) { return "fixed", nil }
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	_, _, err = createAtomicTempFile(root, ".", 0o600)
	if err == nil || !strings.Contains(err.Error(), "too many collisions") {
		t.Fatalf("expected collision exhaustion error, got %v", err)
	}
}

func TestCleanupAtomicTempFileIgnoresClosedFileAndMissingTempPath(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempFile, err := root.OpenFile("temp", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf(closeTempFileErrFmt, err)
	}
	if err := root.Remove("temp"); err != nil {
		t.Fatalf("remove temp file: %v", err)
	}

	if err := cleanupAtomicTempFile(root, "temp", tempFile); err != nil {
		t.Fatalf("expected cleanupAtomicTempFile to ignore benign cleanup errors, got %v", err)
	}
}

func TestCleanupAtomicTempFileReturnsRootRemoveError(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempFile, err := root.OpenFile("temp", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf(closeRootErrFmt, err)
	}

	err = cleanupAtomicTempFile(root, "temp", tempFile)
	if err == nil {
		t.Fatal("expected root remove error after closing root")
	}
}

func TestCleanupAtomicTempFileJoinsCloseAndRemoveErrors(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)
	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}

	err := cleanupAtomicTempFile(root, "temp", &os.File{})
	if err == nil {
		t.Fatal("expected cleanupAtomicTempFile to join close and remove errors")
	}
	var closeErrno syscall.Errno
	if !errors.Is(err, fs.ErrInvalid) && !errors.As(err, &closeErrno) {
		t.Fatalf("expected joined cleanup error to include a stable close failure, got %v", err)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected joined cleanup error to include closed root remove, got %v", err)
	}
}

func TestCleanupAtomicTempFileRejectsAbsoluteTempPath(t *testing.T) {
	root := &fakeRoot{
		remove: func(string) error {
			t.Fatal("expected absolute-path rejection before temp removal")
			return nil
		},
	}
	err := cleanupAtomicTempFile(root, string(os.PathSeparator)+"escape", nil)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute temp-path rejection, got %v", err)
	}
}

func TestCleanupAtomicTempFileJoinsCloseAndAbsoluteTempPathErrors(t *testing.T) {
	closeErr := errors.New("close temp failure")
	err := cleanupAtomicTempFile(&fakeRoot{}, string(os.PathSeparator)+"escape", &fakeFile{
		close: func() error { return closeErr },
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error to be preserved, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute temp-path rejection to be joined, got %v", err)
	}
}

func TestAtomicWriteSessionCloseTempFileNoopWhenAlreadyClosed(t *testing.T) {
	session := &atomicWriteSession{}

	if err := session.closeTempFile(); err != nil {
		t.Fatalf("expected nil closeTempFile error, got %v", err)
	}
}

func TestAtomicWriteSessionReturnsTempWriteErrorBeforeFlushing(t *testing.T) {
	writeErr := errors.New("temp write failure")
	session := &atomicWriteSession{
		root: &fakeRoot{
			rename: func(string, string) error {
				t.Fatal("rename should not run after temp write failure")
				return nil
			},
		},
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func([]byte) (int, error) { return 0, writeErr },
			chmod: func(os.FileMode) error {
				t.Fatal("chmod should not run after temp write failure")
				return nil
			},
			sync: func() error {
				t.Fatal("sync should not run after temp write failure")
				return nil
			},
			close: func() error {
				t.Fatal("close should not run after temp write failure")
				return nil
			},
		},
	}

	err := session.writeAndCommit([]byte("data"), 0o600)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected temp write error, got %v", err)
	}
	if session.tempRel != "temp.json" {
		t.Fatalf("expected temp path to remain after write failure, got %q", session.tempRel)
	}
}

func TestAtomicWriteSessionReturnsTempChmodErrorBeforeSync(t *testing.T) {
	chmodErr := errors.New("temp chmod failure")
	session := &atomicWriteSession{
		root: &fakeRoot{
			rename: func(string, string) error {
				t.Fatal("rename should not run after chmod failure")
				return nil
			},
		},
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func(data []byte) (int, error) { return len(data), nil },
			chmod: func(os.FileMode) error { return chmodErr },
			sync: func() error {
				t.Fatal("sync should not run after chmod failure")
				return nil
			},
			close: func() error {
				t.Fatal("close should not run after chmod failure")
				return nil
			},
		},
	}

	err := session.writeAndCommit([]byte("data"), 0o600)
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected temp chmod error, got %v", err)
	}
	if session.tempRel != "temp.json" {
		t.Fatalf("expected temp path to remain after chmod failure, got %q", session.tempRel)
	}
}

func TestAtomicWriteSessionReturnsTempSyncErrorBeforeClose(t *testing.T) {
	syncErr := errors.New("temp sync failure")
	session := &atomicWriteSession{
		root: &fakeRoot{
			rename: func(string, string) error {
				t.Fatal("rename should not run after temp sync failure")
				return nil
			},
		},
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func(data []byte) (int, error) { return len(data), nil },
			chmod: chmodWithoutError,
			sync:  func() error { return syncErr },
			close: func() error {
				t.Fatal("close should not run after temp sync failure")
				return nil
			},
		},
	}

	err := session.writeAndCommit([]byte("data"), 0o600)
	if !errors.Is(err, syncErr) {
		t.Fatalf("expected temp sync error, got %v", err)
	}
	if session.tempRel != "temp.json" {
		t.Fatalf("expected temp path to remain after sync failure, got %q", session.tempRel)
	}
}

func TestAtomicWriteSessionReturnsTempCloseErrorBeforeRename(t *testing.T) {
	closeErr := errors.New("temp close failure")
	session := &atomicWriteSession{
		root: &fakeRoot{
			rename: func(string, string) error {
				t.Fatal("rename should not run after temp close failure")
				return nil
			},
		},
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func(data []byte) (int, error) { return len(data), nil },
			chmod: chmodWithoutError,
			sync:  func() error { return nil },
			close: func() error { return closeErr },
		},
	}

	err := session.writeAndCommit([]byte("data"), 0o600)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected temp close error, got %v", err)
	}
	if session.tempRel != "temp.json" {
		t.Fatalf("expected temp path to remain after close failure, got %q", session.tempRel)
	}
}

func TestAtomicWriteSessionReturnsRenameErrorAfterFlushingTempFile(t *testing.T) {
	renameErr := errors.New("rename failure")
	events := make([]string, 0, 5)
	session := &atomicWriteSession{
		root: &fakeRoot{
			rename: func(string, string) error {
				events = append(events, "rename")
				return renameErr
			},
		},
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func(data []byte) (int, error) {
				events = append(events, "write")
				return len(data), nil
			},
			chmod: func(os.FileMode) error {
				events = append(events, "chmod")
				return nil
			},
			sync: func() error {
				events = append(events, "sync")
				return nil
			},
			close: func() error {
				events = append(events, "close")
				return nil
			},
		},
	}

	err := session.writeAndCommit([]byte("data"), 0o600)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
	expected := []string{"write", "chmod", "sync", "close", "rename"}
	if !slices.Equal(events, expected) {
		t.Fatalf("unexpected event order: got %#v want %#v", events, expected)
	}
	if session.tempRel != "temp.json" {
		t.Fatalf("expected temp path to remain for cleanup after rename failure, got %q", session.tempRel)
	}
}

func TestRandomTempName(t *testing.T) {
	name, err := randomTempName()
	if err != nil {
		t.Fatalf("randomTempName returned error: %v", err)
	}
	if !strings.HasPrefix(name, atomicTempPrefix) {
		t.Fatalf("expected temp name prefix %q, got %q", atomicTempPrefix, name)
	}
	if len(name) <= len(atomicTempPrefix) {
		t.Fatalf("expected random suffix in temp name, got %q", name)
	}
}

func TestRandomTempNamePropagatesReadError(t *testing.T) {
	originalRandReadFn := randReadFn
	randReadFn = func([]byte) (int, error) { return 0, errors.New("boom") }
	defer func() {
		randReadFn = originalRandReadFn
	}()

	_, err := randomTempName()
	if err == nil || !strings.Contains(err.Error(), "generate temp name") {
		t.Fatalf("expected random read error, got %v", err)
	}
}

func withAtomicWriteFileSystem(t *testing.T, tempFile File, remove func(string) error) {
	t.Helper()
	if remove == nil {
		remove = func(string) error {
			return nil
		}
	}
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return nil, os.ErrNotExist
			},
			openFile: func(string, int, os.FileMode) (File, error) {
				return tempFile, nil
			},
			remove: remove,
			close: func() error {
				return nil
			},
		}, nil
	}})
}

func TestWriteFileUnderCloseRootError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	expectedErr := errors.New("close root failure")
	withRootCloseError(t, expectedErr)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root close failure to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected root close error, got %v", err)
	}
}

func TestWriteFileUnderJoinsPrimaryErrorWithCleanupFailure(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	writeErr := errors.New("write failure")
	cleanupErr := errors.New("cleanup failure")
	tempFile := &fakeFile{
		write: func([]byte) (int, error) {
			return 0, writeErr
		},
		close: func() error {
			return nil
		},
	}
	remove := func(string) error {
		return cleanupErr
	}
	withAtomicWriteFileSystem(t, tempFile, remove)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error to be joined, got %v", err)
	}
}

func TestWriteFileReplacingWithinRootJoinsRenameErrorWithCleanupFailure(t *testing.T) {
	renameErr := errors.New("rename failure")
	cleanupErr := errors.New("cleanup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error {
			return renameErr
		},
		remove: func(string) error {
			return cleanupErr
		},
		close: closeWithoutError,
	}

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected rename error")
	}
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error to be joined, got %v", err)
	}
}

func TestWriteFileReplacingWithinRootReturnsRenameErrorWithoutFallback(t *testing.T) {
	renameErr := errors.New("rename failure")
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)

	targetOpened := false
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				targetOpened = true
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return info, nil },
					close: closeWithoutError,
				}, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error {
			return renameErr
		},
		remove: func(string) error { return nil },
	}

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("hello"), 0o600)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error without fallback, got %v", err)
	}
	if runtime.GOOS == "windows" && !targetOpened {
		t.Fatal("expected existing target writability probe on Windows")
	}
	if runtime.GOOS != "windows" && targetOpened {
		t.Fatal("expected non-Windows rename failure to skip pinned target probe")
	}
}

func TestOpenPinnedReplacementTargetReturnsOpenedFile(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedFile := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return info, nil },
		close: closeWithoutError,
	}
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return expectedFile, nil
		},
	}

	file, err := openPinnedReplacementTarget(root, writeTestFileName, info)
	if err != nil {
		t.Fatalf("expected opened pinned replacement target, got %v", err)
	}
	if file != expectedFile {
		t.Fatalf("expected returned file %p, got %p", expectedFile, file)
	}
}

func TestWriteAtomicReplacementReturnsPinnedTargetCloseErrorAfterCommit(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	closeErr := errors.New("pinned target close failure")

	root := &fakeRoot{
		open: func(string) (File, error) {
			return &fakeFile{sync: func() error { return nil }, close: closeWithoutError}, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return info, nil },
					close: func() error { return closeErr },
				}, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacement(root, rootedTarget{rel: writeTestFileName}, []byte("after"), 0o600, info, atomicReplacementOptions{
		allowInPlaceFallback: true,
	})
	if runtime.GOOS == "windows" {
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected pinned target close error, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("expected non-Windows commit to succeed without pinned target close, got %v", err)
	}
}

func TestWriteAtomicReplacementReturnsPinnedTargetOpenError(t *testing.T) {
	expectedErr := errors.New("open pinned target failure")
	root := &fakeRoot{
		open: func(string) (File, error) {
			return &fakeFile{sync: func() error { return nil }, close: closeWithoutError}, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return nil, expectedErr
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacement(root, rootedTarget{rel: writeTestFileName}, []byte("after"), 0o600, statTestPath(t, t.TempDir()), atomicReplacementOptions{
		allowInPlaceFallback: true,
	})
	if runtime.GOOS == "windows" {
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected pinned target open error, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("expected non-Windows write to skip pinned target open, got %v", err)
	}
}

func TestWriteAtomicReplacementRejectsInvalidTargetBeforeParentOpen(t *testing.T) {
	root := &fakeRoot{
		openRoot: func(string) (Root, error) {
			t.Fatal("invalid target reached parent traversal")
			return nil, nil
		},
	}
	target := rootedTarget{rel: string(os.PathSeparator) + writeTestFileName}

	err := writeAtomicReplacement(root, target, []byte("after"), 0o600, nil, atomicReplacementOptions{})
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected invalid target rejection, got %v", err)
	}
}

func TestWriteAtomicReplacementReturnsParentLookupError(t *testing.T) {
	parentErr := errors.New("parent lookup failure")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "sub" {
				t.Fatalf("unexpected parent lookup %q", name)
			}
			return nil, parentErr
		},
	}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("sub", writeTestFileName)}

	err := writeAtomicReplacement(root, target, []byte("after"), 0o600, nil, atomicReplacementOptions{})
	if !errors.Is(err, parentErr) {
		t.Fatalf("expected parent lookup error, got %v", err)
	}
}

func TestWriteAtomicReplacementJoinsFailureWithPinnedParentCloseError(t *testing.T) {
	parentInfo := statTestPath(t, t.TempDir())
	writeErr := errors.New("candidate creation failure")
	closeErr := errors.New("pinned parent close failure")
	parentClosed := false
	parent := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected opened-parent lookup %q", name)
			}
			return parentInfo, nil
		},
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, writeErr
		},
		close: func() error {
			parentClosed = true
			return closeErr
		},
	}
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "sub" {
				t.Fatalf("unexpected parent lookup %q", name)
			}
			return parentInfo, nil
		},
		openRoot: func(name string) (Root, error) {
			if name != "sub" {
				t.Fatalf("unexpected parent open %q", name)
			}
			return parent, nil
		},
	}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("sub", writeTestFileName)}

	err := writeAtomicReplacement(root, target, []byte("after"), 0o600, nil, atomicReplacementOptions{})
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected candidate and parent-close errors, got %v", err)
	}
	if !parentClosed {
		t.Fatal("expected pinned parent root to close")
	}
}

func TestOpenPinnedReplacementTargetReturnsOpenError(t *testing.T) {
	expectedErr := errors.New("open target failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, expectedErr
		},
	}

	file, err := openPinnedReplacementTarget(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if file != nil {
		t.Fatal("expected pinned target file to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected open target error, got %v", err)
	}
}

func TestOpenPinnedReplacementTargetKeepsStatErrorWhenCloseAlsoFails(t *testing.T) {
	statErr := errors.New("stat target failure")
	closeErr := errors.New("close target failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return nil, statErr },
				close: func() error { return closeErr },
			}, nil
		},
	}

	file, err := openPinnedReplacementTarget(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if file != nil {
		t.Fatal("expected pinned target file to remain nil")
	}
	if !errors.Is(err, statErr) {
		t.Fatalf("expected stat error, got %v", err)
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("expected close error to remain secondary, got %v", err)
	}
}

func TestOpenPinnedReplacementTargetRejectsChangedTarget(t *testing.T) {
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "original")
	changedPath := filepath.Join(dir, "changed")
	if err := os.WriteFile(originalPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("changed"), 0o640); err != nil {
		t.Fatalf("seed changed target: %v", err)
	}
	originalInfo := statTestPath(t, originalPath)
	changedInfo := statTestPath(t, changedPath)

	tests := []struct {
		name       string
		openedInfo fs.FileInfo
	}{
		{name: "non-regular", openedInfo: &modeOverrideFileInfo{FileInfo: changedInfo, mode: os.ModeDir | 0o755}},
		{name: "different identity", openedInfo: changedInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closeCalls := 0
			root := &fakeRoot{
				openFile: func(string, int, os.FileMode) (File, error) {
					return &fakeFile{
						stat: func() (fs.FileInfo, error) { return tt.openedInfo, nil },
						close: func() error {
							closeCalls++
							return nil
						},
					}, nil
				},
			}

			file, err := openPinnedReplacementTarget(root, writeTestFileName, originalInfo)
			if file != nil {
				t.Fatal("expected pinned target file to remain nil")
			}
			if err == nil || !strings.Contains(err.Error(), "target changed while opening for replacement") {
				t.Fatalf("expected target change rejection, got %v", err)
			}
			if closeCalls != 1 {
				t.Fatalf("expected rejected pinned target to close once, got %d closes", closeCalls)
			}
		})
	}
}

func TestOverwritePinnedFileWritesAfterIdentityRevalidation(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)

	data := "before"
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				data += string(p)
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			data = ""
			return nil
		},
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}

	if err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil); err != nil {
		t.Fatalf("overwrite pinned file: %v", err)
	}
	if data != "after" {
		t.Fatalf("unexpected pinned target data: %q", data)
	}
}

func TestOverwritePinnedFileReturnsBeforeRevalidateError(t *testing.T) {
	expectedErr := errors.New("before revalidate failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			t.Fatal("expected early return before path revalidation")
			return nil, nil
		},
	}

	err := overwritePinnedFile(root, writeTestFileName, &fakeFile{}, []byte("after"), func() error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected beforeRevalidate error, got %v", err)
	}
}

func TestOverwritePinnedFilePropagatesTargetLookupError(t *testing.T) {
	expectedErr := errors.New("target lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
	}

	err := overwritePinnedFile(root, writeTestFileName, &fakeFile{}, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected target lookup error, got %v", err)
	}
}

func TestOverwritePinnedFileRejectsTargetSwapBeforeMutation(t *testing.T) {
	originalInfo, swappedInfo := writePinnedTargetInfoPair(t)

	tests := []struct {
		name        string
		swappedInfo fs.FileInfo
	}{
		{name: "symlink", swappedInfo: &modeOverrideFileInfo{FileInfo: swappedInfo, mode: os.ModeSymlink | 0o777}},
		{name: "different identity", swappedInfo: swappedInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			swapInjected := false
			lstatAfterSwap := func(t *testing.T) func(string) (fs.FileInfo, error) {
				t.Helper()
				return func(string) (fs.FileInfo, error) {
					if !swapInjected {
						t.Fatal("target revalidated before swap seam")
					}
					return tt.swappedInfo, nil
				}
			}
			injectSwap := func(t *testing.T) func() error {
				t.Helper()
				return func() error {
					swapInjected = true
					return nil
				}
			}
			assertOverwritePinnedFileRejectsBeforeMutation(t, originalInfo, lstatAfterSwap, injectSwap)
			if !swapInjected {
				t.Fatal("expected swap injection seam to run")
			}
		})
	}
}

func TestOverwritePinnedFileRejectsNonRegularPathBeforeMutation(t *testing.T) {
	originalPath := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(originalPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	originalInfo := statTestPath(t, originalPath)

	for _, tt := range unsafeReplacementPathCases(originalInfo) {
		t.Run(tt.name, func(t *testing.T) {
			statCalls := 0
			target := &truncatingFakeFile{
				fakeFile: &fakeFile{
					stat: func() (fs.FileInfo, error) {
						statCalls++
						return originalInfo, nil
					},
					write: func(p []byte) (int, error) { return len(p), nil },
					close: closeWithoutError,
				},
				truncate: func(int64) error { return nil },
			}
			root := &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return tt.info, nil },
			}

			err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q rejection, got %v", tt.want, err)
			}
			if statCalls != 0 {
				t.Fatalf("expected no opened-file stat after path rejection, got %d calls", statCalls)
			}
		})
	}
}

func TestOverwritePinnedFilePropagatesOpenedFileStatError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedErr := errors.New("opened target stat failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return nil, expectedErr },
		close: closeWithoutError,
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected opened file stat error, got %v", err)
	}
}

func TestOverwritePinnedFileRejectsFileWithoutTruncate(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return info, nil },
		close: closeWithoutError,
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if err == nil || !strings.Contains(err.Error(), "does not support truncation") {
		t.Fatalf("expected truncation support error, got %v", err)
	}
}

func TestOverwritePinnedFilePropagatesTruncateError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedErr := errors.New("truncate target failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat:  func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) { return len(p), nil },
			close: closeWithoutError,
		},
		truncate: func(int64) error { return expectedErr },
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected truncate error, got %v", err)
	}
}

func TestOverwritePinnedFilePropagatesWriteError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedErr := errors.New("write target failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func([]byte) (int, error) {
				return 0, expectedErr
			},
			close: closeWithoutError,
		},
		truncate: func(int64) error { return nil },
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestCloseFilePreservingPrimaryReturnsCloseErrorWithoutPrimary(t *testing.T) {
	expectedErr := errors.New("close file failure")
	file := &fakeFile{
		close: func() error { return expectedErr },
	}

	err := closeFilePreservingPrimary(file, nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestCloseFilePreservingPrimaryKeepsPrimaryError(t *testing.T) {
	primaryErr := errors.New("primary failure")
	closeErr := errors.New("close file failure")
	file := &fakeFile{
		close: func() error { return closeErr },
	}

	err := closeFilePreservingPrimary(file, primaryErr)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("expected primary error, got %v", err)
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("expected close error to remain secondary, got %v", err)
	}
}

func TestWriteFileReplacingUnderWritesFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}

	if err := WriteFileReplacingUnder(rootDir, targetPath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFileReplacingUnder returned error: %v", err)
	}
	assertFileContent(t, targetPath, "hello")
}

func TestWriteFileReplacingUnderReturnsCloseRootError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	expectedErr := errors.New("close root failure")
	withRootCloseError(t, expectedErr)

	err := WriteFileReplacingUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected root close error, got %v", err)
	}
}

func TestWriteFileReplacingWithinRootRejectsAbsolutePath(t *testing.T) {
	assertWriteWithinRootRejectsAbsolutePath(t, WriteFileReplacingWithinRoot)
}

func TestWriteFileUnderWriteError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	writeErr := errors.New("write failure")
	tempFile := &fakeFile{
		write: func([]byte) (int, error) {
			return 0, writeErr
		},
		close: func() error {
			return nil
		},
	}
	withAtomicWriteFileSystem(t, tempFile, nil)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestWriteFileUnderTempFileOperationErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeFile, error)
		assertion string
		expected  error
	}{
		{
			name:      "chmod",
			assertion: "expected chmod error",
			expected:  errors.New("chmod failure"),
			configure: configureTempChmodError,
		},
		{
			name:      "close",
			assertion: "expected temp close error",
			expected:  errors.New("temp close failure"),
			configure: configureTempCloseError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			targetPath := filepath.Join(rootDir, writeTestFileName)
			tempFile := &fakeFile{
				write: func(data []byte) (int, error) {
					return len(data), nil
				},
			}
			tc.configure(tempFile, tc.expected)
			withAtomicWriteFileSystem(t, tempFile, nil)

			err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
			if err == nil {
				t.Fatal(tc.assertion)
			}
			if !errors.Is(err, tc.expected) {
				t.Fatalf("%s, got %v", tc.assertion, err)
			}
		})
	}
}

func configureTempChmodError(file *fakeFile, err error) {
	file.chmod = func(os.FileMode) error {
		return err
	}
	file.close = closeWithoutError
}

func configureTempCloseError(file *fakeFile, err error) {
	file.chmod = chmodWithoutError
	file.close = func() error {
		return err
	}
}

func chmodWithoutError(os.FileMode) error {
	return nil
}

func closeWithoutError() error {
	return nil
}

func TestResolveWriteTargetAbsFailuresViaFileSystem(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hookPath func(rootDir, targetPath string) string
		hookErr  error
		expected string
	}{
		{
			name: "root",
			hookPath: func(rootDir, _ string) string {
				return rootDir
			},
			hookErr:  errors.New("root abs failure"),
			expected: "resolve root path",
		},
		{
			name: "target",
			hookPath: func(_, targetPath string) string {
				return targetPath
			},
			hookErr:  errors.New("target abs failure"),
			expected: "resolve target path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			targetPath := filepath.Join(rootDir, writeTestFileName)

			withFileSystem(t, &fakeFileSystem{abs: func(path string) (string, error) {
				if path == tc.hookPath(rootDir, targetPath) {
					return "", tc.hookErr
				}
				return (&osFileSystem{}).Abs(path)
			}})

			err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
			if err == nil {
				t.Fatalf("expected %s error", tc.expected)
			}
			if !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf(unexpectedErrFmt, err)
			}
		})
	}
}

func TestResolveWriteTargetRelFailureViaFileSystem(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	withFileSystem(t, &fakeFileSystem{rel: func(_, _ string) (string, error) {
		return "", errors.New("rel failure")
	}})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected relative path computation error")
	}
	if !strings.Contains(err.Error(), "compute relative path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestMoveFileUnderRenamesWithinRootAndSetsMode(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileUnder returned error: %v", err)
	}

	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source file to be moved away, got %v", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected moved content %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat moved file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected moved file mode: got %#o", info.Mode().Perm())
	}
}

func TestMoveFileUnderFallsBackToCopyAndSetsMode(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("copied"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			rename: func(oldName, newName string) error {
				if strings.Contains(oldName, atomicTempPrefix) {
					return root.Rename(oldName, newName)
				}
				return syscall.EXDEV
			},
		}, nil
	}})

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileUnder returned error: %v", err)
	}

	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source file to be removed after copy fallback, got %v", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "copied" {
		t.Fatalf("unexpected copied content %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected copied file mode: got %#o", info.Mode().Perm())
	}
}

func TestMoveFileUnderReturnsRenameErrorWithoutFallback(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	renameErr := errors.New("rename failure")

	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			chmod: func(string, os.FileMode) error {
				return nil
			},
			mkdirAll: func(string, os.FileMode) error {
				return nil
			},
			rename: func(string, string) error {
				return renameErr
			},
			remove: func(string) error {
				return nil
			},
			close: func() error {
				return nil
			},
		}, nil
	}})

	err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error without fallback copy, got %v", err)
	}
}

func TestMoveFileWithinRootRejectsAbsoluteSourcePath(t *testing.T) {
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error {
			t.Fatal("expected source-path rejection before root operations")
			return nil
		},
	}

	err := MoveFileWithinRoot(root, string(os.PathSeparator)+"source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute source rejection, got %v", err)
	}
}

func TestMoveFileWithinRootRejectsAbsoluteTargetPath(t *testing.T) {
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error {
			t.Fatal("expected target-path rejection before root operations")
			return nil
		},
	}

	err := MoveFileWithinRoot(root, "source", string(os.PathSeparator)+"target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute target rejection, got %v", err)
	}
}

func TestMoveFileWithinRootPreservesSourceOnNonEXDEVRenameError(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "snapshots"), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	renameErr := errors.New("rename failure")
	root := openTestRoot(t, rootDir)
	failingRoot := &fakeRoot{
		Root: root,
		rename: func(oldName, newName string) error {
			if oldName != filepath.Join("snapshots", "temp.json") || newName != filepath.Join("snapshots", "final.json") {
				t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			}
			return renameErr
		},
	}

	err := MoveFileWithinRoot(failingRoot, filepath.Join("snapshots", "temp.json"), filepath.Join("snapshots", "final.json"), 0o750, 0o640)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error without fallback copy, got %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("expected source file to remain after rename error, got %v", err)
	}
	if _, err := os.Stat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected target file to remain absent after rename error, got %v", err)
	}
}

func TestMoveFileWithinRootPreservesSourceOnChmodError(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "snapshots"), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	chmodErr := errors.New("chmod failure")
	root := openTestRoot(t, rootDir)
	failingRoot := &fakeRoot{
		Root: root,
		chmod: func(name string, perm os.FileMode) error {
			if name != filepath.Join("snapshots", "temp.json") || perm != 0o640 {
				t.Fatalf("unexpected chmod %q %#o", name, perm)
			}
			return chmodErr
		},
		rename: func(oldName, newName string) error {
			t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			return nil
		},
	}

	err := MoveFileWithinRoot(failingRoot, filepath.Join("snapshots", "temp.json"), filepath.Join("snapshots", "final.json"), 0o750, 0o640)
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error without fallback copy, got %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("expected source file to remain after chmod error, got %v", err)
	}
	if _, err := os.Stat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected target file to remain absent after chmod error, got %v", err)
	}
}

func TestMoveFileUnderValidationAndSetupErrors(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")

	t.Run("source escapes root", func(t *testing.T) {
		err := MoveFileUnder(rootDir, filepath.Join(rootDir, "..", "temp.json"), targetPath, 0o750, 0o640)
		if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
			t.Fatalf("expected source escape error, got %v", err)
		}
	})

	t.Run("target escapes root", func(t *testing.T) {
		err := MoveFileUnder(rootDir, sourcePath, filepath.Join(rootDir, "..", "final.json"), 0o750, 0o640)
		if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
			t.Fatalf("expected target escape error, got %v", err)
		}
	})

	t.Run("open root error", func(t *testing.T) {
		openRootErr := errors.New("open root failure")
		withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
			return nil, openRootErr
		}})

		err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
		if !errors.Is(err, openRootErr) {
			t.Fatalf("expected open root error, got %v", err)
		}
	})

	t.Run("mkdir all error", func(t *testing.T) {
		mkdirErr := errors.New("mkdir failure")
		withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
			return &fakeRoot{
				mkdirAll: func(string, os.FileMode) error {
					return mkdirErr
				},
				close: closeWithoutError,
			}, nil
		}})

		err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
		if !errors.Is(err, mkdirErr) {
			t.Fatalf("expected mkdir error, got %v", err)
		}
	})
}

func TestMoveFileUnderCopyFallbackIgnoresMissingSourceRemoval(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")

	withMoveFallbackFileSystem(t, moveFallbackConfig{
		sourcePath:      filepath.Join("snapshots", "temp.json"),
		sourceData:      "copied",
		sourceRemoveErr: os.ErrNotExist,
	})

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("expected os.ErrNotExist source cleanup to be ignored, got %v", err)
	}
}

func TestMoveFileWithinRootReturnsSourceRemovalErrorAfterCopyFallback(t *testing.T) {
	sourceRemoveErr := errors.New("remove source failure")
	root := newMoveFallbackRoot(moveFallbackConfig{
		sourcePath:      "source",
		sourceData:      "copied",
		sourceRemoveErr: sourceRemoveErr,
	})

	if err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640); !errors.Is(err, sourceRemoveErr) {
		t.Fatalf("expected source removal error after copy fallback, got %v", err)
	}
}

func TestMoveFileWithinRootPreservesSourceWhenCopyFallbackFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage moveFallbackFailureStage
	}{
		{name: "open source", stage: moveFallbackFailSourceOpen},
		{name: "read source", stage: moveFallbackFailSourceRead},
		{name: "open temp", stage: moveFallbackFailTempOpen},
		{name: "write temp", stage: moveFallbackFailTempWrite},
		{name: "chmod temp", stage: moveFallbackFailTempChmod},
		{name: "close temp", stage: moveFallbackFailTempClose},
		{name: "rename temp", stage: moveFallbackFailTempRename},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New(tc.name + " failure")
			state := &moveFallbackState{}
			cfg := moveFallbackConfig{
				sourcePath:   "source",
				sourceData:   "copied",
				failureStage: tc.stage,
				failureErr:   sentinel,
				state:        state,
			}

			err := MoveFileWithinRoot(newMoveFallbackRoot(cfg), "source", "target", 0o750, 0o640)
			if err == nil {
				t.Fatal("expected copy fallback failure")
			}
			if !errors.Is(err, syscall.EXDEV) {
				t.Fatalf("expected EXDEV fallback error, got %v", err)
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("expected %s error, got %v", tc.name, err)
			}

			assertMoveFallbackFailureState(t, state, "copied")
		})
	}
}

type moveFallbackConfig struct {
	sourcePath      string
	sourceData      string
	sourceReadErr   error
	sourceCloseErr  error
	tempRemoveErr   error
	sourceRemoveErr error
	rootCloseErr    error
	failureStage    moveFallbackFailureStage
	failureErr      error
	state           *moveFallbackState
}

type moveFallbackFailureStage uint8

const (
	moveFallbackFailSourceOpen moveFallbackFailureStage = iota + 1
	moveFallbackFailSourceRead
	moveFallbackFailTempOpen
	moveFallbackFailTempWrite
	moveFallbackFailTempChmod
	moveFallbackFailTempClose
	moveFallbackFailTempRename
)

type moveFallbackState struct {
	sourceData   string
	sourceExists bool
	tempData     string
	tempExists   bool
	targetData   string
	targetExists bool
}

func withMoveFallbackFileSystem(t *testing.T, cfg moveFallbackConfig) {
	t.Helper()
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return newMoveFallbackRoot(cfg), nil
	}})
}

func newMoveFallbackRoot(cfg moveFallbackConfig) *fakeRoot {
	state := newMoveFallbackState(cfg)
	return &fakeRoot{
		open:     newMoveFallbackOpenHook(cfg, state),
		openFile: newMoveFallbackOpenFileHook(cfg, state),
		chmod: func(string, os.FileMode) error {
			return nil
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
		rename:   newMoveFallbackRenameHook(cfg, state),
		remove:   newMoveFallbackRemoveHook(cfg, state),
		close:    func() error { return cfg.rootCloseErr },
	}
}

func newMoveFallbackState(cfg moveFallbackConfig) *moveFallbackState {
	state := cfg.state
	if state == nil {
		state = &moveFallbackState{}
	}
	state.sourceData = cfg.sourceData
	state.sourceExists = true
	return state
}

func (c *moveFallbackConfig) failure(stage moveFallbackFailureStage) error {
	if stage == moveFallbackFailSourceRead && c.sourceReadErr != nil {
		return c.sourceReadErr
	}
	if c.failureStage == stage {
		return c.failureErr
	}
	return nil
}

func newMoveFallbackOpenHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) (File, error) {
	return func(name string) (File, error) {
		if name != cfg.sourcePath {
			return nil, errors.New("unexpected source open path")
		}
		if err := cfg.failure(moveFallbackFailSourceOpen); err != nil {
			return nil, err
		}
		if !state.sourceExists {
			return nil, os.ErrNotExist
		}
		return newMoveFallbackSourceFile(cfg, state), nil
	}
}

func newMoveFallbackSourceFile(cfg moveFallbackConfig, state *moveFallbackState) File {
	reader := strings.NewReader(state.sourceData)
	return &fakeFile{
		read: func(p []byte) (int, error) {
			if err := cfg.failure(moveFallbackFailSourceRead); err != nil {
				return 0, err
			}
			return reader.Read(p)
		},
		close: func() error { return cfg.sourceCloseErr },
	}
}

func newMoveFallbackOpenFileHook(cfg moveFallbackConfig, state *moveFallbackState) func(string, int, os.FileMode) (File, error) {
	return func(name string, _ int, _ os.FileMode) (File, error) {
		if !isMoveFallbackTempPath(name) {
			return nil, errors.New("unexpected temp file path")
		}
		if err := cfg.failure(moveFallbackFailTempOpen); err != nil {
			return nil, err
		}
		state.tempData = ""
		state.tempExists = true
		return newMoveFallbackTempFile(cfg, state), nil
	}
}

func newMoveFallbackTempFile(cfg moveFallbackConfig, state *moveFallbackState) File {
	return &fakeFile{
		write: func(p []byte) (int, error) {
			if err := cfg.failure(moveFallbackFailTempWrite); err != nil {
				return 0, err
			}
			state.tempData += string(p)
			return len(p), nil
		},
		close: func() error { return cfg.failure(moveFallbackFailTempClose) },
		chmod: func(os.FileMode) error { return cfg.failure(moveFallbackFailTempChmod) },
	}
}

func newMoveFallbackRenameHook(cfg moveFallbackConfig, state *moveFallbackState) func(string, string) error {
	return func(oldName, _ string) error {
		if oldName == cfg.sourcePath {
			return syscall.EXDEV
		}
		if !isMoveFallbackTempPath(oldName) {
			return nil
		}
		if err := cfg.failure(moveFallbackFailTempRename); err != nil {
			return err
		}
		state.targetData = state.tempData
		state.targetExists = true
		state.tempData = ""
		state.tempExists = false
		return nil
	}
}

func newMoveFallbackRemoveHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) error {
	return func(name string) error {
		if isMoveFallbackTempPath(name) {
			state.tempData = ""
			state.tempExists = false
			return cfg.tempRemoveErr
		}
		if name != cfg.sourcePath {
			return nil
		}
		if cfg.sourceRemoveErr == nil {
			state.sourceExists = false
		}
		return cfg.sourceRemoveErr
	}
}

func isMoveFallbackTempPath(name string) bool {
	return strings.Contains(name, atomicTempPrefix)
}

func assertMoveFallbackFailureState(t *testing.T, state *moveFallbackState, wantSourceData string) {
	t.Helper()
	if !state.sourceExists || state.sourceData != wantSourceData {
		t.Fatalf("expected source to remain %q after fallback failure, got exists=%t data=%q", wantSourceData, state.sourceExists, state.sourceData)
	}
	if state.targetExists {
		t.Fatalf("expected target to remain absent after fallback failure, got %q", state.targetData)
	}
	if state.tempExists {
		t.Fatalf("expected temp cleanup after fallback failure, found %q", state.tempData)
	}
}

func copyRootWithOpenError(openErr error) *fakeRoot {
	return &fakeRoot{
		open: func(string) (File, error) { return nil, openErr },
	}
}

func copyRootWithTempOpenError(openFileErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{openFileErr: openFileErr})
}

func copyRootWithTempChmodError(chmodErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{data: "x", chmodErr: chmodErr})
}

func copyRootWithTempCloseError(closeErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{data: "x", closeErr: closeErr})
}

func copyRootWithTempRenameError(renameErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{data: "x", renameErr: renameErr})
}

type tempFileOptions struct {
	data        string
	openFileErr error
	chmodErr    error
	closeErr    error
	renameErr   error
}

func copyRootWithTempFile(opts tempFileOptions) *fakeRoot {
	return &fakeRoot{
		open: func(string) (File, error) {
			return &fakeFile{
				read: func(p []byte) (int, error) {
					copy(p, opts.data)
					return len(opts.data), io.EOF
				},
				close: closeWithoutError,
			}, nil
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
		openFile: func(string, int, os.FileMode) (File, error) {
			if opts.openFileErr != nil {
				return nil, opts.openFileErr
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				close: func() error { return opts.closeErr },
				chmod: func(os.FileMode) error { return opts.chmodErr },
			}, nil
		},
		rename: func(oldName, _ string) error {
			if strings.Contains(oldName, atomicTempPrefix) {
				return opts.renameErr
			}
			return nil
		},
		remove: func(string) error { return nil },
	}
}

func TestCreateTempFileWithinRootRejectsAbsoluteDirectory(t *testing.T) {
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			t.Fatal("expected absolute-path rejection before temp create")
			return nil, nil
		},
	}
	_, _, err := CreateTempFileWithinRoot(root, string(os.PathSeparator)+"escape", 0o600)
	if err == nil || !strings.Contains(err.Error(), "path escapes root") {
		t.Fatalf("expected absolute temp-dir rejection, got %v", err)
	}
}

func TestOpenRootReturnsUsableRoot(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	}()

	file, err := root.Open("file.txt")
	if err != nil {
		t.Fatalf("open file through root: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close root-opened file: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read file through root: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected root-opened content %q", string(data))
	}
}

func TestCreateTempFileWithinRootCreatesRemovableTempFile(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempRel, tempFile, err := CreateTempFileWithinRoot(root, ".", 0o600)
	if err != nil {
		t.Fatalf("CreateTempFileWithinRoot returned error: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(tempRel), atomicTempPrefix) {
		t.Fatalf("expected atomic temp prefix, got %q", tempRel)
	}

	if _, err := tempFile.Write([]byte("hello")); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := CleanupTempFileWithinRoot(root, tempRel, tempFile); err != nil {
		t.Fatalf("cleanup temp file: %v", err)
	}
	if _, err := root.Open(tempRel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected temp file removal, got %v", err)
	}
}

func TestMoveFileUnderJoinsPrimaryAndCleanupErrors(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	primaryErr := errors.New("copy read failure")
	sourceCloseErr := errors.New("close source failure")
	tempRemoveErr := errors.New("remove temp failure")
	sourceRemoveErr := errors.New("remove source failure")
	rootCloseErr := errors.New("close root failure")

	withMoveFallbackFileSystem(t, moveFallbackConfig{
		sourcePath:      filepath.Join("snapshots", "temp.json"),
		sourceReadErr:   primaryErr,
		sourceCloseErr:  sourceCloseErr,
		tempRemoveErr:   tempRemoveErr,
		sourceRemoveErr: sourceRemoveErr,
		rootCloseErr:    rootCloseErr,
	})

	err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
	if err == nil {
		t.Fatal("expected MoveFileUnder error")
	}
	for _, expected := range []error{primaryErr, sourceCloseErr, tempRemoveErr, rootCloseErr} {
		if !errors.Is(err, expected) {
			t.Fatalf("expected joined error to include %v, got %v", expected, err)
		}
	}
	if errors.Is(err, sourceRemoveErr) {
		t.Fatalf("expected fallback failure to preserve source instead of removing it, got %v", err)
	}
}

func TestPrepareAndRenameWithinRootErrors(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	chmodRoot := &fakeRoot{
		chmod: func(string, os.FileMode) error {
			return chmodErr
		},
	}
	if err := prepareAndRenameWithinRoot(chmodRoot, "source", "target", 0o640); !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error, got %v", err)
	}

	renameErr := errors.New("rename failure")
	renameRoot := &fakeRoot{
		chmod: func(string, os.FileMode) error {
			return nil
		},
		rename: func(string, string) error {
			return renameErr
		},
	}
	if err := prepareAndRenameWithinRoot(renameRoot, "source", "target", 0o640); !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
}

func TestCopyFileWithinRootErrorBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		root func(error) *fakeRoot
	}{
		{name: "open source error", err: errors.New("open source failure"), root: copyRootWithOpenError},
		{name: "create temp file error", err: errors.New("open temp failure"), root: copyRootWithTempOpenError},
		{name: "temp chmod error", err: errors.New("temp chmod failure"), root: copyRootWithTempChmodError},
		{name: "temp close error", err: errors.New("temp close failure"), root: copyRootWithTempCloseError},
		{name: "rename temp file error", err: errors.New("rename temp failure"), root: copyRootWithTempRenameError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := copyFileWithinRoot(tc.root(tc.err), "source", "target", 0o640)
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected %s, got %v", tc.name, err)
			}
		})
	}
}
