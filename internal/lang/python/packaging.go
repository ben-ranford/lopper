package python

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

const (
	pythonPyprojectFile   = "pyproject.toml"
	pythonPipfileName     = "Pipfile"
	pythonPipfileLockName = "Pipfile.lock"
	pythonPoetryLockName  = "poetry.lock"
	pythonUVLockName      = "uv.lock"
)

var pythonRequirementNamePattern = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

type lockFallback struct {
	name         string
	dependencies map[string]struct{}
}

type dependencyParser func(repoPath, path string) (map[string]struct{}, []string, error)

type packagingDiscoveryCoordinator struct {
	repoPath     string
	dependencies map[string]struct{}
	warnings     []string
}

func collectDeclaredDependencies(ctx context.Context, repoPath string) (map[string]struct{}, []string, error) {
	coordinator := packagingDiscoveryCoordinator{
		repoPath:     repoPath,
		dependencies: make(map[string]struct{}),
		warnings:     make([]string, 0),
	}
	if err := coordinator.collect(ctx); err != nil {
		return nil, nil, err
	}
	return coordinator.dependencies, coordinator.warnings, nil
}

func (c *packagingDiscoveryCoordinator) collect(ctx context.Context) error {
	return filepath.WalkDir(c.repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return c.walkEntry(path, entry)
	})
}

func (c *packagingDiscoveryCoordinator) walkEntry(path string, entry fs.DirEntry) error {
	if !entry.IsDir() {
		return nil
	}
	if path != c.repoPath && shouldSkipDir(entry.Name()) {
		return filepath.SkipDir
	}

	dirDependencies, dirWarnings, err := collectDirectoryDeclaredDependencies(c.repoPath, path)
	if err != nil {
		return err
	}
	addDependencySet(c.dependencies, dirDependencies)
	c.warnings = append(c.warnings, dirWarnings...)
	return nil
}

func collectDirectoryDeclaredDependencies(repoPath, dir string) (map[string]struct{}, []string, error) {
	files, err := pythonPackagingFiles(dir)
	if err != nil {
		return nil, nil, normalizePackagingStageError("discovery", err)
	}
	if !hasRelevantPythonPackagingFile(files) {
		return nil, nil, nil
	}

	dependencies := make(map[string]struct{})
	warnings := make([]string, 0)

	manifestDependencies, manifestWarnings, err := collectManifestDependencies(repoPath, dir, files)
	if err != nil {
		return nil, nil, normalizePackagingStageError("manifest parsing", err)
	}
	addDependencySet(dependencies, manifestDependencies)
	warnings = append(warnings, manifestWarnings...)
	if len(dependencies) > 0 {
		return dependencies, warnings, nil
	}

	lockFallbacks, lockWarnings, err := collectLockFallbacks(repoPath, dir, files)
	if err != nil {
		return nil, nil, normalizePackagingStageError("lockfile parsing", err)
	}
	warnings = append(warnings, lockWarnings...)
	applyLockfileFallbacks(repoPath, dir, lockFallbacks, dependencies, &warnings)

	return dependencies, warnings, nil
}

func applyLockfileFallbacks(repoPath, dir string, fallbacks []lockFallback, dependencies map[string]struct{}, warnings *[]string) {
	for _, fallback := range fallbacks {
		if len(fallback.dependencies) == 0 {
			continue
		}
		addDependencySet(dependencies, fallback.dependencies)
		*warnings = append(*warnings, fmt.Sprintf("%s: using %s package entries as a fallback because no supported manifest dependency declarations were found", relativePackagingPath(repoPath, dir), fallback.name))
	}
}

func normalizePackagingStageError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("python packaging %s: %w", stage, err)
}

func pythonPackagingFiles(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	files := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		files[entry.Name()] = struct{}{}
	}
	return files, nil
}

func hasRelevantPythonPackagingFile(files map[string]struct{}) bool {
	for _, name := range []string{pythonPyprojectFile, pythonPipfileName, pythonPipfileLockName, pythonPoetryLockName, pythonUVLockName} {
		if hasFile(files, name) {
			return true
		}
	}
	return false
}
