package analysis

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func copyFile(repoPath, scopedRoot, relativePath string) (err error) {
	if !isSafeRelativePath(relativePath) {
		return fmt.Errorf("invalid relative path for scoped copy: %s", relativePath)
	}
	cleanRelativePath := filepath.Clean(relativePath)
	sourcePath := filepath.Join(repoPath, cleanRelativePath)
	targetPath := filepath.Join(scopedRoot, cleanRelativePath)
	if !pathWithin(repoPath, sourcePath) {
		return fmt.Errorf("source path escapes repository scope: %s", sourcePath)
	}
	if !pathWithin(scopedRoot, targetPath) {
		return fmt.Errorf("target path escapes scoped workspace: %s", targetPath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return err
	}

	sourceRoot, err := os.OpenRoot(repoPath)
	if err != nil {
		return fmt.Errorf("open source root: %w", err)
	}
	defer joinCloseError(&err, sourceRoot.Close)

	source, err := sourceRoot.Open(cleanRelativePath)
	if err != nil {
		return err
	}
	defer joinCloseError(&err, source.Close)

	targetRoot, err := os.OpenRoot(scopedRoot)
	if err != nil {
		return fmt.Errorf("open target root: %w", err)
	}
	defer joinCloseError(&err, targetRoot.Close)

	target, err := targetRoot.OpenFile(cleanRelativePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer joinCloseError(&err, target.Close)
	if _, err := io.Copy(target, source); err != nil {
		return err
	}
	return nil
}

func joinCloseError(target *error, closeFn func() error) {
	if closeErr := closeFn(); closeErr != nil {
		*target = errors.Join(*target, closeErr)
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func isSafeRelativePath(relativePath string) bool {
	if filepath.IsAbs(relativePath) {
		return false
	}
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." {
		return false
	}
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}
