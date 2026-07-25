package runtime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "unsafe"

	"github.com/ben-ranford/lopper/internal/safeio"
)

//go:linkname safeioFileSystem github.com/ben-ranford/lopper/internal/safeio.fileSystem
var safeioFileSystem safeio.FileSystem

func TestCloseRuntimeSearchRootWithErrorJoinsCloseError(t *testing.T) {
	rootErr := errors.New("root close failed")
	wantErr := errors.New("validate root failed")

	err := closeRuntimeSearchRootWithError(&stubRoot{closeErr: rootErr}, wantErr)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped root error %v, got %v", wantErr, err)
	}
	if !errors.Is(err, rootErr) {
		t.Fatalf("expected wrapped close error %v, got %v", rootErr, err)
	}
}

func TestIsTrustedRuntimeSearchDirInfoRejectsSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink mode checks are Unix-specific")
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "bin-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat symlinked dir: %v", err)
	}
	if isTrustedRuntimeSearchDirInfo(info) {
		t.Fatal("expected symlinked runtime search dir to be rejected")
	}
}

func TestValidateTrustedRuntimeExecutableRejectsCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
		t.Fatalf("write runtime executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}

	root := &trustedExecutableRootStub{
		lstatInfo: map[string]fs.FileInfo{"npm": info},
		files: map[string]safeio.File{
			"npm": &trustedExecutableFileStub{info: info, closeErr: errors.New("close failed")},
		},
	}

	if validateTrustedRuntimeExecutable(root, "npm") {
		t.Fatal("expected close failure to reject trusted runtime executable")
	}
}

func TestValidateTrustedRuntimeExecutableRejectsOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
		t.Fatalf("write runtime executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}

	root := &trustedExecutableRootStub{
		lstatInfo: map[string]fs.FileInfo{"npm": info},
		openErr:   map[string]error{"npm": errors.New("open failed")},
	}

	if validateTrustedRuntimeExecutable(root, "npm") {
		t.Fatal("expected open failure to reject trusted runtime executable")
	}
}

func TestValidateTrustedRuntimeExecutableRejectsDescriptorTrustMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
		t.Fatalf("write runtime executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}

	root := &trustedExecutableRootStub{
		lstatInfo: map[string]fs.FileInfo{"npm": info},
		files: map[string]safeio.File{
			"npm": &trustedExecutableFileStub{info: fileInfoWithMode(info, 0o644)},
		},
	}

	if validateTrustedRuntimeExecutable(root, "npm") {
		t.Fatal("expected descriptor trust mismatch to reject runtime executable")
	}
}

func TestOpenTrustedRuntimeSearchRootRejectsNonDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file path: %v", err)
	}

	_, err := openTrustedRuntimeSearchRoot(path)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected non-directory runtime search root to be rejected, got %v", err)
	}
}

func TestOpenTrustedRuntimeSearchRootRejectsWorldWritableDirectory(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix permission trust checks are covered here")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod trusted search dir: %v", err)
	}

	_, err := openTrustedRuntimeSearchRoot(dir)
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("expected world-writable runtime search root to be rejected, got %v", err)
	}
}

func TestOpenTrustedRuntimeSearchRootRejectsOpenedNonDirectory(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file path: %v", err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file path: %v", err)
	}

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(name string) (safeio.Root, error) {
			return &stubRoot{selfInfo: info}, nil
		},
	})

	dir := t.TempDir()
	_, err = openTrustedRuntimeSearchRoot(dir)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected opened non-directory runtime root to be rejected, got %v", err)
	}
}

func TestValidateRuntimeTraceReadRejectsPathSwapDuringHashing(t *testing.T) {
	original := writeCoverageTraceFixture(t, "original.ndjson")
	replacement := writeCoverageTraceFixture(t, "replacement.ndjson")

	openedInfo, err := os.Stat(original)
	if err != nil {
		t.Fatalf("stat original trace: %v", err)
	}
	replacementInfo, err := os.Stat(replacement)
	if err != nil {
		t.Fatalf("stat replacement trace: %v", err)
	}

	root := &stubRoot{lstatInfo: map[string]fs.FileInfo{"runtime.ndjson": replacementInfo}}
	file := &trustedExecutableFileStub{info: openedInfo}

	_, err = validateRuntimeTraceRead(root, file, "runtime.ndjson", original, openedInfo)
	if err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected path swap during hashing to be rejected, got %v", err)
	}
}

func TestValidateRuntimeTraceReadRejectsDescriptorSwapDuringHashing(t *testing.T) {
	original := writeCoverageTraceFixture(t, "original.ndjson")
	replacement := writeCoverageTraceFixture(t, "replacement.ndjson")

	openedInfo, err := os.Stat(original)
	if err != nil {
		t.Fatalf("stat original trace: %v", err)
	}
	replacementInfo, err := os.Stat(replacement)
	if err != nil {
		t.Fatalf("stat replacement trace: %v", err)
	}

	root := &stubRoot{lstatInfo: map[string]fs.FileInfo{"runtime.ndjson": replacementInfo}}
	file := &trustedExecutableFileStub{info: replacementInfo}

	_, err = validateRuntimeTraceRead(root, file, "runtime.ndjson", original, openedInfo)
	if err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected descriptor swap during hashing to be rejected, got %v", err)
	}
}

func TestValidatedRuntimeTraceFileStatPropagatesStatError(t *testing.T) {
	wantErr := errors.New("stat failed")

	_, err := validatedRuntimeTraceFileStat(&trustedExecutableFileStub{statErr: wantErr}, "/tmp/runtime.ndjson")
	if err == nil {
		t.Fatal("expected stat failure")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected stat error %v, got %v", wantErr, err)
	}
}

func TestResolveRuntimeExecutablePathInDirRejectsRootCloseFailureAfterMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
		t.Fatalf("write runtime executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(name string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{
					".":   dirInfoFromTempDir(t),
					"npm": info,
				},
				files: map[string]safeio.File{
					"npm": &trustedExecutableFileStub{info: info},
				},
				closeErr: errors.New("close failed"),
			}, nil
		},
	})

	dir := t.TempDir()
	got, ok := resolveRuntimeExecutablePathInDir("npm", dir)
	if ok || got != "" {
		t.Fatalf("expected root close failure to clear resolved executable, got %q, %v", got, ok)
	}
}

func TestValidateRuntimeTraceFileInfoRejectsSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink mode checks are Unix-specific")
	}

	target := filepath.Join(t.TempDir(), "runtime.ndjson")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace file: %v", err)
	}
	link := filepath.Join(t.TempDir(), "runtime-link.ndjson")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat symlink trace: %v", err)
	}

	err = validateRuntimeTraceFileInfo(info, link)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlinked trace metadata to be rejected, got %v", err)
	}
}

func TestValidateRuntimeTraceFileInfoRejectsOversizedMetadata(t *testing.T) {
	path := writeCoverageTraceFixture(t, "oversized.ndjson")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat trace file: %v", err)
	}

	err = validateRuntimeTraceFileInfo(&fileInfoSizeOverride{FileInfo: info, size: maxRuntimeTraceBytes + 1}, path)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized trace metadata to fail with ErrFileTooLarge, got %v", err)
	}
}

func writeCoverageTraceFixture(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write trace fixture %q: %v", name, err)
	}

	mtime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("set trace fixture mtime %q: %v", name, err)
	}
	return path
}

type trustedExecutableRootStub struct {
	lstatInfo map[string]fs.FileInfo
	lstatErr  map[string]error
	openErr   map[string]error
	files     map[string]safeio.File
	closeErr  error
}

func (r *trustedExecutableRootStub) Open(name string) (safeio.File, error) {
	if err, ok := r.openErr[name]; ok {
		return nil, err
	}
	if file, ok := r.files[name]; ok {
		return file, nil
	}
	return nil, os.ErrNotExist
}

func (r *trustedExecutableRootStub) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	return nil, errors.New("unexpected OpenFile")
}

func (r *trustedExecutableRootStub) OpenRoot(name string) (safeio.Root, error) {
	return nil, errors.New("unexpected OpenRoot")
}

func (r *trustedExecutableRootStub) Lstat(name string) (fs.FileInfo, error) {
	if err, ok := r.lstatErr[name]; ok {
		return nil, err
	}
	if info, ok := r.lstatInfo[name]; ok {
		return info, nil
	}
	return nil, os.ErrNotExist
}

func (r *trustedExecutableRootStub) Mkdir(name string, perm os.FileMode) error {
	return errors.New("unexpected Mkdir")
}
func (r *trustedExecutableRootStub) Chmod(name string, perm os.FileMode) error {
	return errors.New("unexpected Chmod")
}
func (r *trustedExecutableRootStub) MkdirAll(name string, perm os.FileMode) error {
	return errors.New("unexpected MkdirAll")
}
func (r *trustedExecutableRootStub) Rename(oldName, newName string) error {
	return errors.New("unexpected Rename")
}
func (r *trustedExecutableRootStub) Remove(name string) error { return errors.New("unexpected Remove") }
func (r *trustedExecutableRootStub) Close() error             { return r.closeErr }

type trustedExecutableFileStub struct {
	info     fs.FileInfo
	statErr  error
	closeErr error
}

func (f *trustedExecutableFileStub) Read(p []byte) (int, error)  { return 0, fs.ErrClosed }
func (f *trustedExecutableFileStub) Write(p []byte) (int, error) { return 0, fs.ErrClosed }
func (f *trustedExecutableFileStub) Close() error                { return f.closeErr }
func (f *trustedExecutableFileStub) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.info, nil
}
func (f *trustedExecutableFileStub) Chmod(perm os.FileMode) error {
	return errors.New("unexpected Chmod")
}

type fileInfoModeOverride struct {
	fs.FileInfo
	mode fs.FileMode
}

func (f *fileInfoModeOverride) Mode() fs.FileMode {
	return f.mode
}

func fileInfoWithMode(info fs.FileInfo, mode fs.FileMode) fs.FileInfo {
	return &fileInfoModeOverride{FileInfo: info, mode: mode}
}

type fileInfoSizeOverride struct {
	fs.FileInfo
	size int64
}

func (f *fileInfoSizeOverride) Size() int64 {
	return f.size
}

type safeioFileSystemStub struct {
	openRoot         func(string) (safeio.Root, error)
	openRootNoFollow func(string) (safeio.Root, error)
}

func (f *safeioFileSystemStub) Abs(path string) (string, error) {
	return path, nil
}

func (f *safeioFileSystemStub) Rel(basepath, targpath string) (string, error) {
	return filepath.Rel(basepath, targpath)
}

func (f *safeioFileSystemStub) OpenRoot(name string) (safeio.Root, error) {
	if f.openRoot != nil {
		return f.openRoot(name)
	}
	return nil, errors.New("unexpected OpenRoot")
}

func (f *safeioFileSystemStub) OpenRootNoFollow(name string) (safeio.Root, error) {
	if f.openRootNoFollow != nil {
		return f.openRootNoFollow(name)
	}
	return nil, errors.New("unexpected OpenRootNoFollow")
}

func withSafeioFileSystemTest(t *testing.T, fs safeio.FileSystem) {
	t.Helper()

	original := safeioFileSystem
	safeioFileSystem = fs
	t.Cleanup(func() {
		safeioFileSystem = original
	})
}

func dirInfoFromTempDir(t *testing.T) fs.FileInfo {
	t.Helper()

	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	return info
}
