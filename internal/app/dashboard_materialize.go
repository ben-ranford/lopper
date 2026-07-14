package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func persistDashboardOutput(formatted, outputPath string) (string, error) {
	return persistCommandOutput(formatted, outputPath, "dashboard report")
}

func persistCommandOutput(formatted, outputPath, label string) (string, error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" || trimmedOutputPath == "-" {
		return formatted, nil
	}

	outputRoot, err := commandOutputRoot(trimmedOutputPath)
	if err != nil {
		return "", err
	}
	if err := ensureCommandOutputParent(outputRoot, trimmedOutputPath); err != nil {
		return "", err
	}
	if err := safeio.WriteFileUnder(outputRoot, trimmedOutputPath, []byte(formatted), 0o600); err != nil {
		return "", err
	}
	return label + " written to " + trimmedOutputPath, nil
}

func commandOutputRoot(outputPath string) (string, error) {
	if filepath.IsAbs(outputPath) {
		return absoluteCommandOutputRoot(outputPath)
	}
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve output workspace: %w", err)
	}
	return workspaceRoot, nil
}

func absoluteCommandOutputRoot(outputPath string) (string, error) {
	outputAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}

	current := filepath.Dir(outputAbs)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("output root contains symlink: %s", current)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("output root is not a directory: %s", current)
			}
			return current, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve output root for %s: no existing parent directory", outputPath)
		}
		current = parent
	}
}

func ensureCommandOutputParent(rootDir, outputPath string) error {
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolve output root: %w", err)
	}
	outputAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, outputAbs)
	if err != nil {
		return fmt.Errorf("compute output path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("output path escapes workspace: %s", outputPath)
	}

	parent := filepath.Dir(outputAbs)
	if err := rejectSymlinkedOutputParent(rootAbs, parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	return rejectSymlinkedOutputParent(rootAbs, parent)
}

func rejectSymlinkedOutputParent(rootAbs, parentAbs string) error {
	rel, err := filepath.Rel(rootAbs, parentAbs)
	if err != nil {
		return fmt.Errorf("compute output parent: %w", err)
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("output parent escapes workspace: %s", parentAbs)
	}

	current := rootAbs
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output parent contains symlink: %s", current)
		}
	}
	return nil
}
