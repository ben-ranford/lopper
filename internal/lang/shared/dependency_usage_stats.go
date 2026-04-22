package shared

import (
	"sort"

	"github.com/ben-ranford/lopper/internal/report"
)

type DependencyStats struct {
	HasImports      bool
	UsedCount       int
	TotalCount      int
	UsedPercent     float64
	TopSymbols      []report.SymbolUsage
	UsedImports     []report.ImportUse
	UnusedImports   []report.ImportUse
	WildcardImports int
}

func BuildDependencyStats(dependency string, files []FileUsage, normalize func(string) string) DependencyStats {
	acc := newStatsAccumulator()
	for _, file := range files {
		acc.collectForFile(dependency, file, normalize)
	}
	return acc.build()
}

func BuildDependencyReportFromStats(name, languageID string, stats DependencyStats) report.DependencyReport {
	return report.DependencyReport{
		Name:              name,
		Language:          languageID,
		UsedExportsCount:  stats.UsedCount,
		TotalExportsCount: stats.TotalCount,
		UsedPercent:       stats.UsedPercent,
		TopUsedSymbols:    stats.TopSymbols,
		UsedImports:       stats.UsedImports,
		UnusedImports:     stats.UnusedImports,
	}
}

type statsAccumulator struct {
	usedImports     map[string]*report.ImportUse
	unusedImports   map[string]*report.ImportUse
	usedSymbols     map[string]struct{}
	allSymbols      map[string]struct{}
	symbolCounts    map[string]int
	wildcardImports int
}

func newStatsAccumulator() *statsAccumulator {
	return &statsAccumulator{
		usedImports:   make(map[string]*report.ImportUse),
		unusedImports: make(map[string]*report.ImportUse),
		usedSymbols:   make(map[string]struct{}),
		allSymbols:    make(map[string]struct{}),
		symbolCounts:  make(map[string]int),
	}
}

func (s *statsAccumulator) collectForFile(dependency string, file FileUsage, normalize func(string) string) {
	for _, imported := range file.Imports {
		if normalize(imported.Dependency) != dependency {
			continue
		}
		s.collectImport(file, imported)
	}
}

func (s *statsAccumulator) collectImport(file FileUsage, imported ImportRecord) {
	s.allSymbols[imported.Name] = struct{}{}
	entry := report.ImportUse{
		Name:      imported.Name,
		Module:    imported.Module,
		Locations: []report.Location{imported.Location},
	}
	if isImportUsed(file, imported) {
		s.addUsedImport(file, imported, entry)
	} else {
		addImport(s.unusedImports, entry)
	}
	if imported.Wildcard {
		s.wildcardImports++
	}
}

func isImportUsed(file FileUsage, imported ImportRecord) bool {
	return imported.Wildcard || file.Usage[imported.Local] > 0
}

func (s *statsAccumulator) addUsedImport(file FileUsage, imported ImportRecord, entry report.ImportUse) {
	s.usedSymbols[imported.Name] = struct{}{}
	count := effectiveUsageCount(file, imported)
	if count > 0 {
		s.symbolCounts[imported.Name] += count
	}
	addImport(s.usedImports, entry)
}

func effectiveUsageCount(file FileUsage, imported ImportRecord) int {
	count := file.Usage[imported.Local]
	if imported.Wildcard && count == 0 {
		return 1
	}
	return count
}

func (s *statsAccumulator) build() DependencyStats {
	usedCount := len(s.usedSymbols)
	totalCount := len(s.allSymbols)
	usedPercent := calculateUsedPercent(usedCount, totalCount)
	topSymbols := buildTopSymbols(s.symbolCounts)
	used := flattenImports(s.usedImports)
	unused := dedupeUnused(flattenImports(s.unusedImports), used)
	return DependencyStats{
		HasImports:      totalCount > 0,
		UsedCount:       usedCount,
		TotalCount:      totalCount,
		UsedPercent:     usedPercent,
		TopSymbols:      topSymbols,
		UsedImports:     used,
		UnusedImports:   unused,
		WildcardImports: s.wildcardImports,
	}
}

func calculateUsedPercent(usedCount, totalCount int) float64 {
	if totalCount == 0 {
		return 0
	}
	return (float64(usedCount) / float64(totalCount)) * 100
}

func buildTopSymbols(symbolCounts map[string]int) []report.SymbolUsage {
	topSymbols := make([]report.SymbolUsage, 0, len(symbolCounts))
	for name, count := range symbolCounts {
		topSymbols = append(topSymbols, report.SymbolUsage{Name: name, Count: count})
	}
	sort.Slice(topSymbols, func(i, j int) bool {
		if topSymbols[i].Count == topSymbols[j].Count {
			return topSymbols[i].Name < topSymbols[j].Name
		}
		return topSymbols[i].Count > topSymbols[j].Count
	})
	if len(topSymbols) > 5 {
		topSymbols = topSymbols[:5]
	}
	return topSymbols
}

func ListDependencies(files []FileUsage, normalize func(string) string) []string {
	set := make(map[string]struct{})
	for _, file := range files {
		for _, imported := range file.Imports {
			if imported.Dependency == "" {
				continue
			}
			set[normalize(imported.Dependency)] = struct{}{}
		}
	}
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func BuildTopReports(topN int, dependencies []string, buildReport func(string) (report.DependencyReport, []string), weights ...report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	reports := make([]report.DependencyReport, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dependency := range dependencies {
		depReport, depWarnings := buildReport(dependency)
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}
	SortReportsByWaste(reports, weights...)
	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}
	if len(reports) == 0 {
		warnings = append(warnings, "no dependency data available for top-N ranking")
	}
	return reports, warnings
}

// SortReportsByWaste annotates each report with a removal-candidate score before sorting.
func SortReportsByWaste(reports []report.DependencyReport, weights ...report.RemovalCandidateWeights) {
	scoringWeights := report.DefaultRemovalCandidateWeights()
	if len(weights) > 0 {
		scoringWeights = weights[0]
	}
	report.AnnotateRemovalCandidateScoresWithWeights(reports, scoringWeights)
	sort.Slice(reports, func(i, j int) bool {
		iScore, iKnown := report.RemovalCandidateScore(reports[i])
		jScore, jKnown := report.RemovalCandidateScore(reports[j])
		if iKnown != jKnown {
			return iKnown
		}
		if iScore == jScore {
			return reports[i].Name < reports[j].Name
		}
		return iScore > jScore
	})
}

func WasteScore(dep report.DependencyReport) (float64, bool) {
	if dep.TotalExportsCount == 0 {
		return -1, false
	}
	return 100 - dep.UsedPercent, true
}

func HasWildcardImport(imports []report.ImportUse) bool {
	for _, imported := range imports {
		if imported.Name == "*" {
			return true
		}
	}
	return false
}

func addImport(dest map[string]*report.ImportUse, entry report.ImportUse) {
	key := entry.Module + ":" + entry.Name
	if current, ok := dest[key]; ok {
		current.Locations = append(current.Locations, entry.Locations...)
		return
	}
	copyEntry := entry
	dest[key] = &copyEntry
}

func flattenImports(source map[string]*report.ImportUse) []report.ImportUse {
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

func dedupeUnused(unused, used []report.ImportUse) []report.ImportUse {
	usedKeys := make(map[string]struct{}, len(used))
	for _, entry := range used {
		usedKeys[entry.Module+":"+entry.Name] = struct{}{}
	}
	filtered := make([]report.ImportUse, 0, len(unused))
	for _, entry := range unused {
		if _, ok := usedKeys[entry.Module+":"+entry.Name]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
