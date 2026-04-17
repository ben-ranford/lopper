package python

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func collectManifestDependencies(repoPath, dir string, files map[string]struct{}) (map[string]struct{}, []string, error) {
	dependencies := make(map[string]struct{})
	warnings := make([]string, 0)

	for _, source := range []struct {
		name   string
		parser dependencyParser
	}{
		{name: pythonPyprojectFile, parser: parsePyprojectDependencies},
		{name: pythonPipfileName, parser: parsePipfileDependencies},
		{name: pythonRequirementsTxt, parser: parseRequirementsDependencies},
	} {
		if !hasFile(files, source.name) {
			continue
		}
		if err := appendParsedDependencies(repoPath, filepath.Join(dir, source.name), source.parser, dependencies, &warnings); err != nil {
			return nil, nil, err
		}
	}

	return dependencies, warnings, nil
}

func appendParsedDependencies(repoPath, path string, parser dependencyParser, destination map[string]struct{}, warnings *[]string) error {
	dependencies, parsedWarnings, err := parser(repoPath, path)
	if err != nil {
		return err
	}
	addDependencySet(destination, dependencies)
	*warnings = append(*warnings, parsedWarnings...)
	return nil
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

func parseRequirementsDependencies(repoPath, path string) (map[string]struct{}, []string, error) {
	content, err := readOptionalFileContent(repoPath, path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return make(map[string]struct{}), nil, nil
	default:
		return nil, nil, fmt.Errorf("read %s: %w", relativePackagingPath(repoPath, path), err)
	}

	dependencies := make(map[string]struct{})
	warnings := make([]string, 0)
	skipped := 0
	pathLabel := relativePackagingPath(repoPath, path)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), len(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "-"):
			skipped++
			continue
		}
		if dependency := dependencyNameFromRequirement(line); dependency != "" {
			dependencies[dependency] = struct{}{}
			continue
		}
		skipped++
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan %s: %w", pathLabel, err)
	}
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf("%s: skipped %d requirements entries with unsupported format", pathLabel, skipped))
	}
	return dependencies, warnings, nil
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
