package python

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func collectLockFallbacks(repoPath, dir string, files map[string]struct{}) ([]lockFallback, []string, error) {
	fallbacks := make([]lockFallback, 0, 3)
	warnings := make([]string, 0)

	for _, source := range []struct {
		name   string
		parser dependencyParser
	}{
		{name: pythonPoetryLockName, parser: parsePackageLockDependencies},
		{name: pythonPipfileLockName, parser: parsePipfileLockDependencies},
		{name: pythonUVLockName, parser: parsePackageLockDependencies},
	} {
		if !hasFile(files, source.name) {
			continue
		}
		lockDependencies, lockWarnings, err := source.parser(repoPath, filepath.Join(dir, source.name))
		if err != nil {
			return nil, nil, err
		}
		fallbacks = append(fallbacks, lockFallback{name: source.name, dependencies: lockDependencies})
		warnings = append(warnings, lockWarnings...)
	}

	return fallbacks, warnings, nil
}

func parsePackageLockDependencies(repoPath, path string) (map[string]struct{}, []string, error) {
	document, warnings, err := readOptionalTOMLDocument(repoPath, path)
	if err != nil || document == nil {
		return make(map[string]struct{}), warnings, err
	}

	dependencies := make(map[string]struct{})
	pathLabel := relativePackagingPath(repoPath, path)
	packages, ok := document["package"]
	if !ok {
		return dependencies, warnings, nil
	}

	entries, ok := packages.([]any)
	if !ok {
		warnings = append(warnings, fmt.Sprintf("%s: skipped package entries with unsupported lockfile shape", pathLabel))
		return dependencies, warnings, nil
	}

	skipped := 0
	for _, entry := range entries {
		packageTable, ok := entry.(map[string]any)
		if !ok {
			skipped++
			continue
		}
		name, ok := packageTable["name"].(string)
		if !ok {
			skipped++
			continue
		}
		if dependency := normalizeDependencyID(name); dependency != "" {
			dependencies[dependency] = struct{}{}
			continue
		}
		skipped++
	}
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("%s: skipped %d lockfile package entries with unsupported metadata", pathLabel, skipped))
	}

	return dependencies, warnings, nil
}

func parsePipfileLockDependencies(repoPath, path string) (map[string]struct{}, []string, error) {
	content, err := readOptionalJSONContent(repoPath, path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return make(map[string]struct{}), nil, nil
	default:
		return nil, nil, fmt.Errorf("read %s: %w", relativePackagingPath(repoPath, path), err)
	}

	document := make(map[string]any)
	if err := json.Unmarshal(content, &document); err != nil {
		return make(map[string]struct{}), []string{fmt.Sprintf("%s: skipped %s parsing after JSON decode error: %v", relativePackagingPath(repoPath, path), pythonPipfileLockName, err)}, nil
	}

	dependencies := make(map[string]struct{})
	for _, section := range []string{"default", "develop"} {
		addDependencyKeys(dependencies, nestedMap(document, section), relativePackagingPath(repoPath, path)+" ["+section+"]")
	}

	return dependencies, nil, nil
}
