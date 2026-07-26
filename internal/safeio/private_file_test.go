package safeio

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type readHookFile struct {
	*os.File
	beforeFirstRead func() error
	statCalls       int
}

func (f *readHookFile) Read(p []byte) (int, error) {
	if f.beforeFirstRead != nil {
		beforeFirstRead := f.beforeFirstRead
		f.beforeFirstRead = nil
		if err := beforeFirstRead(); err != nil {
			return 0, err
		}
	}
	return f.File.Read(p)
}

func (f *readHookFile) Stat() (fs.FileInfo, error) {
	f.statCalls++
	return f.File.Stat()
}

func TestCreatePrivateTempFileProducesOwnerOnlyFile(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open private temp root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close private temp root: %v", closeErr)
		}
	})

	name, file, err := root.CreatePrivateTempFile()
	if err != nil {
		t.Fatalf("create private temp file: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := root.CleanupTempFile(name, file); cleanupErr != nil {
			t.Errorf("cleanup private temp file: %v", cleanupErr)
		}
	})
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("inspect private temp file: %v", err)
	}
	private, err := root.RegularFilePrivateToOwner(name, info)
	if err != nil {
		t.Fatalf("validate private temp file: %v", err)
	}
	if !private {
		t.Fatal("expected private temp file permissions to be owner-only")
	}
}

func TestWritePrivateFileReplacingAtomicallyUsesPrivateReplacement(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "auth.key")
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed private target: %v", err)
	}
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open private replacement root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close private replacement root: %v", closeErr)
		}
	})

	replacement := []byte("after")
	if err := root.WritePrivateFileReplacingAtomically("auth.key", replacement); err != nil {
		t.Fatalf("replace private file: %v", err)
	}
	got, info, err := root.ReadRegularFile("auth.key")
	if err != nil {
		t.Fatalf("read private replacement: %v", err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("private replacement bytes = %q, want %q", got, replacement)
	}
	private, err := root.RegularFilePrivateToOwner("auth.key", info)
	if err != nil {
		t.Fatalf("validate private replacement: %v", err)
	}
	if !private {
		t.Fatal("expected replacement permissions to remain owner-only")
	}
}

func TestReadRegularFilePrivateToOwnerUnderLimitRejectsUnsafeOrChangedTargets(t *testing.T) {
	originalInfo, changedInfo := writePinnedTargetInfoPair(t)
	lstatErr := errors.New("inspect private target")
	openErr := errors.New("open private target")
	statErr := errors.New("stat opened private target")

	tests := []struct {
		name     string
		target   string
		maxBytes int64
		root     Root
		wantErr  error
		wantText string
	}{
		{
			name:     "escaping target",
			target:   filepath.Join("..", "auth.key"),
			root:     &fakeRoot{},
			wantText: "escapes root",
		},
		{
			name:   "lookup error",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, lstatErr },
			},
			wantErr: lstatErr,
		},
		{
			name:   "symlink",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return &modeOverrideFileInfo{FileInfo: originalInfo, mode: os.ModeSymlink | 0o777}, nil
				},
			},
			wantText: "is a symlink",
		},
		{
			name:   "directory",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return &modeOverrideFileInfo{FileInfo: originalInfo, mode: os.ModeDir | 0o700}, nil
				},
			},
			wantText: "not a regular file",
		},
		{
			name:   "open error",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return originalInfo, nil },
				open:  func(string) (File, error) { return nil, openErr },
			},
			wantErr: openErr,
		},
		{
			name:    "opened stat error",
			target:  "auth.key",
			root:    openedFileTestRoot(originalInfo, nil, statErr, nil),
			wantErr: statErr,
		},
		{
			name:    "changed after open",
			target:  "auth.key",
			root:    openedFileTestRoot(originalInfo, changedInfo, nil, nil),
			wantErr: ErrFileChanged,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &WriteRoot{rootAbs: filepath.FromSlash("/private-root"), root: test.root}
			data, info, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit(test.target, test.maxBytes)
			if err == nil {
				t.Fatal("expected unsafe private-file read target to be rejected")
			}
			if len(data) != 0 || info != nil || private {
				t.Fatalf("unexpected read result data=%q info=%#v private=%v", data, info, private)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want identity %v", err, test.wantErr)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestReadRegularFilePrivateToOwnerUnderLimitRejectsPostReadSnapshotFailures(t *testing.T) {
	originalInfo, changedInfo := writePinnedTargetInfoPair(t)
	postReadStatErr := errors.New("stat after private-file read")

	tests := []struct {
		name     string
		postInfo fs.FileInfo
		postErr  error
		wantErr  error
	}{
		{
			name:    "stat error",
			postErr: postReadStatErr,
			wantErr: postReadStatErr,
		},
		{
			name:     "identity changed",
			postInfo: changedInfo,
			wantErr:  ErrFileChanged,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := bytes.NewReader([]byte("secret"))
			readStarted := false
			statCalls := 0
			root := &WriteRoot{
				rootAbs: filepath.FromSlash("/private-root"),
				root: &fakeRoot{
					lstat: func(string) (fs.FileInfo, error) { return originalInfo, nil },
					open: func(string) (File, error) {
						return &fakeFile{
							read: func(p []byte) (int, error) {
								readStarted = true
								return reader.Read(p)
							},
							stat: func() (fs.FileInfo, error) {
								statCalls++
								if statCalls == 1 {
									return originalInfo, nil
								}
								if !readStarted {
									t.Fatal("post-read descriptor stat occurred before content read")
								}
								return test.postInfo, test.postErr
							},
							close: func() error { return nil },
						}, nil
					},
				},
			}

			data, info, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit("auth.key", 0)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want identity %v", err, test.wantErr)
			}
			if len(data) != 0 || info != nil || private {
				t.Fatalf("unexpected read result data=%q info=%#v private=%v", data, info, private)
			}
			if statCalls != 2 {
				t.Fatalf("opened descriptor stat calls = %d, want 2", statCalls)
			}
		})
	}
}

func TestReadRegularFilePrivateToOwnerUnderLimitRejectsSizeChangedDuringRead(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "auth.key")
	if err := os.WriteFile(targetPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed private key: %v", err)
	}
	initialInfo, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("inspect private key: %v", err)
	}
	file, err := os.Open(targetPath)
	if err != nil {
		t.Fatalf("open private key: %v", err)
	}

	sizeChanged := false
	hookedFile := &readHookFile{
		File: file,
		beforeFirstRead: func() (returnErr error) {
			appender, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				return err
			}
			defer func() {
				returnErr = errors.Join(returnErr, appender.Close())
			}()
			if _, err := appender.Write([]byte("!")); err != nil {
				return err
			}
			sizeChanged = true
			return nil
		},
	}
	root := &WriteRoot{
		rootAbs: rootDir,
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return initialInfo, nil },
			open:  func(string) (File, error) { return hookedFile, nil },
		},
	}

	data, info, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit("auth.key", 0)
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("error = %v, want identity %v", err, ErrFileChanged)
	}
	if len(data) != 0 || info != nil || private {
		t.Fatalf("unexpected read result data=%q info=%#v private=%v", data, info, private)
	}
	if !sizeChanged {
		t.Fatal("size-changing read hook was not called")
	}
	if hookedFile.statCalls != 2 {
		t.Fatalf("opened descriptor stat calls = %d, want 2", hookedFile.statCalls)
	}
}

func TestRegularFilePrivateToOwnerRejectsUnsafeOrChangedTargets(t *testing.T) {
	originalInfo, changedInfo := writePinnedTargetInfoPair(t)
	lstatErr := errors.New("inspect private target")
	openErr := errors.New("open private target")
	statErr := errors.New("stat opened private target")

	tests := []struct {
		name         string
		target       string
		expectedInfo fs.FileInfo
		root         Root
		wantErr      error
		wantText     string
	}{
		{
			name:     "escaping target",
			target:   filepath.Join("..", "auth.key"),
			root:     &fakeRoot{},
			wantText: "escapes root",
		},
		{
			name:   "lookup error",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, lstatErr },
			},
			wantErr: lstatErr,
		},
		{
			name:   "symlink",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return &modeOverrideFileInfo{FileInfo: originalInfo, mode: os.ModeSymlink | 0o777}, nil
				},
			},
			wantText: "is a symlink",
		},
		{
			name:   "directory",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) {
					return &modeOverrideFileInfo{FileInfo: originalInfo, mode: os.ModeDir | 0o700}, nil
				},
			},
			wantText: "not a regular file",
		},
		{
			name:         "changed before open",
			target:       "auth.key",
			expectedInfo: changedInfo,
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return originalInfo, nil },
			},
			wantErr: ErrFileChanged,
		},
		{
			name:   "open error",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return originalInfo, nil },
				open:  func(string) (File, error) { return nil, openErr },
			},
			wantErr: openErr,
		},
		{
			name:    "opened stat error",
			target:  "auth.key",
			root:    openedFileTestRoot(originalInfo, nil, statErr, nil),
			wantErr: statErr,
		},
		{
			name:    "changed after open",
			target:  "auth.key",
			root:    openedFileTestRoot(originalInfo, changedInfo, nil, nil),
			wantErr: ErrFileChanged,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &WriteRoot{rootAbs: filepath.FromSlash("/private-root"), root: test.root}
			private, err := root.RegularFilePrivateToOwner(test.target, test.expectedInfo)
			if err == nil {
				t.Fatal("expected unsafe private-file target to be rejected")
			}
			if private {
				t.Fatal("rejected private-file target reported as private")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want identity %v", err, test.wantErr)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestWritePrivateFileReplacingAtomicallyRejectsUnsafeOrUninspectableTarget(t *testing.T) {
	inspectErr := errors.New("inspect replacement target")
	tests := []struct {
		name     string
		target   string
		root     Root
		wantErr  error
		wantText string
	}{
		{
			name:     "escaping target",
			target:   filepath.Join("..", "auth.key"),
			root:     &fakeRoot{},
			wantText: "escapes root",
		},
		{
			name:   "inspection error",
			target: "auth.key",
			root: &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, inspectErr },
			},
			wantErr: inspectErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &WriteRoot{rootAbs: filepath.FromSlash("/private-root"), root: test.root}
			err := root.WritePrivateFileReplacingAtomically(test.target, []byte("replacement"))
			if err == nil {
				t.Fatal("expected private replacement target to be rejected")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want identity %v", err, test.wantErr)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
		})
	}
}
