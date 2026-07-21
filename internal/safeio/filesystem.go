package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileSystem captures the filesystem operations safeio needs.
type FileSystem interface {
	Abs(path string) (string, error)
	Rel(basepath, targpath string) (string, error)
	OpenRoot(name string) (Root, error)
	OpenRootNoFollow(name string) (Root, error)
}

// Root is a filesystem root used for path-confined operations.
type Root interface {
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	OpenRoot(name string) (Root, error)
	Lstat(name string) (fs.FileInfo, error)
	Mkdir(name string, perm os.FileMode) error
	Chmod(name string, perm os.FileMode) error
	MkdirAll(name string, perm os.FileMode) error
	Rename(oldName, newName string) error
	Remove(name string) error
	Close() error
}

// File is the file handle behavior safeio needs for reads and atomic writes.
type File interface {
	io.Reader
	io.Writer
	io.Closer
	Stat() (fs.FileInfo, error)
	Chmod(perm os.FileMode) error
}

var fileSystem FileSystem = &osFileSystem{}

// OpenRoot opens a confined filesystem root.
func OpenRoot(name string) (Root, error) {
	return fileSystem.OpenRoot(name)
}

type osFileSystem struct{}

func (*osFileSystem) Abs(path string) (string, error) {
	return filepath.Abs(path)
}

func (*osFileSystem) Rel(basepath, targpath string) (string, error) {
	return filepath.Rel(basepath, targpath)
}

func (*osFileSystem) OpenRoot(name string) (Root, error) {
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &osRoot{root: root}, nil
}

func (f *osFileSystem) OpenRootNoFollow(name string) (Root, error) {
	volumeRoot := filepath.VolumeName(name) + string(os.PathSeparator)
	rel, err := filepath.Rel(volumeRoot, name)
	if err != nil {
		return nil, err
	}
	root, err := f.OpenRoot(volumeRoot)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return root, nil
	}

	currentPath := volumeRoot
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		next, err := openRootChildNoFollow(root, part, currentPath)
		if err != nil {
			return nil, closeRootWithError(root, err)
		}
		if err := root.Close(); err != nil {
			return nil, closeRootWithError(next, err)
		}
		root = next
	}
	return root, nil
}

func openRootChildNoFollow(root Root, name, path string) (Root, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("root contains symlink: %s", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", path)
	}

	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, closeRootWithError(next, err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, closeRootWithError(next, fmt.Errorf("root changed while opening: %s", path))
	}
	return next, nil
}

func closeRootWithError(root Root, err error) error {
	if closeErr := root.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

type osRoot struct {
	root *os.Root
}

func (r *osRoot) Open(name string) (File, error) {
	return r.root.Open(name)
}

func (r *osRoot) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return r.root.OpenFile(name, flag, perm)
}

func (r *osRoot) OpenRoot(name string) (Root, error) {
	root, err := r.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &osRoot{root: root}, nil
}

func (r *osRoot) Lstat(name string) (fs.FileInfo, error) {
	return r.root.Lstat(name)
}

func (r *osRoot) Mkdir(name string, perm os.FileMode) error {
	return r.root.Mkdir(name, perm)
}

func (r *osRoot) Chmod(name string, perm os.FileMode) error {
	return r.root.Chmod(name, perm)
}

func (r *osRoot) MkdirAll(name string, perm os.FileMode) error {
	return r.root.MkdirAll(name, perm)
}

func (r *osRoot) Rename(oldName, newName string) error {
	return r.root.Rename(oldName, newName)
}

func (r *osRoot) Remove(name string) error {
	return r.root.Remove(name)
}

func (r *osRoot) Close() error {
	return r.root.Close()
}
