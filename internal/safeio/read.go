package safeio

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

var ErrFileTooLarge = errors.New("file exceeds size limit")

var (
	absPathFn      = filepath.Abs
	relPathFn      = filepath.Rel
	openRootFn     = os.OpenRoot
	openRootOpenFn = func(root *os.Root, name string) (*os.File, error) {
		return root.Open(name)
	}
	closeFileFn = func(file *os.File) error {
		return file.Close()
	}
	closeRootFn = func(root *os.Root) error {
		return root.Close()
	}
	closeReadCloserFn = func(reader io.ReadCloser) error {
		return reader.Close()
	}
)

type rootedReadCloser struct {
	file *os.File
	root *os.Root
}

func (r *rootedReadCloser) Read(p []byte) (int, error) {
	return r.file.Read(p)
}

func (r *rootedReadCloser) Close() error {
	return errors.Join(r.file.Close(), r.root.Close())
}

// ReadFileUnder reads targetPath only if it resolves under rootDir.
func ReadFileUnder(rootDir, targetPath string) ([]byte, error) {
	return ReadFileUnderLimit(rootDir, targetPath, 0)
}

// ReadFileUnderLimit reads targetPath only if it resolves under rootDir and
// does not exceed maxBytes when a positive limit is provided.
func ReadFileUnderLimit(rootDir, targetPath string, maxBytes int64) (_ []byte, err error) {
	target, err := resolveRootedTarget(rootDir, targetPath, allowRootTarget)
	if err != nil {
		return nil, err
	}

	root, err := openRootFn(target.rootAbs)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	defer func() {
		if closeErr := closeRootFn(root); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	file, err := openRootOpenFn(root, filepath.Clean(target.rel))
	if err != nil {
		return nil, translateOpenNotExist(err, targetPath)
	}
	defer func() {
		if closeErr := closeFileFn(file); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return readOpenedFile(file, maxBytes)
}

// ReadFile reads the exact targetPath by opening its parent directory as a root.
func ReadFile(targetPath string) (data []byte, err error) {
	file, err := OpenFile(targetPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := closeReadCloserFn(file); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return io.ReadAll(file)
}

// OpenFile opens the exact targetPath by opening its parent directory as a root.
func OpenFile(targetPath string) (io.ReadCloser, error) {
	target, err := resolveExactFileTarget(targetPath)
	if err != nil {
		return nil, err
	}

	root, err := openRootFn(target.parentDir)
	if err != nil {
		return nil, fmt.Errorf("open parent root: %w", err)
	}

	file, err := openRootOpenFn(root, target.fileName)
	if err != nil {
		err = translateOpenNotExist(err, targetPath)
		if closeErr := closeRootFn(root); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return &rootedReadCloser{file: file, root: root}, nil
}

func readOpenedFile(file *os.File, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(file)
	}
	if info, err := file.Stat(); err == nil && info.Mode().IsRegular() && info.Size() > maxBytes {
		return nil, ErrFileTooLarge
	}

	readLimit := maxBytes + 1
	if maxBytes >= math.MaxInt64-1 {
		readLimit = math.MaxInt64
	}
	data, err := io.ReadAll(io.LimitReader(file, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrFileTooLarge
	}
	return data, nil
}
