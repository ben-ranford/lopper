package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	openCommandOutputWriteRootFn = safeio.OpenCanonicalWriteRoot
	writeCommandOutputFileFn     = func(root *safeio.WriteRoot, targetPath string, data []byte, perm, parentPerm os.FileMode) error {
		return root.WriteFileCreatingParents(targetPath, data, perm, parentPerm)
	}
	commandOutputBoundaryAcceptedFn = func() error { return nil }
)

type commandOutputRootBoundary struct {
	path     string
	resolved string
}

type commandOutputDestination struct {
	root       *safeio.WriteRoot
	targetPath string
}

func persistDashboardOutput(formatted, outputPath string, trustedRoots ...string) (string, error) {
	return persistCommandOutput(formatted, outputPath, "dashboard report", trustedRoots...)
}

func persistCommandOutput(formatted, outputPath, label string, trustedRoots ...string) (result string, returnErr error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" || trimmedOutputPath == "-" {
		return formatted, nil
	}
	if hasDirectoryStyleOutputPath(trimmedOutputPath) {
		return "", fmt.Errorf("output path must name a file: %s", trimmedOutputPath)
	}

	destination, err := openCommandOutputDestination(trimmedOutputPath, trustedRoots...)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := destination.root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	if err := writeCommandOutputFileFn(destination.root, destination.targetPath, []byte(formatted), 0o600, 0o750); err != nil {
		return "", err
	}
	return label + " written to " + trimmedOutputPath, nil
}

func commandOutputRoot(outputPath string, trustedRoots ...string) (string, error) {
	root, _, err := commandOutputRootBoundaryForPath(outputPath, trustedRoots...)
	return root.path, err
}

func openCommandOutputDestination(outputPath string, trustedRoots ...string) (commandOutputDestination, error) {
	root, outputAbs, err := commandOutputRootBoundaryForPath(outputPath, trustedRoots...)
	if err != nil {
		return commandOutputDestination{}, err
	}
	targetPath, err := filepath.Rel(root.path, outputAbs)
	if err != nil {
		return commandOutputDestination{}, fmt.Errorf("compute output path: %w", err)
	}
	if targetPath == ".." || strings.HasPrefix(targetPath, ".."+string(os.PathSeparator)) {
		return commandOutputDestination{}, fmt.Errorf("output path escapes workspace: %s", outputPath)
	}
	writeRoot, err := openCommandOutputWriteRootFn(root.resolved)
	if err != nil {
		return commandOutputDestination{}, err
	}
	if err := commandOutputBoundaryAcceptedFn(); err != nil {
		if closeErr := writeRoot.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return commandOutputDestination{}, err
	}
	return commandOutputDestination{root: writeRoot, targetPath: targetPath}, nil
}

func commandOutputRootBoundaryForPath(outputPath string, trustedRoots ...string) (commandOutputRootBoundary, string, error) {
	if err := rejectAmbiguousParentTraversal(outputPath); err != nil {
		return commandOutputRootBoundary{}, "", err
	}
	outputAbs, err := absoluteCommandOutputPath(outputPath)
	if err != nil {
		return commandOutputRootBoundary{}, "", fmt.Errorf("resolve output path: %w", err)
	}

	trustedRoot, err := resolvedCommandOutputRoot(outputAbs, func() (commandOutputRootBoundary, error) {
		return trustedCommandOutputRootBoundaryForRoots(outputAbs, trustedRoots...)
	})
	if trustedRoot.path != "" || err != nil {
		return trustedRoot, outputAbs, err
	}

	workspaceRoot, workspaceErr := resolvedCommandOutputRoot(outputAbs, func() (commandOutputRootBoundary, error) {
		return trustedCommandOutputRootBoundary(outputAbs)
	})
	if workspaceRoot.path != "" {
		return workspaceRoot, outputAbs, nil
	}

	fallbackRoot, err := fallbackCommandOutputRootBoundary(outputAbs, outputPath, workspaceErr)
	return fallbackRoot, outputAbs, err
}

func absoluteCommandOutputPath(outputPath string) (string, error) {
	if filepath.IsAbs(outputPath) {
		return filepath.Clean(outputPath), nil
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	physicalWorkingDirectory, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		return "", err
	}
	return filepath.Join(physicalWorkingDirectory, outputPath), nil
}

func resolvedCommandOutputRoot(outputAbs string, resolve func() (commandOutputRootBoundary, error)) (commandOutputRootBoundary, error) {
	root, err := resolve()
	if err != nil || root.path == "" {
		return root, err
	}
	if err := rejectSymlinkedOutputRoot(root.path, filepath.Dir(outputAbs)); err != nil {
		return commandOutputRootBoundary{}, err
	}
	return root, nil
}

func fallbackCommandOutputRoot(outputAbs, outputPath string, workspaceErr error) (string, error) {
	root, err := fallbackCommandOutputRootBoundary(outputAbs, outputPath, workspaceErr)
	return root.path, err
}

func fallbackCommandOutputRootBoundary(outputAbs, outputPath string, workspaceErr error) (commandOutputRootBoundary, error) {
	existingRoot, err := resolveExistingOutputRoot(outputAbs, outputPath)
	if err != nil {
		return commandOutputRootBoundary{}, err
	}
	if workspaceErr != nil && filepath.IsAbs(outputPath) {
		return existingRoot, nil
	}
	if workspaceErr != nil {
		return commandOutputRootBoundary{}, workspaceErr
	}
	return existingRoot, nil
}

func resolveExistingOutputRoot(outputAbs, outputPath string) (commandOutputRootBoundary, error) {
	current := filepath.Dir(outputAbs)
	for {
		next, done, err := inspectOutputRootPath(current, outputPath)
		if err != nil {
			return commandOutputRootBoundary{}, err
		}
		if done {
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return commandOutputRootBoundary{}, err
			}
			if err := rejectLexicalOutputRootSymlinks(next); err != nil {
				return commandOutputRootBoundary{}, err
			}
			return commandOutputRootBoundary{path: next, resolved: resolved}, nil
		}
		current = next
	}
}

func inspectOutputRootPath(current, outputPath string) (string, bool, error) {
	if isRootLevelSystemAlias(current) {
		return current, true, nil
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

func rejectLexicalOutputRootSymlinks(existingRoot string) error {
	ancestors := []string{filepath.Clean(existingRoot)}
	for {
		parent := filepath.Dir(ancestors[len(ancestors)-1])
		if parent == ancestors[len(ancestors)-1] {
			break
		}
		ancestors = append(ancestors, parent)
	}

	for i := len(ancestors) - 1; i >= 0; i-- {
		if err := rejectLexicalOutputRootPath(ancestors[i]); err != nil {
			return err
		}
	}
	return nil
}

func rejectLexicalOutputRootPath(path string) error {
	if isRootLevelSystemAlias(path) {
		return nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output root contains symlink: %s", path)
	}
	return nil
}

func trustedCommandOutputRoot(outputAbs string) (string, error) {
	root, err := trustedCommandOutputRootBoundary(outputAbs)
	return root.path, err
}

func trustedCommandOutputRootBoundary(outputAbs string) (commandOutputRootBoundary, error) {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return commandOutputRootBoundary{}, fmt.Errorf("resolve output workspace: %w", err)
	}
	withinWorkspace, err := pathWithinRoot(workspaceRoot, outputAbs)
	if err != nil {
		return commandOutputRootBoundary{}, err
	}
	resolvedWorkspaceRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return commandOutputRootBoundary{}, fmt.Errorf("resolve output workspace symlinks: %w", err)
	}
	if withinWorkspace {
		return commandOutputRootBoundary{path: workspaceRoot, resolved: resolvedWorkspaceRoot}, nil
	}
	return resolveAliasedWorkspaceRootBoundary(outputAbs, resolvedWorkspaceRoot)
}

func trustedCommandOutputRootForRoots(outputAbs string, roots ...string) (string, error) {
	root, err := trustedCommandOutputRootBoundaryForRoots(outputAbs, roots...)
	return root.path, err
}

func trustedCommandOutputRootBoundaryForRoots(outputAbs string, roots ...string) (commandOutputRootBoundary, error) {
	for _, root := range roots {
		trustedRoot, err := trustedCommandOutputRootBoundaryForRoot(outputAbs, root)
		if err != nil {
			return commandOutputRootBoundary{}, err
		}
		if trustedRoot.path != "" {
			return trustedRoot, nil
		}
	}
	return commandOutputRootBoundary{}, nil
}

func trustedCommandOutputRootForRoot(outputAbs, root string) (string, error) {
	trustedRoot, err := trustedCommandOutputRootBoundaryForRoot(outputAbs, root)
	return trustedRoot.path, err
}

func trustedCommandOutputRootBoundaryForRoot(outputAbs, root string) (commandOutputRootBoundary, error) {
	trimmedRoot := strings.TrimSpace(root)
	if trimmedRoot == "" {
		return commandOutputRootBoundary{}, nil
	}
	rootAbs, err := filepath.Abs(trimmedRoot)
	if err != nil {
		if filepath.IsAbs(outputAbs) && !filepath.IsAbs(trimmedRoot) {
			return commandOutputRootBoundary{}, nil
		}
		return commandOutputRootBoundary{}, fmt.Errorf("resolve trusted output workspace: %w", err)
	}
	withinWorkspace, err := pathWithinRoot(rootAbs, outputAbs)
	if err != nil {
		return commandOutputRootBoundary{}, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if withinWorkspace {
			return commandOutputRootBoundary{}, fmt.Errorf("resolve trusted output workspace: %w", err)
		}
		return commandOutputRootBoundary{}, nil
	}
	if withinWorkspace {
		if err := validateTrustedCommandOutputRootBoundary(rootAbs, resolvedRoot); err != nil {
			return commandOutputRootBoundary{}, err
		}
		return commandOutputRootBoundary{path: rootAbs, resolved: resolvedRoot}, nil
	}

	aliasedRoot, err := resolveAliasedWorkspaceRootBoundary(outputAbs, resolvedRoot)
	if err != nil || aliasedRoot.path == "" {
		return aliasedRoot, err
	}
	if err := validateTrustedCommandOutputRootBoundary(rootAbs, resolvedRoot); err != nil {
		return commandOutputRootBoundary{}, err
	}
	return aliasedRoot, nil
}

func validateTrustedCommandOutputRootBoundary(rootAbs, resolvedRoot string) error {
	if err := validateTrustedCommandOutputRoot(resolvedRoot); err != nil {
		return err
	}
	info, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("resolve trusted output workspace: %w", err)
	}
	lexicalRoot := rootAbs
	if info.Mode()&os.ModeSymlink != 0 {
		lexicalRoot = filepath.Dir(rootAbs)
	}
	return validateTrustedCommandOutputRoot(lexicalRoot)
}

func validateTrustedCommandOutputRoot(rootAbs string) error {
	info, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("resolve trusted output workspace: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trusted output workspace is a symlink: %s", rootAbs)
	}
	if !info.IsDir() {
		return fmt.Errorf("trusted output workspace is not a directory: %s", rootAbs)
	}
	if err := rejectLexicalOutputRootSymlinks(rootAbs); err != nil {
		return fmt.Errorf("validate trusted output workspace: %w", err)
	}
	return nil
}

func resolveAliasedWorkspaceRoot(outputAbs, workspaceRoot string) (string, error) {
	root, err := resolveAliasedWorkspaceRootBoundary(outputAbs, workspaceRoot)
	return root.path, err
}

func resolveAliasedWorkspaceRootBoundary(outputAbs, workspaceRoot string) (commandOutputRootBoundary, error) {
	current := filepath.Dir(filepath.Clean(outputAbs))
	var aliasRoot commandOutputRootBoundary
	for {
		_, err := os.Lstat(current)
		switch {
		case err == nil:
			resolvedCurrent, err := filepath.EvalSymlinks(current)
			if err != nil {
				return commandOutputRootBoundary{}, err
			}
			withinWorkspace, err := pathWithinRoot(workspaceRoot, resolvedCurrent)
			if err != nil {
				return commandOutputRootBoundary{}, err
			}
			if withinWorkspace {
				aliasRoot = commandOutputRootBoundary{path: current, resolved: resolvedCurrent}
			}
		case !os.IsNotExist(err):
			return commandOutputRootBoundary{}, err
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

func isRootLevelSystemAlias(path string) bool {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return false
	}
	parent := filepath.Dir(cleanPath)
	if parent == cleanPath || filepath.Dir(parent) != parent {
		return false
	}
	linkInfo, err := os.Lstat(cleanPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		return false
	}
	targetInfo, err := os.Stat(cleanPath)
	return err == nil && targetInfo.IsDir()
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

func hasDirectoryStyleOutputPath(path string) bool {
	if hasTrailingOutputPathSeparator(path) {
		return true
	}
	last := 0
	for i := len(path) - 1; i >= 0; i-- {
		if os.IsPathSeparator(path[i]) {
			last = i + 1
			break
		}
	}
	base := path[last:]
	return base == "." || base == ".."
}

func rejectAmbiguousParentTraversal(path string) error {
	seenPathComponent := false
	componentStart := 0
	for i := 0; i <= len(path); i++ {
		if i < len(path) && !os.IsPathSeparator(path[i]) {
			continue
		}
		component := path[componentStart:i]
		componentStart = i + 1
		switch component {
		case "", ".":
		case "..":
			if seenPathComponent {
				return fmt.Errorf("output path contains parent traversal after path component: %s", path)
			}
		default:
			seenPathComponent = true
		}
	}
	return nil
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
