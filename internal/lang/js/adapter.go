package js

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const jsPackageFile = "package.json"

var jsDetectSkippedDirs = map[string]bool{
	".next":    true,
	".turbo":   true,
	"coverage": true,
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "js-ts"
}

func (a *Adapter) Aliases() []string {
	return []string{"js", "ts", "javascript", "typescript"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	_ = ctx
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := addRootSignalDetection(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := scanFilesForJSDetection(repoPath, &detection, roots)
	if err == io.EOF {
		err = nil
	}
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func addRootSignalDetection(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	candidates := []string{jsPackageFile, "tsconfig.json", "jsconfig.json"}
	for _, name := range candidates {
		path := filepath.Join(repoPath, name)
		if _, err := os.Stat(path); err == nil {
			detection.Matched = true
			if name == jsPackageFile {
				detection.Confidence += 45
				roots[repoPath] = struct{}{}
				continue
			}
			detection.Confidence += 20
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func scanFilesForJSDetection(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	const maxFiles = 256
	visitedFiles := 0
	return filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDetectDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		visitedFiles++
		if visitedFiles > maxFiles {
			return io.EOF
		}
		if strings.EqualFold(d.Name(), jsPackageFile) {
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
			return nil
		}
		if isJSExtension(strings.ToLower(filepath.Ext(d.Name()))) {
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
}

func shouldSkipDetectDir(name string) bool {
	return shared.ShouldSkipDir(name, jsDetectSkippedDirs)
}

func isJSExtension(ext string) bool {
	switch ext {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		return true
	default:
		return false
	}
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
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	switch {
	case req.Dependency != "":
		resolvedRoots := resolveDependencyRootsFromScan(repoPath, req.Dependency, scanResult)
		dependencyRootPath := firstResolvedDependencyRoot(resolvedRoots)
		depReport, warnings := buildDependencyReport(repoPath, req.Dependency, dependencyRootPath, scanResult, req.RuntimeProfile, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations), req.SuggestOnly)
		result.Dependencies = []report.DependencyReport{depReport}
		result.Warnings = append(result.Warnings, warnings...)
		if len(resolvedRoots) > 1 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("dependency resolves to multiple node_modules roots: %s", req.Dependency))
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	case req.TopN > 0:
		deps, warnings := buildTopDependencies(repoPath, scanResult, req.TopN, req.RuntimeProfile, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations), resolveRemovalCandidateWeights(req.RemovalCandidateWeights))
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

func buildDependencyReport(repoPath string, dependency string, dependencyRootPath string, scanResult ScanResult, runtimeProfile string, minUsagePercentForRecommendations int, suggestOnly bool) (report.DependencyReport, []string) {
	warnings := make([]string, 0)

	surface, surfaceWarnings := resolveSurfaceWarnings(repoPath, dependency, dependencyRootPath, runtimeProfile)
	warnings = append(warnings, surfaceWarnings...)
	usage := collectDependencyUsageSummary(scanResult, dependency)
	warnings = append(warnings, usage.warnings...)

	totalExports := totalExportCount(surface)
	unusedExports := buildUnusedExports(dependency, surface.Names, usage.usedExports)
	usedPercent := exportUsedPercent(surface, usage.usedExports, totalExports)

	usedExportCount := countUsedExports(surface.Names, usage.usedExports)
	if usedExportCount == 0 && totalExports == 0 {
		usedExportCount = len(usage.usedExports)
	}

	riskCues, riskWarnings := assessRiskCues(repoPath, dependency, dependencyRootPath, surface)
	warnings = append(warnings, riskWarnings...)

	depReport := report.DependencyReport{
		Language:             "js-ts",
		Name:                 dependency,
		UsedExportsCount:     usedExportCount,
		TotalExportsCount:    totalExports,
		UsedPercent:          usedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       buildTopSymbols(usage.counts),
		UsedImports:          usage.usedImports,
		UnusedImports:        usage.unusedImports,
		UnusedExports:        unusedExports,
		RiskCues:             riskCues,
	}
	depReport.Recommendations = buildRecommendations(dependency, depReport, minUsagePercentForRecommendations)
	if suggestOnly {
		codemod, codemodWarnings := BuildSubpathCodemodReport(repoPath, dependency, dependencyRootPath, scanResult)
		depReport.Codemod = codemod
		warnings = append(warnings, codemodWarnings...)
	}
	return depReport, warnings
}

// dependencyUsageSummary captures intermediate usage aggregates for dependency report assembly.
type dependencyUsageSummary struct {
	usedExports   map[string]struct{}
	counts        map[string]int
	usedImports   []report.ImportUse
	unusedImports []report.ImportUse
	warnings      []string
}

type dependencyImportUsage struct {
	UsedExports          map[string]struct{}
	Counts               map[string]int
	UsedImports          map[string]*report.ImportUse
	UnusedImports        map[string]*report.ImportUse
	HasAmbiguousWildcard bool
	Warnings             []string
}

// collectDependencyUsageSummary aggregates dependency import usage into report-ready lists and warnings.
func collectDependencyUsageSummary(scanResult ScanResult, dependency string) dependencyUsageSummary {
	usage := collectDependencyImportUsage(scanResult, dependency)
	usedImportList, unusedImportList := finalizeImportUsageLists(usage.UsedImports, usage.UnusedImports)
	warnings := dependencyUsageWarnings(dependency, usage.UsedExports, usage.HasAmbiguousWildcard)
	warnings = append(warnings, usage.Warnings...)
	return dependencyUsageSummary{
		usedExports:   usage.UsedExports,
		counts:        usage.Counts,
		usedImports:   usedImportList,
		unusedImports: unusedImportList,
		warnings:      warnings,
	}
}

// finalizeImportUsageLists flattens import maps and removes used/unused overlaps from the unused list.
func finalizeImportUsageLists(usedImports, unusedImports map[string]*report.ImportUse) ([]report.ImportUse, []report.ImportUse) {
	usedImportList := flattenImportUses(usedImports)
	unusedImportList := flattenImportUses(unusedImports)
	return usedImportList, removeOverlappingUnusedImports(unusedImportList, usedImportList)
}

func resolveSurfaceWarnings(repoPath, dependency, dependencyRootPath, runtimeProfile string) (ExportSurface, []string) {
	surface := ExportSurface{Names: map[string]struct{}{}}
	warnings := make([]string, 0)
	resolved, err := resolveDependencyExports(dependencyExportRequest{
		repoPath:           repoPath,
		dependency:         dependency,
		dependencyRootPath: dependencyRootPath,
		runtimeProfileName: runtimeProfile,
	})
	if err != nil {
		warnings = append(warnings, err.Error())
		return surface, warnings
	}
	warnings = append(warnings, resolved.Warnings...)
	if resolved.IncludesWildcard {
		warnings = append(warnings, "dependency export surface includes wildcard re-exports")
	}
	return resolved, warnings
}

func collectDependencyImportUsage(scanResult ScanResult, dependency string) dependencyImportUsage {
	result := dependencyImportUsage{
		UsedExports:   make(map[string]struct{}),
		Counts:        make(map[string]int),
		UsedImports:   make(map[string]*report.ImportUse),
		UnusedImports: make(map[string]*report.ImportUse),
	}
	ctx := dependencyImportAttributionContext{
		dependency:    dependency,
		resolver:      newReExportResolver(scanResult),
		usedExports:   result.UsedExports,
		counts:        result.Counts,
		usedImports:   result.UsedImports,
		unusedImports: result.UnusedImports,
	}
	for _, file := range scanResult.Files {
		for _, imp := range file.Imports {
			matched, ambiguous := applyDependencyImportAttribution(file, imp, &ctx)
			if !matched {
				continue
			}
			if ambiguous {
				result.HasAmbiguousWildcard = true
			}
		}
	}
	result.Warnings = ctx.resolver.warnings()
	return result
}

type dependencyImportAttributionContext struct {
	dependency    string
	resolver      *reExportResolver
	usedExports   map[string]struct{}
	counts        map[string]int
	usedImports   map[string]*report.ImportUse
	unusedImports map[string]*report.ImportUse
}

func applyDependencyImportAttribution(file FileScan, imp ImportBinding, ctx *dependencyImportAttributionContext) (matched bool, ambiguous bool) {
	attributed, provenance := attributedImportBinding(file.Path, imp, ctx.dependency, ctx.resolver)
	if !matchesDependency(attributed.Module, ctx.dependency) {
		return false, false
	}

	used := applyImportUsage(attributed, file, ctx.usedExports, ctx.counts)
	entry := recordImportUse(attributed, provenance)
	if used {
		addImportUse(ctx.usedImports, entry)
	} else {
		addImportUse(ctx.unusedImports, entry)
	}

	return true, isAmbiguousImportUsage(attributed, file)
}

func attributedImportBinding(filePath string, imp ImportBinding, dependency string, resolver *reExportResolver) (ImportBinding, string) {
	if resolved, ok := resolver.resolveImportAttribution(filePath, imp, dependency); ok {
		attributed := imp
		attributed.Module = resolved.Module
		attributed.ExportName = resolved.ExportName
		return attributed, resolved.Provenance
	}
	return imp, ""
}

func isAmbiguousImportUsage(imp ImportBinding, file FileScan) bool {
	if imp.ExportName != "*" && imp.ExportName != "default" {
		return false
	}
	// Only flag as ambiguous if it's a wildcard/default import AND the
	// identifier is used directly (not just through property access).
	return hasDirectIdentifierUsage(imp, file)
}

func dependencyUsageWarnings(dependency string, usedExports map[string]struct{}, hasWildcard bool) []string {
	warnings := make([]string, 0)
	if len(usedExports) == 0 {
		warnings = append(warnings, fmt.Sprintf("no used exports found for dependency %q", dependency))
	}
	if hasWildcard {
		warnings = append(warnings, "default or namespace imports reduce export precision")
	}
	return warnings
}

// hasDirectIdentifierUsage checks if an import's local name is used directly
// (not just through property access), which makes wildcard/default imports ambiguous
func hasDirectIdentifierUsage(imp ImportBinding, file FileScan) bool {
	// Check if the identifier is used directly (not through property access)
	directCount := file.IdentifierUsage[imp.LocalName]

	// If there's namespace property usage, check if there's also direct usage beyond that
	if props, hasProps := file.NamespaceUsage[imp.LocalName]; hasProps && len(props) > 0 {
		// If we only have property access and no direct identifier usage, it's not ambiguous
		return directCount > 0
	}

	// No property usage, so any direct usage is ambiguous
	return directCount > 0
}

func buildTopSymbols(counts map[string]int) []report.SymbolUsage {
	topSymbols := make([]report.SymbolUsage, 0, len(counts))
	for name, count := range counts {
		topSymbols = append(topSymbols, report.SymbolUsage{Name: name, Count: count})
	}
	sort.Slice(topSymbols, func(i, j int) bool {
		if topSymbols[i].Count == topSymbols[j].Count {
			return topSymbols[i].Name < topSymbols[j].Name
		}
		return topSymbols[i].Count > topSymbols[j].Count
	})
	if len(topSymbols) > 5 {
		return topSymbols[:5]
	}
	return topSymbols
}

func totalExportCount(surface ExportSurface) int {
	if surface.IncludesWildcard {
		return 0
	}
	return len(surface.Names)
}

func exportUsedPercent(surface ExportSurface, usedExports map[string]struct{}, totalExports int) float64 {
	if totalExports == 0 {
		return 0
	}
	usedCount := countUsedExports(surface.Names, usedExports)
	return (float64(usedCount) / float64(totalExports)) * 100
}

func applyImportUsage(imp ImportBinding, file FileScan, usedExports map[string]struct{}, counts map[string]int) bool {
	switch imp.Kind {
	case ImportNamed:
		return applyNamedImportUsage(imp, file, usedExports, counts)
	case ImportNamespace, ImportDefault:
		return applyNamespaceOrDefaultImportUsage(imp, file, usedExports, counts)
	default:
		return false
	}
}

func applyNamedImportUsage(imp ImportBinding, file FileScan, usedExports map[string]struct{}, counts map[string]int) bool {
	count := file.IdentifierUsage[imp.LocalName]
	if count <= 0 {
		return false
	}
	usedExports[imp.ExportName] = struct{}{}
	counts[imp.ExportName] += count
	return true
}

func applyNamespaceOrDefaultImportUsage(imp ImportBinding, file FileScan, usedExports map[string]struct{}, counts map[string]int) bool {
	used := false
	if props, ok := file.NamespaceUsage[imp.LocalName]; ok {
		for prop, count := range props {
			used = true
			usedExports[prop] = struct{}{}
			counts[prop] += count
		}
	}
	if count := file.IdentifierUsage[imp.LocalName]; count > 0 {
		used = true
		if imp.Kind == ImportDefault {
			usedExports["default"] = struct{}{}
			counts["default"] += count
		} else {
			usedExports["*"] = struct{}{}
			counts["*"] += count
		}
	}
	return used
}

func recordImportUse(binding ImportBinding, provenance string) report.ImportUse {
	provenanceItems := make([]string, 0, 1)
	if provenance != "" {
		provenanceItems = append(provenanceItems, provenance)
	}
	return report.ImportUse{
		Name:       binding.ExportName,
		Module:     binding.Module,
		Locations:  []report.Location{binding.Location},
		Provenance: provenanceItems,
	}
}

func addImportUse(dest map[string]*report.ImportUse, entry report.ImportUse) {
	key := fmt.Sprintf("%s:%s", entry.Module, entry.Name)
	current, ok := dest[key]
	if !ok {
		copyEntry := entry
		dest[key] = &copyEntry
		return
	}
	current.Locations = append(current.Locations, entry.Locations...)
	for _, item := range entry.Provenance {
		if slices.Contains(current.Provenance, item) {
			continue
		}
		current.Provenance = append(current.Provenance, item)
	}
}

func flattenImportUses(source map[string]*report.ImportUse) []report.ImportUse {
	items := make([]report.ImportUse, 0, len(source))
	for _, entry := range source {
		items = append(items, *entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
	return items
}

func removeOverlappingUnusedImports(unused []report.ImportUse, used []report.ImportUse) []report.ImportUse {
	usedKeys := make(map[string]struct{}, len(used))
	for _, entry := range used {
		usedKeys[fmt.Sprintf("%s:%s", entry.Module, entry.Name)] = struct{}{}
	}

	filtered := make([]report.ImportUse, 0, len(unused))
	for _, entry := range unused {
		key := fmt.Sprintf("%s:%s", entry.Module, entry.Name)
		if _, ok := usedKeys[key]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}

	return filtered
}

func buildUnusedExports(module string, surface map[string]struct{}, used map[string]struct{}) []report.SymbolRef {
	items := make([]report.SymbolRef, 0, len(surface))
	for name := range surface {
		if _, ok := used[name]; ok {
			continue
		}
		items = append(items, report.SymbolRef{Name: name, Module: module})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	return items
}

func countUsedExports(surface map[string]struct{}, used map[string]struct{}) int {
	count := 0
	for name := range used {
		if _, ok := surface[name]; ok {
			count++
		}
	}
	return count
}

func matchesDependency(module string, dependency string) bool {
	if module == dependency {
		return true
	}
	if strings.HasPrefix(module, dependency+"/") {
		return true
	}
	return false
}

func buildTopDependencies(repoPath string, scanResult ScanResult, topN int, runtimeProfile string, minUsagePercentForRecommendations int, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencies, dependencyRoots, warnings := listDependencies(repoPath, scanResult)
	if len(dependencies) == 0 {
		return nil, warnings
	}

	reports := make([]report.DependencyReport, 0, len(dependencies))
	for _, dep := range dependencies {
		depReport, depWarnings := buildDependencyReport(repoPath, dep, dependencyRoots[dep], scanResult, runtimeProfile, minUsagePercentForRecommendations, false)
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}

	shared.SortReportsByWaste(reports, weights)

	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}

	return reports, warnings
}

func listDependencies(repoPath string, scanResult ScanResult) ([]string, map[string]string, []string) {
	collector := newDependencyCollector()
	for _, file := range scanResult.Files {
		importerPath := filepath.Join(repoPath, file.Path)
		for _, imp := range file.Imports {
			collector.recordImport(repoPath, importerPath, imp)
		}
	}

	deps := make([]string, 0, len(collector.found))
	for dep := range collector.found {
		deps = append(deps, dep)
	}
	sort.Strings(deps)

	warnings := make([]string, 0, len(collector.missing))
	for dep := range collector.missing {
		warnings = append(warnings, fmt.Sprintf("dependency not found in node_modules: %s", dep))
	}
	for dep := range collector.multiRoot {
		warnings = append(warnings, fmt.Sprintf("dependency resolves to multiple node_modules roots: %s", dep))
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
	c.found[dep] = struct{}{}
	if c.roots[dep] == "" {
		c.roots[dep] = resolvedRoot
		return
	}
	if c.roots[dep] != resolvedRoot {
		c.multiRoot[dep] = struct{}{}
	}
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

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
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

	absRepo, err := filepath.Abs(req.RepoPath)
	if err != nil {
		return ""
	}
	absImporter, err := filepath.Abs(req.ImporterPath)
	if err != nil {
		return ""
	}
	if !isPathWithin(absImporter, absRepo) {
		return ""
	}

	absStart := filepath.Dir(absImporter)

	for {
		root, ok := resolveDependencyRootAtDir(absStart, req.Dependency)
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
