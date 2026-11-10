package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenPinnedRegularFileReadsNestedRegularInput(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "gradle"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "gradle", "settings.gradle"), []byte("settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, rootPath)
	file, err := OpenPinnedRegularFile(root, "gradle/settings.gradle")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil || string(data) != "settings" {
		t.Fatalf("data=%q error=%v", data, err)
	}
}

func TestOpenPinnedRegularFileRejectsSymlinkAndDirectory(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "target"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(rootPath, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root := openTestRoot(t, rootPath)
	if file, err := OpenPinnedRegularFile(root, "link"); file != nil || !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("symlink open file=%v error=%v", file, err)
	}
	if file, err := OpenPinnedRegularFile(root, "."); file != nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("directory open file=%v error=%v", file, err)
	}
	if file, err := OpenPinnedRegularFile(root, "missing"); file != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing leaf file=%v error=%v", file, err)
	}
	if file, err := OpenPinnedRegularFile(root, "nested/missing"); file != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing nested leaf file=%v error=%v", file, err)
	}
}

func TestOpenPinnedRegularFileCleansRejectedDescriptors(t *testing.T) {
	rootPath := t.TempDir()
	regularPath := filepath.Join(rootPath, "regular")
	directoryPath := filepath.Join(rootPath, "directory")
	if err := os.WriteFile(regularPath, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	base := openTestRoot(t, rootPath)
	regularInfo := statTestPath(t, regularPath)
	statErr := errors.New("descriptor stat failed")
	openErr := errors.New("descriptor open failed")
	for _, tc := range []struct {
		name           string
		openFile       func(string, int, os.FileMode) (File, error)
		want           error
		wantCloseCalls int
	}{
		{name: "open failure", openFile: func(string, int, os.FileMode) (File, error) { return nil, openErr }, want: openErr},
		{name: "stat failure", openFile: func(name string, flags int, perm os.FileMode) (File, error) {
			file, err := base.OpenFile(name, flags, perm)
			if err != nil {
				return nil, err
			}
			return &fakeFile{File: file, stat: func() (fs.FileInfo, error) { return nil, statErr }}, nil
		}, want: statErr, wantCloseCalls: 1},
		{name: "descriptor changed to directory", openFile: func(string, int, os.FileMode) (File, error) {
			return base.OpenFile("directory", os.O_RDONLY, 0)
		}, want: fs.ErrInvalid, wantCloseCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closeCalls := 0
			root := &fakeRoot{Root: base, lstat: func(string) (fs.FileInfo, error) { return regularInfo, nil }, openFile: func(name string, flags int, perm os.FileMode) (File, error) {
				file, err := tc.openFile(name, flags, perm)
				if err != nil {
					return nil, err
				}
				return &fakeFile{File: file, close: func() error { closeCalls++; return file.Close() }}, nil
			}}
			file, err := OpenPinnedRegularFile(root, "regular")
			if file != nil || !errors.Is(err, tc.want) || closeCalls != tc.wantCloseCalls {
				t.Fatalf("file=%v error=%v close calls=%d", file, err, closeCalls)
			}
		})
	}
}

func TestOpenPinnedRegularFileClosesDescriptorOnIdentityMismatch(t *testing.T) {
	rootPath := t.TempDir()
	firstPath := filepath.Join(rootPath, "first")
	secondPath := filepath.Join(rootPath, "second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("regular"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := openTestRoot(t, rootPath)
	wantedInfo := statTestPath(t, firstPath)
	opened, err := root.OpenFile("second", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	closeCalls := 0
	wrapper := &fakeRoot{
		Root:  root,
		lstat: func(string) (fs.FileInfo, error) { return wantedInfo, nil },
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{File: opened, close: func() error { closeCalls++; return opened.Close() }}, nil
		},
	}
	file, err := OpenPinnedRegularFile(wrapper, "first")
	if file != nil || err == nil || !strings.Contains(err.Error(), "path changed while opening") || closeCalls != 1 {
		t.Fatalf("file=%v error=%v close calls=%d", file, err, closeCalls)
	}
}

func TestReadRegularFileWithinRootLimitPreservesLimitAndCloseErrors(t *testing.T) {
	rootPath := t.TempDir()
	filePath := filepath.Join(rootPath, "settings.gradle")
	if err := os.WriteFile(filePath, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := openTestRoot(t, rootPath)
	data, err := ReadRegularFileWithinRootLimit(root, "settings.gradle", 4)
	if err != nil || string(data) != "1234" {
		t.Fatalf("exact-boundary data=%q error=%v", data, err)
	}
	if data, err = ReadRegularFileWithinRootLimit(root, "settings.gradle", 3); !reflect.DeepEqual(data, []byte(nil)) || !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("over-limit data=%q error=%v", data, err)
	}
	if data, err = ReadRegularFileWithinRootLimit(root, filepath.Join(rootPath, "settings.gradle"), 4); !reflect.DeepEqual(data, []byte(nil)) || !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("absolute target data=%q error=%v", data, err)
	}

	closeErr := errors.New("close refused")
	wrapper := &fakeRoot{Root: root, openFile: func(name string, flags int, perm os.FileMode) (File, error) {
		file, err := root.OpenFile(name, flags, perm)
		if err != nil {
			return nil, err
		}
		return &fakeFile{File: file, close: func() error { return errors.Join(file.Close(), closeErr) }}, nil
	}}
	data, err = ReadRegularFileWithinRootLimit(wrapper, "settings.gradle", 4)
	if string(data) != "1234" || !errors.Is(err, closeErr) {
		t.Fatalf("close failure data=%q error=%v", data, err)
	}
}
