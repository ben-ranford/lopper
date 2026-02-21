package safeio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileUnder reads targetPath only if it resolves under rootDir.
func ReadFileUnder(rootDir, targetPath string) ([]byte, error) {
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
	defer root.Close()

	rel = filepath.Clean(rel)
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}

// ReadFile reads the exact targetPath by opening its parent directory as a root.
func ReadFile(targetPath string) ([]byte, error) {
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
	defer root.Close()

	file, err := root.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}
