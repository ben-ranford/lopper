package safeio

import (
	"errors"
	"fmt"
	"io"
	"math"
)

var ErrFileTooLarge = errors.New("file exceeds size limit")
var ErrNonRegularFile = errors.New("path is not a regular file")

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
	if err := validateRegularPathWithinRoot(root, targetRel, targetPath); err != nil {
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
	if err := validateRegularPathWithinRoot(root, target.rel, targetPath); err != nil {
		return nil, err
	}

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
	if err := validateRegularPathWithinRoot(root, target.fileName, targetPath); err != nil {
		return nil, err
	}

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
	target, err := resolveExactFileTarget(targetPath)
	if err != nil {
		return nil, err
	}

	root, err := openReadRootNoFollow(target.parentDir, "parent")
	if err != nil {
		return nil, err
	}
	if err := validateRegularPathWithinRoot(root, target.fileName, targetPath); err != nil {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, err
	}

	file, err := OpenPinnedFile(root, target.fileName)
	if err != nil {
		err = translateOpenNotExist(err, targetPath)
		if closeErr := root.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	if err := validateOpenedRegularFile(file); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
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

func validateRegularPathWithinRoot(root Root, targetRel, targetPath string) error {
	info, err := root.Lstat(targetRel)
	if err != nil {
		return translateOpenNotExist(err, targetPath)
	}
	if !info.Mode().IsRegular() {
		return ErrNonRegularFile
	}
	return nil
}

func readOpenedFile(file File, maxBytes int64) ([]byte, error) {
	info, err := file.Stat()
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, ErrNonRegularFile
		}
		if maxBytes > 0 && info.Size() > maxBytes {
			return nil, ErrFileTooLarge
		}
	}
	if maxBytes <= 0 {
		return io.ReadAll(file)
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

func validateOpenedRegularFile(file File) error {
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if !info.Mode().IsRegular() {
		return ErrNonRegularFile
	}
	return nil
}
