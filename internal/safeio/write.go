package safeio

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const atomicTempPrefix = ".safeio-atomic-"

var (
	randomTempNameFn = randomTempName
	randReadFn       = rand.Read
)

// WriteFileUnder atomically writes targetPath only if it resolves under rootDir.
// Existing regular targets must be writable and retain their permission bits.
// Ownership follows atomic replacement semantics; writes never fall back to in-place mutation.
func WriteFileUnder(rootDir, targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	target, err := resolveRootedTarget(rootDir, targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	writePerm, existingRegular, err := resolvedWriteFilePerm(target, perm)
	if err != nil {
		return err
	}

	root, err := fileSystem.OpenRoot(target.rootAbs)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	if existingRegular {
		file, err := root.OpenFile(target.rel, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}

	session, err := newAtomicWriteSession(root, target.rel, writePerm)
	if err != nil {
		return err
	}
	defer func() {
		cleanupErr := session.cleanup()
		if returnErr == nil {
			returnErr = cleanupErr
		}
	}()

	return session.writeAndCommit(data, writePerm)
}

func resolvedWriteFilePerm(target rootedTarget, requestedPerm os.FileMode) (os.FileMode, bool, error) {
	info, err := os.Lstat(target.abs)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, false, fmt.Errorf("target path is a symlink: %s", target.abs)
		}
		if !info.Mode().IsRegular() {
			return 0, false, fmt.Errorf("target path is not a regular file: %s", target.abs)
		}
		return info.Mode().Perm(), true, nil
	case os.IsNotExist(err):
		return requestedPerm, false, nil
	default:
		return 0, false, err
	}
}

func cleanupAtomicTempFile(root Root, tempRel string, tempFile File) error {
	var cleanupErr error
	if tempFile != nil {
		if err := tempFile.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			cleanupErr = err
		}
	}
	if tempRel != "" {
		if err := root.Remove(tempRel); err != nil && !errors.Is(err, os.ErrNotExist) {
			if cleanupErr == nil {
				return err
			}
			return errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func createAtomicTempFile(root Root, dir string, perm os.FileMode) (string, File, error) {
	tempDir := filepath.Clean(dir)
	if tempDir == "." {
		tempDir = ""
	}

	for range 10 {
		name, err := randomTempNameFn()
		if err != nil {
			return "", nil, err
		}
		tempRel := filepath.Join(tempDir, name)
		file, err := root.OpenFile(tempRel, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return tempRel, file, nil
	}

	return "", nil, fmt.Errorf("create temp file: too many collisions")
}

func randomTempName() (string, error) {
	var suffix [8]byte
	if _, err := randReadFn(suffix[:]); err != nil {
		return "", fmt.Errorf("generate temp name: %w", err)
	}
	return fmt.Sprintf("%s%x", atomicTempPrefix, suffix), nil
}
