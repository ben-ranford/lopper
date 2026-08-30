package regressionproof

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestWriteFileWithinRootRejectsChangedTempSourceBeforePublish(t *testing.T) {
	root := newTempSwapRoot()

	err := safeio.WriteFileWithinRoot(root, "target", []byte("expected"), 0o600)
	if err == nil {
		t.Fatal("expected changed temp source rejection")
	}
	if got := string(root.files["target"].data); got != "before" {
		t.Fatalf("substituted temp source was published: %q", got)
	}
}

func newTempSwapRoot() *tempSwapRoot {
	return &tempSwapRoot{
		files: map[string]*tempSwapFile{
			"target": {data: []byte("before"), mode: 0o600, modTime: time.Unix(1, 0)},
		},
	}
}

type tempSwapRoot struct {
	files   map[string]*tempSwapFile
	tempRel string
}

func (r *tempSwapRoot) Open(string) (safeio.File, error) {
	return nil, errors.ErrUnsupported
}

func (r *tempSwapRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if flag&os.O_EXCL == 0 {
		return nil, errors.ErrUnsupported
	}
	r.tempRel = name
	file := &tempSwapFile{mode: perm, modTime: time.Unix(2, 0)}
	r.files[name] = file
	return file, nil
}

func (r *tempSwapRoot) OpenRoot(string) (safeio.Root, error) {
	return nil, errors.ErrUnsupported
}

func (r *tempSwapRoot) Lstat(name string) (fs.FileInfo, error) {
	file, ok := r.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return file.info(name), nil
}

func (r *tempSwapRoot) Mkdir(string, os.FileMode) error {
	return nil
}

func (r *tempSwapRoot) Chmod(name string, perm os.FileMode) error {
	file, ok := r.files[name]
	if !ok {
		return os.ErrNotExist
	}
	file.mode = perm
	return nil
}

func (r *tempSwapRoot) MkdirAll(string, os.FileMode) error {
	return nil
}

func (r *tempSwapRoot) Link(oldName, newName string) error {
	file, ok := r.files[oldName]
	if !ok {
		return os.ErrNotExist
	}
	if _, exists := r.files[newName]; exists {
		return os.ErrExist
	}
	r.files[newName] = file
	return nil
}

func (r *tempSwapRoot) Rename(oldName, newName string) error {
	file, ok := r.files[oldName]
	if !ok {
		return os.ErrNotExist
	}
	if oldName == r.tempRel {
		file = &tempSwapFile{data: []byte("substituted"), mode: file.mode, modTime: time.Unix(3, 0)}
		r.files[oldName] = file
	}
	r.files[newName] = file
	delete(r.files, oldName)
	return nil
}

func (r *tempSwapRoot) Remove(name string) error {
	delete(r.files, name)
	return nil
}

func (r *tempSwapRoot) Close() error {
	return nil
}

type tempSwapFile struct {
	data    []byte
	offset  int
	mode    os.FileMode
	modTime time.Time
}

func (f *tempSwapFile) Read(p []byte) (int, error) {
	if f.offset >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.offset:])
	f.offset += n
	return n, nil
}

func (f *tempSwapFile) Write(p []byte) (int, error) {
	f.data = append(f.data, p...)
	f.modTime = time.Unix(4, 0)
	return len(p), nil
}

func (f *tempSwapFile) Close() error {
	return nil
}

func (f *tempSwapFile) Stat() (fs.FileInfo, error) {
	return f.info(""), nil
}

func (f *tempSwapFile) Chmod(perm os.FileMode) error {
	f.mode = perm
	return nil
}

func (f *tempSwapFile) info(name string) fs.FileInfo {
	return &tempSwapFileInfo{name: filepath.Base(name), size: int64(len(f.data)), mode: f.mode, modTime: f.modTime}
}

type tempSwapFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (i *tempSwapFileInfo) Name() string       { return i.name }
func (i *tempSwapFileInfo) Size() int64        { return i.size }
func (i *tempSwapFileInfo) Mode() os.FileMode  { return i.mode }
func (i *tempSwapFileInfo) ModTime() time.Time { return i.modTime }
func (i *tempSwapFileInfo) IsDir() bool        { return false }
func (i *tempSwapFileInfo) Sys() any           { return nil }
