package js

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDependencyChainBase(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "@scope", "pkg")
	base, parts, ok := dependencyChainBase(depRoot)
	if !ok || base != repo {
		t.Fatalf("expected repo base for dependency chain, got base=%q ok=%v", base, ok)
	}
	if got := strings.Join(parts, "/"); got != "node_modules/@scope/pkg" {
		t.Fatalf("unexpected dependency chain parts: %q", got)
	}

	if _, _, ok := dependencyChainBase(repo); ok {
		t.Fatal("expected non-dependency path to skip dependency chain handling")
	}

	rootBase, rootParts, rootOK := dependencyChainBase(string(os.PathSeparator) + filepath.Join("node_modules", "pkg"))
	if !rootOK || rootBase != string(os.PathSeparator) {
		t.Fatalf("expected filesystem root base for root-level dependency chain, got base=%q ok=%v", rootBase, rootOK)
	}
	if got := strings.Join(rootParts, "/"); got != "node_modules/pkg" {
		t.Fatalf("unexpected root-level dependency chain parts: %q", got)
	}

	windowsBase, windowsParts, windowsOK := dependencyChainBase(`C:\node_modules\pkg`)
	if !windowsOK || windowsBase != `C:\` {
		t.Fatalf("expected drive-root base for windows dependency chain, got base=%q ok=%v", windowsBase, windowsOK)
	}
	if got := strings.Join(windowsParts, "/"); got != "node_modules/pkg" {
		t.Fatalf("unexpected windows dependency chain parts: %q", got)
	}
}

func TestOpenConstrainedRootAndRelativePathWithinRootBranches(t *testing.T) {
	plainDir := t.TempDir()
	root, err := openConstrainedRoot(plainDir)
	if err != nil {
		t.Fatalf("open constrained root for plain dir: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close constrained root for plain dir: %v", err)
	}

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), "{}\n")

	root, err = openConstrainedRoot(depRoot)
	if err != nil {
		t.Fatalf("open constrained dependency root: %v", err)
	}
	if _, err := root.Lstat("."); err != nil {
		t.Fatalf("lstat constrained dependency root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close constrained dependency root: %v", err)
	}

	rel, err := relativePathWithinRoot(repo, filepath.Join(depRoot, "package.json"))
	if err != nil || rel != filepath.Join("node_modules", "pkg", "package.json") {
		t.Fatalf("unexpected relative path: rel=%q err=%v", rel, err)
	}
	if _, err := relativePathWithinRoot(repo, filepath.Join(t.TempDir(), "outside.js")); err == nil {
		t.Fatal("expected outside path to be rejected")
	}
	if _, err := relativePathWithinRoot(repo, filepath.Dir(repo)); err == nil {
		t.Fatal("expected direct parent path to be rejected")
	}
}

func TestOpenConstrainedRootRejectsSymlinkAndNonDirectoryDependencyComponents(t *testing.T) {
	repo := t.TempDir()

	outsideNodeModules := t.TempDir()
	if err := os.Symlink(outsideNodeModules, filepath.Join(repo, "node_modules")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := openConstrainedRoot(filepath.Join(repo, "node_modules", "pkg")); err == nil || !strings.Contains(err.Error(), "symlinked path component") {
		t.Fatalf("expected symlinked dependency component rejection, got %v", err)
	}

	repoWithFile := t.TempDir()
	nodeModules := filepath.Join(repoWithFile, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(nodeModules, "pkg"), "not a directory\n")
	if _, err := openConstrainedRoot(filepath.Join(nodeModules, "pkg", "child")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected non-directory dependency component rejection, got %v", err)
	}

	if _, err := openConstrainedRoot(filepath.Join(t.TempDir(), "missing", "node_modules", "pkg")); err == nil {
		t.Fatal("expected missing dependency base path to fail")
	}
}

func TestResolvePinnedDependencyChainPathAllowsNestedInRepoSymlinkRoots(t *testing.T) {
	repo := t.TempDir()
	pkgRoot := filepath.Join(repo, "packages", "linked")
	transitiveRoot := filepath.Join(pkgRoot, "node_modules", "dep")
	if err := os.MkdirAll(transitiveRoot, 0o755); err != nil {
		t.Fatalf("mkdir transitive root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(pkgRoot, "package.json"), "{}\n")
	testutil.MustWriteFile(t, filepath.Join(transitiveRoot, "package.json"), "{}\n")

	linkedRoot := filepath.Join(repo, "node_modules", "linked")
	if err := os.MkdirAll(filepath.Dir(linkedRoot), 0o755); err != nil {
		t.Fatalf("mkdir linked root parent: %v", err)
	}
	if err := os.Symlink(pkgRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := resolvePinnedRootPath(filepath.Join(linkedRoot, "node_modules", "dep"))
	if err != nil {
		t.Fatalf("resolve nested dependency under in-repo symlink root: %v", err)
	}
	want, err := filepath.EvalSymlinks(transitiveRoot)
	if err != nil {
		t.Fatalf("canonicalize transitive root: %v", err)
	}
	if got != want {
		t.Fatalf("expected nested dependency to pin to %q, got %q", want, got)
	}
}

func TestResolvePinnedDependencyChainPathAllowsPnpmStyleNestedDependencyRoots(t *testing.T) {
	repo := t.TempDir()
	storeRoot := filepath.Join(repo, ".pnpm", "linked@1.0.0", "node_modules", "linked")
	transitiveRoot := filepath.Join(storeRoot, "node_modules", "dep")
	if err := os.MkdirAll(transitiveRoot, 0o755); err != nil {
		t.Fatalf("mkdir transitive pnpm root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(storeRoot, "package.json"), "{}\n")
	testutil.MustWriteFile(t, filepath.Join(transitiveRoot, "package.json"), "{}\n")

	linkedRoot := filepath.Join(repo, "node_modules", "linked")
	if err := os.MkdirAll(filepath.Dir(linkedRoot), 0o755); err != nil {
		t.Fatalf("mkdir linked root parent: %v", err)
	}
	if err := os.Symlink(storeRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := resolvePinnedRootPath(filepath.Join(linkedRoot, "node_modules", "dep"))
	if err != nil {
		t.Fatalf("resolve nested dependency under pnpm symlink root: %v", err)
	}
	want, err := filepath.EvalSymlinks(transitiveRoot)
	if err != nil {
		t.Fatalf("canonicalize pnpm transitive root: %v", err)
	}
	if got != want {
		t.Fatalf("expected pnpm nested dependency to pin to %q, got %q", want, got)
	}
}

func TestResolvePinnedDependencyChainPathRejectsEscapingSymlink(t *testing.T) {
	repo := t.TempDir()
	pkgRoot := filepath.Join(repo, "packages", "linked")
	if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir linked package root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(pkgRoot, "package.json"), "{}\n")

	linkedRoot := filepath.Join(repo, "node_modules", "linked")
	if err := os.MkdirAll(filepath.Dir(linkedRoot), 0o755); err != nil {
		t.Fatalf("mkdir linked root parent: %v", err)
	}
	if err := os.Symlink(pkgRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	outside := t.TempDir()
	escapingTarget := filepath.Join(outside, "dep")
	if err := os.MkdirAll(escapingTarget, 0o755); err != nil {
		t.Fatalf("mkdir escaping target: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(escapingTarget, "package.json"), "{}\n")

	nestedModules := filepath.Join(pkgRoot, "node_modules")
	if err := os.MkdirAll(nestedModules, 0o755); err != nil {
		t.Fatalf("mkdir nested node_modules: %v", err)
	}
	escapingRoot := filepath.Join(nestedModules, "dep")
	if err := os.Symlink(escapingTarget, escapingRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := resolvePinnedRootPath(filepath.Join(linkedRoot, "node_modules", "dep")); err == nil || !strings.Contains(err.Error(), "symlinked path component") {
		t.Fatalf("expected escaping nested dependency symlink to be rejected, got %v", err)
	}
}

func TestResolvePinnedDependencyChainPathRejectsSymlinkedFileTarget(t *testing.T) {
	repo := t.TempDir()
	pkgRoot := filepath.Join(repo, "packages", "linked")
	if err := os.MkdirAll(filepath.Join(pkgRoot, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir linked package node_modules: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(pkgRoot, "package.json"), "{}\n")
	testutil.MustWriteFile(t, filepath.Join(pkgRoot, "dep-file"), "not a directory\n")

	linkedRoot := filepath.Join(repo, "node_modules", "linked")
	if err := os.MkdirAll(filepath.Dir(linkedRoot), 0o755); err != nil {
		t.Fatalf("mkdir linked root parent: %v", err)
	}
	if err := os.Symlink(pkgRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	depLink := filepath.Join(pkgRoot, "node_modules", "dep")
	if err := os.Symlink(filepath.Join(pkgRoot, "dep-file"), depLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := resolvePinnedRootPath(filepath.Join(linkedRoot, "node_modules", "dep"))
	if err == nil || !strings.Contains(err.Error(), "path is not a directory") {
		t.Fatalf("expected symlinked file target to be rejected as non-directory, got %v", err)
	}
}

func TestValidatedDependencyRootAtDirRejectsNonRegularPackageJSON(t *testing.T) {
	repo := t.TempDir()
	for _, tc := range []struct {
		name      string
		setup     func(t *testing.T, depRoot string)
		wantError string
	}{
		{
			name: "symlinked package json",
			setup: func(t *testing.T, depRoot string) {
				target := filepath.Join(depRoot, "manifest.json")
				testutil.MustWriteFile(t, target, "{}\n")
				if err := os.Symlink(target, filepath.Join(depRoot, jsPackageFile)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			wantError: "symlinked file path",
		},
		{
			name: "directory package json",
			setup: func(t *testing.T, depRoot string) {
				if err := os.Mkdir(filepath.Join(depRoot, jsPackageFile), 0o755); err != nil {
					t.Fatalf("mkdir package.json dir: %v", err)
				}
			},
			wantError: "path is not a regular file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			depRoot := filepath.Join(repo, "node_modules", strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.MkdirAll(depRoot, 0o755); err != nil {
				t.Fatalf("mkdir dependency root: %v", err)
			}
			tc.setup(t, depRoot)

			_, err := validatedDependencyRootAtDir(repo, filepath.Base(depRoot))
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q for invalid package.json shape, got %v", tc.wantError, err)
			}
		})
	}
}

func TestValidatedDependencyRootAtDirPropagatesCloseFailure(t *testing.T) {
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(depRoot, jsPackageFile), "{}\n")

	closeErr := errors.New("close failed")
	openDependencyRootNoFollow = func(string) (safeio.Root, error) {
		return &fakeJSRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				return os.Lstat(filepath.Join(depRoot, name))
			},
			closeErr: closeErr,
		}, nil
	}

	if _, err := validatedDependencyRootAtDir(repo, "pkg"); !errors.Is(err, closeErr) {
		t.Fatalf("expected close failure to propagate, got %v", err)
	}
}

func TestValidatedDependencyRootAtDirJoinsValidationAndCloseErrors(t *testing.T) {
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	if err := os.Mkdir(filepath.Join(depRoot, jsPackageFile), 0o755); err != nil {
		t.Fatalf("mkdir package.json dir: %v", err)
	}

	closeErr := errors.New("close failed")
	openDependencyRootNoFollow = func(string) (safeio.Root, error) {
		return &fakeJSRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				return os.Lstat(filepath.Join(depRoot, name))
			},
			closeErr: closeErr,
		}, nil
	}

	_, err := validatedDependencyRootAtDir(repo, "pkg")
	if err == nil {
		t.Fatal("expected invalid package.json shape with close failure")
	}
	if !strings.Contains(err.Error(), "path is not a regular file") || !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("expected joined validation and close errors, got %v", err)
	}
}

func TestOpenRootChildNoFollowBranches(t *testing.T) {
	repo := t.TempDir()
	root, err := openConstrainedRoot(repo)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close root: %v", err)
		}
	})

	if _, err := openRootChildNoFollow(root, "missing", filepath.Join(repo, "missing")); err == nil {
		t.Fatal("expected missing child to fail")
	}

	filePath := filepath.Join(repo, "file")
	testutil.MustWriteFile(t, filePath, "not a directory\n")
	if _, err := openRootChildNoFollow(root, "file", filePath); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected file child rejection, got %v", err)
	}

	openErrRoot := &fakeJSRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			return os.Lstat(repo)
		},
		openRoot: func(name string) (safeio.Root, error) {
			return nil, errors.New("open failed")
		},
	}
	if _, err := openRootChildNoFollow(openErrRoot, ".", repo); err == nil || !strings.Contains(err.Error(), "open failed") {
		t.Fatalf("expected open-root error to propagate, got %v", err)
	}

	childStatErrRoot := &fakeJSRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			return os.Lstat(repo)
		},
		openRoot: func(name string) (safeio.Root, error) {
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return nil, errors.New("child stat failed")
				},
			}, nil
		},
	}
	if _, err := openRootChildNoFollow(childStatErrRoot, ".", repo); err == nil || !strings.Contains(err.Error(), "child stat failed") {
		t.Fatalf("expected child-stat error to propagate, got %v", err)
	}

	otherDir := t.TempDir()
	mismatchRoot := &fakeJSRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			return os.Lstat(repo)
		},
		openRoot: func(name string) (safeio.Root, error) {
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return os.Lstat(otherDir)
				},
			}, nil
		},
	}
	if _, err := openRootChildNoFollow(mismatchRoot, ".", repo); err == nil || !strings.Contains(err.Error(), "path changed while opening") {
		t.Fatalf("expected changed-directory error, got %v", err)
	}
}

type fakeJSRoot struct {
	open     func(string) (safeio.File, error)
	lstat    func(string) (fs.FileInfo, error)
	openRoot func(string) (safeio.Root, error)
	closeErr error
}

func (r *fakeJSRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return nil, errors.New("not implemented")
}
func (r *fakeJSRoot) OpenFile(string, int, os.FileMode) (safeio.File, error) {
	return nil, errors.New("not implemented")
}
func (r *fakeJSRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return nil, errors.New("not implemented")
}
func (r *fakeJSRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return nil, errors.New("not implemented")
}
func (r *fakeJSRoot) Mkdir(string, os.FileMode) error    { return errors.New("not implemented") }
func (r *fakeJSRoot) Chmod(string, os.FileMode) error    { return errors.New("not implemented") }
func (r *fakeJSRoot) MkdirAll(string, os.FileMode) error { return errors.New("not implemented") }
func (r *fakeJSRoot) Rename(string, string) error        { return errors.New("not implemented") }
func (r *fakeJSRoot) Remove(string) error                { return errors.New("not implemented") }
func (r *fakeJSRoot) Close() error                       { return r.closeErr }

func TestOpenRootChildNoFollowJoinsChildStatAndCloseErrors(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Lstat(repo)
	if err != nil {
		t.Fatalf("lstat repo: %v", err)
	}

	parent := &fakeJSRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return info, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return nil, errors.New("child stat failed")
				},
				closeErr: errors.New("child close failed"),
			}, nil
		},
	}

	_, err = openRootChildNoFollow(parent, ".", repo)
	if err == nil {
		t.Fatal("expected joined child-stat and close errors")
	}
	if !strings.Contains(err.Error(), "child stat failed") || !strings.Contains(err.Error(), "child close failed") {
		t.Fatalf("expected joined child-stat and close errors, got %v", err)
	}
}

func TestOpenRootChildNoFollowJoinsPathChangeAndCloseErrors(t *testing.T) {
	repo := t.TempDir()
	otherDir := t.TempDir()
	info, err := os.Lstat(repo)
	if err != nil {
		t.Fatalf("lstat repo: %v", err)
	}

	parent := &fakeJSRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return info, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return os.Lstat(otherDir)
				},
				closeErr: errors.New("child close failed"),
			}, nil
		},
	}

	_, err = openRootChildNoFollow(parent, ".", repo)
	if err == nil {
		t.Fatal("expected joined path-change and close errors")
	}
	if !strings.Contains(err.Error(), "path changed while opening") || !strings.Contains(err.Error(), "child close failed") {
		t.Fatalf("expected joined path-change and close errors, got %v", err)
	}
}

func TestResolveDependencyRootFromDirRejectsStartOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if got := resolveDependencyRootFromDir(repo, outside, "pkg"); got != "" {
		t.Fatalf("expected outside start dir to resolve no dependency root, got %q", got)
	}
}

func TestDependencyResolutionStatusAndPathHelpers(t *testing.T) {
	if _, status := resolveDependencyRootFromDirDetailed("", "", ""); status != dependencyRootMissing {
		t.Fatalf("expected empty resolution request to be treated as missing, got %v", status)
	}
	if root, status := resolveDependencyRootAtDirDetailed(t.TempDir(), "bad/name/extra"); root != "" || status != dependencyRootUnsafe {
		t.Fatalf("expected invalid dependency name to be unsafe, got root=%q status=%v", root, status)
	}
	if dependencyRootErrorIsUnsafe(nil) {
		t.Fatal("expected nil error not to be unsafe")
	}
	if dependencyRootErrorIsUnsafe(os.ErrNotExist) {
		t.Fatal("expected missing error not to be unsafe")
	}
	if !dependencyRootErrorIsUnsafe(errors.New("custom failure")) {
		t.Fatal("expected generic error to be unsafe")
	}
	if got := joinPathPrefix(nil, string(os.PathSeparator)); got != string(os.PathSeparator) {
		t.Fatalf("expected empty prefix to use filesystem root, got %q", got)
	}
	if got := joinPathPrefix([]string{"a", "b"}, "/"); got != "a/b" {
		t.Fatalf("unexpected joined prefix: %q", got)
	}
	path, err := canonicalizeNoFollowParentPath(filepath.Join(t.TempDir(), "child"))
	if err != nil || filepath.Base(path) != "child" {
		t.Fatalf("expected canonicalized child path, got path=%q err=%v", path, err)
	}
}

func TestDependencyConfinementOperationFailures(t *testing.T) {
	originalAbs := absoluteDependencyPath
	originalEval := evaluateDependencySymlinks
	originalRel := relativeDependencyPath
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		absoluteDependencyPath = originalAbs
		evaluateDependencySymlinks = originalEval
		relativeDependencyPath = originalRel
		openDependencyRootNoFollow = originalOpen
	})

	operationErr := errors.New("path operation failed")
	t.Run("absolute path failures are treated as missing", func(t *testing.T) {
		assertDependencyResolutionAbsFailures(t, operationErr)
	})
	t.Run("canonicalization failures propagate", func(t *testing.T) {
		assertDependencyResolutionCanonicalizationFailures(t, operationErr)
	})
	t.Run("validated root reopen failure propagates", func(t *testing.T) {
		evaluateDependencySymlinks = filepath.EvalSymlinks
		openDependencyRootNoFollow = func(string) (safeio.Root, error) {
			return nil, operationErr
		}
		if _, _, err := openValidatedRootNoFollow(t.TempDir()); !errors.Is(err, operationErr) {
			t.Fatalf("expected validated-root reopen failure, got %v", err)
		}
	})
	t.Run("relative path helper failures propagate", func(t *testing.T) {
		assertDependencyResolutionRelativePathFailures(t, operationErr)
	})
}

func assertDependencyResolutionAbsFailures(t *testing.T, operationErr error) {
	t.Helper()

	absoluteDependencyPath = func(path string) (string, error) {
		if path == "repo" {
			return "", operationErr
		}
		return filepath.Abs(path)
	}
	if _, status := resolveDependencyRootFromDirDetailed("repo", "start", "pkg"); status != dependencyRootMissing {
		t.Fatalf("expected repository absolute-path failure to be missing, got %v", status)
	}

	absoluteDependencyPath = func(path string) (string, error) {
		if path == "start" {
			return "", operationErr
		}
		return filepath.Abs(path)
	}
	if _, status := resolveDependencyRootFromDirDetailed(t.TempDir(), "start", "pkg"); status != dependencyRootMissing {
		t.Fatalf("expected start absolute-path failure to be missing, got %v", status)
	}
}

func assertDependencyResolutionCanonicalizationFailures(t *testing.T, operationErr error) {
	t.Helper()

	absoluteDependencyPath = func(string) (string, error) {
		return "", operationErr
	}
	if _, err := canonicalizeNoFollowParentPath("child"); !errors.Is(err, operationErr) {
		t.Fatalf("expected canonical absolute-path failure, got %v", err)
	}

	absoluteDependencyPath = filepath.Abs
	evaluateDependencySymlinks = func(string) (string, error) {
		return "", operationErr
	}
	if _, err := canonicalizeNoFollowParentPath(filepath.Join(t.TempDir(), "child")); !errors.Is(err, operationErr) {
		t.Fatalf("expected parent canonicalization failure, got %v", err)
	}
	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "plain root", path: filepath.Join(t.TempDir(), "child")},
		{name: "dependency base", path: filepath.Join(t.TempDir(), "node_modules", "pkg")},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := openConstrainedRoot(testCase.path); !errors.Is(err, operationErr) {
				t.Fatalf("expected canonicalization failure, got %v", err)
			}
		})
	}
}

func assertDependencyResolutionRelativePathFailures(t *testing.T, operationErr error) {
	t.Helper()

	rootPath := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		t.Fatalf("mkdir relative root: %v", err)
	}
	targetPath := filepath.Join(rootPath, "target.js")
	testutil.MustWriteFile(t, targetPath, "export {}\n")

	openDependencyRootNoFollow = safeio.OpenRootNoFollow
	rootParent := filepath.Dir(rootPath)
	evaluateDependencySymlinks = func(path string) (string, error) {
		if path == rootParent {
			return "", operationErr
		}
		return filepath.EvalSymlinks(path)
	}
	if _, err := relativePathWithinRoot(rootPath, targetPath); !errors.Is(err, operationErr) {
		t.Fatalf("expected root canonicalization failure, got %v", err)
	}

	evaluateDependencySymlinks = func(path string) (string, error) {
		if path == rootPath {
			return "", operationErr
		}
		return filepath.EvalSymlinks(path)
	}
	if _, err := relativePathWithinRoot(rootPath, targetPath); !errors.Is(err, operationErr) {
		t.Fatalf("expected target canonicalization failure, got %v", err)
	}

	evaluateDependencySymlinks = filepath.EvalSymlinks
	relativeDependencyPath = func(string, string) (string, error) {
		return "", operationErr
	}
	if _, err := relativePathWithinRoot(rootPath, targetPath); !errors.Is(err, operationErr) {
		t.Fatalf("expected relative-path failure, got %v", err)
	}
}

func TestOpenConstrainedRootPreservesCloseFailures(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if _, err := openConstrainedRoot(depRoot); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing constrained dependency root to surface not-exist, got %v", err)
	}
}

func TestResolveEntrypointUnderRootDiscardsResultOnCloseFailure(t *testing.T) {
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	depRoot := t.TempDir()
	entryPath := filepath.Join(depRoot, "index.js")
	testutil.MustWriteFile(t, entryPath, "export {}\n")
	closeErr := errors.New("close failed")
	openDependencyRootNoFollow = func(string) (safeio.Root, error) {
		return &fakeJSRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				return os.Lstat(filepath.Join(depRoot, name))
			},
			closeErr: closeErr,
		}, nil
	}

	if resolved, ok, err := resolveEntrypointUnderRoot(depRoot, depRoot, "index.js"); ok || resolved != "" || !errors.Is(err, closeErr) || !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("expected close failure to discard entrypoint and surface error, got resolved=%q ok=%v err=%v", resolved, ok, err)
	}
}

func TestRootWalkHelpers(t *testing.T) {
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return &fakeFileWithoutReadDir{}, nil
		},
	}
	if _, err := readRootDirEntries(root); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected invalid readdir handle, got %v", err)
	}

	child := &fakeJSRoot{closeErr: errors.New("child close failed")}
	visit := func(string, fs.FileInfo) (bool, bool, error) {
		return false, false, errors.New("visit failed")
	}
	err := walkChildRootNoFollow(child, "rel", visit, &rootWalkState{}, nil)
	if err == nil || !strings.Contains(err.Error(), "not implemented") || !strings.Contains(err.Error(), "child close failed") {
		t.Fatalf("expected joined walk child error, got %v", err)
	}

	root = &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return &fakeReadDirFile{readDirErr: errors.New("readdir failed"), closeErr: errors.New("close failed")}, nil
		},
	}
	if _, err := readRootDirEntries(root); err == nil || !strings.Contains(err.Error(), "readdir failed") {
		t.Fatalf("expected read-dir error to propagate, got %v", err)
	}

	root = &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return &fakeReadDirFile{entries: nil, closeErr: errors.New("close failed")}, nil
		},
	}
	if _, err := readRootDirEntries(root); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("expected close error after successful ReadDir, got %v", err)
	}
}

func TestDependencyResolutionAdditionalErrorBranches(t *testing.T) {
	t.Run("validateDirectoryPathNoFollowFromBase propagates canonicalization error", func(t *testing.T) {
		originalAbs := absoluteDependencyPath
		originalEval := evaluateDependencySymlinks
		t.Cleanup(func() {
			absoluteDependencyPath = originalAbs
			evaluateDependencySymlinks = originalEval
		})

		baseDir := filepath.Join(t.TempDir(), "repo")
		canonicalErr := errors.New("canonicalize failed")
		evaluateDependencySymlinks = func(path string) (string, error) {
			if path == filepath.Dir(baseDir) {
				return "", canonicalErr
			}
			return originalEval(path)
		}

		if _, err := validateDirectoryPathNoFollowFromBase(baseDir, "node_modules"); !errors.Is(err, canonicalErr) {
			t.Fatalf("expected canonicalization error, got %v", err)
		}
	})

	t.Run("validateDirectoryPathNoFollowFromBase propagates absolute path error", func(t *testing.T) {
		originalAbs := absoluteDependencyPath
		t.Cleanup(func() {
			absoluteDependencyPath = originalAbs
		})

		baseDir := filepath.Join(t.TempDir(), "repo")
		absErr := errors.New("absolute path failed")
		absoluteDependencyPath = func(path string) (string, error) {
			if path == baseDir {
				return "", absErr
			}
			return originalAbs(path)
		}

		if _, err := validateDirectoryPathNoFollowFromBase(baseDir, "node_modules"); !errors.Is(err, absErr) {
			t.Fatalf("expected absolute path error, got %v", err)
		}
	})

	t.Run("validateDirectoryPathNoFollowFromBase rejects symlink component", func(t *testing.T) {
		repo := t.TempDir()
		target := filepath.Join(repo, "target")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(repo, "linked")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if _, err := validateDirectoryPathNoFollowFromBase(repo, "linked"); err == nil || !strings.Contains(err.Error(), "symlinked path component") {
			t.Fatalf("expected symlink component rejection, got %v", err)
		}
	})

	t.Run("validateDirectoryPathNoFollow propagates dependency base symlink error", func(t *testing.T) {
		originalEval := evaluateDependencySymlinks
		t.Cleanup(func() {
			evaluateDependencySymlinks = originalEval
		})

		repo := filepath.Join(t.TempDir(), "repo")
		baseErr := errors.New("base symlink resolution failed")
		evaluateDependencySymlinks = func(path string) (string, error) {
			if path == repo {
				return "", baseErr
			}
			return originalEval(path)
		}

		path := filepath.Join(repo, "node_modules", "pkg")
		if _, err := validateDirectoryPathNoFollow(path); !errors.Is(err, baseErr) {
			t.Fatalf("expected dependency base symlink error, got %v", err)
		}
	})

	t.Run("validateRegularFileNoFollow propagates canonicalization error", func(t *testing.T) {
		originalAbs := absoluteDependencyPath
		originalEval := evaluateDependencySymlinks
		t.Cleanup(func() {
			absoluteDependencyPath = originalAbs
			evaluateDependencySymlinks = originalEval
		})

		filePath := filepath.Join(t.TempDir(), "package.json")
		canonicalErr := errors.New("canonicalize failed")
		evaluateDependencySymlinks = func(path string) (string, error) {
			if path == filepath.Dir(filePath) {
				return "", canonicalErr
			}
			return originalEval(path)
		}

		if err := validateRegularFileNoFollow(filePath); !errors.Is(err, canonicalErr) {
			t.Fatalf("expected canonicalization error, got %v", err)
		}
	})

	t.Run("validateRegularFileWithinRoot propagates lstat error", func(t *testing.T) {
		lstatErr := errors.New("lstat failed")
		root := &fakeJSRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return nil, lstatErr
			},
		}

		if err := validateRegularFileWithinRoot(root, t.TempDir(), jsPackageFile); !errors.Is(err, lstatErr) {
			t.Fatalf("expected lstat error, got %v", err)
		}
	})

	t.Run("resolvePinnedRootPathWithinBoundary propagates absolute path error", func(t *testing.T) {
		originalAbs := absoluteDependencyPath
		t.Cleanup(func() {
			absoluteDependencyPath = originalAbs
		})

		absErr := errors.New("absolute path failed")
		absoluteDependencyPath = func(string) (string, error) {
			return "", absErr
		}

		if _, err := resolvePinnedRootPathWithinBoundary("ignored", ""); !errors.Is(err, absErr) {
			t.Fatalf("expected absolute path error, got %v", err)
		}
	})

	t.Run("resolvePinnedDependencyChainPath propagates allowed root resolution error", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "node_modules", "pkg"), 0o755); err != nil {
			t.Fatalf("mkdir dependency root: %v", err)
		}

		_, err := resolvePinnedDependencyChainPath(repo, []string{"node_modules", "pkg"}, filepath.Join(repo, "missing", "boundary"))
		if err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected allowed-root resolution failure, got %v", err)
		}
	})

	t.Run("resolvePinnedDependencyChainPath skips empty and dot components", func(t *testing.T) {
		repo := t.TempDir()
		depRoot := filepath.Join(repo, "node_modules", "pkg")
		if err := os.MkdirAll(depRoot, 0o755); err != nil {
			t.Fatalf("mkdir dependency root: %v", err)
		}

		got, err := resolvePinnedDependencyChainPath(repo, []string{"", ".", "node_modules", ".", "pkg"}, "")
		if err != nil {
			t.Fatalf("resolve dependency chain with skipped components: %v", err)
		}
		want, err := filepath.EvalSymlinks(depRoot)
		if err != nil {
			t.Fatalf("canonicalize dependency root: %v", err)
		}
		if got != want {
			t.Fatalf("expected resolved root %q, got %q", want, got)
		}
	})

	t.Run("resolvePinnedDependencyChainBase propagates nested resolution error", func(t *testing.T) {
		repo := t.TempDir()
		basePath := filepath.Join(repo, "node_modules", "pkg")
		if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
			t.Fatalf("mkdir node_modules parent: %v", err)
		}
		testutil.MustWriteFile(t, basePath, "not a directory\n")

		if _, err := resolvePinnedDependencyChainBase(basePath); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("expected nested resolution error, got %v", err)
		}
	})

	t.Run("resolvePinnedSymlinkDependencyComponent propagates eval and stat errors", func(t *testing.T) {
		originalEval := evaluateDependencySymlinks
		t.Cleanup(func() {
			evaluateDependencySymlinks = originalEval
		})

		repo := t.TempDir()
		target := filepath.Join(repo, "target")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		link := filepath.Join(repo, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		evalErr := errors.New("eval failed")
		evaluateDependencySymlinks = func(path string) (string, error) {
			if path == link {
				return "", evalErr
			}
			return originalEval(path)
		}
		if _, err := resolvePinnedSymlinkDependencyComponent(link, repo); !errors.Is(err, evalErr) {
			t.Fatalf("expected eval error, got %v", err)
		}

		evaluateDependencySymlinks = originalEval
		if err := os.RemoveAll(target); err != nil {
			t.Fatalf("remove symlink target: %v", err)
		}
		if _, err := resolvePinnedSymlinkDependencyComponent(link, repo); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected stat error after removing target, got %v", err)
		}
	})
}

type fakeFileWithoutReadDir struct{}

func (*fakeFileWithoutReadDir) Read([]byte) (int, error)   { return 0, io.EOF }
func (*fakeFileWithoutReadDir) Write([]byte) (int, error)  { return 0, errors.New("not implemented") }
func (*fakeFileWithoutReadDir) Close() error               { return nil }
func (*fakeFileWithoutReadDir) Stat() (fs.FileInfo, error) { return nil, errors.New("not implemented") }
func (*fakeFileWithoutReadDir) Chmod(os.FileMode) error    { return errors.New("not implemented") }

type fakeReadDirFile struct {
	entries    []fs.DirEntry
	readDirErr error
	closeErr   error
}

func (*fakeReadDirFile) Read([]byte) (int, error)   { return 0, io.EOF }
func (*fakeReadDirFile) Write([]byte) (int, error)  { return 0, errors.New("not implemented") }
func (f *fakeReadDirFile) Close() error             { return f.closeErr }
func (*fakeReadDirFile) Stat() (fs.FileInfo, error) { return nil, errors.New("not implemented") }
func (*fakeReadDirFile) Chmod(os.FileMode) error    { return errors.New("not implemented") }
func (f *fakeReadDirFile) ReadDir(int) ([]fs.DirEntry, error) {
	return f.entries, f.readDirErr
}

type fakeDirEntry struct {
	name string
	mode fs.FileMode
	info fs.FileInfo
}

func (e *fakeDirEntry) Name() string               { return e.name }
func (e *fakeDirEntry) IsDir() bool                { return e.mode.IsDir() }
func (e *fakeDirEntry) Type() fs.FileMode          { return e.mode.Type() }
func (e *fakeDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }
