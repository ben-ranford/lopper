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
	if _, err := openConstrainedRoot(filepath.Join(t.TempDir(), "child")); !errors.Is(err, operationErr) {
		t.Fatalf("expected plain-root canonicalization failure, got %v", err)
	}
	if _, err := openConstrainedRoot(filepath.Join(t.TempDir(), "node_modules", "pkg")); !errors.Is(err, operationErr) {
		t.Fatalf("expected dependency-base canonicalization failure, got %v", err)
	}

	evaluateDependencySymlinks = filepath.EvalSymlinks
	openDependencyRootNoFollow = func(string) (safeio.Root, error) {
		return nil, operationErr
	}
	if _, _, err := openValidatedRootNoFollow(t.TempDir()); !errors.Is(err, operationErr) {
		t.Fatalf("expected validated-root reopen failure, got %v", err)
	}

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
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	info, err := os.Lstat(repo)
	if err != nil {
		t.Fatalf("lstat repo: %v", err)
	}

	t.Run("child open and current close", func(t *testing.T) {
		openErr := errors.New("child open failed")
		closeErr := errors.New("current close failed")
		openDependencyRootNoFollow = func(string) (safeio.Root, error) {
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return info, nil
				},
				openRoot: func(string) (safeio.Root, error) {
					return nil, openErr
				},
				closeErr: closeErr,
			}, nil
		}

		_, err := openConstrainedRoot(depRoot)
		if !errors.Is(err, openErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined child-open and close errors, got %v", err)
		}
	})

	t.Run("current and next close", func(t *testing.T) {
		currentCloseErr := errors.New("current close failed")
		nextCloseErr := errors.New("next close failed")
		openDependencyRootNoFollow = func(string) (safeio.Root, error) {
			child := &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return info, nil
				},
				closeErr: nextCloseErr,
			}
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return info, nil
				},
				openRoot: func(string) (safeio.Root, error) {
					return child, nil
				},
				closeErr: currentCloseErr,
			}, nil
		}

		_, err := openConstrainedRoot(depRoot)
		if !errors.Is(err, currentCloseErr) || !errors.Is(err, nextCloseErr) {
			t.Fatalf("expected joined current and next close errors, got %v", err)
		}
	})

	t.Run("current close only", func(t *testing.T) {
		currentCloseErr := errors.New("current close failed")
		openDependencyRootNoFollow = func(string) (safeio.Root, error) {
			child := &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return info, nil
				},
			}
			return &fakeJSRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return info, nil
				},
				openRoot: func(string) (safeio.Root, error) {
					return child, nil
				},
				closeErr: currentCloseErr,
			}, nil
		}

		_, err := openConstrainedRoot(depRoot)
		if !errors.Is(err, currentCloseErr) {
			t.Fatalf("expected current close error, got %v", err)
		}
	})
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

	if resolved, ok := resolveEntrypointUnderRoot(depRoot, depRoot, "index.js"); ok || resolved != "" {
		t.Fatalf("expected close failure to discard entrypoint, got resolved=%q ok=%v", resolved, ok)
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
	err := walkChildRootNoFollow(child, "rel", func(string, fs.FileInfo) (bool, bool, error) {
		return false, false, errors.New("visit failed")
	})
	if err == nil || !strings.Contains(err.Error(), "not implemented") || !strings.Contains(err.Error(), "child close failed") {
		t.Fatalf("expected joined walk child error, got %v", err)
	}
}

type fakeFileWithoutReadDir struct{}

func (*fakeFileWithoutReadDir) Read([]byte) (int, error)   { return 0, io.EOF }
func (*fakeFileWithoutReadDir) Write([]byte) (int, error)  { return 0, errors.New("not implemented") }
func (*fakeFileWithoutReadDir) Close() error               { return nil }
func (*fakeFileWithoutReadDir) Stat() (fs.FileInfo, error) { return nil, errors.New("not implemented") }
func (*fakeFileWithoutReadDir) Chmod(os.FileMode) error    { return errors.New("not implemented") }
