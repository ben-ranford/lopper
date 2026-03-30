package ruby

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	rubyAdapterID   = "ruby"
	gemfileName     = "Gemfile"
	gemfileLockName = "Gemfile.lock"
	gemspecExt      = ".gemspec"
	maxDetectFiles  = 1024
)

var (
	gemDeclarationPattern       = regexp.MustCompile(`^\s*gem\s+["']([^"']+)["']`)
	gemGitOptionPattern         = regexp.MustCompile(`(?:^|[,\s])(?::?git\s*=>|git\s*:)`)
	gemPathOptionPattern        = regexp.MustCompile(`(?:^|[,\s])(?::?path\s*=>|path\s*:)`)
	gemSpecPattern              = regexp.MustCompile(`^\s{2,}([A-Za-z0-9_.-]+)\s+\(`)
	gemTopLevelSpecPattern      = regexp.MustCompile(`^\s{4}([A-Za-z0-9_.-]+)\s+\(`)
	gemspecDependencyPattern    = regexp.MustCompile(`^\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?add(?:_runtime|_development)?_dependency\s*(?:\(\s*)?["']([^"']+)["']`)
	gemspecDependencyLineSignal = regexp.MustCompile(`\badd(?:_runtime|_development)?_dependency\b`)
	requirePattern              = regexp.MustCompile(`^\s*require(_relative)?\s+["']([^"']+)["']`)
	rubySkippedDirs             = map[string]bool{
		".bundle":  true,
		"coverage": true,
	}
)

type importBinding = shared.ImportRecord

type fileScan struct {
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	DeclaredDependencies map[string]struct{}
	DeclaredSources      map[string]rubyDependencySource
	ImportedDependencies map[string]struct{}
}

type rubyDependencySource struct {
	Rubygems        bool
	Git             bool
	Path            bool
	DeclaredGemfile bool
	DeclaredLock    bool
}

const (
	rubyDependencySourceBundler  = "bundler"
	rubyDependencySourceRubygems = "rubygems"
	rubyDependencySourceGit      = "git"
	rubyDependencySourcePath     = "path"
	rubyGemfileSectionGem        = "GEM"
	rubyGemfileSectionGit        = "GIT"
	rubyGemfileSectionPath       = "PATH"
	rubyGemfileSpecsSection      = "specs:"
)

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle(rubyAdapterID, []string{"rb"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})
	rootSignals := []shared.RootSignal{
		{Name: gemfileName, Confidence: 60},
		{Name: gemfileLockName, Confidence: 30},
	}

	if err := shared.ApplyRootSignals(repoPath, rootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := walkRubyRepoFiles(ctx, repoPath, func(path string, entry fs.DirEntry) error {
		visited++
		if visited > maxDetectFiles {
			return fs.SkipAll
		}
		switch strings.ToLower(entry.Name()) {
		case strings.ToLower(gemfileName), strings.ToLower(gemfileLockName):
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), gemspecExt) {
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".rb") {
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	scan, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}

	dependencies, warnings := buildRequestedRubyDependencies(req, scan)
	result := report.Report{
		GeneratedAt:  a.Clock(),
		RepoPath:     repoPath,
		Dependencies: dependencies,
		Warnings:     append(scan.Warnings, warnings...),
	}
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	scan := scanResult{
		DeclaredDependencies: make(map[string]struct{}),
		DeclaredSources:      make(map[string]rubyDependencySource),
		ImportedDependencies: make(map[string]struct{}),
	}

	declWarnings, err := loadDeclaredDependencies(repoPath, scan.DeclaredDependencies, scan.DeclaredSources)
	if err != nil {
		return scan, err
	}
	scan.Warnings = append(scan.Warnings, declWarnings...)
	if len(scan.DeclaredDependencies) == 0 {
		scan.Warnings = append(scan.Warnings, "no gem declarations found in Gemfile, Gemfile.lock, or .gemspec files")
	}

	foundRuby := false
	err = walkRubyRepoFiles(ctx, repoPath, func(path string, entry fs.DirEntry) error {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".rb") {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}
		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		imports := parseRequires(content, relPath, scan.DeclaredDependencies)
		for _, imported := range imports {
			scan.ImportedDependencies[imported.Dependency] = struct{}{}
		}
		scan.Files = append(scan.Files, fileScan{
			Imports: imports,
			Usage:   shared.CountUsage(content, imports),
		})
		foundRuby = true
		return nil
	})
	if err != nil {
		return scan, err
	}
	if !foundRuby {
		scan.Warnings = append(scan.Warnings, "no Ruby files found for analysis")
	}
	return scan, nil
}

func walkRubyRepoFiles(ctx context.Context, repoPath string, visitFile func(path string, entry fs.DirEntry) error) error {
	return filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
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
		return visitFile(path, entry)
	})
}

func parseRequires(content []byte, filePath string, declared map[string]struct{}) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		line = shared.StripLineComment(line, "#")
		matches := requirePattern.FindStringSubmatch(line)
		if len(matches) != 3 {
			return nil
		}
		if strings.TrimSpace(matches[1]) != "" {
			return nil
		}
		module := strings.TrimSpace(matches[2])
		dependency := dependencyFromRequire(module, declared)
		if dependency == "" {
			return nil
		}
		name := module
		if slash := strings.LastIndex(name, "/"); slash >= 0 && slash+1 < len(name) {
			name = name[slash+1:]
		}
		if name == "" {
			name = dependency
		}
		return []shared.ImportRecord{{
			Dependency: dependency,
			Module:     module,
			Name:       name,
			Local:      name,
			Wildcard:   false,
			Location:   shared.LocationFromLine(filePath, index, line),
		}}
	})
}

func dependencyFromRequire(module string, declared map[string]struct{}) string {
	if module == "" {
		return ""
	}
	if strings.HasPrefix(module, ".") || strings.HasPrefix(module, "/") {
		return ""
	}
	normalizedModule := normalizeDependencyID(module)
	if _, ok := declared[normalizedModule]; ok {
		return normalizedModule
	}
	root := normalizedModule
	if slash := strings.Index(root, "/"); slash >= 0 {
		root = root[:slash]
	}
	if _, ok := declared[root]; ok {
		return root
	}
	return ""
}

func buildRequestedRubyDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopRubyDependencies)
}

func buildTopRubyDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencies := sortedDependencyUnion(scan.DeclaredDependencies, scan.ImportedDependencies)
	buildReport := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, buildReport, weights)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	importsOf := func(file fileScan) []shared.ImportRecord {
		return file.Imports
	}
	usageOf := func(file fileScan) map[string]int {
		return file.Usage
	}
	fileUsages := shared.MapFileUsages(scan.Files, importsOf, usageOf)
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)

	dependencyReport := shared.BuildDependencyReportFromStats(dependency, "ruby", stats)
	dependencyReport.Provenance = buildRubyDependencyProvenance(scan.DeclaredSources[dependency])
	if stats.WildcardImports > 0 {
		dependencyReport.RiskCues = append(dependencyReport.RiskCues, report.RiskCue{
			Code:     "dynamic-require",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d runtime require signal(s) for this gem", stats.WildcardImports),
		})
	}
	dependencyReport.Recommendations = buildRecommendations(dependencyReport)

	if stats.HasImports {
		return dependencyReport, nil
	}
	return dependencyReport, []string{fmt.Sprintf("no requires found for dependency %q", dependency)}
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 2)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-gem",
			Priority:  "high",
			Message:   fmt.Sprintf("No require usage was detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused gems add maintenance and security overhead.",
		})
	}
	if len(dep.RiskCues) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "review-runtime-requires",
			Priority:  "medium",
			Message:   "Runtime require signals were detected; manually verify usage before removal.",
			Rationale: "Runtime require loading can hide usage from static analysis.",
		})
	}
	return recs
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func sortedDependencyUnion(values ...map[string]struct{}) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for dependency := range value {
			set[dependency] = struct{}{}
		}
	}
	return shared.SortedKeys(set)
}

func loadBundlerDependencies(repoPath string, out map[string]struct{}) error {
	return loadBundlerDependenciesWithSources(repoPath, out, nil)
}

func loadBundlerDependenciesWithSources(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	if err := loadGemfileDependencies(repoPath, out, sources); err != nil {
		return err
	}
	return loadGemfileLockDependencies(repoPath, out, sources)
}

func loadDeclaredDependencies(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) ([]string, error) {
	if err := loadBundlerDependenciesWithSources(repoPath, out, sources); err != nil {
		return nil, err
	}
	return loadGemspecDependencies(repoPath, out)
}

func loadGemfileDependencies(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	content, err := readBundlerFile(repoPath, gemfileName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	for _, line := range strings.Split(string(content), "\n") {
		dependency, kind, ok := parseGemfileDependencyLine(line)
		if !ok {
			continue
		}
		addRubyDependency(out, sources, dependency, kind, gemfileName)
	}
	return nil
}

func loadGemfileLockDependencies(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	content, err := readBundlerFile(repoPath, gemfileLockName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	for _, line := range strings.Split(string(content), "\n") {
		matches := gemSpecPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			out[dependency] = struct{}{}
		}
	}
	parseGemfileLockSourceAttribution(content, out, sources)
	return nil
}

func parseGemfileDependencyLine(line string) (string, string, bool) {
	line = shared.StripLineComment(line, "#")
	matches := gemDeclarationPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return "", "", false
	}
	dependency := normalizeDependencyID(matches[1])
	if dependency == "" {
		return "", "", false
	}
	kind := rubyDependencySourceRubygems
	switch {
	case gemPathOptionPattern.MatchString(line):
		kind = rubyDependencySourcePath
	case gemGitOptionPattern.MatchString(line):
		kind = rubyDependencySourceGit
	}
	return dependency, kind, true
}

func parseGemfileLockSourceAttribution(content []byte, out map[string]struct{}, sources map[string]rubyDependencySource) {
	if sources == nil || len(content) == 0 {
		return
	}
	state := gemfileLockSourceAttributionState{}
	for _, rawLine := range strings.Split(string(content), "\n") {
		applyGemfileLockSourceAttributionLine(rawLine, &state, out, sources)
	}
}

type gemfileLockSourceAttributionState struct {
	currentKind string
	inSpecs     bool
}

func applyGemfileLockSourceAttributionLine(rawLine string, state *gemfileLockSourceAttributionState, out map[string]struct{}, sources map[string]rubyDependencySource) {
	line := strings.TrimRight(rawLine, "\r")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if !isGemfileLockTopLevelLine(line) {
		applyGemfileLockDependencyEntry(line, state, out, sources)
		return
	}

	state.currentKind = parseGemfileLockSection(trimmed)
	state.inSpecs = false
}

func isGemfileLockTopLevelLine(line string) bool {
	return !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")
}

func parseGemfileLockSection(line string) string {
	switch line {
	case rubyGemfileSectionGem:
		return rubyDependencySourceRubygems
	case rubyGemfileSectionGit:
		return rubyDependencySourceGit
	case rubyGemfileSectionPath:
		return rubyDependencySourcePath
	default:
		return ""
	}
}

func applyGemfileLockDependencyEntry(line string, state *gemfileLockSourceAttributionState, out map[string]struct{}, sources map[string]rubyDependencySource) {
	trimmed := strings.TrimSpace(line)
	if trimmed == rubyGemfileSpecsSection {
		state.inSpecs = state.currentKind != ""
		return
	}
	if !state.inSpecs {
		return
	}
	matches := gemTopLevelSpecPattern.FindStringSubmatch(trimmed)
	if len(matches) != 2 {
		return
	}
	addRubyDependency(out, sources, normalizeDependencyID(matches[1]), state.currentKind, gemfileLockName)
}

func addRubyDependency(out map[string]struct{}, sources map[string]rubyDependencySource, dependency, kind, signal string) {
	if dependency == "" {
		return
	}
	if out != nil {
		out[dependency] = struct{}{}
	}
	if sources == nil {
		return
	}
	info := sources[dependency]
	switch kind {
	case rubyDependencySourceRubygems:
		info.Rubygems = true
	case rubyDependencySourceGit:
		info.Git = true
	case rubyDependencySourcePath:
		info.Path = true
	}
	switch signal {
	case gemfileName:
		info.DeclaredGemfile = true
	case gemfileLockName:
		info.DeclaredLock = true
	}
	sources[dependency] = info
}

func buildRubyDependencyProvenance(info rubyDependencySource) *report.DependencyProvenance {
	source := rubyDependencyProvenanceSource(info)
	if source == "" {
		return nil
	}
	return &report.DependencyProvenance{
		Source:     source,
		Confidence: rubyDependencyProvenanceConfidence(info),
		Signals:    rubyDependencyProvenanceSignals(info),
	}
}

func rubyDependencyProvenanceSource(info rubyDependencySource) string {
	kinds := 0
	source := ""
	if info.Rubygems {
		kinds++
		source = rubyDependencySourceRubygems
	}
	if info.Git {
		kinds++
		source = rubyDependencySourceGit
	}
	if info.Path {
		kinds++
		source = rubyDependencySourcePath
	}
	switch kinds {
	case 0:
		return ""
	case 1:
		return source
	default:
		return rubyDependencySourceBundler
	}
}

func rubyDependencyProvenanceConfidence(info rubyDependencySource) string {
	switch {
	case info.DeclaredLock || info.Git || info.Path:
		return "high"
	case info.Rubygems:
		return "medium"
	default:
		return ""
	}
}

func rubyDependencyProvenanceSignals(info rubyDependencySource) []string {
	signals := make([]string, 0, 4)
	if rubyDependencyProvenanceSource(info) == rubyDependencySourceBundler {
		if info.Git {
			signals = append(signals, rubyDependencySourceGit)
		}
		if info.Path {
			signals = append(signals, rubyDependencySourcePath)
		}
		if info.Rubygems {
			signals = append(signals, rubyDependencySourceRubygems)
		}
	}
	if info.DeclaredGemfile {
		signals = append(signals, gemfileName)
	}
	if info.DeclaredLock {
		signals = append(signals, gemfileLockName)
	}
	return signals
}

func readBundlerFile(repoPath, filename string) ([]byte, error) {
	targetPath := filepath.Join(repoPath, filename)
	content, err := safeio.ReadFileUnder(repoPath, targetPath)
	switch {
	case err == nil:
		return content, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	default:
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}
}

func loadGemspecDependencies(repoPath string, out map[string]struct{}) ([]string, error) {
	var warnings []string
	err := walkRubyRepoFiles(context.TODO(), repoPath, func(path string, entry fs.DirEntry) error {
		if !strings.EqualFold(filepath.Ext(entry.Name()), gemspecExt) {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		fileWarnings := parseGemspecDependencies(content, filepath.ToSlash(relPath), out)
		warnings = append(warnings, fileWarnings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return warnings, nil
}

func parseGemspecDependencies(content []byte, filePath string, out map[string]struct{}) []string {
	lines := strings.Split(string(content), "\n")
	var warnings []string
	for index, line := range lines {
		line = shared.StripLineComment(line, "#")
		if !gemspecDependencyLineSignal.MatchString(line) {
			continue
		}
		matches := gemspecDependencyPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			warnings = append(warnings, fmt.Sprintf("could not confidently parse gemspec dependency declaration in %s:%d", filePath, index+1))
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			out[dependency] = struct{}{}
		}
	}
	return warnings
}

func normalizeDependencyID(value string) string {
	value = shared.NormalizeDependencyID(value)
	value = strings.ReplaceAll(value, "_", "-")
	return strings.ReplaceAll(value, ".", "-")
}

func shouldSkipDir(name string) bool {
	if shared.ShouldSkipCommonDir(name) {
		return true
	}
	return rubySkippedDirs[strings.ToLower(name)]
}
