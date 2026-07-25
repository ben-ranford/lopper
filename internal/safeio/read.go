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
var ErrNonRegularFile = errors.New("path is not a regular file")

var readFileTargetReadyFn = func() error { return nil }

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
	expectedPathInfo, expectedOpenedInfo, err := preflightPinnedReadTargetWithinRoot(root, targetRel, targetPath)
	if err != nil {
		return nil, err
	}
	if err := readFileTargetReadyFn(); err != nil {
		return nil, err
	}

	file, err := openPinnedReadTargetWithinRoot(root, ".", targetRel, targetPath, expectedPathInfo, expectedOpenedInfo)
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
	resolvedTarget, err := resolveReadTargetWithinRoot(root, target)
	if err != nil {
		return nil, err
	}
	if err := readFileTargetReadyFn(); err != nil {
		return nil, err
	}

	file, err := openResolvedReadTargetWithinRoot(root, target.rootAbs, resolvedTarget)
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
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	if !info.Mode().IsRegular() {
		return ErrNonRegularFile
	}
	return nil
}

func preflightPinnedReadTargetWithinRoot(root Root, targetRel, targetPath string) (pathInfo fs.FileInfo, openedInfo fs.FileInfo, err error) {
	pathInfo, err = root.Lstat(targetRel)
	if err != nil {
		return nil, nil, translateOpenNotExist(err, targetPath)
	}
	if pathInfo.Mode()&fs.ModeSymlink == 0 {
		if !pathInfo.Mode().IsRegular() {
			return nil, nil, ErrNonRegularFile
		}
		return pathInfo, pathInfo, nil
	}

	file, err := root.Open(targetRel)
	if err != nil {
		return nil, nil, translateOpenNotExist(err, targetPath)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	openedInfo, err = file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, nil, ErrNonRegularFile
	}
	return pathInfo, openedInfo, nil
}

type resolvedReadTarget struct {
	rel                 string
	abs                 string
	requirePinnedRewalk bool
}

func resolveReadTargetWithinRoot(root Root, target rootedTarget) (resolvedReadTarget, error) {
	info, err := root.Lstat(target.rel)
	if err != nil {
		return resolvedReadTarget{}, translateOpenNotExist(err, target.abs)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		if !info.Mode().IsRegular() {
			return resolvedReadTarget{}, ErrNonRegularFile
		}
		return resolvedReadTarget{rel: target.rel, abs: target.abs}, nil
	}

	resolvedAbs, err := filepath.EvalSymlinks(target.abs)
	if err != nil {
		return resolvedReadTarget{}, translateOpenNotExist(err, target.abs)
	}
	canonicalRootAbs, err := filepath.EvalSymlinks(target.rootAbs)
	if err != nil {
		return resolvedReadTarget{}, err
	}
	resolvedRel, err := resolveRelativeTargetWithinRoot(canonicalRootAbs, resolvedAbs)
	if err != nil {
		return resolvedReadTarget{}, ErrNonRegularFile
	}
	if err := validateRegularPathWithinRoot(root, resolvedRel, resolvedAbs); err != nil {
		return resolvedReadTarget{}, err
	}
	return resolvedReadTarget{rel: resolvedRel, abs: resolvedAbs, requirePinnedRewalk: true}, nil
}

func openResolvedReadTargetWithinRoot(root Root, rootAbs string, target resolvedReadTarget) (_ File, err error) {
	if !target.requirePinnedRewalk {
		return root.Open(target.rel)
	}

	expectedInfo, err := os.Stat(target.abs)
	if err != nil {
		return nil, translateOpenNotExist(err, target.abs)
	}
	if !expectedInfo.Mode().IsRegular() {
		return nil, ErrNonRegularFile
	}

	return openPinnedReadTargetWithinRoot(root, rootAbs, target.rel, target.abs, expectedInfo, expectedInfo)
}

func openPinnedReadTargetWithinRoot(root Root, rootAbs, targetRel, targetPath string, expectedPathInfo, expectedOpenedInfo fs.FileInfo) (_ File, err error) {
	parent, closeParent, err := openReadTargetParentNoFollow(root, rootAbs, filepath.Dir(targetRel))
	if err != nil {
		return nil, err
	}
	if closeParent {
		defer func() {
			if closeErr := parent.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}()
	}

	name := filepath.Base(targetRel)
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return nil, translateOpenNotExist(err, targetPath)
	}
	if pathInfo.Mode()&os.ModeSymlink != expectedPathInfo.Mode()&os.ModeSymlink || !os.SameFile(expectedPathInfo, pathInfo) {
		return nil, ErrNonRegularFile
	}

	file, err := parent.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, closeFilePreservingPrimary(file, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(expectedOpenedInfo, openedInfo) {
		return nil, closeFilePreservingPrimary(file, ErrNonRegularFile)
	}
	return file, nil
}

func openReadTargetParentNoFollow(root Root, rootAbs, parentRel string) (Root, bool, error) {
	if parentRel == "." {
		return root, false, nil
	}

	current := root
	currentOwned := false
	currentAbs := rootAbs
	for _, part := range strings.Split(parentRel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		partAbs := filepath.Join(currentAbs, part)
		next, err := openRootChildNoFollow(current, part, partAbs)
		if err != nil {
			return nil, false, closeOpenedReadRootWithError(current, currentOwned, ErrNonRegularFile)
		}
		if currentOwned {
			if err := current.Close(); err != nil {
				return nil, false, closeRootWithError(next, err)
			}
		}
		current = next
		currentOwned = true
		currentAbs = partAbs
	}
	return current, currentOwned, nil
}

func closeOpenedReadRootWithError(root Root, owned bool, err error) error {
	if !owned {
		return err
	}
	return closeRootWithError(root, err)
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
