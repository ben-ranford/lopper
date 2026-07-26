//go:build windows

package runtime

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type windowsRuntimeExecutablePin struct {
	files []*os.File
}

func openPinnedRuntimeExecutableSourceFile(root safeio.Root, name, path string) (safeio.File, fs.FileInfo, error) {
	expected, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	file, info, err := openWindowsPinnedRuntimePath(path, syscall.FILE_ATTRIBUTE_NORMAL, expected)
	if err != nil {
		return nil, nil, err
	}
	return file, info, nil
}

func pinRuntimeExecutable(_ safeio.Root, _ string, path, stageDir string, expected fs.FileInfo) (io.Closer, error) {
	pin := &windowsRuntimeExecutablePin{}
	if err := pin.open(path, syscall.FILE_ATTRIBUTE_NORMAL, expected); err != nil {
		return nil, err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		dirInfo, err := os.Lstat(dir)
		if err != nil {
			return nil, errors.Join(err, pin.Close())
		}
		if err := pin.open(
			dir,
			syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
			dirInfo,
		); err != nil {
			return nil, errors.Join(err, pin.Close())
		}
		if sameRuntimeExecutablePath(dir, stageDir) {
			return pin, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, errors.Join(errors.New("staged runtime executable escaped its private directory"), pin.Close())
		}
	}
}

func (p *windowsRuntimeExecutablePin) open(path string, attributes uint32, expected fs.FileInfo) error {
	file, _, err := openWindowsPinnedRuntimePath(path, attributes, expected)
	if err != nil {
		return err
	}
	p.files = append(p.files, file)
	return nil
}

func openWindowsPinnedRuntimePath(path string, attributes uint32, expected fs.FileInfo) (*os.File, fs.FileInfo, error) {
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	handle, err := syscall.CreateFile(
		pathUTF16,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_EXISTING,
		attributes,
		0,
	)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, nil, errors.Join(errors.New("pin staged runtime executable handle"), syscall.CloseHandle(handle))
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	if expected == nil || !os.SameFile(expected, info) {
		return nil, nil, errors.Join(errors.New("staged runtime executable identity changed before pinning"), file.Close())
	}
	return file, info, nil
}

func (p *windowsRuntimeExecutablePin) Close() error {
	var closeErr error
	for index := len(p.files) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, p.files[index].Close())
	}
	p.files = nil
	return closeErr
}
