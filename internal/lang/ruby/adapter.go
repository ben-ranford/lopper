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
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	gemfileName     = "Gemfile"
	gemfileLockName = "Gemfile.lock"
	maxDetectFiles  = 1024
)

var (
	gemDeclarationPattern = regexp.MustCompile(`^\s*gem\s+["']([^"']+)["']`)
	gemSpecPattern        = regexp.MustCompile(`^\s{2,}([A-Za-z0-9_.-]+)\s+\(`)
	requirePattern        = regexp.MustCompile(`^\s*require(_relative)?\s+["']([^"']+)["']`)
	rubySkippedDirs       = map[string]bool{
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
	ImportedDependencies map[string]struct{}
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "ruby"
}

func (a *Adapter) Aliases() []string {
	return []string{"rb"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
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
		switch strings.ToLower(entry.Name()) {
		case strings.ToLower(gemfileName), strings.ToLower(gemfileLockName):
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
	if err != nil && err != fs.SkipAll {
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
		ImportedDependencies: make(map[string]struct{}),
	}

	if err := loadBundlerDependencies(repoPath, scan.DeclaredDependencies); err != nil {
		return scan, err
	}
	if len(scan.DeclaredDependencies) == 0 {
		scan.Warnings = append(scan.Warnings, "no Bundler gem declarations found in Gemfile or Gemfile.lock")
	}

	foundRuby := false
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
			Wildcard:   true,
			Location:   shared.LocationFromLine(filePath, index, line),
		}}
	})
}

func dependencyFromRequire(module string, declared map[string]struct{}) string {
	if module == "" {
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
	return root
}

func buildRequestedRubyDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopRubyDependencies)
}

func buildTopRubyDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencySet := make(map[string]struct{})
	for dependency := range scan.DeclaredDependencies {
		dependencySet[dependency] = struct{}{}
	}
	for dependency := range scan.ImportedDependencies {
		dependencySet[dependency] = struct{}{}
	}
	dependencies := shared.SortedKeys(dependencySet)
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

	dependencyReport := report.DependencyReport{Name: dependency, Language: "ruby"}
	dependencyReport.UsedExportsCount = stats.UsedCount
	dependencyReport.TotalExportsCount = stats.TotalCount
	dependencyReport.UsedPercent = stats.UsedPercent
	dependencyReport.TopUsedSymbols = stats.TopSymbols
	dependencyReport.UsedImports = stats.UsedImports
	dependencyReport.UnusedImports = stats.UnusedImports
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

func loadBundlerDependencies(repoPath string, out map[string]struct{}) error {
	if err := loadGemfileDependencies(repoPath, out); err != nil {
		return err
	}
	if err := loadGemfileLockDependencies(repoPath, out); err != nil {
		return err
	}
	return nil
}

func loadGemfileDependencies(repoPath string, out map[string]struct{}) error {
	content, err := readBundlerFile(repoPath, gemfileName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		matches := gemDeclarationPattern.FindStringSubmatch(shared.StripLineComment(line, "#"))
		if len(matches) != 2 {
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			out[dependency] = struct{}{}
		}
	}
	return nil
}

func loadGemfileLockDependencies(repoPath string, out map[string]struct{}) error {
	content, err := readBundlerFile(repoPath, gemfileLockName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		matches := gemSpecPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			out[dependency] = struct{}{}
		}
	}
	return nil
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
