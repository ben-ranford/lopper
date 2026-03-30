package cpp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedCPPDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	weights := resolveRemovalCandidateWeights(req.RemovalCandidateWeights)
	switch {
	case req.Dependency != "":
		dependency := shared.NormalizeDependencyID(req.Dependency)
		dep, warnings := buildDependencyReport(dependency, scan, true)
		return []report.DependencyReport{dep}, warnings
	case req.TopN > 0:
		return buildTopCPPDependencies(req.TopN, scan, weights)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopCPPDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencySet := make(map[string]struct{})
	for _, dependency := range scan.Catalog.list() {
		dependencySet[dependency] = struct{}{}
	}
	for _, file := range scan.Files {
		for _, include := range file.Includes {
			if include.Dependency != "" {
				dependencySet[shared.NormalizeDependencyID(include.Dependency)] = struct{}{}
			}
		}
	}

	dependencies := shared.SortedKeys(dependencySet)
	if len(dependencies) == 0 {
		return nil, []string{"no dependency data available for top-N ranking"}
	}
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, false)
	}
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func buildDependencyReport(dependency string, scan scanResult, warnOnNoUsage bool) (report.DependencyReport, []string) {
	reportData := report.DependencyReport{Name: dependency, Language: "cpp"}
	declared := scan.Catalog.contains(dependency)

	usage := collectDependencyUsage(dependency, scan.Files)
	usage.apply(&reportData)

	warnings := buildDependencyUsageWarnings(dependency, scan.Catalog, declared, reportData.TotalExportsCount, warnOnNoUsage)
	addUndeclaredUsageSignals(&reportData, dependency, declared, &warnings)
	return reportData, warnings
}

type dependencyUsageSummary struct {
	usedByHeader        map[string]int
	usedImportsByHeader map[string]*report.ImportUse
}

func collectDependencyUsage(dependency string, files []fileScan) dependencyUsageSummary {
	summary := dependencyUsageSummary{
		usedByHeader:        make(map[string]int),
		usedImportsByHeader: make(map[string]*report.ImportUse),
	}

	for _, file := range files {
		for _, include := range file.Includes {
			if shared.NormalizeDependencyID(include.Dependency) != dependency {
				continue
			}
			summary.usedByHeader[include.Header]++
			entry, ok := summary.usedImportsByHeader[include.Header]
			if !ok {
				entry = &report.ImportUse{Name: include.Header, Module: include.Header}
				summary.usedImportsByHeader[include.Header] = entry
			}
			entry.Locations = append(entry.Locations, include.Location)
		}
	}

	return summary
}

func (s *dependencyUsageSummary) apply(reportData *report.DependencyReport) {
	headers := sortedCountKeys(s.usedByHeader)
	reportData.TotalExportsCount = len(headers)
	reportData.UsedExportsCount = len(headers)
	if reportData.TotalExportsCount > 0 {
		reportData.UsedPercent = 100
	}
	reportData.TopUsedSymbols = buildTopUsedSymbols(s.usedByHeader)
	reportData.UsedImports = flattenImportUses(s.usedImportsByHeader, headers)
}

func buildDependencyUsageWarnings(dependency string, catalog dependencyCatalog, declared bool, totalExports int, warnOnNoUsage bool) []string {
	if totalExports > 0 || !warnOnNoUsage {
		return nil
	}
	if !declared {
		return []string{fmt.Sprintf("no mapped include usage found for dependency %s", dependency)}
	}

	sources := catalog.sources(dependency)
	if len(sources) == 0 {
		return []string{fmt.Sprintf("no mapped include usage found for dependency %s", dependency)}
	}
	return []string{fmt.Sprintf("dependency %s is declared in %s but has no mapped include usage", dependency, strings.Join(sources, " + "))}
}

func addUndeclaredUsageSignals(reportData *report.DependencyReport, dependency string, declared bool, warnings *[]string) {
	if declared || reportData.TotalExportsCount == 0 {
		return
	}
	reportData.RiskCues = append(reportData.RiskCues, report.RiskCue{
		Code:     "undeclared-package-usage",
		Severity: "high",
		Message:  "include evidence suggests package usage that is not declared in vcpkg or Conan manifests",
	})
	reportData.Recommendations = append(reportData.Recommendations, report.Recommendation{
		Code:      "declare-dependency-explicitly",
		Priority:  "high",
		Message:   "Declare this native package explicitly in vcpkg or Conan manifests.",
		Rationale: "Include evidence was found without a matching package-manager declaration.",
	})
	*warnings = append(*warnings, fmt.Sprintf("dependency %q appears in includes but is not declared in vcpkg or Conan manifests", dependency))
}

func buildTopUsedSymbols(usage map[string]int) []report.SymbolUsage {
	symbols := make([]report.SymbolUsage, 0, len(usage))
	for name, count := range usage {
		symbols = append(symbols, report.SymbolUsage{Name: name, Module: name, Count: count})
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Count == symbols[j].Count {
			return symbols[i].Name < symbols[j].Name
		}
		return symbols[i].Count > symbols[j].Count
	})
	if len(symbols) > 5 {
		symbols = symbols[:5]
	}
	return symbols
}

func flattenImportUses(imports map[string]*report.ImportUse, orderedKeys []string) []report.ImportUse {
	items := make([]report.ImportUse, 0, len(imports))
	for _, key := range orderedKeys {
		if current, ok := imports[key]; ok {
			items = append(items, *current)
		}
	}
	return items
}

func sortedCountKeys(values map[string]int) []string {
	items := make([]string, 0, len(values))
	for name := range values {
		items = append(items, name)
	}
	sort.Strings(items)
	return items
}
