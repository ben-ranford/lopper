package js

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func listDependencies(ctx context.Context, repoPath string, scanResult ScanResult) ([]string, map[string]string, []string, error) {
	collector := newDependencyCollector()
	for _, file := range scanResult.Files {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		importerPath := filepath.Join(repoPath, file.Path)
		for _, imp := range file.Imports {
			collector.recordImport(repoPath, importerPath, imp)
		}
	}
	workspaceCatalog, err := loadWorkspaceDependencyCatalog(ctx, repoPath)
	if err != nil {
		return nil, nil, nil, err
	}
	collector.mergeWorkspaceDeclarations(repoPath, workspaceCatalog.declarations)

	deps := make([]string, 0, len(collector.found))
	for dep := range collector.found {
		deps = append(deps, dep)
	}
	sort.Strings(deps)

	warningSet := make(map[string]struct{}, len(collector.missing)+len(collector.multiRoot)+len(workspaceCatalog.warnings))
	for dep := range collector.missing {
		warningSet[fmt.Sprintf("dependency not found in node_modules: %s", dep)] = struct{}{}
	}
	for dep := range collector.unsafe {
		warningSet[fmt.Sprintf("dependency root could not be safely resolved in node_modules: %s (symlinked or opaque layout)", dep)] = struct{}{}
	}
	for dep := range collector.multiRoot {
		warningSet[fmt.Sprintf("dependency resolves to multiple node_modules roots: %s", dep)] = struct{}{}
	}
	for _, warning := range workspaceCatalog.warnings {
		warningSet[warning] = struct{}{}
	}

	warnings := make([]string, 0, len(warningSet))
	for warning := range warningSet {
		warnings = append(warnings, warning)
	}
	sort.Strings(warnings)

	return deps, collector.roots, warnings, nil
}

type dependencyCollector struct {
	found     map[string]struct{}
	roots     map[string]string
	multiRoot map[string]struct{}
	missing   map[string]struct{}
	unsafe    map[string]struct{}
	cache     map[string]string
	statuses  map[string]dependencyRootResolutionStatus
}

func newDependencyCollector() dependencyCollector {
	return dependencyCollector{
		found:     make(map[string]struct{}),
		roots:     make(map[string]string),
		multiRoot: make(map[string]struct{}),
		missing:   make(map[string]struct{}),
		unsafe:    make(map[string]struct{}),
		cache:     make(map[string]string),
		statuses:  make(map[string]dependencyRootResolutionStatus),
	}
}

func (c *dependencyCollector) recordImport(repoPath, importerPath string, imp ImportBinding) {
	dep := dependencyFromModule(imp.Module)
	if dep == "" {
		return
	}
	resolvedRoot, status := c.cachedDependencyRoot(dependencyResolutionRequest{
		RepoPath:     repoPath,
		ImporterPath: importerPath,
		Dependency:   dep,
	})
	if resolvedRoot == "" {
		if _, alreadyFound := c.found[dep]; alreadyFound {
			return
		}
		if status == dependencyRootUnsafe {
			c.markUnsafe(dep)
			return
		}
		if _, alreadyUnsafe := c.unsafe[dep]; alreadyUnsafe {
			return
		}
		c.missing[dep] = struct{}{}
		return
	}
	c.markFound(dep)
	c.recordResolvedRoot(dep, resolvedRoot)
}

func (c *dependencyCollector) cachedDependencyRoot(req dependencyResolutionRequest) (string, dependencyRootResolutionStatus) {
	cacheKey := req.ImporterPath + "\x00" + req.Dependency
	if resolvedRoot, ok := c.cache[cacheKey]; ok {
		return resolvedRoot, c.statuses[cacheKey]
	}
	resolvedRoot, status := resolveDependencyRootFromDirDetailed(req.RepoPath, filepath.Dir(req.ImporterPath), req.Dependency)
	c.cache[cacheKey] = resolvedRoot
	c.statuses[cacheKey] = status
	return resolvedRoot, status
}

func (c *dependencyCollector) markFound(dep string) {
	c.found[dep] = struct{}{}
	delete(c.missing, dep)
	delete(c.unsafe, dep)
}

func (c *dependencyCollector) markUnsafe(dep string) {
	c.unsafe[dep] = struct{}{}
	delete(c.missing, dep)
}

func (c *dependencyCollector) recordResolvedRoot(dep, resolvedRoot string) {
	if strings.TrimSpace(dep) == "" || strings.TrimSpace(resolvedRoot) == "" {
		return
	}
	if c.roots[dep] == "" {
		c.roots[dep] = resolvedRoot
		return
	}
	if c.roots[dep] != resolvedRoot {
		c.multiRoot[dep] = struct{}{}
	}
}

func (c *dependencyCollector) mergeWorkspaceDeclarations(repoPath string, declarations map[string]workspaceDependencyDeclaration) {
	for dep, declaration := range declarations {
		if c.recordWorkspaceDeclarationRoots(repoPath, dep, declaration) {
			c.markFound(dep)
			continue
		}
		if _, alreadyFound := c.found[dep]; !alreadyFound {
			c.recordWorkspaceDeclarationStatus(repoPath, dep, declaration.declarationDirs)
		}
	}
}

func (c *dependencyCollector) recordWorkspaceDeclarationRoots(repoPath, dep string, declaration workspaceDependencyDeclaration) bool {
	resolvedAnyRoot := false
	for _, root := range resolveDependencyRootsFromDeclarationDirs(repoPath, dep, declaration.declarationDirs) {
		resolvedAnyRoot = true
		c.recordResolvedRoot(dep, root)
	}
	return resolvedAnyRoot
}

func (c *dependencyCollector) recordWorkspaceDeclarationStatus(repoPath, dep string, declarationDirs map[string]struct{}) {
	for dir := range declarationDirs {
		if _, status := resolveDependencyRootFromDirDetailed(repoPath, dir, dep); status == dependencyRootUnsafe {
			c.markUnsafe(dep)
			return
		}
	}
	if _, alreadyUnsafe := c.unsafe[dep]; alreadyUnsafe {
		return
	}
	c.missing[dep] = struct{}{}
}

func dependencyFromModule(module string) string {
	module = strings.TrimSpace(module)
	if module == "" {
		return ""
	}

	// Filter out Node.js built-in modules (both "node:*" and bare names like "fs")
	if isNodeBuiltin(module) {
		return ""
	}

	if strings.HasPrefix(module, ".") || strings.HasPrefix(module, "/") {
		return ""
	}

	if strings.HasPrefix(module, "@") {
		parts := strings.Split(module, "/")
		if len(parts) < 2 {
			return ""
		}
		if len(parts[0]) <= 1 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}

	parts := strings.Split(module, "/")
	return parts[0]
}
