package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
)

var ErrFileTooLarge = errors.New("file exceeds size limit")

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
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve root path: %w", err)
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, fmt.Errorf("resolve target path: %w", err)
	}

	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return nil, fmt.Errorf("compute relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("path escapes root: %s", targetPath)
	}

	root, err := os.OpenRoot(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	rel = filepath.Clean(rel)
	file, err := root.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &fs.PathError{Op: "open", Path: targetPath, Err: os.ErrNotExist}
		}
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
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
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return io.ReadAll(file)
}

// OpenFile opens the exact targetPath by opening its parent directory as a root.
func OpenFile(targetPath string) (io.ReadCloser, error) {
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, fmt.Errorf("resolve target path: %w", err)
	}
	parentDir := filepath.Dir(targetAbs)
	fileName := filepath.Base(targetAbs)

	root, err := os.OpenRoot(parentDir)
	if err != nil {
		return nil, fmt.Errorf("open parent root: %w", err)
	}

	file, err := root.Open(fileName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if closeErr := root.Close(); closeErr != nil {
				return nil, errors.Join(&fs.PathError{Op: "open", Path: targetPath, Err: os.ErrNotExist}, closeErr)
			}
			return nil, &fs.PathError{Op: "open", Path: targetPath, Err: os.ErrNotExist}
		}
		if closeErr := root.Close(); closeErr != nil {
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
