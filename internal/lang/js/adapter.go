package js

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("js-ts", []string{"js", "ts", "javascript", "typescript"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	scanResult, err := ScanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.UsageUncertainty = summarizeUsageUncertainty(scanResult)
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	switch {
	case req.Dependency != "":
		resolvedRoots := resolveDependencyRootsFromScan(repoPath, req.Dependency, scanResult)
		dependencyRootPath := firstResolvedDependencyRoot(resolvedRoots)
		depReport, warnings := buildDependencyReport(dependencyReportOptions{
			RepoPath:                          repoPath,
			Dependency:                        req.Dependency,
			DependencyRootPath:                dependencyRootPath,
			ScanResult:                        scanResult,
			RuntimeProfile:                    req.RuntimeProfile,
			MinUsagePercentForRecommendations: resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations),
			SuggestOnly:                       req.SuggestOnly,
			IncludeRegistryProvenance:         req.IncludeRegistryProvenance,
		})
		result.Dependencies = []report.DependencyReport{depReport}
		result.Warnings = append(result.Warnings, warnings...)
		if len(resolvedRoots) > 1 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("dependency resolves to multiple node_modules roots: %s", req.Dependency))
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	case req.TopN > 0:
		deps, warnings := buildTopDependencies(repoPath, scanResult, req.TopN, req.RuntimeProfile, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations), resolveRemovalCandidateWeights(req.RemovalCandidateWeights), req.IncludeRegistryProvenance)
		result.Dependencies = deps
		result.Warnings = append(result.Warnings, warnings...)
		if len(deps) == 0 {
			result.Warnings = append(result.Warnings, "no dependency data available for top-N ranking")
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	default:
		result.Warnings = append(result.Warnings, "no dependency or top-N target provided")
	}

	return result, nil
}

func listDependencies(repoPath string, scanResult ScanResult) ([]string, map[string]string, []string) {
	collector := newDependencyCollector()
	for _, file := range scanResult.Files {
		importerPath := filepath.Join(repoPath, file.Path)
		for _, imp := range file.Imports {
			collector.recordImport(repoPath, importerPath, imp)
		}
	}
	workspaceCatalog := loadWorkspaceDependencyCatalog(repoPath)
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

	return deps, collector.roots, warnings
}

type dependencyCollector struct {
	found     map[string]struct{}
	roots     map[string]string
	multiRoot map[string]struct{}
	missing   map[string]struct{}
	cache     map[string]string
}

func newDependencyCollector() dependencyCollector {
	return dependencyCollector{
		found:     make(map[string]struct{}),
		roots:     make(map[string]string),
		multiRoot: make(map[string]struct{}),
		missing:   make(map[string]struct{}),
		cache:     make(map[string]string),
	}
}

func (c *dependencyCollector) recordImport(repoPath string, importerPath string, imp ImportBinding) {
	dep := dependencyFromModule(imp.Module)
	if dep == "" {
		return
	}
	resolvedRoot := c.cachedDependencyRoot(dependencyResolutionRequest{
		RepoPath:     repoPath,
		ImporterPath: importerPath,
		Dependency:   dep,
	})
	if resolvedRoot == "" {
		if _, alreadyFound := c.found[dep]; alreadyFound {
			return
		}
		c.missing[dep] = struct{}{}
		return
	}
	c.markFound(dep)
	c.recordResolvedRoot(dep, resolvedRoot)
}

func (c *dependencyCollector) cachedDependencyRoot(req dependencyResolutionRequest) string {
	cacheKey := req.ImporterPath + "\x00" + req.Dependency
	if resolvedRoot, ok := c.cache[cacheKey]; ok {
		return resolvedRoot
	}
	resolvedRoot := resolveDependencyRootFromImporter(req)
	c.cache[cacheKey] = resolvedRoot
	return resolvedRoot
}

func (c *dependencyCollector) markFound(dep string) {
	c.found[dep] = struct{}{}
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
		resolvedAnyRoot := false
		for _, root := range resolveDependencyRootsFromDeclarationDirs(repoPath, dep, declaration.declarationDirs) {
			resolvedAnyRoot = true
			c.recordResolvedRoot(dep, root)
		}
		if resolvedAnyRoot {
			c.markFound(dep)
			continue
		}
		if _, alreadyFound := c.found[dep]; !alreadyFound {
			c.missing[dep] = struct{}{}
		}
	}
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
