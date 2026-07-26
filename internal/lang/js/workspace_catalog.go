package js

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
)

const (
	jsPnpmWorkspaceFile             = "pnpm-workspace.yaml"
	jsYarnRCFile                    = ".yarnrc.yml"
	jsWorkspaceManifestReadMaxBytes = jsPackageJSONReadMaxBytes
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

type workspaceCatalogInputs struct {
	rootManifest      packageJSON
	rootManifestFound bool
	pnpmManifest      pnpmWorkspaceManifest
	pnpmFound         bool
	yarnManifest      yarnCatalogManifest
	yarnFound         bool
	patterns          []string
	warnings          []string
}

var openWorkspaceSearchRoot = openConstrainedRoot

func loadWorkspaceDependencyCatalog(ctx context.Context, repoPath string) (workspaceDependencyCatalog, error) {
	catalog := workspaceDependencyCatalog{
		declarations: make(map[string]workspaceDependencyDeclaration),
		warnings:     make([]string, 0),
	}
	if err := ctx.Err(); err != nil {
		return catalog, err
	}
	if strings.TrimSpace(repoPath) == "" {
		return catalog, nil
	}

	inputs := readWorkspaceCatalogInputs(repoPath)
	catalog.warnings = append(catalog.warnings, inputs.warnings...)
	if !inputs.hasSignals() {
		catalog.warnings = dedupeWorkspaceWarnings(catalog.warnings)
		return catalog, nil
	}

	inputs.addDeclarations(&catalog, repoPath)

	if len(inputs.patterns) == 0 {
		catalog.warnings = dedupeWorkspaceWarnings(catalog.warnings)
		return catalog, nil
	}

	workspacePackageDirs, discoveryWarnings, err := discoverWorkspacePackageDirs(ctx, repoPath, inputs.patterns)
	catalog.warnings = append(catalog.warnings, discoveryWarnings...)
	if err != nil {
		return catalog, err
	}
	manifestWarnings, err := addWorkspacePackageManifests(ctx, &catalog, repoPath, workspacePackageDirs)
	catalog.warnings = append(catalog.warnings, manifestWarnings...)
	if err != nil {
		return catalog, err
	}

	catalog.warnings = dedupeWorkspaceWarnings(catalog.warnings)
	return catalog, nil
}

func readWorkspaceCatalogInputs(repoPath string) workspaceCatalogInputs {
	inputs := workspaceCatalogInputs{}
	var warning string
	inputs.rootManifest, inputs.rootManifestFound, warning = readWorkspacePackageJSON(repoPath, filepath.Join(repoPath, jsPackageFile))
	inputs.warnings = appendWorkspaceWarning(inputs.warnings, warning)
	if inputs.rootManifestFound {
		inputs.patterns = append(inputs.patterns, parseWorkspacePatterns(inputs.rootManifest.Workspaces)...)
	}

	inputs.pnpmManifest, inputs.pnpmFound, warning = readPnpmWorkspaceManifest(repoPath)
	inputs.warnings = appendWorkspaceWarning(inputs.warnings, warning)
	if inputs.pnpmFound {
		inputs.patterns = append(inputs.patterns, inputs.pnpmManifest.Packages...)
	}

	inputs.yarnManifest, inputs.yarnFound, warning = readYarnCatalogManifest(repoPath)
	inputs.warnings = appendWorkspaceWarning(inputs.warnings, warning)
	inputs.patterns = dedupeWorkspacePatterns(inputs.patterns)
	return inputs
}

func appendWorkspaceWarning(warnings []string, warning string) []string {
	if warning == "" {
		return warnings
	}
	return append(warnings, warning)
}

func (i *workspaceCatalogInputs) hasSignals() bool {
	return i.pnpmFound || len(i.patterns) > 0 || i.yarnFound
}

func (i *workspaceCatalogInputs) addDeclarations(catalog *workspaceDependencyCatalog, repoPath string) {
	if i.rootManifestFound {
		addManifestDependencies(catalog, repoPath, i.rootManifest)
	}
	addCatalogEntries(catalog, repoPath, i.pnpmManifest.Catalog, i.pnpmManifest.Catalogs)
	addCatalogEntries(catalog, repoPath, i.yarnManifest.Catalog, i.yarnManifest.Catalogs)
}

func addWorkspacePackageManifests(ctx context.Context, catalog *workspaceDependencyCatalog, repoPath string, workspacePackageDirs []string) ([]string, error) {
	warnings := make([]string, 0)
	for _, dir := range workspacePackageDirs {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		manifestPath := filepath.Join(dir, jsPackageFile)
		pkg, found, warning := readWorkspacePackageJSON(repoPath, manifestPath)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if !found {
			continue
		}
		addManifestDependencies(catalog, dir, pkg)
	}
	return warnings, nil
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

	content, err := safeio.ReadFileUnderLimit(repoPath, manifestPath, jsWorkspaceManifestReadMaxBytes)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return packageJSON{}, false, fmt.Sprintf("skipped workspace manifest %s above %d bytes", workspaceDisplayPath(repoPath, manifestPath), jsWorkspaceManifestReadMaxBytes)
		}
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

	manifest, err := shared.ReadYAMLUnderRepoLimit[pnpmWorkspaceManifest](repoPath, path, jsWorkspaceManifestReadMaxBytes)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return pnpmWorkspaceManifest{}, false, fmt.Sprintf("skipped %s above %d bytes", jsPnpmWorkspaceFile, jsWorkspaceManifestReadMaxBytes)
		}
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

	manifest, err := shared.ReadYAMLUnderRepoLimit[yarnCatalogManifest](repoPath, path, jsWorkspaceManifestReadMaxBytes)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return yarnCatalogManifest{}, false, fmt.Sprintf("skipped %s above %d bytes", jsYarnRCFile, jsWorkspaceManifestReadMaxBytes)
		}
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

func discoverWorkspacePackageDirs(ctx context.Context, repoPath string, workspacePatterns []string) ([]string, []string, error) {
	return discoverWorkspacePackageDirsWithEntryLimit(ctx, repoPath, workspacePatterns, defaultJSWalkEntryBudget)
}

func discoverWorkspacePackageDirsWithEntryLimit(ctx context.Context, repoPath string, workspacePatterns []string, entryLimit int) ([]string, []string, error) {
	compiledPatterns, warnings := compileWorkspacePatterns(workspacePatterns)
	dirs := make(map[string]struct{})
	rootManifestPath := filepath.Join(repoPath, jsPackageFile)
	budget := newJSWalkEntryBudget(entryLimit)
	truncated := false

	for _, searchRoot := range workspacePatternSearchRoots(repoPath, workspacePatterns) {
		if err := ctx.Err(); err != nil {
			return sortedWorkspaceDirs(dirs), dedupeWorkspaceWarnings(warnings), err
		}
		foundDirs, rootWarnings, summary, err := discoverWorkspacePackageDirsInRootWithBudget(ctx, repoPath, rootManifestPath, searchRoot, compiledPatterns, budget)
		warnings = append(warnings, rootWarnings...)
		for dir := range foundDirs {
			dirs[dir] = struct{}{}
		}
		if err != nil {
			return sortedWorkspaceDirs(dirs), dedupeWorkspaceWarnings(warnings), err
		}
		if summary.truncated {
			truncated = true
			break
		}
	}
	if truncated {
		warnings = append(warnings, workspaceWalkBudgetWarning(jsWalkSummary{
			entriesVisited: budget.entriesVisited,
			truncated:      true,
		}))
	}

	return sortedWorkspaceDirs(dirs), dedupeWorkspaceWarnings(warnings), nil
}

func sortedWorkspaceDirs(dirs map[string]struct{}) []string {
	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func discoverWorkspacePackageDirsInRoot(ctx context.Context, repoPath, rootManifestPath, searchRoot string, compiledPatterns []workspacePattern) (map[string]struct{}, []string, error) {
	dirs, warnings, _, err := discoverWorkspacePackageDirsInRootWithBudget(ctx, repoPath, rootManifestPath, searchRoot, compiledPatterns, newJSWalkEntryBudget(defaultJSWalkEntryBudget))
	return dirs, warnings, err
}

func discoverWorkspacePackageDirsInRootWithBudget(ctx context.Context, repoPath, rootManifestPath, searchRoot string, compiledPatterns []workspacePattern, budget *jsWalkEntryBudget) (map[string]struct{}, []string, jsWalkSummary, error) {
	dirs := make(map[string]struct{})
	warnings := make([]string, 0)

	if err := ctx.Err(); err != nil {
		return dirs, warnings, jsWalkSummary{entriesVisited: budget.entriesVisited}, err
	}
	if warning, skip := validateWorkspaceSearchRoot(searchRoot); skip {
		if warning != "" {
			warnings = append(warnings, warning)
		}
		return dirs, warnings, jsWalkSummary{entriesVisited: budget.entriesVisited}, nil
	}
	if shared.ShouldSkipDir(filepath.Base(searchRoot), skipDirectories) {
		return dirs, warnings, jsWalkSummary{entriesVisited: budget.entriesVisited}, nil
	}

	summary, walkErr := walkWorkspaceSearchRoot(ctx, repoPath, rootManifestPath, searchRoot, compiledPatterns, dirs, budget)
	if walkErr != nil {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return dirs, warnings, summary, walkErr
		}
		warnings = append(warnings, fmt.Sprintf("unable to scan workspace package manifests under %q: %v", searchRoot, walkErr))
	}
	return dirs, warnings, summary, nil
}

func walkWorkspaceSearchRoot(ctx context.Context, repoPath, rootManifestPath, searchRoot string, compiledPatterns []workspacePattern, dirs map[string]struct{}, budget *jsWalkEntryBudget) (summary jsWalkSummary, err error) {
	root, err := openWorkspaceSearchRoot(searchRoot)
	if err != nil {
		return summary, err
	}
	defer closeReadCloserPreserveErr(root, &err)

	return walkRootNoFollowContext(ctx, root, budget, workspaceRootWalkFunc(repoPath, rootManifestPath, searchRoot, compiledPatterns, dirs), nil)
}

func workspaceRootWalkFunc(repoPath, rootManifestPath, searchRoot string, compiledPatterns []workspacePattern, dirs map[string]struct{}) rootWalkFunc {
	walker := workspacePackageDirWalker(repoPath, rootManifestPath, compiledPatterns, dirs)
	return func(relPath string, info fs.FileInfo) (bool, bool, error) {
		path := filepath.Join(searchRoot, relPath)
		walkErr := walker(path, fs.FileInfoToDirEntry(info), nil)
		if errors.Is(walkErr, fs.SkipDir) {
			return true, false, nil
		}
		return false, false, walkErr
	}
}

func workspaceWalkBudgetWarning(summary jsWalkSummary) string {
	return fmt.Sprintf("stopped workspace package manifest scan after %d entries; results are partial", summary.entriesVisited)
}

func validateWorkspaceSearchRoot(searchRoot string) (string, bool) {
	info, err := os.Stat(searchRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true
		}
		return fmt.Sprintf("unable to access workspace search root %q: %v", searchRoot, err), true
	}
	if !info.IsDir() {
		return "", true
	}
	return "", false
}

func workspacePackageDirWalker(repoPath, rootManifestPath string, compiledPatterns []workspacePattern, dirs map[string]struct{}) fs.WalkDirFunc {
	return func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipWorkspaceWalkDir(entry) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if !isWorkspacePackageManifestEntry(path, entry, rootManifestPath) {
			return nil
		}
		dir := filepath.Dir(path)
		if !workspacePackageDirMatches(repoPath, dir, compiledPatterns) {
			return nil
		}
		dirs[dir] = struct{}{}
		return nil
	}
}

func shouldSkipWorkspaceWalkDir(entry fs.DirEntry) bool {
	return entry.IsDir() && shared.ShouldSkipDir(entry.Name(), skipDirectories)
}

func isWorkspacePackageManifestEntry(path string, entry fs.DirEntry, rootManifestPath string) bool {
	if entry.Name() != jsPackageFile {
		return false
	}
	return filepath.Clean(path) != filepath.Clean(rootManifestPath)
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
