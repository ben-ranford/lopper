package rust

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func scanRepo(ctx context.Context, repoPath string, manifestPaths []string, depLookup map[string]dependencyInfo, renamedAliases map[string][]string) (scanResult, error) {
	return scanRepoWithFallback(ctx, repoPath, manifestPaths, "", depLookup, renamedAliases)
}

func scanRepoWithFallback(ctx context.Context, repoPath string, manifestPaths []string, sourceFallbackRoot string, depLookup map[string]dependencyInfo, renamedAliases map[string][]string) (scanResult, error) {
	result := scanResult{
		UnresolvedImports:   make(map[string]int),
		RenamedAliasesByDep: renamedAliases,
		LocalModuleCache:    make(map[string]bool),
	}
	roots := scanRoots(manifestPaths, repoPath)
	lookupsByRoot := map[string]map[string]dependencyInfo(nil)
	if sourceFallbackRoot != "" {
		roots = scanRootsPreservingNested(manifestPaths, repoPath)
		var err error
		lookupsByRoot, err = manifestDependencyLookupsByRoot(repoPath, manifestPaths)
		if err != nil {
			return scanResult{}, err
		}
	}
	scannedFiles := make(map[string]struct{})
	fileCount := 0
	for _, root := range roots {
		rootLookup := depLookup
		result.RequireDeclaredDependency = false
		if sourceFallbackRoot != "" {
			rootLookup = lookupsByRoot[root]
			result.RequireDeclaredDependency = true
		}
		err := scanRepoRoot(ctx, repoPath, root, rootLookup, scannedFiles, &fileCount, &result)
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return scanResult{}, err
		}
	}
	if sourceFallbackRoot != "" {
		result.RequireDeclaredDependency = true
		err := scanRepoRoot(ctx, repoPath, sourceFallbackRoot, map[string]dependencyInfo{}, scannedFiles, &fileCount, &result)
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return scanResult{}, err
		}
	}
	result.Warnings = append(result.Warnings, compileScanWarnings(result)...)
	result.Warnings = dedupeWarnings(result.Warnings)
	return result, nil
}

func manifestDependencyLookupsByRoot(repoPath string, manifestPaths []string) (map[string]map[string]dependencyInfo, error) {
	lookups := make(map[string]map[string]dependencyInfo, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		_, dependencies, err := parseCargoManifest(manifestPath, repoPath)
		if err != nil {
			if isCargoManifestParseError(err) {
				continue
			}
			return nil, err
		}
		lookups[filepath.Dir(manifestPath)] = dependencies
	}
	return lookups, nil
}

func compileScanWarnings(result scanResult) []string {
	warnings := make([]string, 0, 4+len(result.UnresolvedImports))
	if len(result.Files) == 0 {
		warnings = append(warnings, "no Rust source files found for analysis")
	}
	if result.SkippedLargeFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d Rust files larger than %d bytes", result.SkippedLargeFiles, maxScannableRustFile))
	}
	if result.SkippedFilesByBoundLimit {
		warnings = append(warnings, fmt.Sprintf("Rust source scanning capped at %d files", maxScanFiles))
	}
	if result.MacroAmbiguityDetected {
		warnings = append(warnings, "Rust macro invocations detected; static attribution may be partial for macro- and feature-driven paths")
	}
	return append(warnings, summarizeUnresolved(result.UnresolvedImports)...)
}

func scanRustSourceFile(repoPath string, crateRoot string, path string, depLookup map[string]dependencyInfo, result *scanResult) error {
	content, err := safeio.ReadFileUnderLimit(repoPath, path, maxScannableRustFile)
	if errors.Is(err, safeio.ErrFileTooLarge) {
		result.SkippedLargeFiles++
		return nil
	}
	if err != nil {
		return err
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relativePath = path
	}

	imports := parseRustImportsBytes(content, relativePath, crateRoot, depLookup, result)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	if macroInvokePattern.Match(content) {
		result.MacroAmbiguityDetected = true
	}
	return nil
}
