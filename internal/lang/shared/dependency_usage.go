package shared

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

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
	for _, elem := range items {
		mapped = append(mapped, mapper(elem))
	}
	return mapped
}

func MapFileUsages[T any](files []T, importsOf func(T) []ImportRecord, usageOf func(T) map[string]int) []FileUsage {
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
	scannable := MaskCommentsAndStringsWithProfile(content, inferMaskProfile(imports))
	scanTokenUsage(scannable, importCount, usage)
	declarationTokenHits := countDeclarationTokenHits(scannable, imports)
	for local := range importCount {
		occurrences := usage[local] - declarationTokenHits[local]
		if occurrences < 0 {
			occurrences = 0
		}
		usage[local] = occurrences
	}
	return usage
}

func scanTokenUsage(content []byte, importCount map[string]int, usage map[string]int) {
	for i := 0; i < len(content); {
		if !isWordByte(content[i]) {
			i++
			continue
		}
		start := i
		for i < len(content) && isWordByte(content[i]) {
			i++
		}
		token := string(content[start:i])
		if _, ok := importCount[token]; ok {
			usage[token]++
		}
	}
}

func countDeclarationTokenHits(content []byte, imports []ImportRecord) map[string]int {
	lineStarts := lineStartOffsets(content)
	tokenHits := make(map[string]int)
	for _, imported := range imports {
		if imported.Wildcard || imported.Local == "" {
			continue
		}
		if imported.Location.Line <= 0 {
			// Preserve the legacy "subtract the import declaration once" behavior
			// when callers do not provide precise source locations.
			tokenHits[imported.Local]++
			continue
		}
		if declarationLineContainsToken(content, lineStarts, imported.Location.Line, imported.Local) {
			tokenHits[imported.Local]++
		}
	}
	return tokenHits
}

func declarationLineContainsToken(content []byte, lineStarts []int, line int, token string) bool {
	if line <= 0 || line > len(lineStarts) {
		return false
	}
	lineStart := lineStarts[line-1]
	lineEnd := len(content)
	if line < len(lineStarts) {
		lineEnd = lineStarts[line] - 1
	}
	if lineStart < 0 || lineStart >= lineEnd || lineEnd > len(content) {
		return false
	}
	return containsWordToken(content[lineStart:lineEnd], token)
}

func lineStartOffsets(content []byte) []int {
	starts := make([]int, 0, 64)
	starts = append(starts, 0)
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' && i+1 < len(content) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func containsWordToken(content []byte, token string) bool {
	for i := 0; i < len(content); {
		if !isWordByte(content[i]) {
			i++
			continue
		}
		start := i
		for i < len(content) && isWordByte(content[i]) {
			i++
		}
		if string(content[start:i]) == token {
			return true
		}
	}
	return false
}

type maskProfile struct {
	lineSlashSlash bool
	lineHash       bool
	blockSlashStar bool
	singleQuote    bool
	doubleQuote    bool
	backtickQuote  bool
}

var defaultMaskProfile = maskProfile{
	lineSlashSlash: true,
	lineHash:       true,
	blockSlashStar: true,
	singleQuote:    true,
	doubleQuote:    true,
	backtickQuote:  true,
}

func inferMaskProfile(imports []ImportRecord) maskProfile {
	for _, imported := range imports {
		if imported.Location.File == "" {
			continue
		}
		return maskProfileForFile(imported.Location.File)
	}
	return defaultMaskProfile
}

func maskProfileForFile(filePath string) maskProfile {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".py", ".pyi":
		return maskProfile{
			lineHash:    true,
			singleQuote: true,
			doubleQuote: true,
		}
	case ".rs":
		return maskProfile{
			lineSlashSlash: true,
			blockSlashStar: true,
			singleQuote:    true,
			doubleQuote:    true,
		}
	case ".swift", ".kt", ".kts", ".fs", ".fsx":
		return maskProfile{
			lineSlashSlash: true,
			blockSlashStar: true,
			singleQuote:    true,
			doubleQuote:    true,
		}
	case ".rb":
		return maskProfile{
			lineHash:      true,
			singleQuote:   true,
			doubleQuote:   true,
			backtickQuote: true,
		}
	default:
		return defaultMaskProfile
	}
}

// MaskCommentsAndStrings blanks comment and string literal content while
// preserving newlines and byte offsets for line/column calculations.
func MaskCommentsAndStrings(content []byte) []byte {
	return MaskCommentsAndStringsWithProfile(content, defaultMaskProfile)
}

func MaskCommentsAndStringsForFile(content []byte, filePath string) []byte {
	return MaskCommentsAndStringsWithProfile(content, maskProfileForFile(filePath))
}

func MaskCommentsAndStringsWithProfile(content []byte, profile maskProfile) []byte {
	if !containsMaskableSyntax(content, profile) {
		return content
	}

	masked := make([]byte, len(content))
	copy(masked, content)

	state := scannerStateCode
	for i := 0; i < len(masked); {
		i, state = advanceMasking(masked, i, state, profile)
	}
	return masked
}

func containsMaskableSyntax(content []byte, profile maskProfile) bool {
	for i := 0; i < len(content); i++ {
		switch {
		case profile.singleQuote && content[i] == '\'':
			return true
		case profile.doubleQuote && content[i] == '"':
			return true
		case profile.backtickQuote && content[i] == '`':
			return true
		case profile.lineHash && content[i] == '#':
			return true
		case profile.lineSlashSlash && hasBytePrefix(content, i, '/', '/'):
			return true
		case profile.blockSlashStar && hasBytePrefix(content, i, '/', '*'):
			return true
		}
	}
	return false
}

func advanceMasking(content []byte, index int, state scannerState, profile maskProfile) (int, scannerState) {
	switch state {
	case scannerStateCode:
		return scanCode(content, index, profile)
	case scannerStateLineComment:
		return scanLineComment(content, index)
	case scannerStateBlockComment:
		return scanBlockComment(content, index)
	case scannerStateSingleQuote:
		return scanQuoted(content, index, '\'', scannerStateSingleQuote)
	case scannerStateDoubleQuote:
		return scanQuoted(content, index, '"', scannerStateDoubleQuote)
	case scannerStateBacktick:
		return scanQuoted(content, index, '`', scannerStateBacktick)
	default:
		return index + 1, scannerStateCode
	}
}

func scanCode(content []byte, index int, profile maskProfile) (int, scannerState) {
	if profile.lineSlashSlash && hasBytePrefix(content, index, '/', '/') {
		maskNonNewline(content, index)
		maskNonNewline(content, index+1)
		return index + 2, scannerStateLineComment
	}
	if profile.blockSlashStar && hasBytePrefix(content, index, '/', '*') {
		maskNonNewline(content, index)
		maskNonNewline(content, index+1)
		return index + 2, scannerStateBlockComment
	}
	ch := content[index]
	if profile.lineHash && ch == '#' {
		maskNonNewline(content, index)
		return index + 1, scannerStateLineComment
	}
	if profile.singleQuote && ch == '\'' {
		maskNonNewline(content, index)
		return index + 1, scannerStateSingleQuote
	}
	if profile.doubleQuote && ch == '"' {
		maskNonNewline(content, index)
		return index + 1, scannerStateDoubleQuote
	}
	if profile.backtickQuote && ch == '`' {
		maskNonNewline(content, index)
		return index + 1, scannerStateBacktick
	}
	return index + 1, scannerStateCode
}

func scanLineComment(content []byte, index int) (int, scannerState) {
	if content[index] == '\n' || content[index] == '\r' {
		return index + 1, scannerStateCode
	}
	maskNonNewline(content, index)
	return index + 1, scannerStateLineComment
}

func scanBlockComment(content []byte, index int) (int, scannerState) {
	if hasBytePrefix(content, index, '*', '/') {
		maskNonNewline(content, index)
		maskNonNewline(content, index+1)
		return index + 2, scannerStateCode
	}
	maskNonNewline(content, index)
	return index + 1, scannerStateBlockComment
}

func scanQuoted(content []byte, index int, delimiter byte, state scannerState) (int, scannerState) {
	ch := content[index]
	maskNonNewline(content, index)
	if ch == '\\' && index+1 < len(content) {
		maskNonNewline(content, index+1)
		return index + 2, state
	}
	if ch == delimiter {
		return index + 1, scannerStateCode
	}
	return index + 1, state
}

func hasBytePrefix(content []byte, index int, first, second byte) bool {
	return index+1 < len(content) && content[index] == first && content[index+1] == second
}

type scannerState uint8

const (
	scannerStateCode scannerState = iota
	scannerStateLineComment
	scannerStateBlockComment
	scannerStateSingleQuote
	scannerStateDoubleQuote
	scannerStateBacktick
)

func maskNonNewline(content []byte, index int) {
	if content[index] == '\n' || content[index] == '\r' {
		return
	}
	content[index] = ' '
}

// isWordByte implements an ASCII-only token scanner for import local names.
// It intentionally treats '$' as part of identifiers (common in JS/PHP), so
// tokens such as "$foo" and "foo$bar" can be matched. Non-ASCII bytes are not
// considered word characters and therefore split/ignore Unicode identifiers.
func isWordByte(ch byte) bool {
	return ch == '$' || ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
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

func BuildRequestedDependencies[S any](req language.Request, scan S, normalizeDependencyID func(string) string, buildDependency func(string, S) (report.DependencyReport, []string), buildTop func(int, S) ([]report.DependencyReport, []string)) ([]report.DependencyReport, []string) {
	if req.TopN > 0 {
		return buildTop(req.TopN, scan)
	}
	dependency := normalizeDependencyID(req.Dependency)
	if dependency == "" {
		return nil, []string{"no dependency or top-N target provided"}
	}
	depReport, warnings := buildDependency(dependency, scan)
	return []report.DependencyReport{depReport}, warnings
}

func BuildRequestedDependenciesWithWeights[S any](req language.Request, scan S, normalizeDependencyID func(string) string, buildDependency func(string, S) (report.DependencyReport, []string), resolveWeights func(*report.RemovalCandidateWeights) report.RemovalCandidateWeights, buildTop func(int, S, report.RemovalCandidateWeights) ([]report.DependencyReport, []string)) ([]report.DependencyReport, []string) {
	buildTopWithWeights := func(topN int, current S) ([]report.DependencyReport, []string) {
		return buildTop(topN, current, resolveWeights(req.RemovalCandidateWeights))
	}
	return BuildRequestedDependencies(req, scan, normalizeDependencyID, buildDependency, buildTopWithWeights)
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

// FinalizeDetection applies shared post-processing for language detection.
// It enforces a confidence floor (35) only when matched, always caps confidence at 95,
// and ensures matched detections have at least one root before returning sorted roots.
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

func DetectMatched(ctx context.Context, repoPath string, detectWithConfidence func(context.Context, string) (language.Detection, error)) (bool, error) {
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
