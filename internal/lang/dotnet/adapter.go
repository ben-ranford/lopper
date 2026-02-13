package dotnet

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	centralPackagesFile = "Directory.Packages.props"
	csharpProjectExt    = ".csproj"
	fsharpProjectExt    = ".fsproj"
	solutionFileExt     = ".sln"
	csharpSourceExt     = ".cs"
	fsharpSourceExt     = ".fs"
	maxDetectFiles      = 1024
	maxScanFiles        = 4096
)

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "dotnet"
}

func (a *Adapter) Aliases() []string {
	return []string{"csharp", "cs", "fsharp", "fs"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	if err := applyRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxDetectFiles {
			return fs.SkipAll
		}
		return updateDetection(repoPath, path, entry.Name(), &detection, roots)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)
		switch {
		case strings.EqualFold(name, centralPackagesFile):
			detection.Matched = true
			detection.Confidence += 45
			roots[repoPath] = struct{}{}
		case isProjectManifestName(lower):
			detection.Matched = true
			detection.Confidence += 55
			roots[repoPath] = struct{}{}
		case isSolutionFileName(lower):
			detection.Matched = true
			detection.Confidence += 50
			roots[repoPath] = struct{}{}
			if err := addSolutionRoots(repoPath, filepath.Join(repoPath, name), roots); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateDetection(repoPath, path, name string, detection *language.Detection, roots map[string]struct{}) error {
	lower := strings.ToLower(name)
	switch {
	case lower == strings.ToLower(centralPackagesFile):
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	case isProjectManifestName(lower):
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	case isSolutionFileName(lower):
		detection.Matched = true
		detection.Confidence += 8
		roots[filepath.Dir(path)] = struct{}{}
		if err := addSolutionRoots(repoPath, path, roots); err != nil {
			return err
		}
	case isSourceFileName(lower):
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

	scan, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedDotNetDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(scan.DeclaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no .NET package dependencies discovered from project manifests")
	}
	return result, nil
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                  []fileScan
	DeclaredDependencies   []string
	Warnings               []string
	AmbiguousByDependency  map[string]int
	UndeclaredByDependency map[string]int
	SkippedGeneratedFiles  int
	SkippedFileLimit       bool
}

func buildRequestedDotNetDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsagePercentForRecommendations := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		dep, warnings := buildDependencyReport(dependency, scan, minUsagePercentForRecommendations)
		return []report.DependencyReport{dep}, warnings
	case req.TopN > 0:
		return buildTopDotNetDependencies(req.TopN, scan, minUsagePercentForRecommendations)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopDotNetDependencies(topN int, scan scanResult, minUsagePercentForRecommendations int) ([]report.DependencyReport, []string) {
	set := make(map[string]struct{})
	for _, dep := range scan.DeclaredDependencies {
		if dep != "" {
			set[normalizeDependencyID(dep)] = struct{}{}
		}
	}
	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if imported.Dependency != "" {
				set[normalizeDependencyID(imported.Dependency)] = struct{}{}
			}
		}
	}

	dependencies := make([]string, 0, len(set))
	for dep := range set {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)

	reports := make([]report.DependencyReport, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dep := range dependencies {
		current, currentWarnings := buildDependencyReport(dep, scan, minUsagePercentForRecommendations)
		reports = append(reports, current)
		warnings = append(warnings, currentWarnings...)
	}
	shared.SortReportsByWaste(reports)
	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}
	if len(reports) == 0 {
		warnings = append(warnings, "no dependency data available for top-N ranking")
	}
	return reports, warnings
}

func buildDependencyReport(dependency string, scan scanResult, minUsagePercentForRecommendations int) (report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(
		scan.Files,
		func(file fileScan) []shared.ImportRecord { return file.Imports },
		func(file fileScan) map[string]int { return file.Usage },
	)
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)

	dep := report.DependencyReport{
		Language:          "dotnet",
		Name:              dependency,
		UsedExportsCount:  stats.UsedCount,
		TotalExportsCount: stats.TotalCount,
		UsedPercent:       stats.UsedPercent,
		TopUsedSymbols:    stats.TopSymbols,
		UsedImports:       stats.UsedImports,
		UnusedImports:     stats.UnusedImports,
	}

	ambiguousCount := scan.AmbiguousByDependency[dependency]
	undeclaredCount := scan.UndeclaredByDependency[dependency]
	warnings := make([]string, 0)
	if ambiguousCount > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "ambiguous-namespace-mapping",
			Severity: "medium",
			Message:  "namespace-to-package mapping is ambiguous for one or more imports",
		})
		warnings = append(warnings, fmt.Sprintf("dependency %q has ambiguous namespace mapping in %d import(s)", dependency, ambiguousCount))
	}
	if undeclaredCount > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "undeclared-package-usage",
			Severity: "high",
			Message:  "imports suggest package usage that is not declared in project manifests",
		})
		warnings = append(warnings, fmt.Sprintf("dependency %q appears in source imports but is not declared in project manifests", dependency))
	}
	dep.Recommendations = buildRecommendations(dep, ambiguousCount, undeclaredCount, minUsagePercentForRecommendations)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport, ambiguousCount int, undeclaredCount int, minUsagePercentForRecommendations int) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 3)
	if undeclaredCount > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "declare-dependency-explicitly",
			Priority:  "high",
			Message:   "Declare this package explicitly in project manifests to avoid transitive drift.",
			Rationale: "Source imports appear without a direct package declaration.",
		})
	}
	if ambiguousCount > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "review-namespace-mapping",
			Priority:  "medium",
			Message:   "Review namespace-to-package mapping for this dependency.",
			Rationale: "Multiple declared packages matched the same namespace prefix.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent < float64(minUsagePercentForRecommendations) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "reduce-low-usage-package-surface",
			Priority:  "low",
			Message:   "Consider reducing or replacing low-usage package references.",
			Rationale: "Only a small portion of observed imports appears used.",
		})
	}
	return recommendations
}

func resolveMinUsageRecommendationThreshold(threshold *int) int {
	if threshold != nil {
		return *threshold
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	result := scanResult{
		AmbiguousByDependency:  make(map[string]int),
		UndeclaredByDependency: make(map[string]int),
	}
	if repoPath == "" {
		return result, fs.ErrInvalid
	}

	declared, err := collectDeclaredDependencies(repoPath)
	if err != nil {
		return result, err
	}
	result.DeclaredDependencies = declared

	scanner := newRepoScanner(ctx, repoPath, newDependencyMapper(declared), &result)
	err = filepath.WalkDir(repoPath, scanner.walk)
	if err != nil && err != fs.SkipAll {
		return result, err
	}
	appendScanWarnings(&result)
	return result, nil
}

func collectDeclaredDependencies(repoPath string) ([]string, error) {
	set := make(map[string]struct{})
	err := filepath.WalkDir(repoPath, newDependencyCollector(repoPath, set).walk)
	if err != nil {
		return nil, err
	}
	if err := addAncestorCentralPackages(repoPath, set); err != nil {
		return nil, err
	}
	return sortedDependencies(set), nil
}

func parsePackageReferences(repoPath, manifestPath string) ([]string, error) {
	return parseManifestDependencies(repoPath, manifestPath, packageReferencePattern)
}

func parsePackageVersions(repoPath, manifestPath string) ([]string, error) {
	return parseManifestDependencies(repoPath, manifestPath, packageVersionPattern)
}

func parseManifestDependencies(repoPath, manifestPath string, pattern *regexp.Regexp) ([]string, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return nil, err
	}
	matches := pattern.FindAllSubmatch(content, -1)
	return captureMatches(matches), nil
}

func captureMatches(matches [][][]byte) []string {
	if len(matches) == 0 {
		return nil
	}
	set := make(map[string]struct{})
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		value := normalizeDependencyID(string(match[1]))
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	items := make([]string, 0, len(set))
	for value := range set {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func readSourceFile(repoPath, sourcePath string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnder(repoPath, sourcePath)
	if err != nil {
		return nil, "", err
	}
	relativePath, err := filepath.Rel(repoPath, sourcePath)
	if err != nil {
		relativePath = sourcePath
	}
	return content, relativePath, nil
}

type mappingMetadata struct {
	ambiguousByDependency  map[string]int
	undeclaredByDependency map[string]int
}

func parseImports(content []byte, relativePath string, mapper dependencyMapper) ([]importBinding, mappingMetadata) {
	meta := mappingMetadata{
		ambiguousByDependency:  make(map[string]int),
		undeclaredByDependency: make(map[string]int),
	}
	lines := strings.Split(string(content), "\n")
	imports := make([]importBinding, 0)

	for i, raw := range lines {
		line := stripLineComment(raw)
		if line == "" {
			continue
		}
		if binding, handled := parseCSharpImportLine(line, raw, relativePath, i+1, mapper, &meta); handled {
			if binding != nil {
				imports = append(imports, *binding)
			}
			continue
		}
		if binding := parseFSharpImportLine(line, raw, relativePath, i+1, mapper, &meta); binding != nil {
			imports = append(imports, *binding)
		}
	}

	return imports, meta
}

type repoScanner struct {
	ctx                context.Context
	repoPath           string
	mapper             dependencyMapper
	result             *scanResult
	visitedSourceFiles int
}

func newRepoScanner(ctx context.Context, repoPath string, mapper dependencyMapper, result *scanResult) repoScanner {
	return repoScanner{
		ctx:      ctx,
		repoPath: repoPath,
		mapper:   mapper,
		result:   result,
	}
}

func (s *repoScanner) walk(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if s.ctx != nil && s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	return s.scanFile(path)
}

func (s *repoScanner) scanFile(path string) error {
	if !isSourceFile(path) {
		return nil
	}
	if isGeneratedSource(path) {
		s.result.SkippedGeneratedFiles++
		return nil
	}
	s.visitedSourceFiles++
	if s.visitedSourceFiles > maxScanFiles {
		s.result.SkippedFileLimit = true
		return fs.SkipAll
	}
	content, relativePath, err := readSourceFile(s.repoPath, path)
	if err != nil {
		return err
	}
	imports, mappingMeta := parseImports(content, relativePath, s.mapper)
	usage := shared.CountUsage(content, imports)
	s.result.Files = append(s.result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   usage,
	})
	s.addMappingMeta(mappingMeta)
	return nil
}

func (s *repoScanner) addMappingMeta(meta mappingMetadata) {
	for dep, count := range meta.ambiguousByDependency {
		s.result.AmbiguousByDependency[dep] += count
	}
	for dep, count := range meta.undeclaredByDependency {
		s.result.UndeclaredByDependency[dep] += count
	}
}

func appendScanWarnings(result *scanResult) {
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no C#/F# source files found for analysis")
	}
	if result.SkippedGeneratedFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d generated source file(s)", result.SkippedGeneratedFiles))
	}
	if result.SkippedFileLimit {
		result.Warnings = append(result.Warnings, fmt.Sprintf("source scan capped at %d files", maxScanFiles))
	}
}

type dependencyCollector struct {
	repoPath string
	set      map[string]struct{}
}

func newDependencyCollector(repoPath string, set map[string]struct{}) dependencyCollector {
	return dependencyCollector{
		repoPath: repoPath,
		set:      set,
	}
}

func (c dependencyCollector) walk(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	dependencies, err := parseManifestDependenciesForEntry(c.repoPath, path, entry.Name())
	if err != nil {
		return err
	}
	addDependencies(c.set, dependencies)
	return nil
}

func parseManifestDependenciesForEntry(repoPath, path, name string) ([]string, error) {
	lower := strings.ToLower(name)
	switch {
	case isProjectManifestName(lower):
		return parsePackageReferences(repoPath, path)
	case strings.EqualFold(name, centralPackagesFile):
		return parsePackageVersions(repoPath, path)
	default:
		return nil, nil
	}
}

func addDependencies(set map[string]struct{}, dependencies []string) {
	for _, dep := range dependencies {
		set[normalizeDependencyID(dep)] = struct{}{}
	}
}

func addAncestorCentralPackages(repoPath string, set map[string]struct{}) error {
	for ancestorDir := filepath.Dir(repoPath); ancestorDir != "" && ancestorDir != filepath.Dir(ancestorDir); ancestorDir = filepath.Dir(ancestorDir) {
		path := filepath.Join(ancestorDir, centralPackagesFile)
		_, err := os.Stat(path)
		if err == nil {
			deps, parseErr := parsePackageVersions(ancestorDir, path)
			if parseErr != nil {
				return parseErr
			}
			addDependencies(set, deps)
			return nil
		}
		if os.IsNotExist(err) {
			continue
		}
		return err
	}
	return nil
}

func sortedDependencies(set map[string]struct{}) []string {
	dependencies := make([]string, 0, len(set))
	for dep := range set {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)
	return dependencies
}

func parseCSharpImportLine(
	line, raw, relativePath string,
	lineNumber int,
	mapper dependencyMapper,
	meta *mappingMetadata,
) (*importBinding, bool) {
	module, alias, ok := parseCSharpUsing(line)
	if !ok {
		return nil, false
	}
	dependency, resolved := resolveImportDependency(module, mapper, meta)
	if !resolved {
		return nil, true
	}
	name := alias
	if name == "" {
		name = lastSegment(module)
	}
	if name == "" {
		name = module
	}
	local := alias
	if local == "" {
		local = lastSegment(module)
	}
	binding := buildImportBinding(dependency, module, name, local, relativePath, lineNumber, raw)
	return &binding, true
}

func parseFSharpImportLine(
	line, raw, relativePath string,
	lineNumber int,
	mapper dependencyMapper,
	meta *mappingMetadata,
) *importBinding {
	module, ok := parseFSharpOpen(line)
	if !ok {
		return nil
	}
	dependency, resolved := resolveImportDependency(module, mapper, meta)
	if !resolved {
		return nil
	}
	name := lastSegment(module)
	if name == "" {
		name = module
	}
	binding := buildImportBinding(dependency, module, name, name, relativePath, lineNumber, raw)
	return &binding
}

func resolveImportDependency(module string, mapper dependencyMapper, meta *mappingMetadata) (string, bool) {
	dependency, ambiguous, undeclared := mapper.resolve(module)
	if dependency == "" {
		return "", false
	}
	if ambiguous {
		meta.ambiguousByDependency[dependency]++
	}
	if undeclared {
		meta.undeclaredByDependency[dependency]++
	}
	return dependency, true
}

func buildImportBinding(
	dependency, module, name, local, relativePath string,
	lineNumber int,
	raw string,
) importBinding {
	return importBinding{
		Dependency: dependency,
		Module:     module,
		Name:       name,
		Local:      local,
		Location: report.Location{
			File:   relativePath,
			Line:   lineNumber,
			Column: shared.FirstContentColumn(raw),
		},
	}
}

func parseCSharpUsing(line string) (module string, alias string, ok bool) {
	matches := csharpUsingPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return "", "", false
	}
	expression := strings.TrimSpace(matches[1])
	if expression == "" {
		return "", "", false
	}
	if strings.Contains(expression, "=") {
		parts := strings.SplitN(expression, "=", 2)
		if len(parts) != 2 {
			return "", "", false
		}
		alias = strings.TrimSpace(parts[0])
		module = strings.TrimSpace(parts[1])
		return normalizeNamespace(module), alias, true
	}
	return normalizeNamespace(expression), "", true
}

func parseFSharpOpen(line string) (module string, ok bool) {
	matches := fsharpOpenPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return "", false
	}
	module = normalizeNamespace(matches[1])
	return module, module != ""
}

func normalizeNamespace(module string) string {
	module = strings.TrimSpace(module)
	module = strings.TrimPrefix(module, "global::")
	return module
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".idea", ".vscode", "node_modules", "vendor", "bin", "obj", "dist", "build", "packages":
		return true
	default:
		return false
	}
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case csharpSourceExt, fsharpSourceExt:
		return true
	default:
		return false
	}
}

func isGeneratedSource(path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(lower, ".g.cs"),
		strings.HasSuffix(lower, ".g.i.cs"),
		strings.HasSuffix(lower, ".designer.cs"),
		strings.HasSuffix(lower, ".assemblyinfo.cs"):
		return true
	default:
		return false
	}
}

func stripLineComment(line string) string {
	if index := strings.Index(line, "//"); index >= 0 {
		line = line[:index]
	}
	return strings.TrimSpace(line)
}

func lastSegment(value string) string {
	if value == "" {
		return ""
	}
	parts := strings.Split(value, ".")
	return strings.TrimSpace(parts[len(parts)-1])
}

func normalizeDependencyID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

type dependencyMapper struct {
	declared []string
}

func newDependencyMapper(declared []string) dependencyMapper {
	return dependencyMapper{declared: declared}
}

type dependencyCandidate struct {
	id    string
	score int
}

func (m dependencyMapper) resolve(module string) (dependency string, ambiguous bool, undeclared bool) {
	module = normalizeNamespace(module)
	if module == "" {
		return "", false, false
	}
	if strings.HasPrefix(strings.ToLower(module), "system.") || strings.EqualFold(module, "system") {
		return "", false, false
	}

	moduleID := normalizeDependencyID(module)
	candidates := make([]dependencyCandidate, 0)
	for _, dep := range m.declared {
		score := matchScore(moduleID, dep)
		if score > 0 {
			candidates = append(candidates, dependencyCandidate{id: dep, score: score})
		}
	}
	if len(candidates) == 0 {
		return fallbackDependency(moduleID), false, true
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > 1 && candidates[0].score == candidates[1].score {
		return candidates[0].id, true, false
	}
	return candidates[0].id, false, false
}

func matchScore(module, dependency string) int {
	if module == dependency {
		return 100
	}
	if strings.HasPrefix(module, dependency+".") {
		return 90
	}
	if strings.HasPrefix(dependency, module+".") {
		return 75
	}

	moduleFirst := firstSegment(module)
	dependencyFirst := firstSegment(dependency)
	if moduleFirst != "" && moduleFirst == dependencyFirst {
		return 60
	}
	if lastSegment(module) == lastSegment(dependency) && lastSegment(module) != "" {
		return 50
	}
	if strings.Contains(module, dependency) || strings.Contains(dependency, module) {
		return 40
	}
	return 0
}

func isProjectManifestName(lowerName string) bool {
	return strings.HasSuffix(lowerName, csharpProjectExt) || strings.HasSuffix(lowerName, fsharpProjectExt)
}

func isSolutionFileName(lowerName string) bool {
	return strings.HasSuffix(lowerName, solutionFileExt)
}

func isSourceFileName(lowerName string) bool {
	return strings.HasSuffix(lowerName, csharpSourceExt) || strings.HasSuffix(lowerName, fsharpSourceExt)
}

func fallbackDependency(module string) string {
	segments := strings.Split(module, ".")
	if len(segments) == 0 {
		return module
	}
	if len(segments) > 1 {
		return strings.ToLower(strings.Join(segments[:2], "."))
	}
	return strings.ToLower(segments[0])
}

func firstSegment(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func addSolutionRoots(repoPath string, solutionPath string, roots map[string]struct{}) error {
	content, err := safeio.ReadFileUnder(repoPath, solutionPath)
	if err != nil {
		return err
	}
	matches := solutionProjectPattern.FindAllSubmatch(content, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		relPath := strings.TrimSpace(string(match[1]))
		if relPath == "" {
			continue
		}
		relPath = strings.ReplaceAll(relPath, "\\", string(filepath.Separator))
		projectPath := filepath.Clean(filepath.Join(filepath.Dir(solutionPath), relPath))
		roots[filepath.Dir(projectPath)] = struct{}{}
	}
	return nil
}

var (
	packageReferencePattern = regexp.MustCompile(`(?is)<PackageReference\b[^>]*\bInclude\s*=\s*["']([^"']+)["']`)
	packageVersionPattern   = regexp.MustCompile(`(?is)<PackageVersion\b[^>]*\bInclude\s*=\s*["']([^"']+)["']`)
	csharpUsingPattern      = regexp.MustCompile(`^\s*(?:global\s+)?using\s+(?:static\s+)?([^;]+);`)
	fsharpOpenPattern       = regexp.MustCompile(`^\s*open\s+([A-Za-z_][A-Za-z0-9_\.]*)`)
	solutionProjectPattern  = regexp.MustCompile(`Project\([^\)]*\)\s*=\s*"[^"]+"\s*,\s*"([^"]+\.(?:csproj|fsproj))"`)
)
