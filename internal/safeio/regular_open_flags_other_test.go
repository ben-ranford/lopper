//go:build !unix && !windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPinnedRegularFileUnsupportedPlatformDoesNotOpenLeaf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular.txt")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatalf("write regular test file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat regular test file: %v", err)
	}
	openCalls := 0
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
		openFile: func(string, int, os.FileMode) (File, error) {
			openCalls++
			return nil, errors.New("unexpected leaf open")
		},
	}
	file, err := OpenPinnedRegularFile(root, "regular.txt")
	if file != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("unsupported platform must fail before opening, file=%v err=%v", file, err)
	}
	if openCalls != 0 {
		t.Fatalf("unsupported platform reached leaf OpenFile %d times", openCalls)
	}
}
