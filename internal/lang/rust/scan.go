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
	return scanRepoWithFallback(ctx, repoPath, rustScanOptions{
		manifestPaths:  manifestPaths,
		depLookup:      depLookup,
		renamedAliases: renamedAliases,
	})
}

type rustScanOptions struct {
	manifestPaths       []string
	sourceFallbackRoots []string
	excludedSourceRoots []string
	depLookup           map[string]dependencyInfo
	renamedAliases      map[string][]string
	useRootLookups      bool
}

func scanRepoWithFallback(ctx context.Context, repoPath string, options rustScanOptions) (scanResult, error) {
	result := scanResult{
		UnresolvedImports:   make(map[string]int),
		RenamedAliasesByDep: options.renamedAliases,
		LocalModuleCache:    make(map[string]bool),
	}
	roots := scanRoots(options.manifestPaths, repoPath)
	lookupsByRoot := map[string]map[string]dependencyInfo(nil)
	useRootLookups := len(options.sourceFallbackRoots) > 0 || options.useRootLookups
	if useRootLookups {
		roots = scanRootsPreservingNested(options.manifestPaths, repoPath)
		var err error
		lookupsByRoot, err = manifestDependencyLookupsByRoot(repoPath, options.manifestPaths)
		if err != nil {
			return scanResult{}, err
		}
	}
	scannedFiles := make(map[string]struct{})
	fileCount := 0
	for _, root := range roots {
		rootLookup := options.depLookup
		result.RequireDeclaredDependency = false
		if useRootLookups {
			rootLookup = lookupsByRoot[root]
			result.RequireDeclaredDependency = true
		}
		err := scanRepoRootExcluding(ctx, rustScanRootOptions{
			repoPath:            repoPath,
			root:                root,
			depLookup:           rootLookup,
			excludedSourceRoots: exclusionsOutsideRustRoot(root, options.excludedSourceRoots),
			scannedFiles:        scannedFiles,
			fileCount:           &fileCount,
			result:              &result,
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return scanResult{}, err
		}
	}
	for _, sourceFallbackRoot := range options.sourceFallbackRoots {
		result.RequireDeclaredDependency = true
		err := scanRepoRootExcluding(ctx, rustScanRootOptions{
			repoPath:     repoPath,
			root:         sourceFallbackRoot,
			depLookup:    map[string]dependencyInfo{},
			scannedFiles: scannedFiles,
			fileCount:    &fileCount,
			result:       &result,
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			return scanResult{}, err
		}
	}
	result.Warnings = append(result.Warnings, compileScanWarnings(result)...)
	result.Warnings = dedupeWarnings(result.Warnings)
	return result, nil
}

func exclusionsOutsideRustRoot(root string, excludedSourceRoots []string) []string {
	filtered := make([]string, 0, len(excludedSourceRoots))
	for _, excludedRoot := range excludedSourceRoots {
		if !isSubPath(excludedRoot, root) {
			filtered = append(filtered, excludedRoot)
		}
	}
	return filtered
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
