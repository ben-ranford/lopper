package python

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	toml "github.com/pelletier/go-toml/v2"
)

var pythonRequirementNamePattern = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

func collectDeclaredDependencies(ctx context.Context, repoPath string) (map[string]struct{}, []string, error) {
	dependencies := make(map[string]struct{})
	warnings := make([]string, 0)

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() {
			return nil
		}
		if path != repoPath && shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		dirDependencies, dirWarnings, collectErr := collectDirectoryDeclaredDependencies(repoPath, path)
		if collectErr != nil {
			return collectErr
		}
		for dependency := range dirDependencies {
			dependencies[dependency] = struct{}{}
		}
		warnings = append(warnings, dirWarnings...)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	return dependencies, warnings, nil
}

func collectDirectoryDeclaredDependencies(repoPath, dir string) (map[string]struct{}, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	files := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		files[entry.Name()] = struct{}{}
	}

	hasPyproject := hasFile(files, "pyproject.toml")
	hasPipfile := hasFile(files, "Pipfile")
	hasPipfileLock := hasFile(files, "Pipfile.lock")
	hasPoetryLock := hasFile(files, "poetry.lock")
	hasUVLock := hasFile(files, "uv.lock")
	if !hasPyproject && !hasPipfile && !hasPipfileLock && !hasPoetryLock && !hasUVLock {
		return nil, nil, nil
	}

	dependencies := make(map[string]struct{})
	lockFallbacks := make([]lockFallback, 0, 3)
	warnings := make([]string, 0)

	if hasPyproject {
		path := filepath.Join(dir, "pyproject.toml")
		pyprojectDependencies, pyprojectWarnings, parseErr := parsePyprojectDependencies(repoPath, path)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		addDependencySet(dependencies, pyprojectDependencies)
		warnings = append(warnings, pyprojectWarnings...)
	}

	if hasPipfile {
		path := filepath.Join(dir, "Pipfile")
		pipenvDependencies, pipenvWarnings, parseErr := parsePipfileDependencies(repoPath, path)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		addDependencySet(dependencies, pipenvDependencies)
		warnings = append(warnings, pipenvWarnings...)
	}

	if hasPoetryLock {
		path := filepath.Join(dir, "poetry.lock")
		lockDependencies, lockWarnings, parseErr := parsePackageLockDependencies(repoPath, path)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		lockFallbacks = append(lockFallbacks, lockFallback{name: "poetry.lock", dependencies: lockDependencies})
		warnings = append(warnings, lockWarnings...)
	}

	if hasPipfileLock {
		path := filepath.Join(dir, "Pipfile.lock")
		lockDependencies, lockWarnings, parseErr := parsePipfileLockDependencies(repoPath, path)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		lockFallbacks = append(lockFallbacks, lockFallback{name: "Pipfile.lock", dependencies: lockDependencies})
		warnings = append(warnings, lockWarnings...)
	}

	if hasUVLock {
		path := filepath.Join(dir, "uv.lock")
		lockDependencies, lockWarnings, parseErr := parsePackageLockDependencies(repoPath, path)
		if parseErr != nil {
			return nil, nil, parseErr
		}
		lockFallbacks = append(lockFallbacks, lockFallback{name: "uv.lock", dependencies: lockDependencies})
		warnings = append(warnings, lockWarnings...)
	}

	if len(dependencies) > 0 {
		return dependencies, warnings, nil
	}

	for _, fallback := range lockFallbacks {
		if len(fallback.dependencies) == 0 {
			continue
		}
		addDependencySet(dependencies, fallback.dependencies)
		warnings = append(warnings, fmt.Sprintf("%s: using %s package entries as a fallback because no supported manifest dependency declarations were found", relativePackagingPath(repoPath, dir), fallback.name))
	}

	return dependencies, warnings, nil
}

type lockFallback struct {
	name         string
	dependencies map[string]struct{}
}

func parsePyprojectDependencies(repoPath, path string) (map[string]struct{}, []string, error) {
	document, warnings, err := readOptionalTOMLDocument(repoPath, path)
	if err != nil || document == nil {
		return make(map[string]struct{}), warnings, err
	}

	dependencies := make(map[string]struct{})
	pathLabel := relativePackagingPath(repoPath, path)

	projectTable := nestedMap(document, "project")
	if len(projectTable) > 0 {
		addRequirementList(dependencies, projectTable["dependencies"], pathLabel+" [project.dependencies]", &warnings)
		if optionalTable, ok := projectTable["optional-dependencies"].(map[string]any); ok && len(optionalTable) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: skipped optional dependency groups in project.optional-dependencies: %s", pathLabel, sortedMapKeys(optionalTable)))
		}
	}

	toolTable := nestedMap(document, "tool")
	poetryTable := nestedMap(toolTable, "poetry")
	if len(poetryTable) > 0 {
		addPoetryDependencyTable(dependencies, nestedMap(poetryTable, "dependencies"), pathLabel+" [tool.poetry.dependencies]", &warnings)
		addPoetryDependencyTable(dependencies, nestedMap(poetryTable, "dev-dependencies"), pathLabel+" [tool.poetry.dev-dependencies]", &warnings)
		addPoetryGroups(dependencies, nestedMap(poetryTable, "group"), pathLabel, &warnings)
		if extras := nestedMap(poetryTable, "extras"); len(extras) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: skipped Poetry extras in tool.poetry.extras: %s", pathLabel, sortedMapKeys(extras)))
		}
	}

	addDependencyGroups(dependencies, nestedMap(document, "dependency-groups"), pathLabel, &warnings)
	addRequirementList(dependencies, nestedMap(toolTable, "uv")["dev-dependencies"], pathLabel+" [tool.uv.dev-dependencies]", &warnings)

	return dependencies, warnings, nil
}

func parsePipfileDependencies(repoPath, path string) (map[string]struct{}, []string, error) {
	document, warnings, err := readOptionalTOMLDocument(repoPath, path)
	if err != nil || document == nil {
		return make(map[string]struct{}), warnings, err
	}

	dependencies := make(map[string]struct{})
	pathLabel := relativePackagingPath(repoPath, path)
	addDependencyKeys(dependencies, nestedMap(document, "packages"), pathLabel+" [packages]")
	addDependencyKeys(dependencies, nestedMap(document, "dev-packages"), pathLabel+" [dev-packages]")

	return dependencies, warnings, nil
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
	content, err := safeio.ReadFileUnder(repoPath, path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return make(map[string]struct{}), nil, nil
	default:
		return nil, nil, fmt.Errorf("read %s: %w", relativePackagingPath(repoPath, path), err)
	}

	document := make(map[string]any)
	if err := json.Unmarshal(content, &document); err != nil {
		return make(map[string]struct{}), []string{fmt.Sprintf("%s: skipped Pipfile.lock parsing after JSON decode error: %v", relativePackagingPath(repoPath, path), err)}, nil
	}

	dependencies := make(map[string]struct{})
	for _, section := range []string{"default", "develop"} {
		addDependencyKeys(dependencies, nestedMap(document, section), relativePackagingPath(repoPath, path)+" ["+section+"]")
	}

	return dependencies, nil, nil
}

func readOptionalTOMLDocument(repoPath, path string) (map[string]any, []string, error) {
	content, err := safeio.ReadFileUnder(repoPath, path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return nil, nil, nil
	default:
		return nil, nil, fmt.Errorf("read %s: %w", relativePackagingPath(repoPath, path), err)
	}

	document := make(map[string]any)
	if err := toml.Unmarshal(content, &document); err != nil {
		return make(map[string]any), []string{fmt.Sprintf("%s: skipped TOML parsing after decode error: %v", relativePackagingPath(repoPath, path), err)}, nil
	}
	return document, nil, nil
}

func addRequirementList(dependencies map[string]struct{}, value any, source string, warnings *[]string) {
	specs, ok := stringSlice(value)
	if !ok || len(specs) == 0 {
		return
	}

	skipped := 0
	for _, spec := range specs {
		name := dependencyNameFromRequirement(spec)
		if name == "" {
			skipped++
			continue
		}
		dependencies[name] = struct{}{}
	}
	if skipped > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: skipped %d dependency spec(s) with unsupported format", source, skipped))
	}
}

func addPoetryDependencyTable(dependencies map[string]struct{}, table map[string]any, source string, warnings *[]string) {
	if len(table) == 0 {
		return
	}

	optional := make([]string, 0)
	for _, key := range sortedMapKeySlice(table) {
		if normalizeDependencyID(key) == "python" {
			continue
		}
		if poetryDependencyOptional(table[key]) {
			optional = append(optional, key)
			continue
		}
		if dependency := normalizeDependencyID(key); dependency != "" {
			dependencies[dependency] = struct{}{}
		}
	}
	if len(optional) > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: skipped optional Poetry dependency entries: %s", source, strings.Join(optional, ", ")))
	}
}

func addPoetryGroups(dependencies map[string]struct{}, groups map[string]any, pathLabel string, warnings *[]string) {
	if len(groups) == 0 {
		return
	}

	optionalGroups := make([]string, 0)
	skippedGroups := make([]string, 0)
	for _, group := range sortedMapKeySlice(groups) {
		groupTable, ok := groups[group].(map[string]any)
		if !ok {
			skippedGroups = append(skippedGroups, group)
			continue
		}
		if isTrue(groupTable["optional"]) {
			optionalGroups = append(optionalGroups, group)
			continue
		}
		dependencyTable, ok := groupTable["dependencies"].(map[string]any)
		if !ok {
			skippedGroups = append(skippedGroups, group)
			continue
		}
		addPoetryDependencyTable(dependencies, dependencyTable, pathLabel+" [tool.poetry.group."+group+".dependencies]", warnings)
	}
	if len(optionalGroups) > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: skipped optional Poetry groups: %s", pathLabel, strings.Join(optionalGroups, ", ")))
	}
	if len(skippedGroups) > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: skipped Poetry groups with unsupported metadata: %s", pathLabel, strings.Join(skippedGroups, ", ")))
	}
}

func addDependencyGroups(dependencies map[string]struct{}, groups map[string]any, pathLabel string, warnings *[]string) {
	if len(groups) == 0 {
		return
	}

	skippedGroups := make([]string, 0)
	for _, group := range sortedMapKeySlice(groups) {
		specs, ok := stringSlice(groups[group])
		if !ok {
			skippedGroups = append(skippedGroups, group)
			continue
		}
		addRequirementList(dependencies, specs, pathLabel+" [dependency-groups."+group+"]", warnings)
	}
	if len(skippedGroups) > 0 {
		*warnings = append(*warnings, fmt.Sprintf("%s: skipped dependency groups with unsupported metadata: %s", pathLabel, strings.Join(skippedGroups, ", ")))
	}
}

func addDependencyKeys(dependencies map[string]struct{}, table map[string]any, _ string) {
	for _, key := range sortedMapKeySlice(table) {
		if dependency := normalizeDependencyID(key); dependency != "" {
			dependencies[dependency] = struct{}{}
		}
	}
}

func dependencyNameFromRequirement(spec string) string {
	matches := pythonRequirementNamePattern.FindStringSubmatch(spec)
	if len(matches) != 2 {
		return ""
	}
	return normalizeDependencyID(matches[1])
}

func poetryDependencyOptional(value any) bool {
	table, ok := value.(map[string]any)
	if !ok {
		return false
	}
	return isTrue(table["optional"])
}

func stringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		items := make([]string, 0, len(typed))
		for _, entry := range typed {
			item, ok := entry.(string)
			if !ok {
				return nil, false
			}
			items = append(items, item)
		}
		return items, true
	default:
		return nil, false
	}
}

func nestedMap(document map[string]any, keys ...string) map[string]any {
	current := document
	for _, key := range keys {
		if len(current) == 0 {
			return nil
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func isTrue(value any) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}

func hasFile(files map[string]struct{}, name string) bool {
	_, ok := files[name]
	return ok
}

func addDependencySet(destination, source map[string]struct{}) {
	for dependency := range source {
		destination[dependency] = struct{}{}
	}
}

func sortedDependencyUnion(values ...map[string]struct{}) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for dependency := range value {
			set[dependency] = struct{}{}
		}
	}
	return shared.SortedKeys(set)
}

func sortedMapKeys(values map[string]any) string {
	return strings.Join(sortedMapKeySlice(values), ", ")
}

func sortedMapKeySlice(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func relativePackagingPath(repoPath, path string) string {
	relative, err := filepath.Rel(repoPath, path)
	if err != nil {
		return filepath.Base(path)
	}
	if relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}
