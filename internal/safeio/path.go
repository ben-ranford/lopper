package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type rootedTarget struct {
	rootAbs string
	rel     string
}

type rootedTargetPolicy int

const (
	allowRootTarget rootedTargetPolicy = iota
	rejectRootTarget
)

type exactFileTarget struct {
	parentDir string
	fileName  string
}

func resolveRootedTarget(rootDir, targetPath string, policy rootedTargetPolicy) (rootedTarget, error) {
	rootAbs, err := resolveAbsolutePath("root", rootDir)
	if err != nil {
		return rootedTarget{}, err
	}
	targetAbs, err := resolveAbsolutePath("target", targetPath)
	if err != nil {
		return rootedTarget{}, err
	}

	rel, err := relPathFn(rootAbs, targetAbs)
	if err != nil {
		return rootedTarget{}, fmt.Errorf("compute relative path: %w", err)
	}
	rel, err = normalizeRootedTarget(targetPath, rel, policy)
	if err != nil {
		return rootedTarget{}, err
	}

	return rootedTarget{rootAbs: rootAbs, rel: rel}, nil
}

func resolveExactFileTarget(targetPath string) (exactFileTarget, error) {
	targetAbs, err := resolveAbsolutePath("target", targetPath)
	if err != nil {
		return exactFileTarget{}, err
	}

	return exactFileTarget{
		parentDir: filepath.Dir(targetAbs),
		fileName:  filepath.Base(targetAbs),
	}, nil
}

func resolveAbsolutePath(kind, path string) (string, error) {
	absPath, err := absPathFn(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s path: %w", kind, err)
	}
	return absPath, nil
}

func normalizeRootedTarget(targetPath, rel string, policy rootedTargetPolicy) (string, error) {
	switch {
	case rel == ".":
		if policy == rejectRootTarget {
			return "", fmt.Errorf("target path resolves to root directory: %s", targetPath)
		}
	case rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)):
		return "", fmt.Errorf("path escapes root: %s", targetPath)
	}

	return filepath.Clean(rel), nil
}

func translateOpenNotExist(err error, targetPath string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return &fs.PathError{Op: "open", Path: targetPath, Err: os.ErrNotExist}
	}
	return err
}
