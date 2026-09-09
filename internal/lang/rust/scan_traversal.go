package rust

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
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

func scanRootsPreservingNested(manifestPaths []string, repoPath string) []string {
	roots := make([]string, 0, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		roots = append(roots, filepath.Dir(manifestPath))
	}
	roots = uniquePaths(roots)
	if len(roots) == 0 {
		return []string{repoPath}
	}
	sort.Slice(roots, func(i, j int) bool {
		depthI := strings.Count(filepath.Clean(roots[i]), string(filepath.Separator))
		depthJ := strings.Count(filepath.Clean(roots[j]), string(filepath.Separator))
		if depthI != depthJ {
			return depthI > depthJ
		}
		return roots[i] < roots[j]
	})
	return roots
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

type rustScanRootOptions struct {
	repoPath            string
	root                string
	depLookup           map[string]dependencyInfo
	excludedSourceRoots []string
	scannedFiles        map[string]struct{}
	fileCount           *int
	result              *scanResult
}

func scanRepoRoot(ctx context.Context, repoPath, root string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	return scanRepoRootExcluding(ctx, rustScanRootOptions{
		repoPath:     repoPath,
		root:         root,
		depLookup:    depLookup,
		scannedFiles: scannedFiles,
		fileCount:    fileCount,
		result:       result,
	})
}

func scanRepoRootExcluding(ctx context.Context, options rustScanRootOptions) error {
	return shared.WalkRepoFiles(ctx, options.root, 0, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		return options.scanFile(path)
	})
}

func scanRepoFileEntry(repoPath, root, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	return (rustScanRootOptions{
		repoPath:     repoPath,
		root:         root,
		depLookup:    depLookup,
		scannedFiles: scannedFiles,
		fileCount:    fileCount,
		result:       result,
	}).scanFile(path)
}

func (o rustScanRootOptions) scanFile(path string) error {
	if !strings.EqualFold(filepath.Ext(path), ".rs") || isExcludedRustSource(path, o.excludedSourceRoots) {
		return nil
	}
	if _, ok := o.scannedFiles[path]; ok {
		return nil
	}
	o.scannedFiles[path] = struct{}{}

	(*o.fileCount)++
	if *o.fileCount > maxScanFiles {
		o.result.SkippedFilesByBoundLimit = true
		return fs.SkipAll
	}
	return scanRustSourceFile(o.repoPath, o.root, path, o.depLookup, o.result)
}

func isExcludedRustSource(path string, excludedRoots []string) bool {
	for _, root := range excludedRoots {
		if isSubPath(root, path) {
			return true
		}
	}
	return false
}
