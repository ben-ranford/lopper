package golang

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func workspaceRootModuleDirs(repoPath string, moduleInfo moduleInfo) (map[string]struct{}, error) {
	if moduleInfo.ModulePath != "" {
		return nil, nil
	}

	return goWorkModuleDirs(repoPath)
}

func goWorkModuleDirs(repoPath string) (map[string]struct{}, error) {
	useEntries, err := readGoWorkUseEntries(repoPath)
	if err != nil {
		return nil, err
	}
	if len(useEntries) == 0 {
		return nil, nil
	}

	workspaceRoots := make(map[string]struct{}, len(useEntries))
	for _, rel := range useEntries {
		resolved, ok := resolveRepoBoundedPath(repoPath, rel)
		if !ok {
			continue
		}
		workspaceRoots[resolved] = struct{}{}
	}
	return workspaceRoots, nil
}

func nestedModuleDirs(repoPath string, workspaceModuleDirs map[string]struct{}) (map[string]struct{}, error) {
	dirs := make(map[string]struct{})
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		if path == repoPath {
			return nil
		}
		exists, err := manifestPathExists(filepath.Join(path, goModName))
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if _, ok := workspaceModuleDirs[path]; ok {
			return nil
		}
		dirs[path] = struct{}{}
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

func discoverNestedModules(repoPath string) ([]string, []string, map[string]string, error) {
	nestedDirs, err := nestedModuleDirs(repoPath, nil)
	if err != nil {
		return nil, nil, nil, err
	}

	modules := make([]string, 0, len(nestedDirs))
	dependencies := make([]string, 0)
	replacements := make(map[string]string)
	for dir := range nestedDirs {
		modulePath, deps, moduleReplacements, err := loadGoModFromDir(repoPath, dir)
		if err != nil {
			continue
		}
		if modulePath != "" {
			modules = append(modules, modulePath)
		}
		dependencies = append(dependencies, deps...)
		for replacementImport, dependency := range moduleReplacements {
			if _, ok := replacements[replacementImport]; !ok {
				replacements[replacementImport] = dependency
			}
		}
	}

	return uniqueStrings(modules), uniqueStrings(dependencies), replacements, nil
}

func parseGoMod(content []byte) (string, []string, map[string]string) {
	state := goModParseState{
		depSet:     make(map[string]struct{}),
		replaceSet: make(map[string]string),
	}
	for _, rawLine := range strings.Split(string(content), "\n") {
		processGoModLine(strings.TrimSpace(stripInlineComment(rawLine)), &state)
	}

	dependencies := make([]string, 0, len(state.depSet))
	for dep := range state.depSet {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)
	return state.modulePath, dependencies, state.replaceSet
}

type goModParseState struct {
	modulePath     string
	depSet         map[string]struct{}
	replaceSet     map[string]string
	inRequireBlock bool
	inReplaceBlock bool
}

func processGoModLine(line string, state *goModParseState) {
	if line == "" || state == nil {
		return
	}
	if parseGoModModuleLine(line, state) {
		return
	}
	if parseGoModRequireBlockControl(line, state) {
		return
	}
	if parseGoModReplaceBlockControl(line, state) {
		return
	}
	if state.inReplaceBlock {
		addGoModReplacement(line, state.replaceSet)
		return
	}
	if state.inRequireBlock {
		addGoModDependency(line, state.depSet)
		return
	}
	parseGoModSingleRequire(line, state.depSet)
	parseGoModSingleReplace(line, state.replaceSet)
}

func parseGoModModuleLine(line string, state *goModParseState) bool {
	if !strings.HasPrefix(line, "module ") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		state.modulePath = fields[1]
	}
	return true
}

func parseGoModRequireBlockControl(line string, state *goModParseState) bool {
	return parseGoModBlockControl(line, "require (", &state.inRequireBlock)
}

func parseGoModReplaceBlockControl(line string, state *goModParseState) bool {
	return parseGoModBlockControl(line, "replace (", &state.inReplaceBlock)
}

func parseGoModBlockControl(line string, startToken string, inBlock *bool) bool {
	if inBlock == nil {
		return false
	}
	if strings.HasPrefix(line, startToken) {
		*inBlock = true
		return true
	}
	if *inBlock && line == ")" {
		*inBlock = false
		return true
	}
	return false
}

func parseGoModSingleRequire(line string, depSet map[string]struct{}) {
	parseGoModSingleDirective(line, "require ", func(value string) {
		addGoModDependency(value, depSet)
	})
}

func parseGoModSingleReplace(line string, replaceSet map[string]string) {
	parseGoModSingleDirective(line, "replace ", func(value string) {
		addGoModReplacement(value, replaceSet)
	})
}

func parseGoModSingleDirective(line, prefix string, handler func(string)) {
	if handler == nil || !strings.HasPrefix(line, prefix) {
		return
	}
	handler(strings.TrimPrefix(line, prefix))
}

func addGoModDependency(line string, depSet map[string]struct{}) {
	if depSet == nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	depSet[fields[0]] = struct{}{}
}

func addGoModReplacement(line string, replaceSet map[string]string) {
	if replaceSet == nil {
		return
	}
	parts := strings.SplitN(line, "=>", 2)
	if len(parts) != 2 {
		return
	}
	oldPath := firstToken(parts[0])
	newPath := firstToken(parts[1])
	if oldPath == "" || newPath == "" {
		return
	}
	if isLocalReplaceTarget(newPath) {
		return
	}
	// Track only import-like replacement targets.
	if !looksExternalImport(newPath) {
		return
	}
	replaceSet[newPath] = oldPath
}

func isLocalReplaceTarget(pathValue string) bool {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return false
	}
	if strings.HasPrefix(pathValue, "./") || strings.HasPrefix(pathValue, "../") || strings.HasPrefix(pathValue, "/") {
		return true
	}
	if len(pathValue) >= 2 && pathValue[1] == ':' {
		return true
	}
	return false
}

func firstToken(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func loadGoWorkLocalModules(repoPath string) ([]string, error) {
	useEntries, err := readGoWorkUseEntries(repoPath)
	if err != nil {
		return nil, err
	}
	modulePaths := make([]string, 0)
	for _, rel := range useEntries {
		resolved, ok := resolveRepoBoundedPath(repoPath, rel)
		if !ok {
			continue
		}
		modulePath, _, _, err := loadGoModFromDir(repoPath, resolved)
		if err != nil || modulePath == "" {
			continue
		}
		modulePaths = append(modulePaths, modulePath)
	}
	return uniqueStrings(modulePaths), nil
}

func readGoWorkUseEntries(repoPath string) ([]string, error) {
	workPath := filepath.Join(repoPath, goWorkName)
	exists, err := manifestPathExists(workPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	content, err := safeio.ReadFileUnder(repoPath, workPath)
	if err != nil {
		return nil, err
	}
	return parseGoWorkUseEntries(content), nil
}

func parseGoWorkUseEntries(content []byte) []string {
	entries := make([]string, 0)
	inUseBlock := false
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(stripInlineComment(rawLine))
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "use ("):
			inUseBlock = true
		case inUseBlock && line == ")":
			inUseBlock = false
		case inUseBlock:
			entries = append(entries, normalizeGoWorkPath(line))
		case strings.HasPrefix(line, "use "):
			entries = append(entries, normalizeGoWorkPath(strings.TrimPrefix(line, "use ")))
		}
	}
	return uniqueStrings(entries)
}

func normalizeGoWorkPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"")
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func loadGoModFromDir(repoPath, dir string) (string, []string, map[string]string, error) {
	goModPath := filepath.Join(dir, goModName)
	content, err := safeio.ReadFileUnder(repoPath, goModPath)
	if err != nil {
		return "", nil, nil, err
	}
	modulePath, dependencies, replacements := parseGoMod(content)
	return modulePath, dependencies, replacements, nil
}

func loadVendoredModuleMetadata(repoPath string) (vendoredModuleMetadata, error) {
	metadata := vendoredModuleMetadata{
		ImportToDependency: make(map[string]string),
		Dependencies:       make(map[string]vendoredDependencyMetadata),
	}
	vendorModulesPath := filepath.Join(repoPath, vendorModulesTxtName)
	exists, err := manifestPathExists(vendorModulesPath)
	if err != nil {
		return metadata, err
	}
	if !exists {
		return metadata, nil
	}
	content, err := safeio.ReadFileUnder(repoPath, vendorModulesPath)
	if err != nil {
		return metadata, err
	}
	metadata = parseVendoredModuleMetadata(content)
	metadata.ManifestFound = true
	return metadata, nil
}

func parseVendoredModuleMetadata(content []byte) vendoredModuleMetadata {
	metadata := vendoredModuleMetadata{
		ImportToDependency: make(map[string]string),
		Dependencies:       make(map[string]vendoredDependencyMetadata),
	}

	state := vendoredParseState{currentDependency: ""}
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# "):
			dependency, replacement, ok := parseVendoredModuleHeader(line)
			if !ok {
				state.malformedModuleHeaders++
				state.currentDependency = ""
				continue
			}
			normalizedDependency := normalizeDependencyID(dependency)
			current := metadata.Dependencies[normalizedDependency]
			current.ModulePath = dependency
			if replacement != "" {
				current.Replacement = true
				current.ReplacementTarget = replacement
			}
			metadata.Dependencies[normalizedDependency] = current
			if _, ok := metadata.ImportToDependency[dependency]; !ok {
				metadata.ImportToDependency[dependency] = normalizedDependency
			}
			state.currentDependency = normalizedDependency
		case strings.HasPrefix(line, "## "):
			if state.currentDependency == "" {
				continue
			}
			current := metadata.Dependencies[state.currentDependency]
			applyVendoredMetadataDirective(line, &current)
			metadata.Dependencies[state.currentDependency] = current
		case strings.HasPrefix(line, "#"):
			continue
		default:
			pkg := firstToken(line)
			if pkg == "" {
				continue
			}
			if state.currentDependency == "" {
				state.orphanPackageLines++
				continue
			}
			current := metadata.Dependencies[state.currentDependency]
			if current.ModulePath != "" && !hasImportPathPrefix(pkg, current.ModulePath) {
				state.packagePrefixMismatches++
			}
			current.PackageCount++
			metadata.Dependencies[state.currentDependency] = current
			if _, ok := metadata.ImportToDependency[pkg]; !ok {
				metadata.ImportToDependency[pkg] = state.currentDependency
			}
		}
	}

	appendVendoredMetadataWarnings(&metadata, state)
	return metadata
}

type vendoredParseState struct {
	currentDependency       string
	malformedModuleHeaders  int
	orphanPackageLines      int
	packagePrefixMismatches int
}

func parseVendoredModuleHeader(line string) (string, string, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	if line == "" {
		return "", "", false
	}
	parts := strings.SplitN(line, "=>", 2)
	left := strings.TrimSpace(parts[0])
	fields := strings.Fields(left)
	if len(fields) == 0 {
		return "", "", false
	}
	modulePath := fields[0]
	if !looksExternalImport(modulePath) {
		return "", "", false
	}
	replacement := ""
	if len(parts) == 2 {
		replacement = firstToken(parts[1])
	}
	return modulePath, replacement, true
}

func applyVendoredMetadataDirective(line string, metadata *vendoredDependencyMetadata) {
	if metadata == nil {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "##"))
	if payload == "" {
		return
	}
	for _, item := range strings.Split(payload, ";") {
		trimmed := strings.TrimSpace(item)
		switch {
		case trimmed == "explicit":
			metadata.Explicit = true
		case strings.HasPrefix(trimmed, "go "):
			metadata.GoVersionDirective = strings.TrimSpace(strings.TrimPrefix(trimmed, "go "))
		}
	}
}

func appendVendoredMetadataWarnings(metadata *vendoredModuleMetadata, state vendoredParseState) {
	if metadata == nil {
		return
	}
	if len(metadata.Dependencies) == 0 {
		metadata.Warnings = append(metadata.Warnings, "vendor/modules.txt was found but no module entries were parsed; vendored provenance may be stale")
		return
	}
	if state.malformedModuleHeaders > 0 {
		metadata.Warnings = append(metadata.Warnings, fmt.Sprintf("vendor/modules.txt contained %d malformed module header line(s)", state.malformedModuleHeaders))
	}
	if state.orphanPackageLines > 0 {
		metadata.Warnings = append(metadata.Warnings, fmt.Sprintf("vendor/modules.txt contained %d package line(s) without a preceding module header", state.orphanPackageLines))
	}
	if state.packagePrefixMismatches > 0 {
		metadata.Warnings = append(metadata.Warnings, fmt.Sprintf("vendor/modules.txt contained %d package path(s) that do not match their module header; vendored metadata may be stale", state.packagePrefixMismatches))
	}

	modulesWithoutPackages := 0
	for _, dependency := range metadata.Dependencies {
		if dependency.PackageCount == 0 {
			modulesWithoutPackages++
		}
	}
	if modulesWithoutPackages > 0 {
		metadata.Warnings = append(metadata.Warnings, fmt.Sprintf("vendor/modules.txt listed %d module(s) without package entries", modulesWithoutPackages))
	}
}

func longestVendoredDependency(importPath string, vendoredImportDependencies map[string]string) string {
	if len(vendoredImportDependencies) == 0 {
		return ""
	}
	match := ""
	dependency := ""
	for importPrefix, dep := range vendoredImportDependencies {
		if !hasImportPathPrefix(importPath, importPrefix) {
			continue
		}
		if len(importPrefix) > len(match) {
			match = importPrefix
			dependency = dep
		}
	}
	return dependency
}

func resolveRepoBoundedPath(repoPath, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	resolved := value
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(repoPath, resolved)
	}
	resolved = filepath.Clean(resolved)

	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", false
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", false
	}
	relativeToRepo, err := filepath.Rel(repoAbs, resolvedAbs)
	if err != nil {
		return "", false
	}
	if relativeToRepo == ".." || strings.HasPrefix(relativeToRepo, ".."+string(filepath.Separator)) {
		return "", false
	}
	return resolvedAbs, true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func stripInlineComment(line string) string {
	if index := strings.Index(line, "//"); index >= 0 {
		return line[:index]
	}
	return line
}
