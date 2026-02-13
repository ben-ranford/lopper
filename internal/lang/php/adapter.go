package php

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	composerJSONName = "composer.json"
	composerLockName = "composer.lock"
	maxDetectFiles   = 1024
	maxScanFiles     = 2048
)

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "php"
}

func (a *Adapter) Aliases() []string {
	return []string{"php8", "php7"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := applyPHPRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkPHPDetectionEntry(path, entry, roots, &detection, &visited, maxDetectFiles)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyPHPRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	signals := []struct {
		name       string
		confidence int
	}{
		{name: composerJSONName, confidence: 60},
		{name: composerLockName, confidence: 30},
	}
	for _, signal := range signals {
		candidate := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(candidate); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func walkPHPDetectionEntry(
	path string,
	entry fs.DirEntry,
	roots map[string]struct{},
	detection *language.Detection,
	visited *int,
	maxFiles int,
) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	*visited++
	if *visited > maxFiles {
		return fs.SkipAll
	}

	switch strings.ToLower(entry.Name()) {
	case composerJSONName, composerLockName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	}

	if strings.EqualFold(filepath.Ext(path), ".php") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
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

	composerData, composerWarnings, err := loadComposerData(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, composerWarnings...)

	scan, err := scanRepo(ctx, repoPath, composerData)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedPHPDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func buildRequestedPHPDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scan, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations))
		return []report.DependencyReport{depReport}, warnings
	case req.TopN > 0:
		return buildTopPHPDependencies(req.TopN, scan, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations))
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func resolveMinUsageRecommendationThreshold(threshold *int) int {
	if threshold != nil {
		return *threshold
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func buildTopPHPDependencies(topN int, scan scanResult, minUsagePercent int) ([]report.DependencyReport, []string) {
	dependencies := allDependencies(scan)
	if len(dependencies) == 0 {
		return nil, []string{"no dependency data available for top-N ranking"}
	}

	reports := make([]report.DependencyReport, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dependency := range dependencies {
		depReport, depWarnings := buildDependencyReport(dependency, scan, minUsagePercent)
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}
	shared.SortReportsByWaste(reports)
	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}
	return reports, warnings
}

func allDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for dep := range scan.DeclaredDependencies {
		set[dep] = struct{}{}
	}
	for _, dep := range shared.ListDependencies(phpFileUsages(scan), normalizeDependencyID) {
		set[dep] = struct{}{}
	}
	dependencies := shared.SortedKeys(set)
	return dependencies
}

func buildDependencyReport(dependency string, scan scanResult, minUsagePercent int) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, phpFileUsages(scan), normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	dep := report.DependencyReport{
		Language:             "php",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}
	if grouped := scan.GroupedImportsByDependency[dependency]; grouped > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "grouped-use-import",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d grouped PHP use import(s) for this dependency", grouped),
		})
	}
	if dynamic := scan.DynamicUsageByDependency[dependency]; dynamic > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "dynamic-loading",
			Severity: "high",
			Message:  fmt.Sprintf("found %d file(s) with dynamic/reflection usage that may hide dependency references", dynamic),
		})
	}
	dep.Recommendations = buildRecommendations(dep, minUsagePercent)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport, minUsagePercent int) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 3)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase risk and maintenance surface.",
		})
	}
	if hasRiskCue(dep.RiskCues, "grouped-use-import") {
		recs = append(recs, report.Recommendation{
			Code:      "prefer-explicit-imports",
			Priority:  "medium",
			Message:   "Grouped use imports were detected; prefer explicit imports for clearer attribution.",
			Rationale: "Explicit imports improve readability and reduce ambiguity in static analysis.",
		})
	}
	if hasRiskCue(dep.RiskCues, "dynamic-loading") {
		recs = append(recs, report.Recommendation{
			Code:      "review-dynamic-loading",
			Priority:  "high",
			Message:   "Dynamic loading/reflection patterns were detected; manually review runtime dependency usage.",
			Rationale: "Static analysis can under-report usage when class names are resolved dynamically.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent < float64(minUsagePercent) {
		recs = append(recs, report.Recommendation{
			Code:      "low-usage-dependency",
			Priority:  "medium",
			Message:   fmt.Sprintf("Dependency %q has low observed usage (%.1f%%).", dep.Name, dep.UsedPercent),
			Rationale: "Low-usage dependencies are candidates for removal or replacement.",
		})
	}
	return recs
}

func hasRiskCue(cues []report.RiskCue, code string) bool {
	for _, cue := range cues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

type scanResult struct {
	Files                      []fileScan
	Warnings                   []string
	DeclaredDependencies       map[string]struct{}
	GroupedImportsByDependency map[string]int
	DynamicUsageByDependency   map[string]int
}

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
	Dynamic bool
}

type importBinding = shared.ImportRecord

type composerData struct {
	DeclaredDependencies map[string]struct{}
	NamespaceToDep       map[string]string
	LocalNamespaces      map[string]struct{}
}

type composerManifest struct {
	Name        string            `json:"name"`
	Require     map[string]string `json:"require"`
	RequireDev  map[string]string `json:"require-dev"`
	Autoload    composerAutoload  `json:"autoload"`
	AutoloadDev composerAutoload  `json:"autoload-dev"`
}

type composerAutoload struct {
	PSR4 map[string]any `json:"psr-4"`
}

type composerLock struct {
	Packages    []composerPackage `json:"packages"`
	PackagesDev []composerPackage `json:"packages-dev"`
}

type composerPackage struct {
	Name     string           `json:"name"`
	Autoload composerAutoload `json:"autoload"`
}

type scanState struct {
	visited              int
	unresolvedNamespaces int
	foundPHP             bool
	skippedNestedPackage int
}

func scanRepo(ctx context.Context, repoPath string, composer composerData) (scanResult, error) {
	result := scanResult{
		DeclaredDependencies:       composer.DeclaredDependencies,
		GroupedImportsByDependency: make(map[string]int),
		DynamicUsageByDependency:   make(map[string]int),
	}

	resolver := newDependencyResolver(composer)
	state := scanState{}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		return scanEntry(repoPath, path, entry, resolver, &result, &state)
	})
	if err != nil && err != fs.SkipAll {
		return result, err
	}

	appendScanWarnings(&result, state)

	return result, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func scanEntry(
	repoPath string,
	path string,
	entry fs.DirEntry,
	resolver dependencyResolver,
	result *scanResult,
	state *scanState,
) error {
	if entry.IsDir() {
		return scanDirEntry(repoPath, path, entry, state)
	}
	return scanFileEntry(repoPath, path, resolver, result, state)
}

func scanDirEntry(repoPath string, path string, entry fs.DirEntry, state *scanState) error {
	if shouldSkipDir(entry.Name()) {
		return filepath.SkipDir
	}
	if path != repoPath && hasComposerManifest(path) {
		state.skippedNestedPackage++
		return filepath.SkipDir
	}
	return nil
}

func scanFileEntry(
	repoPath string,
	path string,
	resolver dependencyResolver,
	result *scanResult,
	state *scanState,
) error {
	state.visited++
	if state.visited > maxScanFiles {
		result.Warnings = append(result.Warnings, fmt.Sprintf("scan stopped after %d files to keep analysis bounded", maxScanFiles))
		return fs.SkipAll
	}
	if !strings.EqualFold(filepath.Ext(path), ".php") {
		return nil
	}
	state.foundPHP = true

	content, relPath, err := readPHPFile(repoPath, path)
	if err != nil {
		return err
	}
	imports, groupedByDep, unresolved := parseImports(content, relPath, resolver)
	usage := shared.CountUsage(content, imports)
	dynamic := hasDynamicPatterns(content)

	mergeDependencyCounts(result.GroupedImportsByDependency, groupedByDep)
	if dynamic {
		incrementDynamicUsage(result.DynamicUsageByDependency, imports)
	}
	state.unresolvedNamespaces += unresolved
	result.Files = append(result.Files, fileScan{
		Path:    relPath,
		Imports: imports,
		Usage:   usage,
		Dynamic: dynamic,
	})
	return nil
}

func mergeDependencyCounts(dest, src map[string]int) {
	for dep, count := range src {
		dest[dep] += count
	}
}

func incrementDynamicUsage(dest map[string]int, imports []importBinding) {
	for dep := range dependenciesInFile(imports) {
		dest[dep]++
	}
}

func appendScanWarnings(result *scanResult, state scanState) {
	if !state.foundPHP {
		result.Warnings = append(result.Warnings, "no PHP source files found for analysis")
	}
	if len(result.DeclaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no Composer dependencies discovered from composer.json")
	}
	if state.unresolvedNamespaces > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("unable to map %d PHP import namespace(s) to composer dependencies", state.unresolvedNamespaces))
	}
	if state.skippedNestedPackage > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d nested composer package directory(ies) while scanning", state.skippedNestedPackage))
	}
	if len(result.DynamicUsageByDependency) > 0 {
		result.Warnings = append(result.Warnings, "dynamic loading/reflection patterns detected; dependency usage may be under-reported")
	}
}

func dependenciesInFile(imports []importBinding) map[string]struct{} {
	deps := make(map[string]struct{})
	for _, imp := range imports {
		if imp.Dependency == "" {
			continue
		}
		deps[normalizeDependencyID(imp.Dependency)] = struct{}{}
	}
	return deps
}

func readPHPFile(repoPath, path string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return nil, "", err
	}
	relPath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relPath = path
	}
	return content, relPath, nil
}

func hasComposerManifest(path string) bool {
	_, err := os.Stat(filepath.Join(path, composerJSONName))
	return err == nil
}

func phpFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(
		scan.Files,
		func(file fileScan) []shared.ImportRecord { return file.Imports },
		func(file fileScan) map[string]int { return file.Usage },
	)
}

type dependencyResolver struct {
	namespaceToDep map[string]string
	localNamespace map[string]struct{}
	declared       map[string]struct{}
}

func newDependencyResolver(data composerData) dependencyResolver {
	return dependencyResolver{
		namespaceToDep: data.NamespaceToDep,
		localNamespace: data.LocalNamespaces,
		declared:       data.DeclaredDependencies,
	}
}

func (r dependencyResolver) dependencyFromModule(module string) (string, bool) {
	module = normalizeNamespace(module)
	if module == "" {
		return "", false
	}
	if r.isLocalNamespace(module) {
		return "", false
	}
	if dep := r.resolveWithPSR4(module); dep != "" {
		return dep, true
	}
	if dep := r.resolveByNamespaceHeuristic(module); dep != "" {
		return dep, true
	}
	return "", true
}

func (r dependencyResolver) isLocalNamespace(module string) bool {
	for namespace := range r.localNamespace {
		if namespace == "" {
			continue
		}
		if module == namespace || strings.HasPrefix(module, namespace+`\`) {
			return true
		}
	}
	return false
}

func (r dependencyResolver) resolveWithPSR4(module string) string {
	longest := ""
	selected := ""
	for prefix, dependency := range r.namespaceToDep {
		normalizedPrefix := normalizeNamespace(prefix)
		if normalizedPrefix == "" {
			continue
		}
		if module == normalizedPrefix || strings.HasPrefix(module, normalizedPrefix+`\`) {
			if len(normalizedPrefix) > len(longest) {
				longest = normalizedPrefix
				selected = dependency
			}
		}
	}
	return selected
}

func (r dependencyResolver) resolveByNamespaceHeuristic(module string) string {
	parts := strings.Split(module, `\`)
	if len(parts) < 2 {
		return ""
	}
	vendor := strings.ToLower(strings.TrimSpace(parts[0]))
	name := normalizePackagePart(parts[1])
	if vendor == "" || name == "" {
		return ""
	}
	candidate := normalizeDependencyID(vendor + "/" + name)
	if _, ok := r.declared[candidate]; ok {
		return candidate
	}
	return ""
}

var useStmtPattern = regexp.MustCompile(`(?ms)^\s*use\s+([^;]+);`)
var namespaceRefPattern = regexp.MustCompile(`\\?[A-Za-z_][A-Za-z0-9_]*(?:\\[A-Za-z_][A-Za-z0-9_]*)+`)

func parseImports(content []byte, filePath string, resolver dependencyResolver) ([]importBinding, map[string]int, int) {
	text := string(content)
	matches := useStmtPattern.FindAllStringSubmatchIndex(text, -1)
	imports := make([]importBinding, 0)
	groupedByDep := make(map[string]int)
	unresolved := 0

	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		statement := strings.TrimSpace(text[match[2]:match[3]])
		line := lineNumberAt(text, match[2])
		bindings, groupedDeps, unresolvedCount := parseUseStatement(statement, filePath, line, resolver)
		imports = append(imports, bindings...)
		for dep := range groupedDeps {
			groupedByDep[dep]++
		}
		unresolved += unresolvedCount
	}
	namespaceImports, unresolvedNamespaces := parseNamespaceReferences(content, filePath, resolver)
	imports = append(imports, namespaceImports...)
	unresolved += unresolvedNamespaces
	return imports, groupedByDep, unresolved
}

func parseNamespaceReferences(content []byte, filePath string, resolver dependencyResolver) ([]importBinding, int) {
	text := string(content)
	matches := namespaceRefPattern.FindAllStringIndex(text, -1)
	imports := make([]importBinding, 0)
	unresolved := 0
	seen := make(map[string]struct{})
	for _, match := range matches {
		binding, unresolvedInc, ok := parseNamespaceReference(text, match, filePath, resolver, seen)
		unresolved += unresolvedInc
		if !ok {
			continue
		}
		imports = append(imports, binding)
	}
	return imports, unresolved
}

func parseNamespaceReference(
	text string,
	match []int,
	filePath string,
	resolver dependencyResolver,
	seen map[string]struct{},
) (importBinding, int, bool) {
	module, line, local, ok := parseNamespaceReferenceMetadata(text, match)
	if !ok {
		return importBinding{}, 0, false
	}
	if isUseLine(text, line) {
		return importBinding{}, 0, false
	}
	dependency, resolved := resolver.dependencyFromModule(module)
	if dependency == "" {
		if resolved {
			return importBinding{}, 1, false
		}
		return importBinding{}, 0, false
	}
	if isDuplicateNamespaceReference(seen, module, line) {
		return importBinding{}, 0, false
	}
	return namespaceImportBinding(filePath, line, local, module, dependency), 0, true
}

func parseNamespaceReferenceMetadata(text string, match []int) (string, int, string, bool) {
	if len(match) != 2 {
		return "", 0, "", false
	}
	start := match[0]
	end := match[1]
	rawModule := strings.TrimSpace(text[start:end])
	module := normalizeNamespace(strings.TrimPrefix(rawModule, `\`))
	if module == "" {
		return "", 0, "", false
	}
	line := lineNumberAt(text, start)
	local := lastNamespaceSegment(module)
	if local == "" {
		return "", 0, "", false
	}
	return module, line, local, true
}

func isUseLine(text string, line int) bool {
	lineText := lineTextAt(text, line)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lineText)), "use ")
}

func isDuplicateNamespaceReference(seen map[string]struct{}, module string, line int) bool {
	key := module + ":" + fmt.Sprint(line)
	if _, ok := seen[key]; ok {
		return true
	}
	seen[key] = struct{}{}
	return false
}

func namespaceImportBinding(filePath string, line int, local string, module string, dependency string) importBinding {
	return newImportBinding(filePath, line, dependency, module, local, local, true)
}

func newImportBinding(
	filePath string,
	line int,
	dependency, module, local, name string,
	wildcard bool,
) importBinding {
	if name == "" {
		name = local
	}
	return importBinding{
		Dependency: dependency,
		Module:     module,
		Name:       name,
		Local:      local,
		Wildcard:   wildcard,
		Location: report.Location{
			File:   filePath,
			Line:   line,
			Column: 1,
		},
	}
}

func lineTextAt(text string, targetLine int) string {
	if targetLine <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	index := targetLine - 1
	if index < 0 || index >= len(lines) {
		return ""
	}
	return lines[index]
}

func lineNumberAt(text string, offset int) int {
	if offset <= 0 {
		return 1
	}
	line := 1
	for i := 0; i < len(text) && i < offset; i++ {
		if text[i] == '\n' {
			line++
		}
	}
	return line
}

func parseUseStatement(statement, filePath string, line int, resolver dependencyResolver) ([]importBinding, map[string]struct{}, int) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, nil, 0
	}
	if bindings, groupedDeps, unresolved, ok := parseGroupedUseStatement(statement, filePath, line, resolver); ok {
		return bindings, groupedDeps, unresolved
	}
	return parseFlatUseStatement(statement, filePath, line, resolver)
}

func parseGroupedUseStatement(
	statement, filePath string,
	line int,
	resolver dependencyResolver,
) ([]importBinding, map[string]struct{}, int, bool) {
	open := strings.Index(statement, "{")
	close := strings.LastIndex(statement, "}")
	if open < 0 || close <= open {
		return nil, nil, 0, false
	}
	base := normalizeNamespace(statement[:open])
	inside := statement[open+1 : close]
	imports, groupedDeps, unresolved := parseUseParts(strings.Split(inside, ","), base, filePath, line, resolver, true)
	return imports, groupedDeps, unresolved, true
}

func parseFlatUseStatement(statement, filePath string, line int, resolver dependencyResolver) ([]importBinding, map[string]struct{}, int) {
	imports, _, unresolved := parseUseParts(strings.Split(statement, ","), "", filePath, line, resolver, false)
	return imports, map[string]struct{}{}, unresolved
}

func parseUseParts(
	parts []string,
	base, filePath string,
	line int,
	resolver dependencyResolver,
	collectGroupedDeps bool,
) ([]importBinding, map[string]struct{}, int) {
	imports := make([]importBinding, 0)
	groupedDeps := make(map[string]struct{})
	unresolved := 0
	for _, part := range parts {
		binding, dep, ok, unresolvedImport := parseUsePart(strings.TrimSpace(part), base, filePath, line, resolver)
		if unresolvedImport {
			unresolved++
		}
		if !ok {
			continue
		}
		imports = append(imports, binding)
		if collectGroupedDeps && dep != "" {
			groupedDeps[dep] = struct{}{}
		}
	}
	return imports, groupedDeps, unresolved
}

func parseUsePart(part, base, filePath string, line int, resolver dependencyResolver) (importBinding, string, bool, bool) {
	module, local, ok := parseUsePartModuleAndLocal(part, base)
	if !ok {
		return importBinding{}, "", false, false
	}
	dependency, resolved := resolver.dependencyFromModule(module)
	if dependency == "" {
		return importBinding{}, "", false, resolved
	}

	binding := newImportBinding(filePath, line, dependency, module, local, lastNamespaceSegment(module), false)
	return binding, normalizeDependencyID(dependency), true, false
}

func parseUsePartModuleAndLocal(part, base string) (string, string, bool) {
	module, local := splitAlias(stripUseImportQualifier(part))
	if base != "" {
		module = normalizeNamespace(base + `\` + module)
	}
	module = normalizeNamespace(module)
	if module == "" {
		return "", "", false
	}
	if local == "" {
		local = lastNamespaceSegment(module)
	}
	if local == "" {
		return "", "", false
	}
	return module, local, true
}

func stripUseImportQualifier(part string) string {
	part = strings.TrimSpace(part)
	partLower := strings.ToLower(part)
	if strings.HasPrefix(partLower, "function ") {
		return strings.TrimSpace(part[len("function "):])
	}
	if strings.HasPrefix(partLower, "const ") {
		return strings.TrimSpace(part[len("const "):])
	}
	return part
}

func splitAlias(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	parts := regexp.MustCompile(`(?i)\s+as\s+`).Split(value, 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return value, ""
}

func lastNamespaceSegment(module string) string {
	module = normalizeNamespace(module)
	if module == "" {
		return ""
	}
	parts := strings.Split(module, `\`)
	return strings.TrimSpace(parts[len(parts)-1])
}

func normalizeNamespace(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, `\`)
	value = strings.TrimSuffix(value, `\`)
	return value
}

var dynamicPattern = regexp.MustCompile(`(?m)(new\s+\$[A-Za-z_]|\$[A-Za-z_][A-Za-z0-9_]*\s*::|\b(class_exists|interface_exists|trait_exists|method_exists)\s*\()`) //nolint:lll

func hasDynamicPatterns(content []byte) bool {
	return dynamicPattern.Match(content)
}

func loadComposerData(repoPath string) (composerData, []string, error) {
	data := composerData{
		DeclaredDependencies: make(map[string]struct{}),
		NamespaceToDep:       make(map[string]string),
		LocalNamespaces:      make(map[string]struct{}),
	}
	warnings := make([]string, 0)

	manifest, hasManifest, err := readComposerManifest(repoPath)
	if err != nil {
		return data, nil, err
	}
	if !hasManifest {
		warnings = append(warnings, "composer.json not found in analysis root")
	}
	if hasManifest {
		collectDeclaredDependencies(manifest, data.DeclaredDependencies)
		collectLocalNamespaces(manifest, data.LocalNamespaces)
	}

	if err := loadComposerLockMappings(repoPath, &data); err != nil {
		return data, nil, err
	}
	return data, warnings, nil
}

func readComposerManifest(repoPath string) (composerManifest, bool, error) {
	bytes, found, err := readOptionalRepoFile(repoPath, composerJSONName)
	if err != nil {
		return composerManifest{}, false, err
	}
	if !found {
		return composerManifest{}, false, nil
	}
	manifest := composerManifest{}
	if err := unmarshalRepoJSON(composerJSONName, bytes, &manifest); err != nil {
		return composerManifest{}, false, err
	}
	return manifest, true, nil
}

func collectDeclaredDependencies(manifest composerManifest, out map[string]struct{}) {
	for name := range manifest.Require {
		if dep, ok := normalizeComposerDependency(name); ok {
			out[dep] = struct{}{}
		}
	}
	for name := range manifest.RequireDev {
		if dep, ok := normalizeComposerDependency(name); ok {
			out[dep] = struct{}{}
		}
	}
}

func collectLocalNamespaces(manifest composerManifest, out map[string]struct{}) {
	for namespace := range manifest.Autoload.PSR4 {
		out[normalizeNamespace(namespace)] = struct{}{}
	}
	for namespace := range manifest.AutoloadDev.PSR4 {
		out[normalizeNamespace(namespace)] = struct{}{}
	}
}

func normalizeComposerDependency(name string) (string, bool) {
	name = normalizeDependencyID(name)
	if name == "" || name == "php" {
		return "", false
	}
	if strings.HasPrefix(name, "ext-") || strings.HasPrefix(name, "lib-") {
		return "", false
	}
	return name, true
}

func loadComposerLockMappings(repoPath string, data *composerData) error {
	bytes, found, err := readOptionalRepoFile(repoPath, composerLockName)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	lock := composerLock{}
	if err := unmarshalRepoJSON(composerLockName, bytes, &lock); err != nil {
		return err
	}
	for _, pkg := range append(lock.Packages, lock.PackagesDev...) {
		dep := normalizeDependencyID(pkg.Name)
		if dep == "" {
			continue
		}
		for namespace := range pkg.Autoload.PSR4 {
			normalized := normalizeNamespace(namespace)
			if normalized == "" {
				continue
			}
			data.NamespaceToDep[normalized] = dep
		}
	}
	return nil
}

func readOptionalRepoFile(repoPath, filename string) ([]byte, bool, error) {
	path := filepath.Join(repoPath, filename)
	bytes, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return bytes, true, nil
}

func unmarshalRepoJSON(filename string, bytes []byte, dest any) error {
	if err := json.Unmarshal(bytes, dest); err != nil {
		return fmt.Errorf("parse %s: %w", filename, err)
	}
	return nil
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.ReplaceAll(value, "_", "-")
}

func normalizePackagePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, ".", "-")
	parts := make([]rune, 0, len(value)+4)
	for i, r := range value {
		if i > 0 && r >= 'A' && r <= 'Z' && parts[len(parts)-1] != '-' {
			parts = append(parts, '-')
		}
		parts = append(parts, r)
	}
	cleaned := strings.ToLower(string(parts))
	cleaned = strings.Trim(cleaned, "-")
	cleaned = regexp.MustCompile(`-+`).ReplaceAllString(cleaned, "-")
	return cleaned
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", "node_modules", "vendor", "dist", "build", ".next", ".turbo", "coverage", "tmp", "cache":
		return true
	default:
		return false
	}
}
