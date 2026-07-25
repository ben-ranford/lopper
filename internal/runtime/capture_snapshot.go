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

type runtimeTraceSnapshot struct {
	info   os.FileInfo
	data   []byte
	digest string
}

func snapshotRuntimeTraceFile(tracePath string) (_ runtimeTraceSnapshot, err error) {
	absolutePath, err := filepath.Abs(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	traceRootPath, err := runtimeTraceRootPath(filepath.Dir(absolutePath))
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: open trace root: %w", err)
	}
	root, err := safeio.OpenRootNoFollow(traceRootPath)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: open trace root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	fileName := filepath.Base(absolutePath)
	pathInfo, err := root.Lstat(fileName)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(pathInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if snapshotRuntimeTraceFileAfterPathSnapshotHook != nil {
		snapshotRuntimeTraceFileAfterPathSnapshotHook()
	}

	file, err := root.Open(fileName)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if snapshotRuntimeTraceFileAfterOpenHook != nil {
		snapshotRuntimeTraceFileAfterOpenHook()
	}

	openedInfo, err := file.Stat()
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(openedInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(pathInfo, openedInfo) || !sameRuntimeTraceMetadata(pathInfo, openedInfo) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while opening: %s", tracePath)
	}

	data, err := io.ReadAll(newRuntimeTraceByteLimitReader(file, maxRuntimeTraceBytes))
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	descriptorInfo, err := file.Stat()
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(descriptorInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	currentPathInfo, err := root.Lstat(fileName)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(currentPathInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(openedInfo, descriptorInfo) ||
		!sameRuntimeTraceMetadata(openedInfo, descriptorInfo) ||
		!os.SameFile(descriptorInfo, currentPathInfo) ||
		!sameRuntimeTraceMetadata(descriptorInfo, currentPathInfo) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while hashing: %s", tracePath)
	}

	digest := sha256.Sum256(data)
	return runtimeTraceSnapshot{
		info:   descriptorInfo,
		data:   data,
		digest: fmt.Sprintf("%x", digest),
	}, nil
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
