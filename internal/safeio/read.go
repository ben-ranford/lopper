package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

var ErrFileTooLarge = errors.New("file exceeds size limit")
var ErrNonRegularFile = errors.New("path is not a regular file")

var readFileTargetReadyFn = func() error { return nil }
var readRootOpenReadyFn = func() error { return nil }
var evalSymlinksFn = filepath.EvalSymlinks

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
	expectedPathInfo, expectedOpenedInfo := fs.FileInfo(nil), fs.FileInfo(nil)
	if filepath.Dir(targetRel) == "." {
		expectedPathInfo, expectedOpenedInfo, err = preflightPinnedReadTargetWithinRoot(root, targetRel, targetPath)
		if err != nil {
			return nil, translateOpenNotExist(err, targetPath)
		}
	} else {
		parent, closeParent, err := openReadTargetParentNoFollow(root, ".", filepath.Dir(targetRel))
		if err != nil {
			return nil, err
		}
		name := filepath.Base(targetRel)
		expectedPathInfo, expectedOpenedInfo, err = preflightPinnedReadTargetWithinRoot(parent, name, targetPath)
		if closeParent {
			if closeErr := parent.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}
		if err != nil {
			return nil, translateOpenNotExist(err, targetPath)
		}
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

	root, err := openPinnedReadRoot(target.rootAbs)
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
	expectedPathInfo, expectedOpenedInfo, err := preflightPinnedReadTargetWithinRoot(root, resolvedTarget.rel, resolvedTarget.abs)
	if err != nil {
		return nil, err
	}
	if err := readFileTargetReadyFn(); err != nil {
		return nil, err
	}

	file, err := openPinnedReadTargetWithinRoot(root, target.rootAbs, resolvedTarget.rel, resolvedTarget.rel, expectedPathInfo, expectedOpenedInfo)
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

	root, err := openExactParentRoot(target.parentDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	expectedPathInfo, expectedOpenedInfo, err := preflightPinnedReadTargetWithinRoot(root, target.fileName, targetPath)
	if err != nil {
		return nil, err
	}
	if err := readFileTargetReadyFn(); err != nil {
		return nil, err
	}

	file, err := openPinnedReadTargetWithinRoot(root, target.parentDir, target.fileName, target.fileName, expectedPathInfo, expectedOpenedInfo)
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

	root, err := openExactParentRoot(target.parentDir)
	if err != nil {
		return nil, err
	}
	expectedPathInfo, expectedOpenedInfo, err := preflightPinnedReadTargetWithinRoot(root, target.fileName, targetPath)
	if err != nil {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, err
	}
	if err := readFileTargetReadyFn(); err != nil {
		if closeErr := root.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}

	file, err := openPinnedReadTargetWithinRoot(root, target.parentDir, target.fileName, target.fileName, expectedPathInfo, expectedOpenedInfo)
	if err != nil {
		err = translateOpenNotExist(err, targetPath)
		if closeErr := root.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return &rootedReadCloser{file: file, root: root}, nil
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

func openExactParentRoot(parentDir string) (Root, error) {
	expectedInfo, err := os.Stat(parentDir)
	if err != nil {
		return nil, fmt.Errorf("open parent root: %w", err)
	}
	if !expectedInfo.IsDir() {
		return nil, fmt.Errorf("open parent root: %w", ErrNonRegularFile)
	}
	canonicalParentDir, err := evalSymlinksFn(parentDir)
	if err != nil {
		return nil, fmt.Errorf("open parent root: %w", err)
	}
	if !samePinnedRootPath(parentDir, canonicalParentDir) {
		return nil, fmt.Errorf("open parent root: root contains symlink: %s", parentDir)
	}

	root, err := fileSystem.OpenRootNoFollow(canonicalParentDir)
	if err != nil {
		return nil, fmt.Errorf("open parent root: %w", err)
	}
	openedInfo, err := root.Lstat(".")
	if err != nil {
		return nil, closeRootWithError(root, fmt.Errorf("open parent root: %w", err))
	}
	if openedInfo.IsDir() && !os.SameFile(expectedInfo, openedInfo) {
		return nil, closeRootWithError(root, fmt.Errorf("open parent root: %w", ErrNonRegularFile))
	}
	return root, nil
}

func openPinnedReadRoot(rootDir string) (Root, error) {
	expectedInfo, err := os.Stat(rootDir)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	if !expectedInfo.IsDir() {
		return nil, fmt.Errorf("open root: %w", ErrNonRegularFile)
	}
	if err := readRootOpenReadyFn(); err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}

	root, err := fileSystem.OpenRootNoFollow(rootDir)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	openedInfo, err := root.Lstat(".")
	if err != nil {
		return nil, closeRootWithError(root, fmt.Errorf("open root: %w", err))
	}
	if !openedInfo.IsDir() || !os.SameFile(expectedInfo, openedInfo) {
		return nil, closeRootWithError(root, fmt.Errorf("open root: root changed while opening: %s", rootDir))
	}
	return root, nil
}

func preflightPinnedReadTargetWithinRoot(root Root, targetRel, targetPath string) (pathInfo, openedInfo fs.FileInfo, err error) {
	pathInfo, err = root.Lstat(targetRel)
	if err != nil {
		return nil, nil, err
	}
	if pathInfo.Mode()&fs.ModeSymlink == 0 {
		if !pathInfo.Mode().IsRegular() {
			return nil, nil, ErrNonRegularFile
		}
		return pathInfo, pathInfo, nil
	}

	openedInfo, err = rootedTargetStat(root, targetRel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, translateOpenNotExist(err, targetPath)
		}
		if errors.Is(err, ErrPathEscapesRoot) || errors.Is(err, syscall.ELOOP) {
			return nil, nil, &targetPathSymlinkError{path: targetRel}
		}
		return nil, nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		if openedInfo.Mode()&os.ModeSymlink != 0 {
			return nil, nil, &targetPathSymlinkError{path: targetRel}
		}
		return nil, nil, ErrNonRegularFile
	}
	return pathInfo, openedInfo, nil
}

func rootedTargetStat(root Root, targetRel string) (fs.FileInfo, error) {
	info, err := root.Stat(targetRel)
	if err != nil && isPathEscapesParentError(err) {
		return nil, &targetPathSymlinkError{path: targetRel}
	}
	return info, normalizePathEscapesRootError(targetRel, err)
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
	if info.Mode()&fs.ModeSymlink == 0 && !info.Mode().IsRegular() {
		return resolvedReadTarget{}, ErrNonRegularFile
	}

	resolvedAbs, err := evalSymlinksFn(target.abs)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return resolvedReadTarget{}, &targetPathSymlinkError{path: target.rel}
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return resolvedReadTarget{}, &targetPathSymlinkError{path: target.rel}
		}
		return resolvedReadTarget{}, translateOpenNotExist(err, target.abs)
	}
	canonicalRootAbs, err := evalSymlinksFn(target.rootAbs)
	if err != nil {
		return resolvedReadTarget{}, err
	}
	resolvedRel, err := resolveRelativeTargetWithinRoot(canonicalRootAbs, resolvedAbs)
	if err != nil {
		return resolvedReadTarget{}, &targetPathSymlinkError{path: target.rel}
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

func openPinnedReadTargetWithinRoot(root Root, rootAbs, targetRel, targetPath string, expectedPathInfo, expectedOpenedInfo fs.FileInfo) (file File, err error) {
	parent, closeParent, err := openReadTargetParentNoFollow(root, rootAbs, filepath.Dir(targetRel))
	if err != nil {
		return nil, err
	}
	defer closeOpenedReadTargetParent(parent, closeParent, &file, &err)

	name := filepath.Base(targetRel)
	if err := validatePinnedReadLeaf(parent, name, targetRel, targetPath, expectedPathInfo); err != nil {
		return nil, err
	}

	file, err = parent.Open(name)
	if err != nil {
		if expectedPathInfo.Mode()&os.ModeSymlink != 0 {
			return nil, &targetPathSymlinkError{path: targetRel}
		}
		return nil, normalizePathEscapesRootError(targetPath, err)
	}
	if err := validatePinnedOpenedReadFile(file, targetRel, targetPath, expectedPathInfo, expectedOpenedInfo); err != nil {
		return nil, err
	}
	return file, nil
}

func validatePinnedReadLeaf(parent Root, name, targetRel, targetPath string, expectedPathInfo fs.FileInfo) error {
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return translateOpenNotExist(err, targetPath)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 && expectedPathInfo.Mode()&os.ModeSymlink == 0 {
		return &targetPathSymlinkError{path: targetRel}
	}
	if pathInfo.Mode()&os.ModeSymlink != expectedPathInfo.Mode()&os.ModeSymlink {
		return fmt.Errorf("path changed while opening: %s", targetPath)
	}
	if os.SameFile(expectedPathInfo, pathInfo) {
		return nil
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return &targetPathSymlinkError{path: targetRel}
	}
	if !pathInfo.Mode().IsRegular() {
		return ErrNonRegularFile
	}
	return fmt.Errorf("path changed while opening: %s", targetPath)
}

func validatePinnedOpenedReadFile(file File, targetRel, targetPath string, expectedPathInfo, expectedOpenedInfo fs.FileInfo) error {
	openedInfo, err := file.Stat()
	if err != nil {
		return closeFileAndJoinError(file, err)
	}
	if !openedInfo.Mode().IsRegular() {
		if expectedPathInfo.Mode()&os.ModeSymlink != 0 {
			return closeFileAndJoinError(file, &targetPathSymlinkError{path: targetRel})
		}
		return closeFileAndJoinError(file, ErrNonRegularFile)
	}
	if os.SameFile(expectedOpenedInfo, openedInfo) {
		return nil
	}
	return closeFileAndJoinError(file, fmt.Errorf("path changed while opening: %s", targetPath))
}

func closeOpenedReadTargetParent(parent Root, closeParent bool, file *File, err *error) {
	if !closeParent {
		return
	}
	if closeErr := parent.Close(); closeErr != nil {
		if *err == nil && *file != nil {
			*err = closeFileAndJoinError(*file, closeErr)
			*file = nil
			return
		}
		*err = errors.Join(*err, closeErr)
	}
}

func isPathEscapesParentError(err error) bool {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && strings.Contains(pathErr.Err.Error(), "path escapes from parent") {
		return true
	}
	return strings.Contains(err.Error(), "path escapes from parent")
}

func samePinnedRootPath(requestedPath, canonicalPath string) bool {
	if filepath.Clean(requestedPath) == filepath.Clean(canonicalPath) {
		return true
	}
	if runtime.GOOS != "darwin" {
		return false
	}
	for _, alias := range []struct {
		requested string
		canonical string
	}{
		{requested: filepath.Join(string(os.PathSeparator), "tmp"), canonical: filepath.Join(string(os.PathSeparator), "private", "tmp")},
		{requested: filepath.Join(string(os.PathSeparator), "var"), canonical: filepath.Join(string(os.PathSeparator), "private", "var")},
	} {
		if requestedPath == alias.requested && canonicalPath == alias.canonical {
			return true
		}
		if strings.HasPrefix(filepath.Clean(requestedPath), alias.requested+string(os.PathSeparator)) &&
			strings.HasPrefix(filepath.Clean(canonicalPath), alias.canonical+string(os.PathSeparator)) {
			relRequested := strings.TrimPrefix(filepath.Clean(requestedPath), alias.requested)
			relCanonical := strings.TrimPrefix(filepath.Clean(canonicalPath), alias.canonical)
			if relRequested == relCanonical {
				return true
			}
		}
	}
	return false
}

func closeFileAndJoinError(file File, primaryErr error) error {
	closeErr := file.Close()
	if closeErr != nil {
		return errors.Join(primaryErr, closeErr)
	}
	return primaryErr
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
			if !errors.Is(err, fs.ErrNotExist) {
				err = errors.Join(ErrNonRegularFile, err)
			}
			return nil, false, closeOpenedReadRootWithError(current, currentOwned, err)
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
