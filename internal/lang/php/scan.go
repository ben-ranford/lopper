package php

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type importBinding = shared.ImportRecord

type scanResult struct {
	Files                      []fileScan
	Warnings                   []string
	DeclaredDependencies       map[string]struct{}
	GroupedImportsByDependency map[string]int
	DynamicUsageByDependency   map[string]int
	UsageIncomplete            bool
}

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
	Dynamic bool
}

type scanState struct {
	visited                       int
	unresolvedNamespaces          int
	foundPHP                      bool
	skippedLargeFiles             int
	skippedNestedPackage          int
	useStatementLimitHits         int
	useBindingLimitHits           int
	namespaceDeclarationLimitHits int
	namespaceReferenceLimitHits   int
	namespaceResolutionLimitHits  int
}

type scanCoordinator struct {
	repoPath           string
	excludedPaths      map[string]struct{}
	resolver           composerResolver
	shortOpenTagPolicy phpShortOpenTagPolicy
	result             scanResult
	state              scanState
}

func newScanCoordinatorWithExcludedPaths(repoPath string, composer composerData, excludedPaths map[string]struct{}) scanCoordinator {
	return scanCoordinator{
		repoPath:      repoPath,
		excludedPaths: excludedPaths,
		resolver:      newComposerResolver(composer),
		result: scanResult{
			DeclaredDependencies:       composer.DeclaredDependencies,
			GroupedImportsByDependency: make(map[string]int),
			DynamicUsageByDependency:   make(map[string]int),
			UsageIncomplete:            composer.UsageIncomplete,
		},
		shortOpenTagPolicy: composer.ShortOpenTagPolicy,
	}
}

func scanRepo(ctx context.Context, repoPath string, composer composerData) (scanResult, error) {
	return scanRepoWithExcludedPaths(ctx, repoPath, composer, nil)
}

func scanRepoWithExcludedPaths(ctx context.Context, repoPath string, composer composerData, excludedPaths map[string]struct{}) (scanResult, error) {
	coordinator := newScanCoordinatorWithExcludedPaths(repoPath, composer, excludedPaths)
	return coordinator.scan(ctx)
}

func (c *scanCoordinator) scan(ctx context.Context) (scanResult, error) {
	err := filepath.WalkDir(c.repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		return c.scanEntry(path, entry)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return c.result, err
	}

	appendScanWarnings(&c.result, c.state)
	return c.result, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (c *scanCoordinator) scanEntry(path string, entry fs.DirEntry) error {
	if isExcludedPath(c.excludedPaths, path) {
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if entry.IsDir() {
		return scanDirEntryWithExcludedPaths(c.repoPath, path, entry, &c.state, c.excludedPaths)
	}
	return c.scanFile(path)
}

func scanDirEntry(repoPath string, path string, entry fs.DirEntry, state *scanState) error {
	return scanDirEntryWithExcludedPaths(repoPath, path, entry, state, nil)
}

func scanDirEntryWithExcludedPaths(repoPath string, path string, entry fs.DirEntry, state *scanState, excludedPaths map[string]struct{}) error {
	if isExcludedPath(excludedPaths, path) {
		return filepath.SkipDir
	}
	if shouldSkipDir(entry.Name()) {
		return filepath.SkipDir
	}
	if path != repoPath && hasComposerManifest(path) {
		state.skippedNestedPackage++
		return filepath.SkipDir
	}
	return nil
}

func excludedPathsForRepo(repoPath string, directories, files []string) map[string]struct{} {
	if len(directories) == 0 && len(files) == 0 {
		return nil
	}
	repoPath = filepath.Clean(repoPath)
	paths := append(append([]string(nil), directories...), files...)
	excludedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleanedPath := filepath.Clean(path)
		relativePath, err := filepath.Rel(repoPath, cleanedPath)
		if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			continue
		}
		excludedPaths[cleanedPath] = struct{}{}
	}
	return excludedPaths
}

func isExcludedPath(paths map[string]struct{}, path string) bool {
	_, excluded := paths[filepath.Clean(path)]
	return excluded
}

func (c *scanCoordinator) scanFile(path string) error {
	c.state.visited++
	if c.state.visited > maxScanFiles {
		c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("scan stopped after %d files to keep analysis bounded", maxScanFiles))
		c.result.UsageIncomplete = true
		return fs.SkipAll
	}
	if !strings.EqualFold(filepath.Ext(path), ".php") {
		return nil
	}
	c.state.foundPHP = true

	content, relPath, err := readPHPFile(c.repoPath, path)
	if isPureOversizedFileError(err) {
		c.state.skippedLargeFiles++
		c.result.UsageIncomplete = true
		return nil
	}
	if err != nil {
		return err
	}

	resolver := c.resolver
	if c.shortOpenTagPolicy.hasSettings() {
		resolver.allowPHPShortOpenTags = c.shortOpenTagPolicy.enabledForFile(path)
		if c.shortOpenTagPolicy.incompleteForFile(path) {
			c.result.UsageIncomplete = true
		}
	}
	parsed := parsePHPImports(content, relPath, resolver)
	usage := shared.CountUsage(content, parsed.imports)
	dynamic := hasDynamicPatterns(content)

	mergeDependencyCounts(c.result.GroupedImportsByDependency, parsed.groupedByDep)
	if dynamic {
		incrementDynamicUsage(c.result.DynamicUsageByDependency, parsed.imports)
	}
	c.state.unresolvedNamespaces += parsed.unresolvedCount
	if parsed.useStatementLimitHit {
		c.state.useStatementLimitHits++
		c.result.UsageIncomplete = true
	}
	if parsed.useBindingLimitHit {
		c.state.useBindingLimitHits++
		c.result.UsageIncomplete = true
	}
	if parsed.namespaceDeclarationLimitHit {
		c.state.namespaceDeclarationLimitHits++
		c.result.UsageIncomplete = true
	}
	if parsed.namespaceReferenceLimitHit {
		c.state.namespaceReferenceLimitHits++
		c.result.UsageIncomplete = true
	}
	if parsed.namespaceResolutionLimitHit {
		c.state.namespaceResolutionLimitHits++
		c.result.UsageIncomplete = true
	}
	c.result.Files = append(c.result.Files, fileScan{
		Path:    relPath,
		Imports: parsed.imports,
		Usage:   usage,
		Dynamic: dynamic,
	})
	return nil
}

func scanFileEntry(repoPath string, path string, resolver composerResolver, result *scanResult, state *scanState) error {
	coordinator := scanCoordinator{
		repoPath: repoPath,
		resolver: resolver,
		result:   *result,
		state:    *state,
	}
	if err := coordinator.scanFile(path); err != nil {
		*result = coordinator.result
		*state = coordinator.state
		return err
	}
	*result = coordinator.result
	*state = coordinator.state
	return nil
}

func mergeDependencyCounts(dest, src map[string]int) {
	for dep, count := range src {
		dest[dep] += count
	}
}

func incrementDynamicUsage(dest map[string]int, imports []importBinding) {
	for dep := range dependenciesInFile(imports) {
		dest[dep]++
	}
}

func appendScanWarnings(result *scanResult, state scanState) {
	if !state.foundPHP {
		result.Warnings = append(result.Warnings, "no PHP source files found for analysis")
	}
	if len(result.DeclaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no Composer dependencies discovered from composer.json")
	}
	if state.unresolvedNamespaces > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("unable to map %d PHP import namespace(s) to composer dependencies", state.unresolvedNamespaces))
	}
	if state.skippedNestedPackage > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d nested composer package directory(ies) while scanning", state.skippedNestedPackage))
	}
	if state.skippedLargeFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d large PHP file(s) above %d bytes", state.skippedLargeFiles, maxScannablePHPFile))
	}
	if state.useStatementLimitHits > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped PHP use import scan after %d statement(s) in %d file(s) to keep analysis bounded", maxPHPUseStatementsPerFile, state.useStatementLimitHits))
	}
	if state.useBindingLimitHits > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped PHP use import scan after %d binding part(s) in %d file(s) to keep analysis bounded", maxPHPUseStatementsPerFile, state.useBindingLimitHits))
	}
	if state.namespaceDeclarationLimitHits > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped PHP namespace declaration scan after %d declaration(s) in %d file(s) to keep analysis bounded", maxPHPNamespaceDeclarationsPerFile, state.namespaceDeclarationLimitHits))
	}
	if state.namespaceReferenceLimitHits > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped PHP namespace reference scan after %d match(es) in %d file(s) to keep analysis bounded", maxPHPNamespaceReferencesPerFile, state.namespaceReferenceLimitHits))
	}
	if state.namespaceResolutionLimitHits > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped PHP namespace resolution after %d segment(s) or %d ancestor lookup byte(s) in %d file(s) to keep analysis bounded", maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes, state.namespaceResolutionLimitHits))
	}
	if len(result.DynamicUsageByDependency) > 0 {
		result.Warnings = append(result.Warnings, "dynamic loading/reflection patterns detected; dependency usage may be under-reported")
	}
}

func dependenciesInFile(imports []importBinding) map[string]struct{} {
	deps := make(map[string]struct{})
	for _, imp := range imports {
		if imp.Dependency == "" {
			continue
		}
		deps[normalizeDependencyID(imp.Dependency)] = struct{}{}
	}
	return deps
}

func readPHPFile(repoPath, path string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnderLimit(repoPath, path, maxScannablePHPFile)
	if err != nil {
		return nil, "", err
	}
	relPath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relPath = path
	}
	return content, relPath, nil
}

func hasComposerManifest(path string) bool {
	_, err := os.Stat(filepath.Join(path, composerJSONName))
	return err == nil
}

func phpFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
}
