package shared

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

var usagePatternCache sync.Map

type ImportRecord struct {
	Dependency string
	Module     string
	Name       string
	Local      string
	Location   report.Location
	Wildcard   bool
}

type FileUsage struct {
	Imports []ImportRecord
	Usage   map[string]int
}

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

func FirstContentColumn(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return i + 1
		}
	}
	return 1
}

func MapSlice[T any, R any](items []T, mapper func(T) R) []R {
	mapped := make([]R, 0, len(items))
	for _, item := range items {
		mapped = append(mapped, mapper(item))
	}
	return mapped
}

func MapFileUsages[T any](
	files []T,
	importsOf func(T) []ImportRecord,
	usageOf func(T) map[string]int,
) []FileUsage {
	return MapSlice(files, func(file T) FileUsage {
		return FileUsage{
			Imports: importsOf(file),
			Usage:   usageOf(file),
		}
	})
}

func CountUsage(content []byte, imports []ImportRecord) map[string]int {
	importCount := make(map[string]int)
	for _, imported := range imports {
		if imported.Wildcard || imported.Local == "" {
			continue
		}
		importCount[imported.Local]++
	}

	usage := make(map[string]int, len(importCount))
	text := string(content)
	for local, count := range importCount {
		pattern := usagePattern(local)
		occurrences := len(pattern.FindAllStringIndex(text, -1)) - count
		if occurrences < 0 {
			occurrences = 0
		}
		usage[local] = occurrences
	}
	return usage
}

func usagePattern(local string) *regexp.Regexp {
	if cached, ok := usagePatternCache.Load(local); ok {
		return cached.(*regexp.Regexp)
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `\b`)
	actual, _ := usagePatternCache.LoadOrStore(local, pattern)
	return actual.(*regexp.Regexp)
}

func BuildDependencyStats(dependency string, files []FileUsage, normalize func(string) string) DependencyStats {
	acc := newStatsAccumulator()
	for _, file := range files {
		acc.collectForFile(dependency, file, normalize)
	}
	return acc.build()
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

func (acc *statsAccumulator) collectForFile(dependency string, file FileUsage, normalize func(string) string) {
	for _, imported := range file.Imports {
		if normalize(imported.Dependency) != dependency {
			continue
		}
		acc.collectImport(file, imported)
	}
}

func (acc *statsAccumulator) collectImport(file FileUsage, imported ImportRecord) {
	acc.allSymbols[imported.Name] = struct{}{}
	entry := report.ImportUse{
		Name:      imported.Name,
		Module:    imported.Module,
		Locations: []report.Location{imported.Location},
	}
	if isImportUsed(file, imported) {
		acc.addUsedImport(file, imported, entry)
	} else {
		addImport(acc.unusedImports, entry)
	}
	if imported.Wildcard {
		acc.wildcardImports++
	}
}

func isImportUsed(file FileUsage, imported ImportRecord) bool {
	return imported.Wildcard || file.Usage[imported.Local] > 0
}

func (acc *statsAccumulator) addUsedImport(file FileUsage, imported ImportRecord, entry report.ImportUse) {
	acc.usedSymbols[imported.Name] = struct{}{}
	count := effectiveUsageCount(file, imported)
	if count > 0 {
		acc.symbolCounts[imported.Name] += count
	}
	addImport(acc.usedImports, entry)
}

func effectiveUsageCount(file FileUsage, imported ImportRecord) int {
	count := file.Usage[imported.Local]
	if imported.Wildcard && count == 0 {
		return 1
	}
	return count
}

func (acc *statsAccumulator) build() DependencyStats {
	usedCount := len(acc.usedSymbols)
	totalCount := len(acc.allSymbols)
	usedPercent := calculateUsedPercent(usedCount, totalCount)
	topSymbols := buildTopSymbols(acc.symbolCounts)
	used := flattenImports(acc.usedImports)
	unused := dedupeUnused(flattenImports(acc.unusedImports), used)
	return DependencyStats{
		HasImports:      totalCount > 0,
		UsedCount:       usedCount,
		TotalCount:      totalCount,
		UsedPercent:     usedPercent,
		TopSymbols:      topSymbols,
		UsedImports:     used,
		UnusedImports:   unused,
		WildcardImports: acc.wildcardImports,
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

func BuildTopReports(
	topN int,
	dependencies []string,
	buildReport func(string) (report.DependencyReport, []string),
) ([]report.DependencyReport, []string) {
	reports := make([]report.DependencyReport, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dependency := range dependencies {
		depReport, depWarnings := buildReport(dependency)
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}
	SortReportsByWaste(reports)
	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}
	if len(reports) == 0 {
		warnings = append(warnings, "no dependency data available for top-N ranking")
	}
	return reports, warnings
}

func SortReportsByWaste(reports []report.DependencyReport) {
	report.AnnotateRemovalCandidateScores(reports)
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

func SortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func DefaultRepoPath(repoPath string) string {
	if repoPath == "" {
		return "."
	}
	return repoPath
}

func NormalizeDependencyID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ShouldSkipCommonDir(name string) bool {
	_, skip := commonSkippedDirs[strings.ToLower(name)]
	return skip
}

var commonSkippedDirs = map[string]struct{}{
	".cache": {}, ".git": {}, ".hg": {}, ".idea": {}, ".next": {}, ".svn": {},
	"build": {}, "dist": {}, "node_modules": {}, "out": {}, "target": {}, "vendor": {},
}

func FinalizeDetection(repoPath string, detection language.Detection, roots map[string]struct{}) language.Detection {
	if detection.Matched && detection.Confidence < 35 {
		detection.Confidence = 35
	}
	if detection.Confidence > 95 {
		detection.Confidence = 95
	}
	if len(roots) == 0 && detection.Matched {
		roots[repoPath] = struct{}{}
	}
	detection.Roots = SortedKeys(roots)
	return detection
}

func DetectMatched(
	ctx context.Context,
	repoPath string,
	detectWithConfidence func(context.Context, string) (language.Detection, error),
) (bool, error) {
	detection, err := detectWithConfidence(ctx, repoPath)
	if err != nil {
		return false, err
	}
	return detection.Matched, nil
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
