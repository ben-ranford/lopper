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
