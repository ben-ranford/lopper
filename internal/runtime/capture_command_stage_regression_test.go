package runtime

import (
	"bytes"
	"context"
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

func TestTrustedRuntimeExecutableSourceNilLifecycle(t *testing.T) {
	t.Run("nil source", func(t *testing.T) {
		if executable, err := newTrustedRuntimeExecutableFromSource(nil); err == nil || executable != nil {
			t.Fatalf("expected nil source rejection, executable=%#v err=%v", executable, err)
		}
	})

	t.Run("nil close", func(t *testing.T) {
		var source *runtimeExecutableSource
		if err := source.Close(); err != nil {
			t.Fatalf("expected nil source close to succeed, got %v", err)
		}
	})
}

func TestTrustedRuntimeExecutableSourceStageFailurePreservesCloseErrors(t *testing.T) {
	stageErrDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-stage-missing-tmp-"), "missing")
	t.Setenv("TMPDIR", stageErrDir)
	fileCloseErr := errors.New("source file close failed")
	rootCloseErr := errors.New("source root close failed")
	source := &runtimeExecutableSource{
		path: filepath.Join(stageErrDir, "node"),
		file: &trustedExecutableFileStub{closeErr: fileCloseErr},
		root: &stubRoot{closeErr: rootCloseErr},
	}

	_, err := newTrustedRuntimeExecutableFromSource(source)
	if err == nil || !strings.Contains(err.Error(), "stage trusted runtime executable") {
		t.Fatalf("expected staged executable construction error, got %v", err)
	}
	if !errors.Is(err, fileCloseErr) || !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected source close error identities, got %v", err)
	}
}

func TestTrustedRuntimeExecutableSourceSuccessPreservesCloseError(t *testing.T) {
	sourceDir := testutil.SecureHomeTempDir(t, "runtime-source-close-error-")
	sourcePath := filepath.Join(sourceDir, "node")
	writeRuntimeTestExecutable(t, sourcePath, "#!/bin/sh\nexit 0\n")
	root, err := safeio.OpenRootNoFollow(sourceDir)
	if err != nil {
		t.Fatalf("open source root: %v", err)
	}
	file, err := root.Open("node")
	if err != nil {
		t.Fatalf("open source executable: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat source executable: %v", err)
	}
	closeErr := errors.New("source close failed")
	source := &runtimeExecutableSource{
		path: sourcePath,
		file: &runtimeCloseErrorFile{File: file, err: closeErr},
		root: root,
		info: info,
	}

	executable, err := newTrustedRuntimeExecutableFromSource(source)
	if err == nil || executable != nil {
		t.Fatalf("expected source close failure after staging, executable=%#v err=%v", executable, err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected source close error identity, got %v", err)
	}
}

func TestStageRuntimeExecutableRootSelectionFailure(t *testing.T) {
	wantErr := errors.New("stage root selection failed")
	withRuntimeExecutableStageRootTest(t, func(string) (string, error) {
		return "", wantErr
	})

	assertStageRuntimeExecutableError(t, openRuntimeExecutableSourceForTest(t), wantErr)
}

func TestStageRuntimeExecutableUsesSourceFilesystemRoot(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Windows uses the default temp root")
	}

	source := openRuntimeExecutableSourceForTest(t)
	stage, err := stageRuntimeExecutable(source)
	if err != nil {
		t.Fatalf("stage runtime executable: %v", err)
	}
	if err := source.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("close source after stage: %v", err)
	}
	if got, want := filepath.Dir(stage.dirPath), filepath.Dir(source.path); got != want {
		t.Fatalf("expected stage root %q, got %q", want, got)
	}
	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup staged runtime executable: %v", err)
	}
}

func TestStageRuntimeExecutableConstructionRootSetupFailures(t *testing.T) {
	assertStageRuntimeExecutableFailure(t, "open private root failed", func(t *testing.T, source *runtimeExecutableSource) {
		wantErr := errors.New("open private root failed")
		withSafeioFileSystemTest(t, &safeioFileSystemStub{
			openRootNoFollow: func(string) (safeio.Root, error) {
				return nil, wantErr
			},
		})
		assertStageRuntimeExecutableError(t, source, wantErr)
	})

	assertStageRuntimeExecutableFailure(t, "stat private root failed", func(t *testing.T, source *runtimeExecutableSource) {
		wantErr := errors.New("stat private root failed")
		withSafeioFileSystemTest(t, &safeioFileSystemStub{
			openRootNoFollow: func(string) (safeio.Root, error) {
				return &stubRoot{lstatErr: map[string]error{".": wantErr}}, nil
			},
		})
		assertStageRuntimeExecutableError(t, source, wantErr)
	})
}

func TestStageRuntimeExecutableConstructionLayoutAndFileFailures(t *testing.T) {
	assertStageRuntimeExecutableFailure(t, "prepare private layout failed", func(t *testing.T, source *runtimeExecutableSource) {
		wantErr := errors.New("prepare private layout failed")
		withSafeioFileSystemTest(t, &safeioFileSystemStub{
			openRootNoFollow: func(name string) (safeio.Root, error) {
				info, err := os.Stat(name)
				if err != nil {
					return nil, err
				}
				return &stubRoot{
					selfInfo: info,
					mkdirErr: map[string]error{runtimeExecutableImageDir: wantErr},
				}, nil
			},
		})
		assertStageRuntimeExecutableError(t, source, wantErr)
	})

	assertStageRuntimeExecutableFailure(t, "create private executable failed", func(t *testing.T, source *runtimeExecutableSource) {
		wantErr := errors.New("create private executable failed")
		withRuntimeStageRootOverride(t, func(root safeio.Root) safeio.Root {
			return &runtimeStageRootOverride{
				Root: root,
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return nil, wantErr
				},
			}
		})
		assertStageRuntimeExecutableError(t, source, wantErr)
	})
}

func TestStageRuntimeExecutableConstructionPinFailure(t *testing.T) {
	if !isWindowsRuntime() {
		assertStageRuntimeExecutableFailure(t, "pin private executable failed", func(t *testing.T, source *runtimeExecutableSource) {
			wantErr := errors.New("pin private executable failed")
			withRuntimeStageRootOverride(t, func(root safeio.Root) safeio.Root {
				openCalls := 0
				return &runtimeStageRootOverride{
					Root: root,
					open: func(name string) (safeio.File, error) {
						openCalls++
						if openCalls == 2 {
							return nil, wantErr
						}
						return root.Open(name)
					},
				}
			})
			assertStageRuntimeExecutableError(t, source, wantErr)
		})
	}
}

func TestStageRuntimeExecutableConstructionSealFailures(t *testing.T) {
	assertStageRuntimeExecutableFailure(t, "seal image directory failed", func(t *testing.T, source *runtimeExecutableSource) {
		wantErr := errors.New("seal image directory failed")
		withRuntimeStageRootOverride(t, func(root safeio.Root) safeio.Root {
			return &runtimeStageRootOverride{
				Root: root,
				chmod: func(name string, mode os.FileMode) error {
					if name == runtimeExecutableImageDir && mode == 0o500 {
						return wantErr
					}
					return root.Chmod(name, mode)
				},
			}
		})
		assertStageRuntimeExecutableError(t, source, wantErr)
	})

	assertStageRuntimeExecutableFailure(t, "seal private root failed", func(t *testing.T, source *runtimeExecutableSource) {
		wantErr := errors.New("seal private root failed")
		withRuntimeStageRootOverride(t, func(root safeio.Root) safeio.Root {
			return &runtimeStageRootOverride{
				Root: root,
				chmod: func(name string, mode os.FileMode) error {
					if name == "." && mode == 0o500 {
						return wantErr
					}
					return root.Chmod(name, mode)
				},
			}
		})
		assertStageRuntimeExecutableError(t, source, wantErr)
	})
}

func TestRuntimeExecutableCopyFailures(t *testing.T) {
	info := runtimeExecutableInfoForTest(t, 1)

	t.Run("source read", func(t *testing.T) {
		wantErr := errors.New("source read failed")
		source := &runtimeExecutableSource{
			file: &runtimeStageFileStub{reader: &runtimeErrorReader{err: wantErr}},
			info: info,
		}
		destination := &runtimeSyncStageFileStub{runtimeStageFileStub: &runtimeStageFileStub{}}
		if _, err := copyRuntimeExecutable(destination, source); !errors.Is(err, wantErr) {
			t.Fatalf("expected source read failure, got %v", err)
		}
	})

	t.Run("size mismatch", func(t *testing.T) {
		source := &runtimeExecutableSource{
			file: &runtimeStageFileStub{reader: bytes.NewReader(nil)},
			info: info,
		}
		destination := &runtimeSyncStageFileStub{runtimeStageFileStub: &runtimeStageFileStub{}}
		if _, err := copyRuntimeExecutable(destination, source); err == nil || !strings.Contains(err.Error(), "size changed") {
			t.Fatalf("expected copied size mismatch, got %v", err)
		}
	})

	t.Run("destination without sync", func(t *testing.T) {
		source := &runtimeExecutableSource{
			file: &runtimeStageFileStub{reader: bytes.NewReader([]byte("x"))},
			info: info,
		}
		if _, err := copyRuntimeExecutable(&runtimeStageFileStub{}, source); err == nil ||
			!strings.Contains(err.Error(), "does not support sync") {
			t.Fatalf("expected missing sync rejection, got %v", err)
		}
	})

	t.Run("destination sync", func(t *testing.T) {
		wantErr := errors.New("sync failed")
		source := &runtimeExecutableSource{
			file: &runtimeStageFileStub{reader: bytes.NewReader([]byte("x"))},
			info: info,
		}
		destination := &runtimeSyncStageFileStub{
			runtimeStageFileStub: &runtimeStageFileStub{},
			syncErr:              wantErr,
		}
		if _, err := copyRuntimeExecutable(destination, source); !errors.Is(err, wantErr) {
			t.Fatalf("expected sync failure, got %v", err)
		}
	})
}

func TestRuntimeExecutableDigestFailures(t *testing.T) {
	t.Run("digest open", func(t *testing.T) {
		wantErr := errors.New("digest open failed")
		root := &trustedExecutableRootStub{openErr: map[string]error{"node": wantErr}}
		if err := verifyRuntimeExecutableDigest(root, "node", nil); !errors.Is(err, wantErr) {
			t.Fatalf("expected digest open failure, got %v", err)
		}
	})

	t.Run("digest read and close", func(t *testing.T) {
		readErr := errors.New("digest read failed")
		closeErr := errors.New("digest close failed")
		root := &trustedExecutableRootStub{
			files: map[string]safeio.File{
				"node": &runtimeStageFileStub{
					reader:   &runtimeErrorReader{err: readErr},
					closeErr: closeErr,
				},
			},
		}
		err := verifyRuntimeExecutableDigest(root, "node", nil)
		if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected digest read and close errors, got %v", err)
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		root := &trustedExecutableRootStub{
			files: map[string]safeio.File{
				"node": &runtimeStageFileStub{reader: bytes.NewReader([]byte("replacement"))},
			},
		}
		if err := verifyRuntimeExecutableDigest(root, "node", []byte("wrong")); err == nil ||
			!strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("expected digest mismatch, got %v", err)
		}
	})
}

func TestRuntimeExecutableSourceRestatFailure(t *testing.T) {
	dir := testutil.SecureHomeTempDir(t, "runtime-source-restat-")
	path := filepath.Join(dir, "node")
	writeRuntimeTestExecutable(t, path, "#!/bin/sh\nexit 0\n")
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat source executable: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat source directory: %v", err)
	}
	wantErr := errors.New("source executable restat failed")
	file := &runtimeStageFileStub{
		statResults: []runtimeStageStatResult{
			{info: fileInfo},
			{err: wantErr},
		},
	}
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				lstatInfo: map[string]fs.FileInfo{
					".":    dirInfo,
					"node": fileInfo,
				},
				files: map[string]safeio.File{"node": file},
			}, nil
		},
	})

	source, err := openTrustedRuntimeExecutableSource(path)
	if source != nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected source restat failure, source=%#v err=%v", source, err)
	}
}

func TestRuntimeExecutableStageLayoutFailures(t *testing.T) {
	t.Run("bin directory creation", func(t *testing.T) {
		wantErr := errors.New("bin directory creation failed")
		stage := &runtimeExecutableStage{
			root: &stubRoot{
				mkdirErr: map[string]error{filepath.Join(runtimeExecutableImageDir, "bin"): wantErr},
			},
		}
		if err := stage.prepareLayout("/installation/bin/node"); !errors.Is(err, wantErr) {
			t.Fatalf("expected bin directory creation failure, got %v", err)
		}
	})

	t.Run("installation layout read", func(t *testing.T) {
		stageDir := testutil.SecureHomeTempDir(t, "runtime-layout-read-")
		root := openRuntimeStageRootForTest(t, stageDir)
		stage := &runtimeExecutableStage{dirPath: stageDir, root: root}
		sourcePath := filepath.Join(stageDir, "missing-installation", "bin", "node")
		if err := stage.prepareLayout(sourcePath); err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing installation layout error, got %v", err)
		}
	})

	t.Run("source layout read", func(t *testing.T) {
		stage := &runtimeExecutableStage{}
		missingDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-missing-layout-"), "missing")
		if err := stage.linkLayoutEntries(missingDir, runtimeExecutableImageDir); err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected source layout read failure, got %v", err)
		}
	})

	t.Run("layout link creation", func(t *testing.T) {
		sourceDir := testutil.SecureHomeTempDir(t, "runtime-layout-link-source-")
		if err := os.WriteFile(filepath.Join(sourceDir, "lib"), []byte("layout"), 0o600); err != nil {
			t.Fatalf("write layout source: %v", err)
		}
		stage := &runtimeExecutableStage{
			dirPath: filepath.Join(testutil.SecureHomeTempDir(t, "runtime-layout-link-stage-"), "missing"),
		}
		if err := stage.linkLayoutEntries(sourceDir, runtimeExecutableImageDir); err == nil {
			t.Fatal("expected layout link creation failure")
		}
	})
}

func TestRuntimeExecutableStageCopyAndVerifyFailures(t *testing.T) {
	info := runtimeExecutableInfoForTest(t, 1)
	newSource := func() *runtimeExecutableSource {
		return &runtimeExecutableSource{
			file: &runtimeStageFileStub{reader: bytes.NewReader([]byte("x"))},
			info: info,
		}
	}
	newDestination := func(closeErr error) safeio.File {
		return &runtimeSyncStageFileStub{
			runtimeStageFileStub: &runtimeStageFileStub{closeErr: closeErr},
		}
	}

	t.Run("destination close", func(t *testing.T) {
		wantErr := errors.New("destination close failed")
		stage := &runtimeExecutableStage{
			fileName: "node",
			root: &runtimeStageRootOverride{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return newDestination(wantErr), nil
				},
			},
		}
		if err := stage.copyAndVerify(newSource()); !errors.Is(err, wantErr) {
			t.Fatalf("expected destination close failure, got %v", err)
		}
	})

	t.Run("destination chmod", func(t *testing.T) {
		wantErr := errors.New("destination chmod failed")
		stage := &runtimeExecutableStage{
			fileName: "node",
			root: &runtimeStageRootOverride{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return newDestination(nil), nil
				},
				chmod: func(string, os.FileMode) error {
					return wantErr
				},
			},
		}
		if err := stage.copyAndVerify(newSource()); !errors.Is(err, wantErr) {
			t.Fatalf("expected destination chmod failure, got %v", err)
		}
	})

	t.Run("destination lstat", func(t *testing.T) {
		wantErr := errors.New("destination lstat failed")
		stage := &runtimeExecutableStage{
			fileName: "node",
			root: &runtimeStageRootOverride{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return newDestination(nil), nil
				},
				chmod: func(string, os.FileMode) error {
					return nil
				},
				lstat: func(string) (fs.FileInfo, error) {
					return nil, wantErr
				},
			},
		}
		if err := stage.copyAndVerify(newSource()); !errors.Is(err, wantErr) {
			t.Fatalf("expected destination lstat failure, got %v", err)
		}
	})

	t.Run("destination metadata", func(t *testing.T) {
		stage := &runtimeExecutableStage{
			fileName: "node",
			root: &runtimeStageRootOverride{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return newDestination(nil), nil
				},
				chmod: func(string, os.FileMode) error {
					return nil
				},
				lstat: func(string) (fs.FileInfo, error) {
					return &fileInfoSizeOverride{FileInfo: info, size: 2}, nil
				},
			},
		}
		if err := stage.copyAndVerify(newSource()); err == nil ||
			!strings.Contains(err.Error(), "metadata mismatch") {
			t.Fatalf("expected destination metadata mismatch, got %v", err)
		}
	})

	t.Run("destination digest", func(t *testing.T) {
		wantErr := errors.New("destination digest failed")
		stage := &runtimeExecutableStage{
			fileName: "node",
			root: &runtimeStageRootOverride{
				open: func(string) (safeio.File, error) {
					return nil, wantErr
				},
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return newDestination(nil), nil
				},
				chmod: func(string, os.FileMode) error {
					return nil
				},
				lstat: func(string) (fs.FileInfo, error) {
					return info, nil
				},
			},
		}
		if err := stage.copyAndVerify(newSource()); !errors.Is(err, wantErr) {
			t.Fatalf("expected destination digest failure, got %v", err)
		}
	})
}

func TestRuntimeExecutableStagePinFailure(t *testing.T) {
	t.Run("pin stat", func(t *testing.T) {
		if isWindowsRuntime() {
			t.Skip("safeio descriptor pin injection is Unix-specific")
		}
		info := runtimeExecutableInfoForTest(t, 1)
		wantErr := errors.New("pin stat failed")
		closeErr := errors.New("pin close failed")
		root := &trustedExecutableRootStub{
			files: map[string]safeio.File{
				"node": &runtimeStageFileStub{statErr: wantErr, closeErr: closeErr},
			},
		}
		pin, err := pinRuntimeExecutable(root, "node", "/stage/node", "/stage", info)
		if pin != nil || !errors.Is(err, wantErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected pin stat and close errors, pin=%#v err=%v", pin, err)
		}
	})
}

func TestRuntimeExecutableCleanupExecutableFailures(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		if err := removeStagedRuntimeExecutable(&stubRoot{}, "missing", nil); err != nil {
			t.Fatalf("expected absent staged executable cleanup to succeed, got %v", err)
		}
	})

	t.Run("executable lstat", func(t *testing.T) {
		wantErr := errors.New("executable lstat failed")
		root := &stubRoot{lstatErr: map[string]error{"node": wantErr}}
		if err := removeStagedRuntimeExecutable(root, "node", nil); !errors.Is(err, wantErr) {
			t.Fatalf("expected executable lstat failure, got %v", err)
		}
	})

	t.Run("executable identity", func(t *testing.T) {
		rootDir := testutil.SecureHomeTempDir(t, "runtime-cleanup-identity-")
		first := filepath.Join(rootDir, "first")
		second := filepath.Join(rootDir, "second")
		writeRuntimeTestExecutable(t, first, "#!/bin/sh\nexit 0\n")
		writeRuntimeTestExecutable(t, second, "#!/bin/sh\nexit 0\n")
		firstInfo, err := os.Lstat(first)
		if err != nil {
			t.Fatalf("lstat first executable: %v", err)
		}
		root := openRuntimeStageRootForTest(t, rootDir)
		if err := removeStagedRuntimeExecutable(root, "second", firstInfo); err == nil ||
			!strings.Contains(err.Error(), "changed before cleanup") {
			t.Fatalf("expected executable identity mismatch, got %v", err)
		}
	})

	t.Run("executable symlink", func(t *testing.T) {
		rootDir := testutil.SecureHomeTempDir(t, "runtime-cleanup-symlink-")
		writeRuntimeTestExecutable(t, filepath.Join(rootDir, "target"), "#!/bin/sh\nexit 0\n")
		if err := os.Symlink("target", filepath.Join(rootDir, "node")); err != nil {
			t.Fatalf("create staged executable symlink: %v", err)
		}
		root := openRuntimeStageRootForTest(t, rootDir)
		if err := removeStagedRuntimeExecutable(root, "node", nil); err == nil ||
			!strings.Contains(err.Error(), "became a symlink") {
			t.Fatalf("expected executable symlink rejection, got %v", err)
		}
	})
}

func TestRuntimeExecutableCleanupLayoutFailures(t *testing.T) {
	t.Run("missing layout link", func(t *testing.T) {
		if err := removeStagedRuntimeLayoutLink(&stubRoot{}, "missing"); err != nil {
			t.Fatalf("expected absent layout link cleanup to succeed, got %v", err)
		}
	})

	t.Run("layout link lstat", func(t *testing.T) {
		wantErr := errors.New("layout link lstat failed")
		root := &stubRoot{lstatErr: map[string]error{"lib": wantErr}}
		if err := removeStagedRuntimeLayoutLink(root, "lib"); !errors.Is(err, wantErr) {
			t.Fatalf("expected layout link lstat failure, got %v", err)
		}
	})

	t.Run("layout regular file", func(t *testing.T) {
		path := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-cleanup-layout-"), "lib")
		if err := os.WriteFile(path, []byte("not a link"), 0o600); err != nil {
			t.Fatalf("write replacement layout entry: %v", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat replacement layout entry: %v", err)
		}
		root := &stubRoot{lstatInfo: map[string]fs.FileInfo{"lib": info}}
		if err := removeStagedRuntimeLayoutLink(root, "lib"); err == nil ||
			!strings.Contains(err.Error(), "no longer a symlink") {
			t.Fatalf("expected regular layout entry rejection, got %v", err)
		}
	})
}

func TestRuntimeExecutableCleanupStageDirFailures(t *testing.T) {
	t.Run("missing private directory", func(t *testing.T) {
		path := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-cleanup-missing-dir-"), "missing")
		if err := removeRuntimeExecutableStageDir(path, nil); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing private directory error, got %v", err)
		}
	})

	t.Run("private directory identity", func(t *testing.T) {
		first := testutil.SecureHomeTempDir(t, "runtime-cleanup-first-dir-")
		second := testutil.SecureHomeTempDir(t, "runtime-cleanup-second-dir-")
		firstInfo, err := os.Lstat(first)
		if err != nil {
			t.Fatalf("lstat first private directory: %v", err)
		}
		if err := removeRuntimeExecutableStageDir(second, firstInfo); err == nil ||
			!strings.Contains(err.Error(), "directory changed") {
			t.Fatalf("expected private directory identity mismatch, got %v", err)
		}
	})
}

func TestRuntimeExecutableCleanupNilStage(t *testing.T) {
	var stage *runtimeExecutableStage
	if err := stage.cleanup(); err != nil {
		t.Fatalf("expected nil stage cleanup to succeed, got %v", err)
	}
}

func assertStageRuntimeExecutableFailure(t *testing.T, name string, check func(*testing.T, *runtimeExecutableSource)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		check(t, openRuntimeExecutableSourceForTest(t))
	})
}

func assertStageRuntimeExecutableError(t *testing.T, source *runtimeExecutableSource, wantErr error) {
	t.Helper()
	if stage, err := stageRuntimeExecutable(source); !errors.Is(err, wantErr) || stage != nil {
		t.Fatalf("expected stage failure %q, stage=%#v err=%v", wantErr, stage, err)
	}
}

func TestTrustedRuntimeResolutionCapabilityErrors(t *testing.T) {
	if cmd, err := newTrustedRuntimeCommand(context.Background(), "node", nil, nil); err == nil || cmd != nil {
		t.Fatalf("expected nil resolution rejection, cmd=%#v err=%v", cmd, err)
	}

	fileCloseErr := errors.New("mismatched source file close failed")
	rootCloseErr := errors.New("mismatched source root close failed")
	resolution := resolvedRuntimeExecutable{
		path: "/expected/node",
		source: &runtimeExecutableSource{
			path: "/other/node",
			file: &trustedExecutableFileStub{closeErr: fileCloseErr},
			root: &stubRoot{closeErr: rootCloseErr},
		},
	}
	source, err := pinRuntimeExecutableResolution("node", &resolution)
	if source != nil || err == nil {
		t.Fatalf("expected mismatched capability rejection, source=%#v err=%v", source, err)
	}
	if !errors.Is(err, fileCloseErr) || !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected mismatched capability close errors, got %v", err)
	}
	if resolution.source != nil {
		t.Fatal("expected rejected capability ownership to be consumed")
	}

	if isWindowsRuntime() {
		return
	}
	aliasDir := testutil.SecureHomeTempDir(t, "runtime-resolution-alias-")
	canonicalDir := testutil.SecureHomeTempDir(t, "runtime-resolution-canonical-")
	canonicalPath := filepath.Join(canonicalDir, "node")
	writeRuntimeTestExecutable(t, canonicalPath, "#!/bin/sh\nexit 0\n")
	aliasPath := filepath.Join(aliasDir, "node")
	if err := os.Symlink(canonicalPath, aliasPath); err != nil {
		t.Fatalf("create runtime resolution alias: %v", err)
	}
	aliasResolution := resolvedRuntimeExecutable{path: aliasPath}
	source, err = pinRuntimeExecutableResolution("node", &aliasResolution)
	if source != nil || err == nil || !strings.Contains(err.Error(), "not trusted at launch boundary") {
		t.Fatalf("expected canonical alias mismatch rejection, source=%#v err=%v", source, err)
	}
}

func openRuntimeExecutableSourceForTest(t *testing.T) *runtimeExecutableSource {
	t.Helper()

	sourceDir := testutil.SecureHomeTempDir(t, "runtime-stage-source-")
	sourcePath := filepath.Join(sourceDir, "node")
	writeRuntimeTestExecutable(t, sourcePath, "#!/bin/sh\nexit 0\n")
	root, err := safeio.OpenRootNoFollow(sourceDir)
	if err != nil {
		t.Fatalf("open runtime executable source root: %v", err)
	}
	file, err := root.Open("node")
	if err != nil {
		closeErr := root.Close()
		t.Fatalf("open runtime executable source: %v; close root: %v", err, closeErr)
	}
	info, err := file.Stat()
	if err != nil {
		closeErr := errors.Join(file.Close(), root.Close())
		t.Fatalf("stat runtime executable source: %v; close source: %v", err, closeErr)
	}
	source := &runtimeExecutableSource{path: sourcePath, file: file, root: root, info: info}
	t.Cleanup(func() {
		if err := source.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
			t.Errorf("close runtime executable source: %v", err)
		}
	})
	return source
}

func withRuntimeStageRootOverride(t *testing.T, decorate func(safeio.Root) safeio.Root) {
	t.Helper()

	original := safeioFileSystem
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(name string) (safeio.Root, error) {
			root, err := original.OpenRootNoFollow(name)
			if err != nil {
				return nil, err
			}
			return decorate(root), nil
		},
	})
}

func withRuntimeExecutableStageRootTest(t *testing.T, fn func(string) (string, error)) {
	t.Helper()

	original := runtimeExecutableStageRoot
	runtimeExecutableStageRoot = fn
	t.Cleanup(func() {
		runtimeExecutableStageRoot = original
	})
}

func openRuntimeStageRootForTest(t *testing.T, path string) safeio.Root {
	t.Helper()

	root, err := safeio.OpenRootNoFollow(path)
	if err != nil {
		t.Fatalf("open runtime stage root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
			t.Errorf("close runtime stage root: %v", err)
		}
	})
	return root
}

func runtimeExecutableInfoForTest(t *testing.T, size int64) fs.FileInfo {
	t.Helper()

	path := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-stage-info-"), "node")
	if err := os.WriteFile(path, []byte("x"), 0o500); err != nil {
		t.Fatalf("write runtime executable metadata fixture: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat runtime executable metadata fixture: %v", err)
	}
	return &fileInfoSizeOverride{FileInfo: info, size: size}
}

type runtimeStageRootOverride struct {
	safeio.Root
	open     func(string) (safeio.File, error)
	openFile func(string, int, os.FileMode) (safeio.File, error)
	chmod    func(string, os.FileMode) error
	lstat    func(string) (fs.FileInfo, error)
}

func (r *runtimeStageRootOverride) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return r.Root.Open(name)
}

func (r *runtimeStageRootOverride) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return r.Root.OpenFile(name, flag, perm)
}

func (r *runtimeStageRootOverride) Chmod(name string, mode os.FileMode) error {
	if r.chmod != nil {
		return r.chmod(name, mode)
	}
	return r.Root.Chmod(name, mode)
}

func (r *runtimeStageRootOverride) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return r.Root.Lstat(name)
}

type runtimeStageFileStub struct {
	reader      io.Reader
	writer      io.Writer
	info        fs.FileInfo
	statErr     error
	statResults []runtimeStageStatResult
	statCalls   int
	closeErr    error
}

func (f *runtimeStageFileStub) Read(buffer []byte) (int, error) {
	if f.reader == nil {
		return 0, io.EOF
	}
	return f.reader.Read(buffer)
}

func (f *runtimeStageFileStub) Write(buffer []byte) (int, error) {
	if f.writer == nil {
		return len(buffer), nil
	}
	return f.writer.Write(buffer)
}

func (f *runtimeStageFileStub) Close() error {
	return f.closeErr
}

func (f *runtimeStageFileStub) Stat() (fs.FileInfo, error) {
	if f.statCalls < len(f.statResults) {
		result := f.statResults[f.statCalls]
		f.statCalls++
		return result.info, result.err
	}
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.info, nil
}

func (f *runtimeStageFileStub) Chmod(os.FileMode) error {
	return nil
}

type runtimeSyncStageFileStub struct {
	*runtimeStageFileStub
	syncErr error
}

func (f *runtimeSyncStageFileStub) Sync() error {
	return f.syncErr
}

type runtimeCloseErrorFile struct {
	safeio.File
	err error
}

func (f *runtimeCloseErrorFile) Close() error {
	return errors.Join(f.File.Close(), f.err)
}

type runtimeErrorReader struct {
	err error
}

func (r *runtimeErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type runtimeStageStatResult struct {
	info fs.FileInfo
	err  error
}
