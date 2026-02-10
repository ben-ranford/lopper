package python

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

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
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
	if err != nil {
		return false, err
	}
	return detection.Matched, nil
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	_ = ctx
	if repoPath == "" {
		repoPath = "."
	}

	detection := language.Detection{}
	roots := make(map[string]struct{})

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
			return language.Detection{}, err
		}
	}

	const maxFiles = 512
	visited := 0
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

		visited++
		if visited > maxFiles {
			return fs.SkipAll
		}

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
		return nil
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
	detection.Roots = sortedKeys(roots)
	return detection, nil
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

	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scanResult)
		result.Dependencies = []report.DependencyReport{depReport}
		result.Warnings = append(result.Warnings, warnings...)
		result.Summary = report.ComputeSummary(result.Dependencies)
	case req.TopN > 0:
		dependencies := listDependencies(scanResult)
		reports := make([]report.DependencyReport, 0, len(dependencies))
		for _, dependency := range dependencies {
			depReport, warnings := buildDependencyReport(dependency, scanResult)
			reports = append(reports, depReport)
			result.Warnings = append(result.Warnings, warnings...)
		}
		sort.Slice(reports, func(i, j int) bool {
			iScore, iKnown := wasteScore(reports[i])
			jScore, jKnown := wasteScore(reports[j])
			if iKnown != jKnown {
				return iKnown
			}
			if iScore == jScore {
				return reports[i].Name < reports[j].Name
			}
			return iScore > jScore
		})
		if req.TopN > 0 && req.TopN < len(reports) {
			reports = reports[:req.TopN]
		}
		result.Dependencies = reports
		if len(reports) == 0 {
			result.Warnings = append(result.Warnings, "no dependency data available for top-N ranking")
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	default:
		result.Warnings = append(result.Warnings, "no dependency or top-N target provided")
	}

	return result, nil
}

type importBinding struct {
	Dependency string
	Module     string
	Name       string
	Local      string
	Location   report.Location
	Wildcard   bool
}

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
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".py") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(repoPath, path)
		if err != nil {
			relativePath = path
		}
		imports := parseImports(content, relativePath, repoPath)
		result.Files = append(result.Files, fileScan{
			Path:    relativePath,
			Imports: imports,
			Usage:   countUsage(content, imports),
		})
		return nil
	})
	if err != nil {
		return result, err
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Python files found for analysis")
	}
	return result, nil
}

var (
	importLinePattern = regexp.MustCompile(`^\s*import\s+(.+)$`)
	fromLinePattern   = regexp.MustCompile(`^\s*from\s+([A-Za-z_][A-Za-z0-9_\.]*)\s+import\s+(.+)$`)
)

func parseImports(content []byte, filePath string, repoPath string) []importBinding {
	lines := strings.Split(string(content), "\n")
	bindings := make([]importBinding, 0)

	for index, line := range lines {
		lineNoComment := stripComment(line)
		if strings.TrimSpace(lineNoComment) == "" {
			continue
		}

		if matches := importLinePattern.FindStringSubmatch(lineNoComment); len(matches) == 2 {
			parts := splitCSV(matches[1])
			for _, part := range parts {
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
					Location: report.Location{
						File:   filePath,
						Line:   index + 1,
						Column: firstContentColumn(lineNoComment),
					},
				})
			}
			continue
		}

		if matches := fromLinePattern.FindStringSubmatch(lineNoComment); len(matches) == 3 {
			moduleName := strings.TrimSpace(matches[1])
			if strings.HasPrefix(moduleName, ".") {
				continue
			}
			dependency := dependencyFromModule(repoPath, moduleName)
			if dependency == "" {
				continue
			}
			for _, part := range splitCSV(matches[2]) {
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
					Location: report.Location{
						File:   filePath,
						Line:   index + 1,
						Column: firstContentColumn(lineNoComment),
					},
				})
			}
		}
	}
	return bindings
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
	if index := strings.Index(line, "#"); index >= 0 {
		return line[:index]
	}
	return line
}

func firstContentColumn(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return i + 1
		}
	}
	return 1
}

func countUsage(content []byte, imports []importBinding) map[string]int {
	importCount := make(map[string]int)
	for _, binding := range imports {
		if binding.Wildcard || binding.Local == "" {
			continue
		}
		importCount[binding.Local]++
	}

	usage := make(map[string]int, len(importCount))
	text := string(content)
	for local, count := range importCount {
		pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `\b`)
		occurrences := len(pattern.FindAllStringIndex(text, -1)) - count
		if occurrences < 0 {
			occurrences = 0
		}
		usage[local] = occurrences
	}
	return usage
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	usedImports := make(map[string]*report.ImportUse)
	unusedImports := make(map[string]*report.ImportUse)
	usedSymbols := make(map[string]struct{})
	allSymbols := make(map[string]struct{})
	symbolCounts := make(map[string]int)
	var warnings []string
	wildcardImports := 0

	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if normalizeDependencyID(imported.Dependency) != dependency {
				continue
			}

			allSymbols[imported.Name] = struct{}{}
			used := imported.Wildcard || file.Usage[imported.Local] > 0
			if imported.Wildcard {
				wildcardImports++
			}
			entry := report.ImportUse{
				Name:   imported.Name,
				Module: imported.Module,
				Locations: []report.Location{
					imported.Location,
				},
			}
			if used {
				usedSymbols[imported.Name] = struct{}{}
				count := file.Usage[imported.Local]
				if imported.Wildcard && count == 0 {
					count = 1
				}
				if count > 0 {
					symbolCounts[imported.Name] += count
				}
				addImport(usedImports, entry)
			} else {
				addImport(unusedImports, entry)
			}
		}
	}

	if len(allSymbols) == 0 {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	usedCount := len(usedSymbols)
	totalCount := len(allSymbols)
	usedPercent := 0.0
	if totalCount > 0 {
		usedPercent = (float64(usedCount) / float64(totalCount)) * 100
	}

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

	dep := report.DependencyReport{
		Language:             "python",
		Name:                 dependency,
		UsedExportsCount:     usedCount,
		TotalExportsCount:    totalCount,
		UsedPercent:          usedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       topSymbols,
		UsedImports:          flattenImports(usedImports),
		UnusedImports:        dedupeUnused(flattenImports(unusedImports), flattenImports(usedImports)),
	}
	if wildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d wildcard import(s) for this dependency", wildcardImports),
		})
	}
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func listDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if imported.Dependency == "" {
				continue
			}
			set[normalizeDependencyID(imported.Dependency)] = struct{}{}
		}
	}
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
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

func dedupeUnused(unused []report.ImportUse, used []report.ImportUse) []report.ImportUse {
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
	if hasWildcardImport(dep.UsedImports) || hasWildcardImport(dep.UnusedImports) {
		recs = append(recs, report.Recommendation{
			Code:      "avoid-star-imports",
			Priority:  "medium",
			Message:   "Wildcard imports were detected; prefer explicit symbol imports.",
			Rationale: "Explicit imports improve readability and analysis precision.",
		})
	}
	return recs
}

func hasWildcardImport(imports []report.ImportUse) bool {
	for _, imported := range imports {
		if imported.Name == "*" {
			return true
		}
	}
	return false
}

func wasteScore(dep report.DependencyReport) (float64, bool) {
	if dep.TotalExportsCount == 0 {
		return -1, false
	}
	return 100 - dep.UsedPercent, true
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
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", "node_modules", "__pycache__", ".venv", "venv", "dist", "build", ".mypy_cache", ".pytest_cache":
		return true
	default:
		return false
	}
}

func sortedKeys(values map[string]struct{}) []string {
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

var pythonStdlib = map[string]bool{
	"abc": true, "argparse": true, "asyncio": true, "collections": true, "contextlib": true, "copy": true,
	"csv": true, "dataclasses": true, "datetime": true, "functools": true, "hashlib": true, "http": true,
	"importlib": true, "itertools": true, "json": true, "logging": true, "math": true, "os": true,
	"pathlib": true, "queue": true, "random": true, "re": true, "socket": true, "sqlite3": true,
	"ssl": true, "statistics": true, "string": true, "subprocess": true, "sys": true, "threading": true,
	"time": true, "typing": true, "unittest": true, "urllib": true, "uuid": true, "xml": true, "zipfile": true,
}
