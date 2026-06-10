package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

const (
	unexpectedErrFmt = "unexpected error: %v"
	escapesRootErr   = "path escapes root"
	getwdErrFmt      = "getwd: %v"
	restoreWDErrFmt  = "restore wd %s: %v"
	mkdirDeadDirFmt  = "mkdir deadDir: %v"
	chdirDeadDirFmt  = "chdir deadDir: %v"
	removeDeadDirFmt = "remove deadDir: %v"
)

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}

func withRemovedWorkingDir(t *testing.T, deadDirName string) {
	t.Helper()
	deadDir := filepath.Join(t.TempDir(), deadDirName)
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf(mkdirDeadDirFmt, err)
	}
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf(chdirDeadDirFmt, err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf(removeDeadDirFmt, err)
	}
}

func withFileSystem(t *testing.T, fsys FileSystem) {
	t.Helper()
	originalFileSystem := fileSystem
	fileSystem = fsys
	t.Cleanup(func() {
		fileSystem = originalFileSystem
	})
}

func openTestRoot(t *testing.T, rootDir string) Root {
	t.Helper()
	root, err := (&osFileSystem{}).OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	})
	return root
}

type fakeFileSystem struct {
	base     FileSystem
	abs      func(path string) (string, error)
	rel      func(basepath, targpath string) (string, error)
	openRoot func(name string) (Root, error)
}

func (f *fakeFileSystem) fallback() FileSystem {
	if f.base != nil {
		return f.base
	}
	return &osFileSystem{}
}

func (f *fakeFileSystem) Abs(path string) (string, error) {
	if f.abs != nil {
		return f.abs(path)
	}
	return f.fallback().Abs(path)
}

func (f *fakeFileSystem) Rel(basepath, targpath string) (string, error) {
	if f.rel != nil {
		return f.rel(basepath, targpath)
	}
	return f.fallback().Rel(basepath, targpath)
}

func (f *fakeFileSystem) OpenRoot(name string) (Root, error) {
	if f.openRoot != nil {
		return f.openRoot(name)
	}
	return f.fallback().OpenRoot(name)
}

type fakeRoot struct {
	Root
	open     func(name string) (File, error)
	openFile func(name string, flag int, perm os.FileMode) (File, error)
	rename   func(oldName, newName string) error
	remove   func(name string) error
	close    func() error
}

func (r *fakeRoot) Open(name string) (File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return r.Root.Open(name)
}

func (r *fakeRoot) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return r.Root.OpenFile(name, flag, perm)
}

func (r *fakeRoot) Rename(oldName, newName string) error {
	if r.rename != nil {
		return r.rename(oldName, newName)
	}
	return r.Root.Rename(oldName, newName)
}

func (r *fakeRoot) Remove(name string) error {
	if r.remove != nil {
		return r.remove(name)
	}
	return r.Root.Remove(name)
}

func (r *fakeRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return r.Root.Close()
}

type fakeFile struct {
	File
	read  func(p []byte) (int, error)
	write func(p []byte) (int, error)
	close func() error
	stat  func() (fs.FileInfo, error)
	chmod func(perm os.FileMode) error
}

func (f *fakeFile) Read(p []byte) (int, error) {
	if f.read != nil {
		return f.read(p)
	}
	return f.File.Read(p)
}

func (f *fakeFile) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	return f.File.Write(p)
}

func (f *fakeFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return f.File.Close()
}

func (f *fakeFile) Stat() (fs.FileInfo, error) {
	if f.stat != nil {
		return f.stat()
	}
	return f.File.Stat()
}

func (f *fakeFile) Chmod(perm os.FileMode) error {
	if f.chmod != nil {
		return f.chmod(perm)
	}
	return f.File.Chmod(perm)
}
