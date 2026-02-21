package python

import (
	"context"
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

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "python"
}

func (a *Adapter) Aliases() []string {
	return []string{"py"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	detection, err := a.DetectWithConfidence(ctx, repoPath)
	return detection.Matched, err
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	_ = ctx
	if repoPath == "" {
		repoPath = "."
	}

	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := applyPythonRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 512
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return walkPythonDetectionEntry(path, entry, roots, &detection, &visited, maxFiles)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	if detection.Matched && detection.Confidence < 35 {
		detection.Confidence = 35
	}
	if detection.Confidence > 95 {
		detection.Confidence = 95
	}
	if len(roots) == 0 && detection.Matched {
		roots[repoPath] = struct{}{}
	}
	detection.Roots = shared.SortedKeys(roots)
	return detection, nil
}

func walkPythonDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	(*visited)++
	if *visited > maxFiles {
		return fs.SkipAll
	}
	updateDetectionFromPythonFile(path, entry, roots, detection)
	return nil
}

func applyPythonRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	rootSignals := []struct {
		name       string
		confidence int
	}{
		{name: "pyproject.toml", confidence: 50},
		{name: "requirements.txt", confidence: 35},
		{name: "setup.py", confidence: 35},
	}
	for _, signal := range rootSignals {
		path := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(path); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func updateDetectionFromPythonFile(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
	switch strings.ToLower(entry.Name()) {
	case "pyproject.toml", "requirements.txt", "setup.py":
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	if strings.HasSuffix(strings.ToLower(path), ".py") {
		detection.Matched = true
		detection.Confidence += 2
	}
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

	scanResult, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	dependencies, warnings := buildRequestedPythonDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	return result, nil
}

func buildRequestedPythonDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	dependency := normalizeDependencyID(req.Dependency)
	if dependency != "" {
		depReport, warnings := buildDependencyReport(dependency, scan)
		return []report.DependencyReport{depReport}, warnings
	}
	topN := req.TopN
	if topN > 0 {
		weights := resolveRemovalCandidateWeights(req.RemovalCandidateWeights)
		return buildTopPythonDependencies(topN, scan, weights)
	}
	return nil, []string{"no dependency or top-N target provided"}
}

func buildTopPythonDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	fileUsages := pythonFileUsages(scan)
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	return shared.BuildTopReports(topN, dependencies, func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files    []fileScan
	Warnings []string
}

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	result := scanResult{}
	if repoPath == "" {
		return result, fmt.Errorf("repo path is empty")
	}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return scanPythonRepoEntry(repoPath, path, entry, &result)
	})
	if err != nil {
		return result, err
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Python files found for analysis")
	}
	return result, nil
}

func scanPythonRepoEntry(repoPath string, path string, entry fs.DirEntry, result *scanResult) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(path), ".py") {
		return nil
	}
	cleanPath, err := enforceRepoBoundary(repoPath, path)
	if err != nil {
		return err
	}
	content, relativePath, err := readPythonFile(repoPath, cleanPath)
	if err != nil {
		return err
	}
	imports := parseImports(content, relativePath, repoPath)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	return nil
}

func enforceRepoBoundary(repoPath, path string) (string, error) {
	cleanRepo := filepath.Clean(repoPath)
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, cleanRepo+string(os.PathSeparator)) || cleanPath == cleanRepo {
		return cleanPath, nil
	}
	return "", fmt.Errorf("refusing to read path outside repo: %s", path)
}

func readPythonFile(repoPath, cleanPath string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnder(repoPath, cleanPath)
	if err != nil {
		return nil, "", err
	}
	relativePath, err := filepath.Rel(repoPath, cleanPath)
	if err != nil {
		relativePath = cleanPath
	}
	return content, relativePath, nil
}

var (
	importLinePattern = regexp.MustCompile(`^\s*import\s+(.+)$`)
	fromLinePattern   = regexp.MustCompile(`^\s*from\s+([A-Za-z_][A-Za-z0-9_\.]*)\s+import\s+(.+)$`)
	pythonSkippedDirs = map[string]bool{
		"__pycache__":   true,
		".venv":         true,
		"venv":          true,
		".mypy_cache":   true,
		".pytest_cache": true,
	}
)

func parseImports(content []byte, filePath string, repoPath string) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		lineNoComment := stripComment(line)
		if strings.TrimSpace(lineNoComment) == "" {
			return nil
		}

		if matches := importLinePattern.FindStringSubmatch(lineNoComment); len(matches) == 2 {
			return parseImportLine(matches[1], filePath, repoPath, index, lineNoComment)
		}

		if matches := fromLinePattern.FindStringSubmatch(lineNoComment); len(matches) == 3 {
			return parseFromImportLine(matches[1], matches[2], filePath, repoPath, index, lineNoComment)
		}
		return nil
	})
}

func parseImportLine(partsText string, filePath string, repoPath string, index int, line string) []importBinding {
	bindings := make([]importBinding, 0)
	for _, part := range splitCSV(partsText) {
		moduleName, local := parseImportPart(part)
		if moduleName == "" {
			continue
		}
		dependency := dependencyFromModule(repoPath, moduleName)
		if dependency == "" {
			continue
		}
		if local == "" {
			local = strings.Split(moduleName, ".")[0]
		}
		bindings = append(bindings, importBinding{
			Dependency: dependency,
			Module:     moduleName,
			Name:       moduleName,
			Local:      local,
			Location:   importLocation(filePath, index, line),
		})
	}
	return bindings
}

func parseFromImportLine(moduleValue string, symbolsValue string, filePath string, repoPath string, index int, line string) []importBinding {
	moduleName := strings.TrimSpace(moduleValue)
	if strings.HasPrefix(moduleName, ".") {
		return nil
	}
	dependency := dependencyFromModule(repoPath, moduleName)
	if dependency == "" {
		return nil
	}

	bindings := make([]importBinding, 0)
	for _, part := range splitCSV(symbolsValue) {
		symbol, local := parseImportPart(part)
		if symbol == "" {
			continue
		}
		if local == "" {
			local = symbol
		}
		bindings = append(bindings, importBinding{
			Dependency: dependency,
			Module:     moduleName,
			Name:       symbol,
			Local:      local,
			Wildcard:   symbol == "*",
			Location:   importLocation(filePath, index, line),
		})
	}
	return bindings
}

func importLocation(filePath string, index int, line string) report.Location {
	return shared.LocationFromLine(filePath, index, line)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		items = append(items, part)
	}
	return items
}

func parseImportPart(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if strings.Contains(value, " as ") {
		pieces := strings.SplitN(value, " as ", 2)
		moduleName := strings.TrimSpace(pieces[0])
		local := strings.TrimSpace(pieces[1])
		return moduleName, local
	}
	return value, ""
}

func stripComment(line string) string {
	return shared.StripLineComment(line, "#")
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, pythonFileUsages(scan), normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	dep := report.DependencyReport{
		Language:             "python",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d wildcard import(s) for this dependency", stats.WildcardImports),
		})
	}
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 2)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	if shared.HasWildcardImport(dep.UsedImports) || shared.HasWildcardImport(dep.UnusedImports) {
		recs = append(recs, report.Recommendation{
			Code:      "avoid-star-imports",
			Priority:  "medium",
			Message:   "Wildcard imports were detected; prefer explicit symbol imports.",
			Rationale: "Explicit imports improve readability and analysis precision.",
		})
	}
	return recs
}

func dependencyFromModule(repoPath string, moduleName string) string {
	moduleName = strings.TrimSpace(moduleName)
	if moduleName == "" {
		return ""
	}
	root := strings.Split(moduleName, ".")[0]
	if root == "" {
		return ""
	}
	if pythonStdlib[root] {
		return ""
	}
	if isLocalModule(repoPath, root) {
		return ""
	}
	return normalizeDependencyID(root)
}

func isLocalModule(repoPath string, root string) bool {
	if _, err := os.Stat(filepath.Join(repoPath, root+".py")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(repoPath, root, "__init__.py")); err == nil {
		return true
	}
	return false
}

func normalizeDependencyID(value string) string {
	return strings.ReplaceAll(shared.NormalizeDependencyID(value), "_", "-")
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(name, pythonSkippedDirs)
}

func pythonFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(
		scan.Files,
		func(file fileScan) []shared.ImportRecord { return file.Imports },
		func(file fileScan) map[string]int { return file.Usage },
	)
}

var pythonStdlib = map[string]bool{
	"abc": true, "argparse": true, "asyncio": true, "collections": true, "contextlib": true, "copy": true,
	"csv": true, "dataclasses": true, "datetime": true, "functools": true, "hashlib": true, "http": true,
	"importlib": true, "itertools": true, "json": true, "logging": true, "math": true, "os": true,
	"pathlib": true, "queue": true, "random": true, "re": true, "socket": true, "sqlite3": true,
	"ssl": true, "statistics": true, "string": true, "subprocess": true, "sys": true, "threading": true,
	"time": true, "typing": true, "unittest": true, "urllib": true, "uuid": true, "xml": true, "zipfile": true,
}
