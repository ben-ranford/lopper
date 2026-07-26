package runtime

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

var stableRuntimeTraceFileAfterFirstSnapshotHook func()
var snapshotRuntimeTraceFileAfterPathSnapshotHook func()
var snapshotRuntimeTraceFileAfterOpenHook func()

func stableRuntimeTraceFileSnapshot(tracePath string) (runtimeTraceSnapshot, error) {
	first, err := snapshotRuntimeTraceFile(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if stableRuntimeTraceFileAfterFirstSnapshotHook != nil {
		stableRuntimeTraceFileAfterFirstSnapshotHook()
	}
	second, err := snapshotRuntimeTraceFile(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(first.info, second.info) || !sameRuntimeTraceSnapshot(first, second) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while hashing: %s", tracePath)
	}
	return first, nil
}

func stableRuntimeTraceFileSnapshotWithinRoot(root safeio.Root, fileName string, tracePath string) (runtimeTraceSnapshot, error) {
	first, err := snapshotRuntimeTraceFileWithinRoot(root, fileName, tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if stableRuntimeTraceFileAfterFirstSnapshotHook != nil {
		stableRuntimeTraceFileAfterFirstSnapshotHook()
	}
	second, err := snapshotRuntimeTraceFileWithinRoot(root, fileName, tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(first.info, second.info) || !sameRuntimeTraceSnapshot(first, second) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while hashing: %s", tracePath)
	}
	return first, nil
}

type runtimeTraceSnapshot struct {
	info   os.FileInfo
	data   []byte
	digest string
}

func snapshotRuntimeTraceFile(tracePath string) (_ runtimeTraceSnapshot, err error) {
	root, fileName, err := openRuntimeTraceRoot(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return snapshotRuntimeTraceFileWithinRoot(root, fileName, tracePath)
}

func snapshotRuntimeTraceFileWithinRoot(root safeio.Root, fileName string, tracePath string) (_ runtimeTraceSnapshot, err error) {
	_, file, openedInfo, err := openRuntimeTraceFile(root, fileName, tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	data, err := io.ReadAll(newRuntimeTraceByteLimitReader(file, maxRuntimeTraceBytes))
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	descriptorInfo, err := validateRuntimeTraceRead(root, file, fileName, tracePath, openedInfo)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}

	digest := sha256.Sum256(data)
	return runtimeTraceSnapshot{
		info:   descriptorInfo,
		data:   data,
		digest: fmt.Sprintf("%x", digest),
	}, nil
}

func openRuntimeTraceRoot(tracePath string) (safeio.Root, string, error) {
	absolutePath, err := filepath.Abs(tracePath)
	if err != nil {
		return nil, "", fmt.Errorf("hash runtime trace: %w", err)
	}
	traceRootPath, err := runtimeTraceRootPath(filepath.Dir(absolutePath))
	if err != nil {
		return nil, "", fmt.Errorf("hash runtime trace: open trace root: %w", err)
	}
	root, err := safeio.OpenRootNoFollow(traceRootPath)
	if err != nil {
		return nil, "", fmt.Errorf("hash runtime trace: open trace root: %w", err)
	}
	return root, filepath.Base(absolutePath), nil
}

func openRuntimeTraceFile(root safeio.Root, fileName string, tracePath string) (os.FileInfo, safeio.File, os.FileInfo, error) {
	pathInfo, err := validatedRuntimeTracePathInfo(root, fileName, tracePath)
	if err != nil {
		return nil, nil, nil, err
	}
	if snapshotRuntimeTraceFileAfterPathSnapshotHook != nil {
		snapshotRuntimeTraceFileAfterPathSnapshotHook()
	}

	file, err := root.Open(fileName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hash runtime trace: %w", err)
	}
	if snapshotRuntimeTraceFileAfterOpenHook != nil {
		snapshotRuntimeTraceFileAfterOpenHook()
	}

	openedInfo, err := validatedRuntimeTraceFileStat(file, tracePath)
	if err != nil {
		closeErr := file.Close()
		return nil, nil, nil, errors.Join(err, closeErr)
	}
	if err := ensureSameRuntimeTraceFile(pathInfo, openedInfo, tracePath, "opening"); err != nil {
		closeErr := file.Close()
		return nil, nil, nil, errors.Join(err, closeErr)
	}
	return pathInfo, file, openedInfo, nil
}

func validateRuntimeTraceRead(root safeio.Root, file safeio.File, fileName string, tracePath string, openedInfo os.FileInfo) (os.FileInfo, error) {
	descriptorInfo, err := validatedRuntimeTraceFileStat(file, tracePath)
	if err != nil {
		return nil, err
	}
	currentPathInfo, err := validatedRuntimeTracePathInfo(root, fileName, tracePath)
	if err != nil {
		return nil, err
	}
	if err := ensureSameRuntimeTraceFile(openedInfo, descriptorInfo, tracePath, "hashing"); err != nil {
		return nil, err
	}
	if err := ensureSameRuntimeTraceFile(descriptorInfo, currentPathInfo, tracePath, "hashing"); err != nil {
		return nil, err
	}
	return descriptorInfo, nil
}

func validatedRuntimeTracePathInfo(root safeio.Root, fileName string, tracePath string) (os.FileInfo, error) {
	info, err := root.Lstat(fileName)
	if err != nil {
		return nil, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(info, tracePath); err != nil {
		return nil, err
	}
	return info, nil
}

func validatedRuntimeTraceFileStat(file safeio.File, tracePath string) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(info, tracePath); err != nil {
		return nil, err
	}
	return info, nil
}

func ensureSameRuntimeTraceFile(expected os.FileInfo, actual os.FileInfo, tracePath string, stage string) error {
	if os.SameFile(expected, actual) && sameRuntimeTraceMetadata(expected, actual) {
		return nil
	}
	return fmt.Errorf("hash runtime trace: trace file changed while %s: %s", stage, tracePath)
}

func runtimeTraceRootPath(path string) (string, error) {
	cleanPath := filepath.Clean(path)
	resolvedPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return "", err
	}
	if resolvedPath == cleanPath {
		return cleanPath, nil
	}
	if allowsSystemRuntimeTraceRootAlias(cleanPath, resolvedPath) {
		return resolvedPath, nil
	}
	return "", fmt.Errorf("root contains symlink: %s", cleanPath)
}

func allowsSystemRuntimeTraceRootAlias(path, resolvedPath string) bool {
	if goruntime.GOOS != "darwin" {
		return false
	}
	for _, prefix := range []string{"/var", "/tmp"} {
		if (path == prefix || strings.HasPrefix(path, prefix+string(os.PathSeparator))) &&
			resolvedPath == "/private"+path {
			return true
		}
	}
	return false
}

func validateRuntimeTraceFileInfo(info os.FileInfo, tracePath string) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("hash runtime trace: trace file must not be a symlink: %s", tracePath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("hash runtime trace: trace file must be regular: %s", tracePath)
	}
	if info.Size() > maxRuntimeTraceBytes {
		return fmt.Errorf("hash runtime trace: %w", safeio.ErrFileTooLarge)
	}
	return nil
}

func sameRuntimeTraceSnapshot(first, second runtimeTraceSnapshot) bool {
	return sameRuntimeTraceMetadata(first.info, second.info) && first.digest == second.digest
}

func sameRuntimeTraceMetadata(first, second os.FileInfo) bool {
	return first.Size() == second.Size() &&
		first.Mode() == second.Mode() &&
		first.ModTime().Equal(second.ModTime())
}
