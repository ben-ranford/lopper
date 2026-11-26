package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRegularParserReadsUsePositiveLimits(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "manifest")
	if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRegularFileUnderLimit(root, file, 4)
	if err != nil || string(data) != "safe" {
		t.Fatalf("expected complete input at the bound, got %q, %v", data, err)
	}
	pinnedRoot := openTestRoot(t, root)
	data, err = ReadRegularFileWithinRootLimit(pinnedRoot, "manifest", 4)
	if err != nil || string(data) != "safe" {
		t.Fatalf("expected direct rooted read at the bound, got %q, %v", data, err)
	}
	for _, maxBytes := range []int64{0, -1} {
		if _, err := ReadRegularFileWithinRootLimit(pinnedRoot, "manifest", maxBytes); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("expected %d-byte limit to be rejected, got %v", maxBytes, err)
		}
	}
	if _, err := ReadRegularFileLimit(file, 3); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected exact-file byte limit, got %v", err)
	}
	if _, err := ReadRegularFileLimit(file, 0); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected zero limit to be rejected, got %v", err)
	}
	if _, err := ReadRegularFileUnderLimit(root, filepath.Join(root, "missing"), 4); !os.IsNotExist(err) {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFileWithinRootLimit(pinnedRoot, filepath.Join("nested", "missing"), 4); !os.IsNotExist(err) {
		t.Fatalf("expected nested missing file error, got %v", err)
	}
	if _, err := ReadRegularFileLimit(root, 4); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected directory to be rejected, got %v", err)
	}
}

func TestRegularParserReadsPreserveResolutionAndCloseErrors(t *testing.T) {
	t.Run("resolution", func(t *testing.T) {
		expectedErr := errors.New("resolve regular parser input")
		withFileSystem(t, &fakeFileSystem{abs: func(string) (string, error) {
			return "", expectedErr
		}})

		if _, err := ReadRegularFileUnderLimit("root", "manifest", 4); !errors.Is(err, expectedErr) {
			t.Fatalf("expected rooted resolution error, got %v", err)
		}
		if _, err := ReadRegularFileLimit("manifest", 4); !errors.Is(err, expectedErr) {
			t.Fatalf("expected exact resolution error, got %v", err)
		}
	})

	t.Run("root open", func(t *testing.T) {
		expectedErr := errors.New("open regular parser root")
		withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(string) (Root, error) {
			return nil, expectedErr
		}})

		if _, err := ReadRegularFileLimit(filepath.Join(t.TempDir(), "manifest"), 4); !errors.Is(err, expectedErr) {
			t.Fatalf("expected root open error, got %v", err)
		}
	})

	t.Run("root close", func(t *testing.T) {
		rootPath := t.TempDir()
		filePath := filepath.Join(rootPath, "manifest")
		if err := os.WriteFile(filePath, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		expectedErr := errors.New("close regular parser root")
		withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(path string) (Root, error) {
			root, err := (&osFileSystem{}).OpenRootNoFollow(path)
			if err != nil {
				return nil, err
			}
			return &fakeRoot{Root: root, close: func() error {
				return errors.Join(root.Close(), expectedErr)
			}}, nil
		}})

		data, err := ReadRegularFileLimit(filePath, 4)
		if string(data) != "safe" || !errors.Is(err, expectedErr) {
			t.Fatalf("expected data and root close error, got %q, %v", data, err)
		}
	})
}
