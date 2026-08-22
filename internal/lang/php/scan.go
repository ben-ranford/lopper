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
	visited                     int
	unresolvedNamespaces        int
	foundPHP                    bool
	skippedLargeFiles           int
	skippedNestedPackage        int
	useStatementLimitHits       int
	useBindingLimitHits         int
	namespaceReferenceLimitHits int
}

type scanCoordinator struct {
	repoPath string
	resolver composerResolver
	result   scanResult
	state    scanState
}

func newScanCoordinator(repoPath string, composer composerData) scanCoordinator {
	return scanCoordinator{
		repoPath: repoPath,
		resolver: newComposerResolver(composer),
		result: scanResult{
			DeclaredDependencies:       composer.DeclaredDependencies,
			GroupedImportsByDependency: make(map[string]int),
			DynamicUsageByDependency:   make(map[string]int),
			UsageIncomplete:            composer.UsageIncomplete,
		},
	}
}

func scanRepo(ctx context.Context, repoPath string, composer composerData) (scanResult, error) {
	coordinator := newScanCoordinator(repoPath, composer)
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
	if entry.IsDir() {
		return scanDirEntry(c.repoPath, path, entry, &c.state)
	}
	return c.scanFile(path)
}

func scanDirEntry(repoPath string, path string, entry fs.DirEntry, state *scanState) error {
	if shouldSkipDir(entry.Name()) {
		return filepath.SkipDir
	}
	if path != repoPath && hasComposerManifest(path) {
		state.skippedNestedPackage++
		return filepath.SkipDir
	}
	return nil
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

	parsed := parsePHPImports(content, relPath, c.resolver)
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
	if parsed.namespaceReferenceLimitHit {
		c.state.namespaceReferenceLimitHits++
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
	if state.namespaceReferenceLimitHits > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped PHP namespace reference scan after %d match(es) in %d file(s) to keep analysis bounded", maxPHPNamespaceReferencesPerFile, state.namespaceReferenceLimitHits))
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
