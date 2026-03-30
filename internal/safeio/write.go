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
	randomTempNameFn  = randomTempName
	randReadFn        = rand.Read
	cleanupTempFileFn = cleanupAtomicTempFile
	chmodFileFn       = func(file *os.File, perm os.FileMode) error {
		return file.Chmod(perm)
	}
	openFileFn = func(root *os.Root, rel string, perm os.FileMode) (*os.File, error) {
		return root.OpenFile(rel, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	}
	renameFileFn = func(root *os.Root, oldName, newName string) error {
		return root.Rename(oldName, newName)
	}
	writeFileFn = func(file *os.File, data []byte) (int, error) {
		return file.Write(data)
	}
)

// WriteFileUnder atomically writes targetPath only if it resolves under rootDir.
func WriteFileUnder(rootDir, targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	target, err := resolveRootedTarget(rootDir, targetPath, rejectRootTarget)
	if err != nil {
		return err
	}

	root, err := openRootFn(target.rootAbs)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() {
		if closeErr := closeRootFn(root); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	session, err := newAtomicWriteSession(root, target.rel, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := session.cleanup(); cleanupErr != nil && returnErr == nil {
			returnErr = cleanupErr
		}
	}()

	return session.writeAndCommit(data, perm)
}

func cleanupAtomicTempFile(root *os.Root, tempRel string, tempFile *os.File) error {
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

func createAtomicTempFile(root *os.Root, dir string, perm os.FileMode) (string, *os.File, error) {
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
		file, err := openFileFn(root, tempRel, perm)
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
