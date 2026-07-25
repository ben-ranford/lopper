package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
)

var ErrFileTooLarge = errors.New("file exceeds size limit")

type rootedReadCloser struct {
	file File
	root Root
}

func (r *rootedReadCloser) Read(p []byte) (int, error) {
	return r.file.Read(p)
}

func (r *rootedReadCloser) ReadAt(p []byte, offset int64) (int, error) {
	readerAt, ok := r.file.(io.ReaderAt)
	if !ok {
		return 0, errors.New("file does not support random access")
	}
	return readerAt.ReadAt(p, offset)
}

func (r *rootedReadCloser) Close() error {
	return errors.Join(r.file.Close(), r.root.Close())
}

func (r *rootedReadCloser) Stat() (fs.FileInfo, error) {
	return r.file.Stat()
}

// ReadFileUnder reads targetPath only if it resolves under rootDir.
func ReadFileUnder(rootDir, targetPath string) ([]byte, error) {
	return ReadFileUnderLimit(rootDir, targetPath, 0)
}

// ReadFileWithinRoot reads targetPath using an already-open confined root.
func ReadFileWithinRoot(root Root, targetPath string) (_ []byte, err error) {
	return ReadFileWithinRootLimit(root, targetPath, 0)
}

// OpenFileWithinRoot opens targetPath using an already-open confined root.
func OpenFileWithinRoot(root Root, targetPath string) (File, error) {
	targetRel, err := resolveRelativeTarget(targetPath, allowRootTarget)
	if err != nil {
		return nil, err
	}

	file, err := OpenPinnedFile(root, targetRel)
	if err != nil {
		return nil, translateOpenNotExist(err, targetPath)
	}
	return file, nil
}

// ReadFileWithinRootLimit reads targetPath using an already-open confined root
// and does not exceed maxBytes when a positive limit is provided.
func ReadFileWithinRootLimit(root Root, targetPath string, maxBytes int64) (_ []byte, err error) {
	file, err := OpenFileWithinRoot(root, targetPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return readOpenedFile(file, maxBytes)
}

// ReadFileUnderLimit reads targetPath only if it resolves under rootDir and
// does not exceed maxBytes when a positive limit is provided.
func ReadFileUnderLimit(rootDir, targetPath string, maxBytes int64) (_ []byte, err error) {
	target, err := resolveRootedTarget(rootDir, targetPath, allowRootTarget)
	if err != nil {
		return nil, err
	}

	root, err := openReadRootNoFollow(target.rootAbs, "root")
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	file, err := OpenPinnedFile(root, target.rel)
	if err != nil {
		return nil, translateOpenNotExist(err, targetPath)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return readOpenedFile(file, maxBytes)
}

// ReadFileLimit reads the exact targetPath by opening its parent directory as a root.
func ReadFileLimit(targetPath string, maxBytes int64) (data []byte, err error) {
	target, err := resolveExactFileTarget(targetPath)
	if err != nil {
		return nil, err
	}

	root, err := openReadRootNoFollow(target.parentDir, "parent")
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	file, err := OpenPinnedFile(root, target.fileName)
	if err != nil {
		return nil, translateOpenNotExist(err, targetPath)
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
	return openExactFile(targetPath, fileSystem.OpenRoot, "open parent root")
}

// OpenFileNoFollow opens the exact targetPath while rejecting symlinks in any
// parent path component.
func OpenFileNoFollow(targetPath string) (io.ReadCloser, error) {
	target, err := resolveExactFileTarget(targetPath)
	if err != nil {
		return nil, err
	}

	root, err := openParentRootNoFollow(target.parentDir)
	if err != nil {
		return nil, fmt.Errorf("open canonical parent root: %w", normalizeOpenParentRootNoFollowError(err, target.parentDir))
	}

	file, err := root.OpenNoFollow(target.fileName)
	if err != nil {
		err = translateOpenNotExist(err, targetPath)
		if closeErr := root.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return &rootedReadCloser{file: file, root: root}, nil
}

func openExactFile(targetPath string, openRoot func(string) (Root, error), openRootLabel string) (io.ReadCloser, error) {
	target, err := resolveExactFileTarget(targetPath)
	if err != nil {
		return nil, err
	}

	root, err := openRoot(target.parentDir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", openRootLabel, err)
	}

	file, err := OpenPinnedFile(root, target.fileName)
	if err != nil {
		err = translateOpenNotExist(err, targetPath)
		if closeErr := root.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return &rootedReadCloser{file: file, root: root}, nil
}

func openReadRootNoFollow(rootDir, label string) (Root, error) {
	root, err := fileSystem.OpenRootNoFollow(rootDir)
	if err != nil {
		return nil, fmt.Errorf("open %s root: %w", label, err)
	}
	return root, nil
}

func openParentRootNoFollow(parentDir string) (Root, error) {
	volumeRoot := filepath.VolumeName(parentDir) + string(filepath.Separator)
	rel, err := filepath.Rel(volumeRoot, parentDir)
	if err != nil {
		return nil, err
	}

	root, err := fileSystem.OpenRoot(volumeRoot)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return root, nil
	}

	current := root
	currentPath := volumeRoot
	parts := strings.Split(rel, string(filepath.Separator))
	current, currentPath, parts, err = openParentRootNoFollowAlias(current, currentPath, parts)
	if err != nil {
		return nil, err
	}

	for _, part := range nonDotPathParts(strings.Join(parts, string(filepath.Separator)), string(filepath.Separator)) {
		partPath := filepath.Join(currentPath, part)
		next, openErr := openRootChildNoFollow(current, part, partPath)
		if openErr != nil {
			return nil, closeRootWithError(current, openErr)
		}
		if err := current.Close(); err != nil {
			return nil, closeRootWithError(next, err)
		}
		current = next
		currentPath = partPath
	}
	return current, nil
}

func normalizeOpenParentRootNoFollowError(err error, parentDir string) error {
	var symlinkErr *RootContainsSymlinkError
	if errors.As(err, &symlinkErr) {
		return &PathContainsSymlinkError{Path: parentDir, Err: err}
	}
	return err
}

func readOpenedFile(file File, maxBytes int64) ([]byte, error) {
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
