package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/errutil"
)

var ErrPathEscapesRoot = errors.New("path escapes root")

type rootedTarget struct {
	rootAbs string
	rel     string
	abs     string
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

type pathEscapesRootError struct {
	path string
}

func (e *pathEscapesRootError) Error() string {
	return fmt.Sprintf("%s: %s", ErrPathEscapesRoot, e.path)
}

func (*pathEscapesRootError) Unwrap() error {
	return ErrPathEscapesRoot
}

func newPathEscapesRootError(path string) error {
	return &pathEscapesRootError{path: path}
}

func normalizePathEscapesRootError(path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrPathEscapesRoot) {
		return err
	}

	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathEscapesRootInvariant(pathErr.Path) {
		return newPathEscapesRootError(path)
	}
	return err
}

func pathEscapesRootInvariant(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := resolveRelativeTarget(path, allowRootTarget)
	return errors.Is(err, ErrPathEscapesRoot)
}

func resolveRelativeTarget(targetPath string, policy rootedTargetPolicy) (string, error) {
	if filepath.IsAbs(targetPath) {
		return "", newPathEscapesRootError(targetPath)
	}
	return normalizeRootedTarget(targetPath, filepath.Clean(targetPath), policy)
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

	rel, err := fileSystem.Rel(rootAbs, targetAbs)
	if err != nil {
		return rootedTarget{}, fmt.Errorf("compute relative path: %w", err)
	}
	rel, err = normalizeRootedTarget(targetPath, rel, policy)
	if err != nil {
		return rootedTarget{}, err
	}

	return rootedTarget{rootAbs: rootAbs, rel: rel, abs: targetAbs}, nil
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
	absPath, err := fileSystem.Abs(path)
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
		return "", newPathEscapesRootError(targetPath)
	}

	return filepath.Clean(rel), nil
}

func translateOpenNotExist(err error, targetPath string) error {
	if errors.Is(err, fs.ErrNotExist) {
		pathErr := &fs.PathError{Op: "open", Path: targetPath, Err: err}
		if isPureSentinelError(err, fs.ErrNotExist) {
			pathErr.Err = os.ErrNotExist
		}
		return pathErr
	}
	return err
}

func isPureSentinelError(err error, sentinels ...error) bool {
	return errutil.IsPureSentinelError(err, sentinels...)
}
