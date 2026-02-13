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
		switch {
		case strings.EqualFold(name, centralPackagesFile):
			detection.Matched = true
			detection.Confidence += 45
			roots[repoPath] = struct{}{}
		case strings.HasSuffix(strings.ToLower(name), ".csproj"), strings.HasSuffix(strings.ToLower(name), ".fsproj"):
			detection.Matched = true
			detection.Confidence += 55
			roots[repoPath] = struct{}{}
		case strings.HasSuffix(strings.ToLower(name), ".sln"):
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

func updateDetection(repoPath string, path string, name string, detection *language.Detection, roots map[string]struct{}) error {
	lower := strings.ToLower(name)
	switch {
	case lower == strings.ToLower(centralPackagesFile):
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	case strings.HasSuffix(lower, ".csproj"), strings.HasSuffix(lower, ".fsproj"):
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	case strings.HasSuffix(lower, ".sln"):
		detection.Matched = true
		detection.Confidence += 8
		roots[filepath.Dir(path)] = struct{}{}
		if err := addSolutionRoots(repoPath, path, roots); err != nil {
			return err
		}
	case strings.HasSuffix(lower, ".cs"), strings.HasSuffix(lower, ".fs"):
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

	mapper := newDependencyMapper(declared)
	visitedSourceFiles := 0
	err = filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
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
		if !isSourceFile(path) {
			return nil
		}
		if isGeneratedSource(path) {
			result.SkippedGeneratedFiles++
			return nil
		}
		visitedSourceFiles++
		if visitedSourceFiles > maxScanFiles {
			result.SkippedFileLimit = true
			return fs.SkipAll
		}
		content, relativePath, err := readSourceFile(repoPath, path)
		if err != nil {
			return err
		}
		imports, mappingMeta := parseImports(content, relativePath, mapper)
		usage := shared.CountUsage(content, imports)
		result.Files = append(result.Files, fileScan{
			Path:    relativePath,
			Imports: imports,
			Usage:   usage,
		})
		for dep, count := range mappingMeta.ambiguousByDependency {
			result.AmbiguousByDependency[dep] += count
		}
		for dep, count := range mappingMeta.undeclaredByDependency {
			result.UndeclaredByDependency[dep] += count
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return result, err
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no C#/F# source files found for analysis")
	}
	if result.SkippedGeneratedFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d generated source file(s)", result.SkippedGeneratedFiles))
	}
	if result.SkippedFileLimit {
		result.Warnings = append(result.Warnings, fmt.Sprintf("source scan capped at %d files", maxScanFiles))
	}
	return result, nil
}

func collectDeclaredDependencies(repoPath string) ([]string, error) {
	set := make(map[string]struct{})

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		lower := strings.ToLower(entry.Name())
		switch {
		case strings.HasSuffix(lower, ".csproj"), strings.HasSuffix(lower, ".fsproj"):
			deps, parseErr := parsePackageReferences(repoPath, path)
			if parseErr != nil {
				return parseErr
			}
			for _, dep := range deps {
				set[normalizeDependencyID(dep)] = struct{}{}
			}
		case strings.EqualFold(entry.Name(), centralPackagesFile):
			deps, parseErr := parsePackageVersions(repoPath, path)
			if parseErr != nil {
				return parseErr
			}
			for _, dep := range deps {
				set[normalizeDependencyID(dep)] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ancestorDir := filepath.Dir(repoPath)
	for ancestorDir != "" && ancestorDir != filepath.Dir(ancestorDir) {
		path := filepath.Join(ancestorDir, centralPackagesFile)
		_, statErr := os.Stat(path)
		if statErr == nil {
			deps, parseErr := parsePackageVersions(ancestorDir, path)
			if parseErr != nil {
				return nil, parseErr
			}
			for _, dep := range deps {
				set[normalizeDependencyID(dep)] = struct{}{}
			}
			break
		}
		if os.IsNotExist(statErr) {
			ancestorDir = filepath.Dir(ancestorDir)
			continue
		}
		return nil, statErr
	}

	dependencies := make([]string, 0, len(set))
	for dep := range set {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)
	return dependencies, nil
}

func parsePackageReferences(repoPath string, manifestPath string) ([]string, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return nil, err
	}
	matches := packageReferencePattern.FindAllSubmatch(content, -1)
	return captureMatches(matches), nil
}

func parsePackageVersions(repoPath string, manifestPath string) ([]string, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return nil, err
	}
	matches := packageVersionPattern.FindAllSubmatch(content, -1)
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

func readSourceFile(repoPath string, sourcePath string) ([]byte, string, error) {
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
		if module, alias, ok := parseCSharpUsing(line); ok {
			dependency, ambiguous, undeclared := mapper.resolve(module)
			if dependency == "" {
				continue
			}
			if ambiguous {
				meta.ambiguousByDependency[dependency]++
			}
			if undeclared {
				meta.undeclaredByDependency[dependency]++
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
			imports = append(imports, importBinding{
				Dependency: dependency,
				Module:     module,
				Name:       name,
				Local:      local,
				Location: report.Location{
					File:   relativePath,
					Line:   i + 1,
					Column: shared.FirstContentColumn(raw),
				},
			})
			continue
		}

		module, ok := parseFSharpOpen(line)
		if !ok {
			continue
		}
		dependency, ambiguous, undeclared := mapper.resolve(module)
		if dependency == "" {
			continue
		}
		if ambiguous {
			meta.ambiguousByDependency[dependency]++
		}
		if undeclared {
			meta.undeclaredByDependency[dependency]++
		}
		name := lastSegment(module)
		if name == "" {
			name = module
		}
		imports = append(imports, importBinding{
			Dependency: dependency,
			Module:     module,
			Name:       name,
			Local:      name,
			Location: report.Location{
				File:   relativePath,
				Line:   i + 1,
				Column: shared.FirstContentColumn(raw),
			},
		})
	}

	return imports, meta
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
	case ".cs", ".fs":
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

func matchScore(module string, dependency string) int {
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
