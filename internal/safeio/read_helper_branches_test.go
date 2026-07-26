package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func makeRegularFileSymlinkFixture(t *testing.T) (linkInfo, targetInfo fs.FileInfo) {
	t.Helper()
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	linkPath := filepath.Join(rootDir, "linked.txt")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return lstatTestPath(t, linkPath), statTestPath(t, targetPath)
}

func fakeRootExpectingChild(t *testing.T, name string, info fs.FileInfo, child Root) *fakeRoot {
	t.Helper()
	return &fakeRoot{
		lstat: func(got string) (fs.FileInfo, error) {
			if got != name {
				t.Fatalf("unexpected root lstat %q", got)
			}
			return info, nil
		},
		openRoot: func(got string) (Root, error) {
			if got != name {
				t.Fatalf("unexpected root open %q", got)
			}
			return child, nil
		},
	}
}

func fakeRootOpeningLink(linkInfo fs.FileInfo, file File, err error) *fakeRoot {
	return &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
		stat: func(string) (fs.FileInfo, error) {
			if file == nil {
				return nil, err
			}
			return file.Stat()
		},
		open: func(string) (File, error) {
			return file, err
		},
	}
}

func fakeDotRoot(t *testing.T, info fs.FileInfo, closeFn func() error) *fakeRoot {
	t.Helper()
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected dot-root lstat %q", name)
			}
			return info, nil
		},
		close: closeFn,
	}
}

func fakeNestedChildRoot(t *testing.T, selfInfo fs.FileInfo, childName string, childInfo fs.FileInfo, child Root, closeFn func() error) *fakeRoot {
	t.Helper()
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case ".":
				return selfInfo, nil
			case childName:
				return childInfo, nil
			default:
				t.Fatalf("unexpected nested-child lstat %q", name)
				return nil, nil
			}
		},
		openRoot: func(name string) (Root, error) {
			if name != childName {
				t.Fatalf("unexpected nested-child open %q", name)
			}
			return child, nil
		},
		close: closeFn,
	}
}

func fakeFileExpectingStat(t *testing.T, expectedName string, info fs.FileInfo, onClose func() error) func(string) (File, error) {
	t.Helper()
	return func(name string) (File, error) {
		if name != expectedName {
			t.Fatalf("unexpected open %q", name)
		}
		return &fakeFile{
			stat: func() (fs.FileInfo, error) {
				return info, nil
			},
			close: onClose,
		}, nil
	}
}

func TestOpenRootNoFollowDelegatesToConfiguredFileSystemFromReadHelpers(t *testing.T) {
	expectedRoot := &fakeRoot{}
	calledWith := ""
	withFileSystem(t, &fakeFileSystem{
		openRootNoFollow: func(name string) (Root, error) {
			calledWith = name
			return expectedRoot, nil
		},
	})

	root, err := OpenRootNoFollow("/tmp/safeio-root")
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	if root != expectedRoot {
		t.Fatal("expected delegated root to be returned")
	}
	if calledWith != "/tmp/safeio-root" {
		t.Fatalf("expected OpenRootNoFollow to delegate path, got %q", calledWith)
	}
}

func TestOpenRootNoFollowOpensNestedDirectory(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root dir: %v", err)
	}
	nested := filepath.Join(rootDir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested root: %v", err)
	}

	root, err := (&osFileSystem{}).OpenRootNoFollow(nested)
	if err != nil {
		t.Fatalf("OpenRootNoFollow nested path: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close nested root: %v", closeErr)
		}
	}()

	if _, err := root.Lstat("."); err != nil {
		t.Fatalf("lstat nested root: %v", err)
	}
}

func TestResolveRelativeTargetWithinRootRejectsEscapingPath(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(filepath.Dir(rootDir), "outside.txt")

	_, err := resolveRelativeTargetWithinRoot(rootDir, targetPath)
	if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf("expected root escape rejection, got %v", err)
	}
}

func TestResolveRelativeTargetWithinRootWrapsRelativePathFailure(t *testing.T) {
	expectedErr := errors.New("rel failure")
	withFileSystem(t, &fakeFileSystem{
		rel: func(basepath, targpath string) (string, error) {
			return "", expectedErr
		},
	})

	_, err := resolveRelativeTargetWithinRoot("/root", "/root/file.txt")
	if err == nil {
		t.Fatal("expected relative path failure")
	}
	if !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "compute relative path") {
		t.Fatalf("expected wrapped relative path failure, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootRejectsNonRegularNonSymlink(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return dirInfo, nil
		},
	}

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "target", "target")
	if !errors.Is(err, ErrNonRegularFile) {
		t.Fatalf("expected ErrNonRegularFile, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootTranslatesMissingPath(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, fs.ErrNotExist
		},
	}

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "missing.txt", "missing.txt")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated missing path error, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootTranslatesMissingSymlinkTarget(t *testing.T) {
	linkPath := filepath.Join(t.TempDir(), "missing-link")
	if err := os.Symlink("missing-target", linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkInfo := lstatTestPath(t, linkPath)
	root := fakeRootOpeningLink(linkInfo, nil, fs.ErrNotExist)

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "target", "target")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated missing file error, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootReturnsStatError(t *testing.T) {
	linkInfo, _ := makeRegularFileSymlinkFixture(t)
	statErr := errors.New("stat failure")
	openedFile := &fakeFile{
		stat: func() (fs.FileInfo, error) {
			return nil, statErr
		},
	}
	root := fakeRootOpeningLink(linkInfo, openedFile, nil)

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "target", "target")
	if !errors.Is(err, statErr) {
		t.Fatalf("expected stat error, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootRejectsOpenedNonRegularSymlinkTarget(t *testing.T) {
	linkInfo, _ := makeRegularFileSymlinkFixture(t)
	dirInfo := statTestPath(t, t.TempDir())
	openedFile := &fakeFile{
		stat: func() (fs.FileInfo, error) {
			return dirInfo, nil
		},
		close: func() error { return nil },
	}
	root := fakeRootOpeningLink(linkInfo, openedFile, nil)

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "target", "target")
	if !errors.Is(err, ErrNonRegularFile) {
		t.Fatalf("expected ErrNonRegularFile, got %v", err)
	}
}

func TestReadFileWithinRootLimitReturnsReadinessError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	root := openTestRoot(t, rootDir)
	expectedErr := errors.New("target not ready")
	originalReady := readFileTargetReadyFn
	readFileTargetReadyFn = func() error { return expectedErr }
	t.Cleanup(func() {
		readFileTargetReadyFn = originalReady
	})

	data, err := ReadFileWithinRootLimit(root, writeTestFileName, 0)
	if len(data) != 0 {
		t.Fatalf("expected no data when readiness fails, got %q", string(data))
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected readiness error, got %v", err)
	}
}

func TestReadFileUnderLimitReturnsReadinessError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("target not ready")
	originalReady := readFileTargetReadyFn
	readFileTargetReadyFn = func() error { return expectedErr }
	t.Cleanup(func() {
		readFileTargetReadyFn = originalReady
	})

	data, err := ReadFileUnderLimit(rootDir, targetPath, 0)
	if len(data) != 0 {
		t.Fatalf("expected no data when readiness fails, got %q", string(data))
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected readiness error, got %v", err)
	}
}

func TestReadFileUnderLimitPropagatesPreflightErrorAfterResolution(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	targetInfo := statTestPath(t, targetPath)
	lstatCalls := 0

	withFileSystem(t, &fakeFileSystem{
		base: &osFileSystem{},
		openRootNoFollow: func(name string) (Root, error) {
			if name != rootDir {
				t.Fatalf("unexpected root open %q", name)
			}
			return &fakeRoot{
				lstat: func(path string) (fs.FileInfo, error) {
					switch path {
					case ".":
						return statTestPath(t, rootDir), nil
					case writeTestFileName:
						lstatCalls++
						if lstatCalls < 3 {
							return targetInfo, nil
						}
						return nil, fs.ErrNotExist
					default:
						t.Fatalf("unexpected lstat %q", path)
						return nil, nil
					}
				},
				close: func() error { return nil },
			}, nil
		},
	})

	data, err := ReadFileUnderLimit(rootDir, targetPath, 0)
	if len(data) != 0 {
		t.Fatalf("expected no data when preflight fails, got %q", string(data))
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated preflight error, got %v", err)
	}
}

func TestValidateRegularPathWithinRootTranslatesMissingPath(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, fs.ErrNotExist
		},
	}

	err := validateRegularPathWithinRoot(root, "missing.txt", "missing.txt")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated missing file error, got %v", err)
	}
}

func TestValidateRegularPathWithinRootAllowsSymlink(t *testing.T) {
	linkInfo, _ := makeRegularFileSymlinkFixture(t)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
	}

	if err := validateRegularPathWithinRoot(root, "linked.txt", "linked.txt"); err != nil {
		t.Fatalf("expected symlink validation to defer to open-time checks, got %v", err)
	}
}

func TestOpenExactParentRootPropagatesNoFollowOpenError(t *testing.T) {
	parentDir := t.TempDir()
	openErr := errors.New("open root no-follow failed")

	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(name string) (Root, error) {
		resolvedParentDir, err := filepath.EvalSymlinks(parentDir)
		if err != nil {
			t.Fatalf("resolve parent dir: %v", err)
		}
		if name != resolvedParentDir {
			t.Fatalf("unexpected root open %q", name)
		}
		return nil, openErr
	}})

	root, err := openExactParentRoot(parentDir)
	if root != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected no-follow open error, got root=%v err=%v", root, err)
	}
}

func TestOpenExactParentRootPropagatesEvalSymlinksError(t *testing.T) {
	parentDir := t.TempDir()
	expectedErr := errors.New("eval symlinks failed")
	originalEval := evalSymlinksFn
	evalSymlinksFn = func(path string) (string, error) {
		if path != parentDir {
			t.Fatalf("unexpected EvalSymlinks path %q", path)
		}
		return "", expectedErr
	}
	t.Cleanup(func() {
		evalSymlinksFn = originalEval
	})

	root, err := openExactParentRoot(parentDir)
	if root != nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected EvalSymlinks error, got root=%v err=%v", root, err)
	}
}

func TestOpenExactParentRootJoinsLookupAndCloseErrors(t *testing.T) {
	parentDir := t.TempDir()
	lookupErr := errors.New("lookup failed")
	closeErr := errors.New("close failed")

	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "." {
					t.Fatalf("unexpected lstat %q", name)
				}
				return nil, lookupErr
			},
			close: func() error { return closeErr },
		}, nil
	}})

	root, err := openExactParentRoot(parentDir)
	if root != nil || !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined lookup/close errors, got root=%v err=%v", root, err)
	}
}

func TestOpenPinnedReadRootReturnsReadyError(t *testing.T) {
	rootDir := t.TempDir()
	readyErr := errors.New("ready failed")

	originalReady := readRootOpenReadyFn
	readRootOpenReadyFn = func() error { return readyErr }
	t.Cleanup(func() {
		readRootOpenReadyFn = originalReady
	})

	root, err := openPinnedReadRoot(rootDir)
	if root != nil || !errors.Is(err, readyErr) {
		t.Fatalf("expected ready error, got root=%v err=%v", root, err)
	}
}

func TestOpenPinnedReadRootPropagatesOpenRootError(t *testing.T) {
	rootDir := t.TempDir()
	openErr := errors.New("open root failed")

	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		if name != rootDir {
			t.Fatalf("unexpected root open %q", name)
		}
		return nil, openErr
	}})

	root, err := openPinnedReadRoot(rootDir)
	if root != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected open-root error, got root=%v err=%v", root, err)
	}
}

func TestOpenPinnedReadRootJoinsLookupAndCloseErrors(t *testing.T) {
	rootDir := t.TempDir()
	lookupErr := errors.New("lookup failed")
	closeErr := errors.New("close failed")

	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "." {
					t.Fatalf("unexpected lstat %q", name)
				}
				return nil, lookupErr
			},
			close: func() error { return closeErr },
		}, nil
	}})

	root, err := openPinnedReadRoot(rootDir)
	if root != nil || !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined lookup/close errors, got root=%v err=%v", root, err)
	}
}

func TestOpenPinnedReadRootRejectsChangedOpenedRoot(t *testing.T) {
	rootDir := t.TempDir()
	otherFile := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(otherFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write other file: %v", err)
	}
	otherInfo := statTestPath(t, otherFile)

	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "." {
					t.Fatalf("unexpected lstat %q", name)
				}
				return otherInfo, nil
			},
			close: func() error { return nil },
		}, nil
	}})

	root, err := openPinnedReadRoot(rootDir)
	if root != nil {
		t.Fatal("expected changed opened root to be rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "root changed while opening") {
		t.Fatalf("expected root-changed error, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootReturnsSymlinkInfos(t *testing.T) {
	linkInfo, targetInfo := makeRegularFileSymlinkFixture(t)
	openedFile := &fakeFile{
		stat: func() (fs.FileInfo, error) {
			return targetInfo, nil
		},
		close: func() error { return nil },
	}
	root := fakeRootOpeningLink(linkInfo, openedFile, nil)

	pathInfo, openedInfo, err := preflightPinnedReadTargetWithinRoot(root, "linked.txt", "linked.txt")
	if err != nil {
		t.Fatalf("preflightPinnedReadTargetWithinRoot returned error: %v", err)
	}
	if pathInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected path info to remain symlink, got mode %v", pathInfo.Mode())
	}
	if !openedInfo.Mode().IsRegular() {
		t.Fatalf("expected opened info to be regular, got mode %v", openedInfo.Mode())
	}
}

func TestPreflightPinnedReadTargetWithinRootRejectsEscapingSymlinkTarget(t *testing.T) {
	linkInfo, _ := makeRegularFileSymlinkFixture(t)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
		stat: func(string) (fs.FileInfo, error) {
			return nil, ErrPathEscapesRoot
		},
	}

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "linked.txt", "linked.txt")
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestPreflightPinnedReadTargetWithinRootRejectsSymlinkStatResult(t *testing.T) {
	linkInfo, _ := makeRegularFileSymlinkFixture(t)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
		stat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
	}

	_, _, err := preflightPinnedReadTargetWithinRoot(root, "linked.txt", "linked.txt")
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestResolveReadTargetWithinRootTranslatesBrokenSymlink(t *testing.T) {
	rootDir := t.TempDir()
	linkPath := filepath.Join(rootDir, "broken.txt")
	if err := os.Symlink("missing-target", linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	root := openTestRoot(t, rootDir)
	_, err := resolveReadTargetWithinRoot(root, rootedTarget{
		rootAbs: rootDir,
		rel:     "broken.txt",
		abs:     linkPath,
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated broken symlink error, got %v", err)
	}
}

func TestResolveReadTargetWithinRootRejectsSymlinkLoop(t *testing.T) {
	rootDir := t.TempDir()
	linkPath := filepath.Join(rootDir, "loop.txt")
	if err := os.Symlink("loop.txt", linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkInfo := lstatTestPath(t, linkPath)
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "loop.txt" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return linkInfo, nil
		},
	}
	originalEval := evalSymlinksFn
	evalSymlinksFn = func(path string) (string, error) {
		switch path {
		case linkPath:
			return "", syscall.ELOOP
		case rootDir:
			return rootDir, nil
		default:
			t.Fatalf("unexpected EvalSymlinks path %q", path)
			return "", nil
		}
	}
	t.Cleanup(func() {
		evalSymlinksFn = originalEval
	})

	_, err := resolveReadTargetWithinRoot(root, rootedTarget{
		rootAbs: rootDir,
		rel:     "loop.txt",
		abs:     linkPath,
	})
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestResolveReadTargetWithinRootRejectsSymlinkToDirectory(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "dir-target"), 0o755); err != nil {
		t.Fatalf("mkdir dir target: %v", err)
	}
	linkPath := filepath.Join(rootDir, "dir-link")
	if err := os.Symlink("dir-target", linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	root := openTestRoot(t, rootDir)
	_, err := resolveReadTargetWithinRoot(root, rootedTarget{
		rootAbs: rootDir,
		rel:     "dir-link",
		abs:     linkPath,
	})
	if !errors.Is(err, ErrNonRegularFile) {
		t.Fatalf("expected ErrNonRegularFile, got %v", err)
	}
}

func TestResolveReadTargetWithinRootRejectsSymlinkEscapingRoot(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	linkPath := filepath.Join(rootDir, "outside-link")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	root := openTestRoot(t, rootDir)
	_, err := resolveReadTargetWithinRoot(root, rootedTarget{
		rootAbs: rootDir,
		rel:     "outside-link",
		abs:     linkPath,
	})
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestResolveReadTargetWithinRootRejectsUnreadableSymlinkTarget(t *testing.T) {
	rootDir := t.TempDir()
	linkPath := filepath.Join(rootDir, "locked-link")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkInfo := lstatTestPath(t, linkPath)
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "locked-link" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return linkInfo, nil
		},
	}
	expectedErr := fs.ErrPermission
	originalEval := evalSymlinksFn
	evalSymlinksFn = func(path string) (string, error) {
		switch path {
		case linkPath:
			return "", expectedErr
		case rootDir:
			return rootDir, nil
		default:
			t.Fatalf("unexpected EvalSymlinks path %q", path)
			return "", nil
		}
	}
	t.Cleanup(func() {
		evalSymlinksFn = originalEval
	})

	_, err := resolveReadTargetWithinRoot(root, rootedTarget{
		rootAbs: rootDir,
		rel:     "locked-link",
		abs:     linkPath,
	})
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestResolveReadTargetWithinRootPropagatesCanonicalRootResolutionError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	targetInfo := statTestPath(t, targetPath)
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "target.txt" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return targetInfo, nil
		},
	}

	_, err := resolveReadTargetWithinRoot(root, rootedTarget{
		rootAbs: filepath.Join(rootDir, "missing-root"),
		rel:     "target.txt",
		abs:     targetPath,
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected canonical-root resolution error, got %v", err)
	}
}

func TestOpenResolvedReadTargetWithinRootRejectsMissingRewalkTarget(t *testing.T) {
	target := resolvedReadTarget{rel: "file.txt", abs: filepath.Join(t.TempDir(), "missing.txt"), requirePinnedRewalk: true}

	file, err := openResolvedReadTargetWithinRoot(&fakeRoot{}, t.TempDir(), target)
	if file != nil {
		t.Fatal("expected rewalk open to fail without a file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated missing file error, got %v", err)
	}
}

func TestOpenResolvedReadTargetWithinRootRejectsNonRegularRewalkTarget(t *testing.T) {
	dirPath := t.TempDir()
	target := resolvedReadTarget{rel: "dir", abs: dirPath, requirePinnedRewalk: true}

	file, err := openResolvedReadTargetWithinRoot(&fakeRoot{}, t.TempDir(), target)
	if file != nil {
		t.Fatal("expected directory target to be rejected")
	}
	if !errors.Is(err, ErrNonRegularFile) {
		t.Fatalf("expected ErrNonRegularFile, got %v", err)
	}
}

func TestOpenResolvedReadTargetWithinRootRewalksPinnedRegularTarget(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	root := openTestRoot(t, rootDir)
	file, err := openResolvedReadTargetWithinRoot(root, rootDir, resolvedReadTarget{
		rel:                 "target.txt",
		abs:                 targetPath,
		requirePinnedRewalk: true,
	})
	if err != nil {
		t.Fatalf("expected rewalk open to succeed, got %v", err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Fatalf("close rewalked file: %v", err)
		}
	})
}

func TestOpenResolvedReadTargetWithinRootOpensDirectTargetWithoutPinnedRewalk(t *testing.T) {
	expectedFile := &fakeFile{}
	root := &fakeRoot{
		open: func(name string) (File, error) {
			if name != "file.txt" {
				t.Fatalf("unexpected open %q", name)
			}
			return expectedFile, nil
		},
	}

	file, err := openResolvedReadTargetWithinRoot(root, t.TempDir(), resolvedReadTarget{
		rel:                 "file.txt",
		abs:                 filepath.Join(t.TempDir(), "file.txt"),
		requirePinnedRewalk: false,
	})
	if err != nil {
		t.Fatalf("expected direct open without rewalk, got %v", err)
	}
	if file != expectedFile {
		t.Fatalf("expected opened file to be returned, got %v", file)
	}
}

func TestOpenPinnedReadTargetWithinRootRejectsChangedLeaf(t *testing.T) {
	originalPath := filepath.Join(t.TempDir(), "original.txt")
	changedPath := filepath.Join(t.TempDir(), "changed.txt")
	if err := os.WriteFile(originalPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write changed file: %v", err)
	}
	expectedInfo := statTestPath(t, originalPath)
	changedInfo := statTestPath(t, changedPath)

	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return changedInfo, nil
		},
		open: func(string) (File, error) {
			t.Fatal("expected pinned open to stop before opening changed leaf")
			return nil, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "target.txt", "target.txt", expectedInfo, expectedInfo)
	if file != nil {
		t.Fatal("expected changed leaf to be rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "path changed while opening: target.txt") {
		t.Fatalf("expected changed-leaf error, got %v", err)
	}
}

func TestOpenPinnedReadTargetWithinRootRejectsChangedLeafToDirectory(t *testing.T) {
	originalPath := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(originalPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	expectedInfo := statTestPath(t, originalPath)
	dirInfo := statTestPath(t, t.TempDir())
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return dirInfo, nil
		},
		open: func(string) (File, error) {
			t.Fatal("expected non-regular replacement to be rejected before open")
			return nil, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "target.txt", "target.txt", expectedInfo, expectedInfo)
	if file != nil {
		t.Fatal("expected non-regular replacement to be rejected")
	}
	if !errors.Is(err, ErrNonRegularFile) {
		t.Fatalf("expected ErrNonRegularFile, got %v", err)
	}
}

func TestOpenPinnedReadTargetWithinRootRejectsIntroducedLeafSymlink(t *testing.T) {
	linkInfo, targetInfo := makeRegularFileSymlinkFixture(t)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
		open: func(string) (File, error) {
			t.Fatal("expected introduced symlink to be rejected before open")
			return nil, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "linked.txt", "linked.txt", targetInfo, targetInfo)
	if file != nil {
		t.Fatal("expected introduced leaf symlink to be rejected")
	}
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestOpenPinnedReadTargetWithinRootRejectsRemovedLeafSymlink(t *testing.T) {
	linkInfo, targetInfo := makeRegularFileSymlinkFixture(t)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return targetInfo, nil
		},
		open: func(string) (File, error) {
			t.Fatal("expected removed symlink to be rejected before open")
			return nil, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "linked.txt", "linked.txt", linkInfo, targetInfo)
	if file != nil {
		t.Fatal("expected removed leaf symlink to be rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "path changed while opening: linked.txt") {
		t.Fatalf("expected path-changed error, got %v", err)
	}
}

func TestOpenPinnedReadTargetWithinRootRejectsChangedSymlinkLeaf(t *testing.T) {
	rootA := t.TempDir()
	secondRoot := t.TempDir()
	firstTarget := filepath.Join(rootA, "first.txt")
	secondTarget := filepath.Join(secondRoot, "second.txt")
	if err := os.WriteFile(firstTarget, []byte("first"), 0o600); err != nil {
		t.Fatalf("write first target: %v", err)
	}
	if err := os.WriteFile(secondTarget, []byte("second"), 0o600); err != nil {
		t.Fatalf("write second target: %v", err)
	}
	expectedLink := filepath.Join(rootA, "expected-link.txt")
	actualLink := filepath.Join(secondRoot, "actual-link.txt")
	if err := os.Symlink(firstTarget, expectedLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(secondTarget, actualLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	expectedLinkInfo := lstatTestPath(t, expectedLink)
	actualLinkInfo := lstatTestPath(t, actualLink)
	expectedOpenedInfo := statTestPath(t, firstTarget)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return actualLinkInfo, nil
		},
		open: func(string) (File, error) {
			t.Fatal("expected changed symlink leaf to be rejected before open")
			return nil, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "linked.txt", "linked.txt", expectedLinkInfo, expectedOpenedInfo)
	if file != nil {
		t.Fatal("expected changed symlink leaf to be rejected")
	}
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestOpenPinnedReadTargetWithinRootJoinsOpenStatAndCloseErrors(t *testing.T) {
	parentDir := t.TempDir()
	parentInfo := statTestPath(t, parentDir)
	targetPath := filepath.Join(parentDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	targetInfo := statTestPath(t, targetPath)
	statErr := errors.New("opened stat failure")
	rootClosed := false
	fileClosed := false

	childInfo := statTestPath(t, parentDir)
	child := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case ".":
				return childInfo, nil
			case "target.txt":
				return targetInfo, nil
			default:
				t.Fatalf("unexpected lstat %q", name)
				return nil, nil
			}
		},
		open: func(name string) (File, error) {
			if name != "target.txt" {
				t.Fatalf("unexpected open %q", name)
			}
			return &fakeFile{
				stat: func() (fs.FileInfo, error) {
					return nil, statErr
				},
				close: func() error {
					fileClosed = true
					return errors.New("opened close failure")
				},
			}, nil
		},
		close: func() error {
			rootClosed = true
			return nil
		},
	}
	root := fakeRootExpectingChild(t, "nested", parentInfo, child)

	file, err := openPinnedReadTargetWithinRoot(root, parentDir, filepath.Join("nested", "target.txt"), targetPath, targetInfo, targetInfo)
	if file != nil {
		t.Fatal("expected failed pinned open to return nil file")
	}
	if !errors.Is(err, statErr) {
		t.Fatalf("expected stat error, got %v", err)
	}
	if !fileClosed {
		t.Fatal("expected failed pinned open to close the opened file")
	}
	if !rootClosed {
		t.Fatal("expected owned parent root to be closed")
	}
}

func TestOpenPinnedReadTargetWithinRootTranslatesMissingLeaf(t *testing.T) {
	expectedInfoPath := filepath.Join(t.TempDir(), "expected.txt")
	if err := os.WriteFile(expectedInfoPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write expected file: %v", err)
	}
	expectedInfo := statTestPath(t, expectedInfoPath)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, fs.ErrNotExist
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "target.txt", "target.txt", expectedInfo, expectedInfo)
	if file != nil {
		t.Fatal("expected missing leaf to fail")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected translated missing file error, got %v", err)
	}
}

func TestOpenPinnedReadTargetWithinRootClosesFileWhenOpenedTargetChanges(t *testing.T) {
	parentDir := t.TempDir()
	targetPath := filepath.Join(parentDir, "target.txt")
	otherPath := filepath.Join(parentDir, "other.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatalf("write other file: %v", err)
	}
	expectedInfo := statTestPath(t, targetPath)
	openedInfo := statTestPath(t, otherPath)
	fileClosed := false
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "target.txt" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return expectedInfo, nil
		},
		open: fakeFileExpectingStat(t, "target.txt", openedInfo, func() error {
			fileClosed = true
			return nil
		}),
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "target.txt", "target.txt", expectedInfo, expectedInfo)
	if file != nil {
		t.Fatal("expected changed opened target to be rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "path changed while opening: target.txt") {
		t.Fatalf("expected changed-opened-target error, got %v", err)
	}
	if !fileClosed {
		t.Fatal("expected mismatched opened target to be closed")
	}
}

func TestOpenPinnedReadTargetWithinRootRejectsOpenedNonRegularSymlinkTarget(t *testing.T) {
	linkInfo, targetInfo := makeRegularFileSymlinkFixture(t)
	dirInfo := statTestPath(t, t.TempDir())
	fileClosed := false
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
		open: func(string) (File, error) {
			return &fakeFile{
				stat: func() (fs.FileInfo, error) {
					return dirInfo, nil
				},
				close: func() error {
					fileClosed = true
					return nil
				},
			}, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "linked.txt", "linked.txt", linkInfo, targetInfo)
	if file != nil {
		t.Fatal("expected opened non-regular symlink target to be rejected")
	}
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
	if !fileClosed {
		t.Fatal("expected opened non-regular symlink target to be closed")
	}
}

func TestOpenPinnedReadTargetWithinRootTranslatesExpectedSymlinkOpenError(t *testing.T) {
	linkInfo, targetInfo := makeRegularFileSymlinkFixture(t)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return linkInfo, nil
		},
		open: func(string) (File, error) {
			return nil, fs.ErrNotExist
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", "linked.txt", "linked.txt", linkInfo, targetInfo)
	if file != nil {
		t.Fatal("expected symlink open error to fail")
	}
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected ErrTargetPathSymlink, got %v", err)
	}
}

func TestSamePinnedRootPathRecognizesTrustedAliasMatches(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trusted aliases are macOS-specific")
	}

	if !samePinnedRootPath(filepath.Join(string(os.PathSeparator), "tmp"), filepath.Join(string(os.PathSeparator), "private", "tmp")) {
		t.Fatal("expected /tmp trusted alias to match /private/tmp")
	}
	if !samePinnedRootPath(filepath.Join(string(os.PathSeparator), "var"), filepath.Join(string(os.PathSeparator), "private", "var")) {
		t.Fatal("expected /var trusted alias to match /private/var")
	}
	if !samePinnedRootPath(filepath.Join(string(os.PathSeparator), "var", "folders", "x"), filepath.Join(string(os.PathSeparator), "private", "var", "folders", "x")) {
		t.Fatal("expected nested /var trusted alias to match /private/var")
	}
}

func TestOpenPinnedReadTargetWithinRootJoinsOwnedParentCloseError(t *testing.T) {
	parentDir := t.TempDir()
	targetPath := filepath.Join(parentDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	targetInfo := statTestPath(t, targetPath)
	closeErr := errors.New("owned parent close failure")
	parentInfo := statTestPath(t, parentDir)
	fileClosed := false

	child := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case ".":
				return parentInfo, nil
			case "target.txt":
				return targetInfo, nil
			default:
				t.Fatalf("unexpected child lstat %q", name)
				return nil, nil
			}
		},
		open: fakeFileExpectingStat(t, "target.txt", targetInfo, func() error {
			fileClosed = true
			return nil
		}),
		close: func() error { return closeErr },
	}
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "nested" {
				t.Fatalf("unexpected root lstat %q", name)
			}
			return statTestPath(t, parentDir), nil
		},
		openRoot: func(name string) (Root, error) {
			if name != "nested" {
				t.Fatalf("unexpected root open %q", name)
			}
			return child, nil
		},
	}

	file, err := openPinnedReadTargetWithinRoot(root, parentDir, filepath.Join("nested", "target.txt"), targetPath, targetInfo, targetInfo)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected owned parent close failure, got %v", err)
	}
	if file != nil {
		t.Fatal("expected opened file to be closed and discarded when owned parent close fails")
	}
	if !fileClosed {
		t.Fatal("expected opened file to be closed when owned parent close fails")
	}
}

func TestOpenReadTargetParentNoFollowReturnsOriginalRootForCurrentDirectory(t *testing.T) {
	root := &fakeRoot{}

	parent, owned, err := openReadTargetParentNoFollow(root, "/root", ".")
	if err != nil {
		t.Fatalf("openReadTargetParentNoFollow returned error: %v", err)
	}
	if parent != root {
		t.Fatal("expected current-directory parent lookup to return the original root")
	}
	if owned {
		t.Fatal("expected original root to remain unowned")
	}
}

func TestOpenReadTargetParentNoFollowSkipsEmptyAndDotComponents(t *testing.T) {
	levelOne := statTestPath(t, t.TempDir())
	levelTwo := statTestPath(t, t.TempDir())
	second := fakeDotRoot(t, levelTwo, nil)
	first := fakeNestedChildRoot(t, levelOne, "second", levelTwo, second, func() error { return nil })
	root := fakeRootExpectingChild(t, "first", levelOne, first)

	parent, owned, err := openReadTargetParentNoFollow(root, "/root", "."+string(os.PathSeparator)+"first"+string(os.PathSeparator)+string(os.PathSeparator)+"second")
	if err != nil {
		t.Fatalf("openReadTargetParentNoFollow with dot path: %v", err)
	}
	if parent != second || !owned {
		t.Fatalf("expected second root to be returned as owned parent, got parent=%v owned=%v", parent, owned)
	}
}

func TestOpenReadTargetParentNoFollowClosesNextRootWhenCurrentCloseFails(t *testing.T) {
	levelOne := statTestPath(t, t.TempDir())
	levelTwo := statTestPath(t, t.TempDir())
	currentCloseErr := errors.New("current close failure")
	nextCloseErr := errors.New("next close failure")
	nextClosed := false

	second := fakeDotRoot(t, levelTwo, func() error {
		nextClosed = true
		return nextCloseErr
	})
	first := fakeNestedChildRoot(t, levelOne, "second", levelTwo, second, func() error { return currentCloseErr })
	root := fakeRootExpectingChild(t, "first", levelOne, first)

	parent, owned, err := openReadTargetParentNoFollow(root, "/root", filepath.Join("first", "second"))
	if parent != nil || owned {
		t.Fatalf("expected close failure to abort traversal, got parent=%v owned=%v", parent, owned)
	}
	if !errors.Is(err, currentCloseErr) || !errors.Is(err, nextCloseErr) {
		t.Fatalf("expected joined current and next close errors, got %v", err)
	}
	if !nextClosed {
		t.Fatal("expected next root to be closed on current close failure")
	}
}

func TestOpenReadTargetParentNoFollowClosesOwnedRootOnTraversalFailure(t *testing.T) {
	rootDir := t.TempDir()
	levelOneInfo := statTestPath(t, rootDir)
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, fs.ErrNotExist
		},
		close: func() error {
			childClosed = true
			return nil
		},
	}
	root := fakeRootExpectingChild(t, "first", levelOneInfo, child)

	parent, owned, err := openReadTargetParentNoFollow(root, rootDir, filepath.Join("first", "second"))
	if parent != nil || owned {
		t.Fatal("expected traversal failure to return no parent")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing-child traversal error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected owned child root to be closed")
	}
}

func TestOpenReadTargetParentNoFollowJoinsOwnedRootCloseErrorOnTraversalFailure(t *testing.T) {
	rootDir := t.TempDir()
	levelOneInfo := statTestPath(t, rootDir)
	closeErr := errors.New("owned child close failure")
	child := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return levelOneInfo, nil
			}
			return nil, fs.ErrNotExist
		},
		close: func() error {
			return closeErr
		},
	}
	root := fakeRootExpectingChild(t, "first", levelOneInfo, child)

	_, _, err := openReadTargetParentNoFollow(root, rootDir, filepath.Join("first", "second"))
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, closeErr) {
		t.Fatalf("expected traversal failure joined with close error, got %v", err)
	}
}

func TestOpenReadTargetParentNoFollowJoinsOriginalTraversalError(t *testing.T) {
	rootDir := t.TempDir()
	levelOneInfo := statTestPath(t, rootDir)
	childLookupErr := errors.New("child lookup sentinel")
	closeErr := errors.New("owned child close sentinel")
	child := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return levelOneInfo, nil
			}
			return nil, childLookupErr
		},
		close: func() error {
			return closeErr
		},
	}
	root := fakeRootExpectingChild(t, "first", levelOneInfo, child)

	_, _, err := openReadTargetParentNoFollow(root, rootDir, filepath.Join("first", "second"))
	if !errors.Is(err, ErrNonRegularFile) || !errors.Is(err, childLookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected traversal failure to preserve original and close errors, got %v", err)
	}
}

func TestOpenReadTargetParentNoFollowClosesNextRootWhenIntermediateCloseFails(t *testing.T) {
	rootDir := t.TempDir()
	firstInfo := statTestPath(t, rootDir)
	secondInfo := statTestPath(t, rootDir)
	closeErr := errors.New("intermediate close failure")
	nextClosed := false

	next := fakeDotRoot(t, secondInfo, func() error {
		nextClosed = true
		return nil
	})
	first := fakeNestedChildRoot(t, firstInfo, "second", secondInfo, next, func() error { return closeErr })
	root := fakeRootExpectingChild(t, "first", firstInfo, first)

	parent, owned, err := openReadTargetParentNoFollow(root, rootDir, filepath.Join("first", "second"))
	if parent != nil || owned {
		t.Fatal("expected intermediate close failure to abort traversal")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected intermediate close error, got %v", err)
	}
	if !nextClosed {
		t.Fatal("expected newly opened next root to be closed when intermediate close fails")
	}
}

func TestCloseOpenedReadRootWithErrorReturnsOriginalErrorWhenRootIsNotOwned(t *testing.T) {
	expectedErr := errors.New("primary error")
	root := &fakeRoot{close: func() error {
		t.Fatal("expected close to be skipped for unowned root")
		return nil
	}}

	err := closeOpenedReadRootWithError(root, false, expectedErr)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected original error, got %v", err)
	}
}

func TestCloseOpenedReadRootWithErrorJoinsOwnedRootCloseError(t *testing.T) {
	expectedErr := errors.New("primary error")
	closeErr := errors.New("close error")
	root := &fakeRoot{close: func() error {
		return closeErr
	}}

	err := closeOpenedReadRootWithError(root, true, expectedErr)
	if !errors.Is(err, expectedErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined primary and close errors, got %v", err)
	}
}

func TestCloseOpenedReadTargetParentJoinsParentCloseErrorWithoutFile(t *testing.T) {
	primaryErr := errors.New("primary error")
	closeErr := errors.New("close error")
	parent := &fakeRoot{close: func() error { return closeErr }}

	closeOpenedReadTargetParent(parent, true, nil, &primaryErr)
	if !errors.Is(primaryErr, closeErr) {
		t.Fatalf("expected parent close error to be joined, got %v", primaryErr)
	}
}
