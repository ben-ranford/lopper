package rust

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
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
	completedRoots      map[string]struct{}
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
	return walkRustScanFiles(ctx, options.root, options.completedRoots, options.scanFile)
}

func walkRustScanFiles(ctx context.Context, root string, completedRoots map[string]struct{}, visit func(path string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if !samePath(path, root) && (shouldSkipDir(entry.Name()) || isCompletedRustScanRoot(path, completedRoots)) {
				return filepath.SkipDir
			}
			return nil
		}
		return visit(path)
	})
}

func isCompletedRustScanRoot(path string, roots map[string]struct{}) bool {
	_, ok := roots[filepath.Clean(path)]
	return ok
}

func scanRepoFileEntry(repoPath, root, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	options := rustScanRootOptions{
		repoPath:     repoPath,
		root:         root,
		depLookup:    depLookup,
		scannedFiles: scannedFiles,
		fileCount:    fileCount,
		result:       result,
	}
	return options.scanFile(path)
}

func (o *rustScanRootOptions) scanFile(path string) error {
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
