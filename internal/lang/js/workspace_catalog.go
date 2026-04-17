package js

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	jsPnpmWorkspaceFile = "pnpm-workspace.yaml"
	jsYarnRCFile        = ".yarnrc.yml"
)

type workspaceDependencyCatalog struct {
	declarations map[string]workspaceDependencyDeclaration
	warnings     []string
}

type workspaceDependencyDeclaration struct {
	declarationDirs map[string]struct{}
}

type pnpmWorkspaceManifest struct {
	Packages []string                  `yaml:"packages"`
	Catalog  map[string]any            `yaml:"catalog"`
	Catalogs map[string]map[string]any `yaml:"catalogs"`
}

type yarnCatalogManifest struct {
	Catalog  map[string]any            `yaml:"catalog"`
	Catalogs map[string]map[string]any `yaml:"catalogs"`
}

type workspacePattern struct {
	exclude bool
	regex   *regexp.Regexp
}

func loadWorkspaceDependencyCatalog(repoPath string) workspaceDependencyCatalog {
	catalog := workspaceDependencyCatalog{
		declarations: make(map[string]workspaceDependencyDeclaration),
		warnings:     make([]string, 0),
	}
	if strings.TrimSpace(repoPath) == "" {
		return catalog
	}

	rootManifest, rootManifestFound, rootManifestWarning := readWorkspacePackageJSON(repoPath, filepath.Join(repoPath, jsPackageFile))
	if rootManifestWarning != "" {
		catalog.warnings = append(catalog.warnings, rootManifestWarning)
	}

	workspacePatterns := make([]string, 0)
	if rootManifestFound {
		workspacePatterns = append(workspacePatterns, parseWorkspacePatterns(rootManifest.Workspaces)...)
	}

	pnpmManifest, pnpmFound, pnpmWarning := readPnpmWorkspaceManifest(repoPath)
	if pnpmWarning != "" {
		catalog.warnings = append(catalog.warnings, pnpmWarning)
	}
	if pnpmFound {
		workspacePatterns = append(workspacePatterns, pnpmManifest.Packages...)
	}

	yarnManifest, yarnFound, yarnWarning := readYarnCatalogManifest(repoPath)
	if yarnWarning != "" {
		catalog.warnings = append(catalog.warnings, yarnWarning)
	}

	workspacePatterns = dedupeWorkspacePatterns(workspacePatterns)
	hasWorkspaceSignals := pnpmFound || len(workspacePatterns) > 0 || yarnFound
	if !hasWorkspaceSignals {
		catalog.warnings = dedupeWorkspaceWarnings(catalog.warnings)
		return catalog
	}

	if rootManifestFound {
		addManifestDependencies(&catalog, repoPath, rootManifest)
	}
	addCatalogEntries(&catalog, repoPath, pnpmManifest.Catalog, pnpmManifest.Catalogs)
	addCatalogEntries(&catalog, repoPath, yarnManifest.Catalog, yarnManifest.Catalogs)

	if len(workspacePatterns) == 0 {
		catalog.warnings = dedupeWorkspaceWarnings(catalog.warnings)
		return catalog
	}

	workspacePackageDirs, discoveryWarnings := discoverWorkspacePackageDirs(repoPath, workspacePatterns)
	catalog.warnings = append(catalog.warnings, discoveryWarnings...)
	for _, dir := range workspacePackageDirs {
		manifestPath := filepath.Join(dir, jsPackageFile)
		pkg, found, warning := readWorkspacePackageJSON(repoPath, manifestPath)
		if warning != "" {
			catalog.warnings = append(catalog.warnings, warning)
		}
		if !found {
			continue
		}
		addManifestDependencies(&catalog, dir, pkg)
	}

	catalog.warnings = dedupeWorkspaceWarnings(catalog.warnings)
	return catalog
}

func readWorkspacePackageJSON(repoPath, manifestPath string) (packageJSON, bool, string) {
	if strings.TrimSpace(manifestPath) == "" {
		return packageJSON{}, false, ""
	}

	if info, err := os.Stat(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return packageJSON{}, false, ""
		}
		return packageJSON{}, false, fmt.Sprintf("unable to read workspace manifest %s: %v", workspaceDisplayPath(repoPath, manifestPath), err)
	} else if info.IsDir() {
		return packageJSON{}, false, fmt.Sprintf("workspace manifest path is a directory: %s", workspaceDisplayPath(repoPath, manifestPath))
	}

	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return packageJSON{}, false, fmt.Sprintf("unable to read workspace manifest %s: %v", workspaceDisplayPath(repoPath, manifestPath), err)
	}

	var pkg packageJSON
	if err := json.Unmarshal(content, &pkg); err != nil {
		return packageJSON{}, false, fmt.Sprintf("failed to parse workspace manifest %s: %v", workspaceDisplayPath(repoPath, manifestPath), err)
	}
	return pkg, true, ""
}

func readPnpmWorkspaceManifest(repoPath string) (pnpmWorkspaceManifest, bool, string) {
	path := filepath.Join(repoPath, jsPnpmWorkspaceFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return pnpmWorkspaceManifest{}, false, ""
		}
		return pnpmWorkspaceManifest{}, false, fmt.Sprintf("unable to read %s: %v", jsPnpmWorkspaceFile, err)
	}

	manifest, err := shared.ReadYAMLUnderRepo[pnpmWorkspaceManifest](repoPath, path)
	if err != nil {
		return pnpmWorkspaceManifest{}, false, fmt.Sprintf("failed to parse %s: %v", jsPnpmWorkspaceFile, err)
	}
	return manifest, true, ""
}

func readYarnCatalogManifest(repoPath string) (yarnCatalogManifest, bool, string) {
	path := filepath.Join(repoPath, jsYarnRCFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return yarnCatalogManifest{}, false, ""
		}
		return yarnCatalogManifest{}, false, fmt.Sprintf("unable to read %s: %v", jsYarnRCFile, err)
	}

	manifest, err := shared.ReadYAMLUnderRepo[yarnCatalogManifest](repoPath, path)
	if err != nil {
		return yarnCatalogManifest{}, false, fmt.Sprintf("failed to parse %s: %v", jsYarnRCFile, err)
	}
	if len(manifest.Catalog) == 0 && len(manifest.Catalogs) == 0 {
		return manifest, false, ""
	}
	return manifest, true, ""
}

func parseWorkspacePatterns(value any) []string {
	patterns := make([]string, 0)
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			pattern, ok := item.(string)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(pattern)
			if trimmed != "" {
				patterns = append(patterns, trimmed)
			}
		}
	case map[string]any:
		patterns = append(patterns, parseWorkspacePatterns(typed["packages"])...)
	}
	return dedupeWorkspacePatterns(patterns)
}

func addManifestDependencies(catalog *workspaceDependencyCatalog, declarationDir string, pkg packageJSON) {
	for _, dependencies := range []map[string]string{
		pkg.Dependencies,
		pkg.DevDependencies,
		pkg.PeerDependencies,
		pkg.OptionalDependencies,
	} {
		for name := range dependencies {
			catalog.addDependency(name, declarationDir)
		}
	}
}

func addCatalogEntries(catalog *workspaceDependencyCatalog, declarationDir string, defaults map[string]any, named map[string]map[string]any) {
	for name := range defaults {
		catalog.addDependency(name, declarationDir)
	}
	for _, entries := range named {
		for name := range entries {
			catalog.addDependency(name, declarationDir)
		}
	}
}

func (c *workspaceDependencyCatalog) addDependency(dep, declarationDir string) {
	name := strings.TrimSpace(dep)
	if !isSafeDependencyName(name) {
		return
	}

	entry := c.declarations[name]
	if entry.declarationDirs == nil {
		entry.declarationDirs = make(map[string]struct{})
	}
	if strings.TrimSpace(declarationDir) != "" {
		entry.declarationDirs[declarationDir] = struct{}{}
	}
	c.declarations[name] = entry
}

func discoverWorkspacePackageDirs(repoPath string, workspacePatterns []string) ([]string, []string) {
	compiledPatterns, warnings := compileWorkspacePatterns(workspacePatterns)
	dirs := make(map[string]struct{})
	rootManifestPath := filepath.Join(repoPath, jsPackageFile)

	for _, searchRoot := range workspacePatternSearchRoots(repoPath, workspacePatterns) {
		foundDirs, rootWarnings := discoverWorkspacePackageDirsInRoot(repoPath, rootManifestPath, searchRoot, compiledPatterns)
		warnings = append(warnings, rootWarnings...)
		for dir := range foundDirs {
			dirs[dir] = struct{}{}
		}
	}

	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out, dedupeWorkspaceWarnings(warnings)
}

func discoverWorkspacePackageDirsInRoot(repoPath, rootManifestPath, searchRoot string, compiledPatterns []workspacePattern) (map[string]struct{}, []string) {
	dirs := make(map[string]struct{})
	warnings := make([]string, 0)

	info, err := os.Stat(searchRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("unable to access workspace search root %q: %v", searchRoot, err))
		}
		return dirs, warnings
	}
	if !info.IsDir() {
		return dirs, warnings
	}

	walkErr := filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shared.ShouldSkipDir(entry.Name(), skipDirectories) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != jsPackageFile {
			return nil
		}
		if filepath.Clean(path) == filepath.Clean(rootManifestPath) {
			return nil
		}
		dir := filepath.Dir(path)
		if !workspacePackageDirMatches(repoPath, dir, compiledPatterns) {
			return nil
		}
		dirs[dir] = struct{}{}
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan workspace package manifests under %q: %v", searchRoot, walkErr))
	}
	return dirs, warnings
}

func workspacePackageDirMatches(repoPath, dir string, patterns []workspacePattern) bool {
	relDir, ok := workspaceRelativeDir(repoPath, dir)
	if !ok {
		return false
	}
	return matchesWorkspacePatterns(relDir, patterns)
}

func workspacePatternSearchRoots(repoPath string, workspacePatterns []string) []string {
	repoRoot := filepath.Clean(repoPath)
	roots := make(map[string]struct{})
	hasPositivePattern := false

	for _, rawPattern := range workspacePatterns {
		pattern, exclude := normalizeWorkspacePattern(rawPattern)
		if pattern == "" || exclude {
			continue
		}
		hasPositivePattern = true

		searchRoot := repoRoot
		literalRoot := workspacePatternLiteralRoot(pattern)
		if literalRoot != "" {
			candidateRoot := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(literalRoot)))
			if isPathWithin(candidateRoot, repoRoot) {
				searchRoot = candidateRoot
			}
		}
		roots[searchRoot] = struct{}{}
	}

	if !hasPositivePattern || len(roots) == 0 {
		return []string{repoRoot}
	}

	out := make([]string, 0, len(roots))
	for root := range roots {
		out = append(out, root)
	}
	sort.Strings(out)
	return collapseWorkspaceSearchRoots(out)
}

func workspacePatternLiteralRoot(pattern string) string {
	normalized := strings.TrimSpace(pattern)
	for strings.HasPrefix(normalized, "./") {
		normalized = strings.TrimPrefix(normalized, "./")
	}
	normalized = strings.TrimPrefix(filepath.ToSlash(normalized), "/")

	parts := strings.Split(normalized, "/")
	literalParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, "*?[{") {
			break
		}
		literalParts = append(literalParts, part)
	}
	if len(literalParts) == 0 {
		return ""
	}
	return filepath.Join(literalParts...)
}

func collapseWorkspaceSearchRoots(roots []string) []string {
	collapsed := make([]string, 0, len(roots))
	for _, root := range roots {
		nested := false
		for _, existing := range collapsed {
			if root == existing || isPathWithin(root, existing) {
				nested = true
				break
			}
		}
		if !nested {
			collapsed = append(collapsed, root)
		}
	}
	return collapsed
}

func workspaceRelativeDir(repoPath, dir string) (string, bool) {
	rel, err := filepath.Rel(repoPath, dir)
	if err != nil {
		return "", false
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func compileWorkspacePatterns(patterns []string) ([]workspacePattern, []string) {
	compiled := make([]workspacePattern, 0, len(patterns))
	warnings := make([]string, 0)

	for _, raw := range patterns {
		normalized, exclude := normalizeWorkspacePattern(raw)
		if normalized == "" {
			continue
		}
		re, err := compileWorkspacePatternRegex(normalized)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("unable to parse workspace pattern %q: %v", raw, err))
			continue
		}
		compiled = append(compiled, workspacePattern{
			exclude: exclude,
			regex:   re,
		})
	}

	return compiled, dedupeWorkspaceWarnings(warnings)
}

func normalizeWorkspacePattern(pattern string) (string, bool) {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return "", false
	}
	exclude := false
	if strings.HasPrefix(trimmed, "!") {
		exclude = true
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
	}
	trimmed = filepath.ToSlash(trimmed)
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.TrimSuffix(trimmed, "/")
	return strings.TrimSpace(trimmed), exclude
}

func compileWorkspacePatternRegex(pattern string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				builder.WriteString(".*")
				i++
				continue
			}
			builder.WriteString(`[^/]*`)
		case '?':
			builder.WriteString(`[^/]`)
		case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			builder.WriteByte('\\')
			builder.WriteByte(ch)
		default:
			builder.WriteByte(ch)
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

func matchesWorkspacePatterns(relDir string, patterns []workspacePattern) bool {
	if len(patterns) == 0 {
		return true
	}

	matched := workspacePatternDefaultMatch(patterns)
	for _, pattern := range patterns {
		if pattern.regex.MatchString(relDir) {
			matched = !pattern.exclude
		}
	}
	return matched
}

func workspacePatternDefaultMatch(patterns []workspacePattern) bool {
	for _, pattern := range patterns {
		if !pattern.exclude {
			return false
		}
	}
	return true
}

func workspaceDisplayPath(repoPath, targetPath string) string {
	rel, err := filepath.Rel(repoPath, targetPath)
	if err != nil {
		return filepath.Base(targetPath)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return filepath.Base(targetPath)
	}
	return clean
}

func dedupeWorkspaceWarnings(warnings []string) []string {
	deduped := shared.UniqueTrimmedStrings(warnings)
	sort.Strings(deduped)
	return deduped
}

func dedupeWorkspacePatterns(patterns []string) []string {
	return shared.UniqueTrimmedStrings(patterns)
}
