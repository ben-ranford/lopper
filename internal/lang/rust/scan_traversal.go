package rust

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func scanRoots(manifestPaths []string, repoPath string) []string {
	roots := make([]string, 0, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		roots = append(roots, filepath.Dir(manifestPath))
	}
	roots = uniquePaths(roots)
	if len(roots) == 0 {
		return []string{repoPath}
	}
	return collapseScanRoots(roots)
}

func collapseScanRoots(roots []string) []string {
	collapsed := make([]string, 0, len(roots))
	for _, root := range roots {
		if hasScanRootParent(collapsed, root) {
			continue
		}
		collapsed = dropNestedScanRoots(collapsed, root)
		collapsed = append(collapsed, root)
	}
	return collapsed
}

func hasScanRootParent(roots []string, candidate string) bool {
	for _, root := range roots {
		if isSubPath(root, candidate) {
			return true
		}
	}
	return false
}

func dropNestedScanRoots(roots []string, candidate string) []string {
	filtered := roots[:0]
	for _, root := range roots {
		if isSubPath(candidate, root) {
			continue
		}
		filtered = append(filtered, root)
	}
	return filtered
}

func scanRepoRoot(ctx context.Context, repoPath, root string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	return shared.WalkRepoFiles(ctx, root, 0, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		return scanRepoFileEntry(repoPath, root, path, depLookup, scannedFiles, fileCount, result)
	})
}

func scanRepoFileEntry(repoPath, root, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	if !strings.EqualFold(filepath.Ext(path), ".rs") {
		return nil
	}
	if _, ok := scannedFiles[path]; ok {
		return nil
	}
	scannedFiles[path] = struct{}{}

	(*fileCount)++
	if *fileCount > maxScanFiles {
		result.SkippedFilesByBoundLimit = true
		return fs.SkipAll
	}
	return scanRustSourceFile(repoPath, root, path, depLookup, result)
}
