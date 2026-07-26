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
	"github.com/ben-ranford/lopper/internal/testutil"
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

	for _, tc := range []struct {
		name     string
		closeErr error
	}{
		{name: "close succeeds"},
		{name: "close fails", closeErr: errors.New("close failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{"npm": info},
				files: map[string]safeio.File{
					"npm": &trustedExecutableFileStub{
						info:     fileInfoWithMode(info, 0o644),
						closeErr: tc.closeErr,
					},
				},
			}

			if validateTrustedRuntimeExecutable(root, "npm") {
				t.Fatal("expected descriptor trust mismatch to reject runtime executable")
			}
		})
	}
}

func TestValidateTrustedRuntimeExecutableRejectsStatFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o500); err != nil {
		t.Fatalf("write runtime executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}

	for _, tc := range []struct {
		name     string
		closeErr error
	}{
		{name: "close succeeds"},
		{name: "close fails", closeErr: errors.New("close failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{"npm": info},
				files: map[string]safeio.File{
					"npm": &trustedExecutableFileStub{
						statErr:  errors.New("stat failed"),
						closeErr: tc.closeErr,
					},
				},
			}

			if validateTrustedRuntimeExecutable(root, "npm") {
				t.Fatal("expected stat failure to reject trusted runtime executable")
			}
		})
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

func TestOpenTrustedRuntimeSearchRootPropagatesLstatFailure(t *testing.T) {
	wantErr := errors.New("lstat failed")

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(name string) (safeio.Root, error) {
			return &stubRoot{lstatErr: map[string]error{".": wantErr}}, nil
		},
	})

	_, err := openTrustedRuntimeSearchRoot(t.TempDir())
	if err == nil {
		t.Fatal("expected lstat failure")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected lstat error %v, got %v", wantErr, err)
	}
}

func TestOpenTrustedRuntimeSearchRootRejectsInvalidPath(t *testing.T) {
	if _, err := openTrustedRuntimeSearchRoot(string([]byte{0})); err == nil {
		t.Fatal("expected invalid runtime search path to be rejected")
	}
}

func TestValidateTrustedRuntimeExecutableRejectsOpenedDirectory(t *testing.T) {
	dirInfo := dirInfoFromTempDir(t)
	root := &trustedExecutableRootStub{
		lstatInfo: map[string]fs.FileInfo{"npm": dirInfo},
	}

	if validateTrustedRuntimeExecutable(root, "npm") {
		t.Fatal("expected directory candidate to be rejected")
	}
}

func TestValidateTrustedRuntimeExecutableRejectsSymlinkMetadata(t *testing.T) {
	path := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-symlink-metadata-"), "npm")
	writeRuntimeTestExecutable(t, path, "#!/bin/sh\nexit 0\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}

	root := &trustedExecutableRootStub{
		lstatInfo: map[string]fs.FileInfo{
			"npm": fileInfoWithMode(info, os.ModeSymlink|0o777),
		},
	}
	if validateTrustedRuntimeExecutable(root, "npm") {
		t.Fatal("expected symlink metadata to reject runtime executable")
	}
}

func TestIsTrustedRuntimeSearchDirInfoRejectsNonDirectory(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file path: %v", err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file path: %v", err)
	}
	if isTrustedRuntimeSearchDirInfo(info) {
		t.Fatal("expected non-directory runtime search info to be rejected")
	}
}

func TestIsTrustedRuntimeSearchDirInfoRejectsSyntheticSymlinkDirectory(t *testing.T) {
	info, err := os.Stat(testutil.SecureHomeTempDir(t, "runtime-synthetic-symlink-dir-"))
	if err != nil {
		t.Fatalf("stat runtime directory: %v", err)
	}
	symlinkDir := fileInfoWithMode(info, os.ModeDir|os.ModeSymlink|0o755)
	if isTrustedRuntimeSearchDirInfo(symlinkDir) {
		t.Fatal("expected synthetic symlink directory to be rejected")
	}
}

func TestTrustedSearchDirsSkipsRootOpenFailure(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-open-failure-")

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return nil, os.ErrPermission
		},
	})

	if got := trustedSearchDirs(dir); len(got) != 0 {
		t.Fatalf("expected root open failure to drop trusted search dir, got %v", got)
	}
}

func TestTrustedSearchDirsSkipsCloseFailure(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-close-failure-")

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(name string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{".": dirInfoFromTempDir(t)},
				closeErr:  errors.New("close failed"),
			}, nil
		},
	})

	if got := trustedSearchDirs(dir); len(got) != 0 {
		t.Fatalf("expected close failure to drop trusted search dir, got %v", got)
	}
}

func TestValidateTrustedRuntimeAncestorChainRejectsMalformedPaths(t *testing.T) {
	if validateTrustedRuntimeAncestorChain("relative", false) {
		t.Fatal("expected relative ancestor chain to be rejected")
	}
	if trustedRuntimeAncestorPath(filepath.Join(testutil.SecureHomeTempDir(t, "runtime-missing-ancestor-"), "missing"), false) {
		t.Fatal("expected missing ancestor to be rejected")
	}
	if os.PathSeparator != '\\' && !validateTrustedRuntimeAncestorChain(string(os.PathSeparator), false) {
		t.Fatal("expected filesystem root to have a trusted ancestor chain")
	}
}

func TestResolveRuntimeExecutablePathInDirReturnsCanonicalMatch(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-direct-match-")
	path := filepath.Join(dir, "node")
	writeRuntimeTestExecutable(t, path, "#!/bin/sh\nexit 0\n")

	got, ok := resolveRuntimeExecutablePathInDir("node", dir)
	if !ok || got != path {
		t.Fatalf("expected direct canonical match %q, got %q, %v", path, got, ok)
	}
}

func TestResolveRuntimeExecutablePropagatesPinnedSourceCloseFailure(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-resolved-close-failure-")
	path := filepath.Join(dir, "node")
	writeRuntimeTestExecutable(t, path, "#!/bin/sh\nexit 0\n")
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat runtime executable directory: %v", err)
	}
	wantErr := errors.New("pinned source close failed")
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{
					".":    dirInfo,
					"node": fileInfo,
				},
				files: map[string]safeio.File{
					"node": &trustedExecutableFileStub{info: fileInfo, closeErr: wantErr},
				},
			}, nil
		},
	})

	resolution, err := resolveRuntimeExecutable("node", []string{dir})
	if err == nil || resolution.path != "" {
		t.Fatalf("expected pinned source close failure, resolution=%#v err=%v", resolution, err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected pinned source close error identity, got %v", err)
	}
}

func TestOpenTrustedRuntimeExecutableCandidateRejectsExternalIdentityMismatch(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-candidate-identity-")
	candidate := filepath.Join(dir, "node")
	replacement := filepath.Join(dir, "replacement")
	writeRuntimeTestExecutable(t, candidate, "#!/bin/sh\nexit 0\n")
	writeRuntimeTestExecutable(t, replacement, "#!/bin/sh\nexit 0\n")
	replacementInfo, err := os.Stat(replacement)
	if err != nil {
		t.Fatalf("stat replacement executable: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat runtime executable directory: %v", err)
	}
	for _, tc := range []struct {
		name     string
		closeErr error
	}{
		{name: "close succeeds"},
		{name: "close fails", closeErr: errors.New("close failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withSafeioFileSystemTest(t, &safeioFileSystemStub{
				openRootNoFollow: func(string) (safeio.Root, error) {
					return &trustedExecutableRootStub{
						lstatInfo: map[string]fs.FileInfo{
							".":    dirInfo,
							"node": replacementInfo,
						},
						files: map[string]safeio.File{
							"node": &trustedExecutableFileStub{
								info:     replacementInfo,
								closeErr: tc.closeErr,
							},
						},
					}, nil
				},
			})

			source, trusted := openTrustedRuntimeExecutableCandidate("node", candidate)
			if trusted || source != nil {
				t.Fatalf("expected external candidate identity mismatch rejection, source=%#v trusted=%t", source, trusted)
			}
		})
	}
}

func TestResolvePinnedRuntimeExecutableRejectsInvalidCLIWhenSourceCloseFails(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix symlink fixture is covered here")
	}

	rootDir := testutil.SecureHomeTempDir(t, "runtime-invalid-cli-close-")
	searchDir := filepath.Join(rootDir, "tools")
	canonicalDir := filepath.Join(rootDir, "canonical")
	if err := os.MkdirAll(searchDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime launcher directory: %v", err)
	}
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical CLI directory: %v", err)
	}
	cliPath := filepath.Join(canonicalDir, "npm-cli.js")
	writeRuntimeTestExecutable(t, cliPath, "#!/usr/bin/env node\n")
	if err := os.Symlink(cliPath, filepath.Join(searchDir, "npm")); err != nil {
		t.Fatalf("symlink invalid CLI launcher: %v", err)
	}

	dirInfo, err := os.Stat(canonicalDir)
	if err != nil {
		t.Fatalf("stat canonical CLI directory: %v", err)
	}
	cliInfo, err := os.Stat(cliPath)
	if err != nil {
		t.Fatalf("stat canonical CLI: %v", err)
	}
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{
					".":          dirInfo,
					"npm-cli.js": cliInfo,
				},
				files: map[string]safeio.File{
					"npm-cli.js": &trustedExecutableFileStub{
						info:     cliInfo,
						closeErr: errors.New("close failed"),
					},
				},
			}, nil
		},
	})

	if resolution, ok := resolvePinnedRuntimeExecutableInDir("npm", searchDir); ok {
		t.Fatalf("expected invalid CLI layout rejection, got %#v", resolution)
	}
}

func TestResolveTrustedRuntimeExecutableCandidateRejectsDanglingSymlink(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-dangling-candidate-")
	candidate := filepath.Join(dir, "node")
	if err := os.Symlink(filepath.Join(dir, "missing-node"), candidate); err != nil {
		t.Fatalf("symlink dangling runtime candidate: %v", err)
	}

	if source, trusted := openTrustedRuntimeExecutableCandidate("node", candidate); trusted || source != nil {
		t.Fatalf("expected dangling runtime candidate to be rejected, got %#v, %v", source, trusted)
	}
}

func TestResolveTrustedRuntimeExecutableCandidateRejectsCanonicalRootOpenFailure(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-canonical-open-failure-")
	candidate := filepath.Join(dir, "node")
	writeRuntimeTestExecutable(t, candidate, "#!/bin/sh\nexit 0\n")

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return nil, os.ErrPermission
		},
	})

	if source, trusted := openTrustedRuntimeExecutableCandidate("node", candidate); trusted || source != nil {
		t.Fatalf("expected canonical root open failure to reject candidate, got %#v, %v", source, trusted)
	}
}

func TestRuntimePathWithinRootPropagatesRelError(t *testing.T) {
	if _, err := runtimePathWithinRoot(string([]byte{0}), "/tmp"); err == nil {
		t.Fatal("expected invalid root path to fail")
	}
	if _, err := runtimePathWithinRoot("/tmp", string([]byte{0})); err == nil {
		t.Fatal("expected invalid target path to fail")
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
	dir := testutil.SecureHomeTempDir(t, "runtime-close-after-match-")
	path := filepath.Join(dir, "npm")
	writeRuntimeTestExecutable(t, path, "#!/bin/sh\nexit 0\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat runtime executable root: %v", err)
	}

	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(name string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{
					".":   dirInfo,
					"npm": info,
				},
				files: map[string]safeio.File{
					"npm": &trustedExecutableFileStub{info: info},
				},
				closeErr: errors.New("close failed"),
			}, nil
		},
	})

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
func (r *trustedExecutableRootStub) Link(oldName, newName string) error {
	return errors.New("unexpected Link")
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

type fileInfoStatOverride struct {
	fs.FileInfo
	sys any
}

func (f *fileInfoStatOverride) Sys() any {
	return f.sys
}

func fileInfoWithStat(info fs.FileInfo, sys any) fs.FileInfo {
	return &fileInfoStatOverride{FileInfo: info, sys: sys}
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
