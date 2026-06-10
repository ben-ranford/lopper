package js

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type dependencyResolutionRequest struct {
	RepoPath     string
	ImporterPath string
	Dependency   string
}

func resolveDependencyRootFromImporter(req dependencyResolutionRequest) string {
	if req.RepoPath == "" || req.ImporterPath == "" || req.Dependency == "" {
		return ""
	}
	return resolveDependencyRootFromDir(req.RepoPath, filepath.Dir(req.ImporterPath), req.Dependency)
}

func resolveDependencyRootFromDir(repoPath, startDir, dependency string) string {
	if repoPath == "" || startDir == "" || dependency == "" {
		return ""
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return ""
	}
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	if !isPathWithin(absStart, absRepo) {
		return ""
	}

	for {
		root, ok := resolveDependencyRootAtDir(absStart, dependency)
		if ok {
			return root
		}
		if absStart == absRepo {
			break
		}
		parent := filepath.Dir(absStart)
		if parent == absStart {
			break
		}
		absStart = parent
	}
	return ""
}

func resolveDependencyRootsFromDeclarationDirs(repoPath string, dependency string, declarationDirs map[string]struct{}) []string {
	rootsSet := make(map[string]struct{})
	for dir := range declarationDirs {
		if resolved := resolveDependencyRootFromDir(repoPath, dir, dependency); resolved != "" {
			rootsSet[resolved] = struct{}{}
		}
	}

	roots := make([]string, 0, len(rootsSet))
	for root := range rootsSet {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func resolveDependencyRootsFromScan(repoPath string, dependency string, scanResult ScanResult) []string {
	rootsSet := make(map[string]struct{})
	for _, file := range scanResult.Files {
		for _, imp := range file.Imports {
			if !matchesDependency(imp.Module, dependency) {
				continue
			}
			importerPath := filepath.Join(repoPath, file.Path)
			if resolved := resolveDependencyRootFromImporter(dependencyResolutionRequest{
				RepoPath:     repoPath,
				ImporterPath: importerPath,
				Dependency:   dependency,
			}); resolved != "" {
				rootsSet[resolved] = struct{}{}
			}
		}
	}
	roots := make([]string, 0, len(rootsSet))
	for root := range rootsSet {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func firstResolvedDependencyRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

func resolveDependencyRootAtDir(rootDir, dependency string) (string, bool) {
	root := filepath.Join(rootDir, "node_modules", dependencyPath(dependency))
	info, err := os.Stat(filepath.Join(root, "package.json"))
	if err != nil || info.IsDir() {
		return "", false
	}
	return root, true
}

func isPathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
