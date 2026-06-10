package js

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func summarizeUsageUncertainty(scanResult ScanResult) *report.UsageUncertainty {
	usage := &report.UsageUncertainty{}
	for _, file := range scanResult.Files {
		usage.ConfirmedImportUses += len(file.Imports)
		usage.UncertainImportUses += len(file.UncertainImports)
		for _, item := range file.UncertainImports {
			if len(usage.Samples) >= 5 {
				break
			}
			if len(item.Locations) == 0 {
				continue
			}
			usage.Samples = append(usage.Samples, item.Locations[0])
		}
	}
	if usage.ConfirmedImportUses == 0 && usage.UncertainImportUses == 0 {
		return nil
	}
	return usage
}

type dependencyReportOptions struct {
	RepoPath                          string
	Dependency                        string
	DependencyRootPath                string
	ScanResult                        ScanResult
	RuntimeProfile                    string
	MinUsagePercentForRecommendations int
	SuggestOnly                       bool
	IncludeRegistryProvenance         bool
}

func buildDependencyReport(opts dependencyReportOptions) (report.DependencyReport, []string) {
	warnings := make([]string, 0)

	surface, surfaceWarnings := resolveSurfaceWarnings(opts.RepoPath, opts.Dependency, opts.DependencyRootPath, opts.RuntimeProfile)
	warnings = append(warnings, surfaceWarnings...)
	usage := collectDependencyUsageSummary(opts.ScanResult, opts.Dependency)
	warnings = append(warnings, usage.warnings...)

	totalExports := totalExportCount(surface)
	unusedExports := buildUnusedExports(opts.Dependency, surface.Names, usage.usedExports)
	usedPercent := exportUsedPercent(surface, usage.usedExports, totalExports)

	usedExportCount := countUsedExports(surface.Names, usage.usedExports)
	if usedExportCount == 0 && totalExports == 0 {
		usedExportCount = len(usage.usedExports)
	}

	riskCues, riskWarnings := assessRiskCues(opts.RepoPath, opts.Dependency, opts.DependencyRootPath, surface)
	warnings = append(warnings, riskWarnings...)
	license, provenance, licenseWarnings := detectLicenseAndProvenance(opts.DependencyRootPath, opts.IncludeRegistryProvenance)
	warnings = append(warnings, licenseWarnings...)

	depReport := report.DependencyReport{
		Language:             "js-ts",
		Name:                 opts.Dependency,
		UsedExportsCount:     usedExportCount,
		TotalExportsCount:    totalExports,
		UsedPercent:          usedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       buildTopSymbols(usage.counts),
		UsedImports:          usage.usedImports,
		UnusedImports:        usage.unusedImports,
		UnusedExports:        unusedExports,
		RiskCues:             riskCues,
		License:              license,
		Provenance:           provenance,
	}
	depReport.Recommendations = buildRecommendations(opts.Dependency, depReport, opts.MinUsagePercentForRecommendations)
	if opts.SuggestOnly {
		codemod, codemodWarnings := BuildSubpathCodemodReport(opts.RepoPath, opts.Dependency, opts.DependencyRootPath, opts.ScanResult)
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
	case ImportSideEffect:
		return applySideEffectImportUsage(imp, usedExports)
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
	if count := file.IdentifierUsage[imp.LocalName]; count > 0 && hasDirectIdentifierUsage(imp, file) {
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

func applySideEffectImportUsage(imp ImportBinding, usedExports map[string]struct{}) bool {
	exportName := imp.ExportName
	if exportName == "" {
		exportName = sideEffectImportName
	}
	usedExports[exportName] = struct{}{}
	return true
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

func removeOverlappingUnusedImports(unused, used []report.ImportUse) []report.ImportUse {
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

func matchesDependency(module, dependency string) bool {
	if module == dependency {
		return true
	}
	if strings.HasPrefix(module, dependency+"/") {
		return true
	}
	return false
}

func buildTopDependencies(repoPath string, scanResult ScanResult, topN int, runtimeProfile string, minUsagePercentForRecommendations int, weights report.RemovalCandidateWeights, includeRegistryProvenance bool) ([]report.DependencyReport, []string) {
	dependencies, dependencyRoots, warnings := listDependencies(repoPath, scanResult)
	if len(dependencies) == 0 {
		return nil, warnings
	}

	reports := make([]report.DependencyReport, 0, len(dependencies))
	for _, dep := range dependencies {
		depReport, depWarnings := buildDependencyReport(dependencyReportOptions{
			RepoPath:                          repoPath,
			Dependency:                        dep,
			DependencyRootPath:                dependencyRoots[dep],
			ScanResult:                        scanResult,
			RuntimeProfile:                    runtimeProfile,
			MinUsagePercentForRecommendations: minUsagePercentForRecommendations,
			SuggestOnly:                       false,
			IncludeRegistryProvenance:         includeRegistryProvenance,
		})
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}

	shared.SortReportsByWaste(reports, weights)

	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}

	return reports, warnings
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	return shared.ResolveMinUsageRecommendationThreshold(value)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	return shared.ResolveRemovalCandidateWeights(value)
}
