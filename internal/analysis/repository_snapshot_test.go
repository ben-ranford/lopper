package analysis

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestSnapshotRepositoryRootReportsCreationAndCopyErrors(t *testing.T) {
	t.Run("temporary directory", func(t *testing.T) {
		blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(blockingPath, []byte("file"), 0o600); err != nil {
			t.Fatalf("write blocking temporary path: %v", err)
		}
		t.Setenv("TMPDIR", blockingPath)
		if _, err := snapshotRepositoryRoot(context.Background(), &snapshotRootStub{}, ""); err == nil {
			t.Fatal("expected repository snapshot temporary-directory failure")
		}
	})

	t.Run("source directory read", func(t *testing.T) {
		expectedErr := errors.New("source directory unavailable")
		source := &snapshotRootStub{
			directories: map[string]snapshotReadDirResult{
				".": {err: expectedErr},
			},
		}
		if _, err := snapshotRepositoryRoot(context.Background(), source, ""); !errors.Is(err, expectedErr) {
			t.Fatalf("expected repository snapshot copy error, got %v", err)
		}
	})
}

func TestCopyRepositoryDirectoryPropagatesRootedOperationErrors(t *testing.T) {
	for _, tc := range repositorySnapshotCopyErrorTests() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, source, target, wantErr := tc.build()
			if err := copyRepositoryDirectory(ctx, source, target, ".", "", &repositorySnapshotBudget{}, nil); !errors.Is(err, wantErr) {
				t.Fatalf("expected %s error, got %v", tc.name, err)
			}
		})
	}
}

func TestCopyRepositoryDirectoryHonorsCancellationAndBounds(t *testing.T) {
	t.Run("cancellation", testCopyRepositoryDirectoryCancellation)
	t.Run("cancellation after directory read", testCopyRepositoryDirectoryPostReadCancellation)
	t.Run("unsupported special file", testCopyRepositoryDirectorySkipsSpecialFiles)
	t.Run("byte budget", testCopyRepositoryDirectoryByteBudget)
	t.Run("file budget", testRepositorySnapshotFileBudget)
	t.Run("budget no-op and success", testRepositorySnapshotBudgetNoOpAndSuccess)
}

func testCopyRepositoryDirectoryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := snapshotSourceWithEntry("file", snapshotLstat(0o600))
	if err := copyRepositoryDirectory(ctx, source, &snapshotRootStub{}, ".", "", &repositorySnapshotBudget{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func testCopyRepositoryDirectoryPostReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &snapshotRootStub{
		open: func(string) (safeio.File, error) {
			return &snapshotFileStub{readDir: func(int) ([]fs.DirEntry, error) {
				cancel()
				return []fs.DirEntry{&snapshotDirEntry{name: "file"}}, nil
			}}, nil
		},
	}
	if err := copyRepositoryDirectory(ctx, source, &snapshotRootStub{}, ".", "", &repositorySnapshotBudget{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected post-read cancellation, got %v", err)
	}
}

func testCopyRepositoryDirectorySkipsSpecialFiles(t *testing.T) {
	source := snapshotSourceWithEntry("socket", snapshotLstat(fs.ModeSocket|0o600))
	if err := copyRepositoryDirectory(context.Background(), source, &snapshotRootStub{}, ".", "", &repositorySnapshotBudget{}, nil); err != nil {
		t.Fatalf("expected unsupported special file to be omitted, got %v", err)
	}
}

func testCopyRepositoryDirectoryByteBudget(t *testing.T) {
	source := snapshotSourceWithEntry("file", func(name string) (fs.FileInfo, error) {
		return &snapshotFileInfo{name: name, mode: 0o600, size: maxRepositorySnapshotBytes + 1}, nil
	})
	err := copyRepositoryDirectory(context.Background(), source, &snapshotRootStub{}, ".", "", &repositorySnapshotBudget{}, nil)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("expected byte-limit rejection, got %v", err)
	}
}

func testRepositorySnapshotFileBudget(t *testing.T) {
	budget := &repositorySnapshotBudget{files: maxRepositorySnapshotFiles}
	if err := budget.noteFile(1); err == nil || !strings.Contains(err.Error(), "file limit") {
		t.Fatalf("expected file-limit rejection, got %v", err)
	}
}

func testRepositorySnapshotBudgetNoOpAndSuccess(t *testing.T) {
	var nilBudget *repositorySnapshotBudget
	if err := nilBudget.noteFile(1); err != nil {
		t.Fatalf("expected nil budget to allow noteFile, got %v", err)
	}
	budget := &repositorySnapshotBudget{}
	if err := budget.noteFile(1); err != nil {
		t.Fatalf("expected in-budget file to succeed, got %v", err)
	}
	if budget.files != 1 || budget.bytes != 1 {
		t.Fatalf("unexpected budget accounting after noteFile: %#v", budget)
	}
}

type repositorySnapshotCopyErrorTest struct {
	name  string
	build func() (context.Context, *snapshotRootStub, *snapshotRootStub, error)
}

func repositorySnapshotCopyErrorTests() []repositorySnapshotCopyErrorTest {
	return []repositorySnapshotCopyErrorTest{
		{
			name: "lstat",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("lstat failed")
				return context.Background(), snapshotSourceWithEntry("entry", func(string) (fs.FileInfo, error) {
					return nil, expectedErr
				}), &snapshotRootStub{}, expectedErr
			},
		},
		{
			name: "readlink",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("readlink failed")
				source := snapshotSourceWithEntry("link", snapshotLstat(fs.ModeSymlink|0o777))
				source.readlink = func(string) (string, error) { return "", expectedErr }
				return context.Background(), source, &snapshotRootStub{}, expectedErr
			},
		},
		{
			name: "mkdir",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("mkdir failed")
				source := snapshotSourceWithEntry("dir", snapshotLstat(fs.ModeDir|0o750))
				target := &snapshotRootStub{mkdir: func(string, os.FileMode) error { return expectedErr }}
				return context.Background(), source, target, expectedErr
			},
		},
		{
			name: "recursive read",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("recursive read failed")
				source := snapshotSourceWithEntry("dir", snapshotLstat(fs.ModeDir|0o750))
				source.directories["dir"] = snapshotReadDirResult{err: expectedErr}
				return context.Background(), source, &snapshotRootStub{}, expectedErr
			},
		},
		{
			name: "chmod",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("chmod failed")
				source := snapshotSourceWithEntry("dir", snapshotLstat(fs.ModeDir|0o750))
				source.directories["dir"] = snapshotReadDirResult{}
				target := &snapshotRootStub{chmod: func(string, os.FileMode) error { return expectedErr }}
				return context.Background(), source, target, expectedErr
			},
		},
		{
			name: "source file open",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("source open failed")
				source := snapshotSourceWithEntry("file", snapshotLstat(0o600))
				source.open = func(string) (safeio.File, error) { return nil, expectedErr }
				return context.Background(), source, &snapshotRootStub{}, expectedErr
			},
		},
		{
			name: "target file open",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("target open failed")
				source := repositorySnapshotOpenSource(strings.NewReader("content"), nil)
				target := &snapshotRootStub{openFile: func(string, int, os.FileMode) (safeio.File, error) { return nil, expectedErr }}
				return context.Background(), source, target, expectedErr
			},
		},
		{
			name: "copy",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("copy failed")
				source := repositorySnapshotOpenSource(&snapshotErrorReader{err: expectedErr}, nil)
				target := &snapshotRootStub{openFile: func(string, int, os.FileMode) (safeio.File, error) { return &snapshotFileStub{}, nil }}
				return context.Background(), source, target, expectedErr
			},
		},
		{
			name: "write",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("write failed")
				return repositorySnapshotFileTransferErrorTest(expectedErr, &snapshotFileStub{writeErr: expectedErr})
			},
		},
		{
			name: "close",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("close failed")
				source := repositorySnapshotOpenSource(strings.NewReader("content"), expectedErr)
				target := &snapshotRootStub{openFile: func(string, int, os.FileMode) (safeio.File, error) { return &snapshotFileStub{}, nil }}
				return context.Background(), source, target, expectedErr
			},
		},
		{
			name: "target close",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				expectedErr := errors.New("target close failed")
				return repositorySnapshotFileTransferErrorTest(expectedErr, &snapshotFileStub{closeErr: expectedErr})
			},
		},
		{
			name: "copy cancellation",
			build: func() (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
				ctx, cancel := context.WithCancel(context.Background())
				source := snapshotSourceWithEntry("file", snapshotLstat(0o600))
				source.open = func(string) (safeio.File, error) {
					return &snapshotFileStub{reader: &snapshotCancelReader{cancel: cancel, data: []byte("content")}}, nil
				}
				target := &snapshotRootStub{openFile: func(string, int, os.FileMode) (safeio.File, error) { return &snapshotFileStub{}, nil }}
				return ctx, source, target, context.Canceled
			},
		},
	}
}

func repositorySnapshotOpenSource(reader io.Reader, closeErr error) *snapshotRootStub {
	source := snapshotSourceWithEntry("file", snapshotLstat(0o600))
	source.open = func(string) (safeio.File, error) {
		return &snapshotFileStub{reader: reader, closeErr: closeErr}, nil
	}
	return source
}

func repositorySnapshotFileTransferErrorTest(expectedErr error, targetFile *snapshotFileStub) (context.Context, *snapshotRootStub, *snapshotRootStub, error) {
	return context.Background(), repositorySnapshotOpenSource(strings.NewReader("content"), nil), repositorySnapshotTargetWithStubFile(targetFile), expectedErr
}

func repositorySnapshotTargetWithStubFile(file *snapshotFileStub) *snapshotRootStub {
	return &snapshotRootStub{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return file, nil
		},
	}
}

func TestSnapshotRepositoryRootSkipsGitDirectory(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".git", "config"), "[core]\n")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "tracked\n")
	root, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open repo root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close repo root: %v", err)
		}
	})

	snapshot, err := snapshotRepositoryRoot(context.Background(), root, "")
	if err != nil {
		t.Fatalf("snapshot repository root: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(snapshot.path); err != nil {
			t.Errorf("remove snapshot: %v", err)
		}
	}()

	if _, err := os.Stat(filepath.Join(snapshot.path, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected .git omission, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(snapshot.path, "tracked.txt")); err != nil || string(data) != "tracked\n" {
		t.Fatalf("expected tracked file in snapshot, data=%q err=%v", data, err)
	}
}

func TestRepositorySnapshotDiagnosticsReportUnsafeJVMSymlinks(t *testing.T) {
	diagnostics := repositorySnapshotDiagnostics{}
	diagnostics.recordUnsafeSymlink(filepath.Join("src", "main", "java", "Outside.java"), repositorySnapshotUnsafeSymlinkEscapesRoot)
	diagnostics.recordUnsafeSymlink("build.gradle", repositorySnapshotUnsafeSymlinkUntrusted)
	diagnostics.recordUnsafeSymlink(filepath.Join("docs", "ignored.md"), repositorySnapshotUnsafeSymlinkUntrusted)

	got := diagnostics.warnings()
	want := []string{
		"skipped JVM source symlink " + filepath.Join("src", "main", "java", "Outside.java") + ": target escapes repo root",
		"unable to read build.gradle: " + safeio.ErrTargetPathSymlink.Error(),
		"skipped 1 unreadable or untrusted JVM source symlink(s)",
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected warning count: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("warning[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func snapshotSourceWithEntry(name string, lstat func(string) (fs.FileInfo, error)) *snapshotRootStub {
	return &snapshotRootStub{
		directories: map[string]snapshotReadDirResult{
			".": {entries: []fs.DirEntry{&snapshotDirEntry{name: name}}},
		},
		lstat: lstat,
	}
}

func snapshotLstat(mode fs.FileMode) func(string) (fs.FileInfo, error) {
	return func(name string) (fs.FileInfo, error) {
		return &snapshotFileInfo{name: name, mode: mode}, nil
	}
}

type snapshotReadDirResult struct {
	entries []fs.DirEntry
	err     error
}

type snapshotRootStub struct {
	directories map[string]snapshotReadDirResult
	open        func(string) (safeio.File, error)
	openFile    func(string, int, os.FileMode) (safeio.File, error)
	lstat       func(string) (fs.FileInfo, error)
	readlink    func(string) (string, error)
	symlink     func(string, string) error
	mkdir       func(string, os.FileMode) error
	chmod       func(string, os.FileMode) error
}

func (r *snapshotRootStub) Open(name string) (safeio.File, error) {
	if result, ok := r.directories[name]; ok {
		return &snapshotFileStub{
			readDir: func(int) ([]fs.DirEntry, error) {
				return result.entries, result.err
			},
		}, nil
	}
	if r.open != nil {
		return r.open(name)
	}
	return nil, fs.ErrNotExist
}

func (r *snapshotRootStub) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return &snapshotFileStub{}, nil
}

func (r *snapshotRootStub) OpenRoot(string) (safeio.Root, error) {
	return r, nil
}

func (r *snapshotRootStub) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return &snapshotFileInfo{name: name, mode: fs.ModeDir | 0o750}, nil
}

func (r *snapshotRootStub) Readlink(name string) (string, error) {
	if r.readlink != nil {
		return r.readlink(name)
	}
	return "", fs.ErrInvalid
}

func (r *snapshotRootStub) Symlink(oldName, newName string) error {
	if r.symlink != nil {
		return r.symlink(oldName, newName)
	}
	return nil
}

func (r *snapshotRootStub) Mkdir(name string, perm os.FileMode) error {
	if r.mkdir != nil {
		return r.mkdir(name, perm)
	}
	return nil
}

func (r *snapshotRootStub) Chmod(name string, perm os.FileMode) error {
	if r.chmod != nil {
		return r.chmod(name, perm)
	}
	return nil
}

func (*snapshotRootStub) MkdirAll(string, os.FileMode) error {
	return nil
}

func (*snapshotRootStub) Link(string, string) error {
	return nil
}

func (*snapshotRootStub) Rename(string, string) error {
	return nil
}

func (*snapshotRootStub) Remove(string) error {
	return nil
}

func (*snapshotRootStub) Close() error {
	return nil
}

type snapshotFileStub struct {
	reader   io.Reader
	readDir  func(int) ([]fs.DirEntry, error)
	writeErr error
	closeErr error
}

func (f *snapshotFileStub) Read(data []byte) (int, error) {
	if f.reader == nil {
		return 0, io.EOF
	}
	return f.reader.Read(data)
}

func (f *snapshotFileStub) Write(data []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(data), nil
}

func (f *snapshotFileStub) Close() error {
	return f.closeErr
}

func (*snapshotFileStub) Stat() (fs.FileInfo, error) {
	return &snapshotFileInfo{mode: 0o600}, nil
}

func (*snapshotFileStub) Chmod(os.FileMode) error {
	return nil
}

func (f *snapshotFileStub) ReadDir(count int) ([]fs.DirEntry, error) {
	if f.readDir == nil {
		return nil, fs.ErrInvalid
	}
	return f.readDir(count)
}

type snapshotErrorReader struct {
	err error
}

func (r *snapshotErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type snapshotCancelReader struct {
	cancel func()
	data   []byte
	read   bool
}

func (r *snapshotCancelReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	copy(buffer, r.data)
	r.cancel()
	return len(r.data), nil
}

type snapshotDirEntry struct {
	name string
}

func (e *snapshotDirEntry) Name() string               { return e.name }
func (*snapshotDirEntry) IsDir() bool                  { return false }
func (*snapshotDirEntry) Type() fs.FileMode            { return 0 }
func (e *snapshotDirEntry) Info() (fs.FileInfo, error) { return &snapshotFileInfo{name: e.name}, nil }

type snapshotFileInfo struct {
	name string
	mode fs.FileMode
	size int64
}

func (i *snapshotFileInfo) Name() string      { return i.name }
func (i *snapshotFileInfo) Size() int64       { return i.size }
func (i *snapshotFileInfo) Mode() fs.FileMode { return i.mode }
func (*snapshotFileInfo) ModTime() time.Time  { return time.Time{} }
func (i *snapshotFileInfo) IsDir() bool       { return i.mode.IsDir() }
func (*snapshotFileInfo) Sys() any            { return nil }
