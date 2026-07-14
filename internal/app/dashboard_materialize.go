package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	if hasTrailingOutputPathSeparator(trimmedOutputPath) {
		return "", fmt.Errorf("output path must name a file: %s", trimmedOutputPath)
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
	return rootedCommandOutputRoot(outputPath)
}

func absoluteCommandOutputRoot(outputPath string) (string, error) {
	return rootedCommandOutputRoot(outputPath)
}

func rootedCommandOutputRoot(outputPath string) (string, error) {
	outputAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	workspaceRoot, err := trustedCommandOutputRoot(outputAbs)
	if err != nil {
		return "", err
	}

	existingRoot, err := resolveExistingOutputRoot(outputAbs, outputPath)
	if err != nil {
		return "", err
	}
	if workspaceRoot == "" {
		return existingRoot, nil
	}
	if err := rejectSymlinkedOutputRoot(workspaceRoot, filepath.Dir(outputAbs)); err != nil {
		return "", err
	}
	return workspaceRoot, nil
}

func resolveExistingOutputRoot(outputAbs, outputPath string) (string, error) {
	current := filepath.Dir(outputAbs)
	for {
		next, done, err := inspectOutputRootPath(current, outputPath)
		if done || err != nil {
			return next, err
		}
		current = next
	}
}

func inspectOutputRootPath(current, outputPath string) (string, bool, error) {
	if isKnownSystemAliasRoot(current) {
		if _, err := os.Stat(current); err == nil {
			return current, true, nil
		}
	}

	info, err := os.Lstat(current)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", true, fmt.Errorf("output root contains symlink: %s", current)
		}
		if !info.IsDir() {
			return "", true, fmt.Errorf("output root is not a directory: %s", current)
		}
		return current, true, nil
	case !os.IsNotExist(err):
		return "", true, err
	}

	parent := filepath.Dir(current)
	if parent == current {
		return "", true, fmt.Errorf("resolve output root for %s: no existing parent directory", outputPath)
	}
	return parent, false, nil
}

func trustedCommandOutputRoot(outputAbs string) (string, error) {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve output workspace: %w", err)
	}
	withinWorkspace, err := pathWithinRoot(workspaceRoot, outputAbs)
	if err != nil {
		return "", err
	}
	if !withinWorkspace {
		resolvedWorkspaceRoot, err := filepath.EvalSymlinks(workspaceRoot)
		if err != nil {
			return "", fmt.Errorf("resolve output workspace symlinks: %w", err)
		}
		aliasedWorkspaceRoot, err := resolveAliasedWorkspaceRoot(outputAbs, resolvedWorkspaceRoot)
		if err != nil {
			return "", err
		}
		if aliasedWorkspaceRoot == "" {
			return "", nil
		}
		return aliasedWorkspaceRoot, nil
	}
	return workspaceRoot, nil
}

func resolveAliasedWorkspaceRoot(outputAbs, workspaceRoot string) (string, error) {
	current := filepath.Dir(filepath.Clean(outputAbs))
	var aliasRoot string
	for {
		_, err := os.Lstat(current)
		switch {
		case err == nil:
			resolvedCurrent, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			withinWorkspace, err := pathWithinRoot(workspaceRoot, resolvedCurrent)
			if err != nil {
				return "", err
			}
			if withinWorkspace {
				aliasRoot = current
			}
		case !os.IsNotExist(err):
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return aliasRoot, nil
		}
		current = parent
	}
}

func pathWithinRoot(rootAbs, targetAbs string) (bool, error) {
	if pathsUseDifferentWindowsVolumes(rootAbs, targetAbs) {
		return false, nil
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false, fmt.Errorf("compute output path: %w", err)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))), nil
}

func isKnownSystemAliasRoot(path string) bool {
	return runtime.GOOS == "darwin" && (path == "/tmp" || path == "/var")
}

func pathsUseDifferentWindowsVolumes(rootAbs, targetAbs string) bool {
	rootVolume := pathVolumeName(rootAbs)
	targetVolume := pathVolumeName(targetAbs)
	return rootVolume != "" && targetVolume != "" && rootVolume != targetVolume
}

func pathVolumeName(path string) string {
	volume := filepath.VolumeName(path)
	if volume == "" && len(path) >= 2 && path[1] == ':' {
		drive := path[0]
		if ('a' <= drive && drive <= 'z') || ('A' <= drive && drive <= 'Z') {
			volume = path[:2]
		}
	}
	return strings.ToLower(volume)
}

func hasTrailingOutputPathSeparator(path string) bool {
	return path != "" && os.IsPathSeparator(path[len(path)-1])
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
	return rejectSymlinkedPath(rootAbs, parentAbs, "output parent escapes workspace: %s", "output parent contains symlink: %s")
}

func rejectSymlinkedOutputRoot(rootAbs, parentAbs string) error {
	return rejectSymlinkedPath(rootAbs, parentAbs, "output root escapes workspace: %s", "output root contains symlink: %s")
}

func rejectSymlinkedPath(rootAbs, parentAbs, escapeFormat, symlinkFormat string) error {
	rel, err := filepath.Rel(rootAbs, parentAbs)
	if err != nil {
		return fmt.Errorf("compute output parent: %w", err)
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf(escapeFormat, parentAbs)
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
			return fmt.Errorf(symlinkFormat, current)
		}
	}
	return nil
}
