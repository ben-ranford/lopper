package safeio

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	rootAbs, rel, err := resolveWriteTarget(rootDir, targetPath)
	if err != nil {
		return err
	}

	root, err := openRootFn(rootAbs)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() {
		if closeErr := closeRootFn(root); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	tempRel, tempFile, err := createAtomicTempFile(root, filepath.Dir(rel), perm)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := cleanupTempFileFn(root, tempRel, tempFile); cleanupErr != nil && returnErr == nil {
			returnErr = cleanupErr
		}
	}()

	if _, err := writeFileFn(tempFile, data); err != nil {
		return err
	}
	if err := chmodFileFn(tempFile, perm); err != nil {
		return err
	}
	if err := closeFileFn(tempFile); err != nil {
		return err
	}
	tempFile = nil

	if err := renameFileFn(root, tempRel, rel); err != nil {
		return err
	}
	tempRel = ""
	return nil
}

func resolveWriteTarget(rootDir, targetPath string) (string, string, error) {
	rootAbs, err := absPathFn(rootDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve root path: %w", err)
	}
	targetAbs, err := absPathFn(targetPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve target path: %w", err)
	}
	rel, err := relPathFn(rootAbs, targetAbs)
	if err != nil {
		return "", "", fmt.Errorf("compute relative path: %w", err)
	}
	rel, err = validateRelativeTarget(targetPath, rel)
	if err != nil {
		return "", "", err
	}
	return rootAbs, rel, nil
}

func validateRelativeTarget(targetPath, rel string) (string, error) {
	switch {
	case rel == ".":
		return "", fmt.Errorf("target path resolves to root directory: %s", targetPath)
	case rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)):
		return "", fmt.Errorf("path escapes root: %s", targetPath)
	default:
		return filepath.Clean(rel), nil
	}
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
