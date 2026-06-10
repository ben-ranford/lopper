package safeio

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FileSystem captures the filesystem operations safeio needs.
type FileSystem interface {
	Abs(path string) (string, error)
	Rel(basepath, targpath string) (string, error)
	OpenRoot(name string) (Root, error)
}

// Root is a filesystem root used for path-confined operations.
type Root interface {
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
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

type osRoot struct {
	root *os.Root
}

func (r *osRoot) Open(name string) (File, error) {
	return r.root.Open(name)
}

func (r *osRoot) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return r.root.OpenFile(name, flag, perm)
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
