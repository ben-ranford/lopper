package js

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	return "js-ts"
}

func (a *Adapter) Aliases() []string {
	return []string{"js", "ts", "javascript", "typescript"}
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

	candidates := []string{"package.json", "tsconfig.json", "jsconfig.json"}
	for _, name := range candidates {
		path := filepath.Join(repoPath, name)
		if _, err := os.Stat(path); err == nil {
			detection.Matched = true
			switch name {
			case "package.json":
				detection.Confidence += 45
				roots[repoPath] = struct{}{}
			default:
				detection.Confidence += 20
			}
		} else if !os.IsNotExist(err) {
			return language.Detection{}, err
		}
	}

	const maxFiles = 256
	visitedFiles := 0
	err := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".idea", "dist", "build", "vendor", "node_modules", ".next", ".turbo", "coverage":
				return filepath.SkipDir
			}
			return nil
		}

		visitedFiles++
		if visitedFiles > maxFiles {
			return io.EOF
		}

		if strings.EqualFold(d.Name(), "package.json") {
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
			return nil
		}

		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
	if err == io.EOF {
		err = nil
	}
	if err != nil {
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
	detection.Roots = mapKeysSorted(roots)
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

	scanResult, err := ScanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	switch {
	case req.Dependency != "":
		depReport, warnings := buildDependencyReport(repoPath, req.Dependency, scanResult)
		result.Dependencies = []report.DependencyReport{depReport}
		result.Warnings = append(result.Warnings, warnings...)
		result.Summary = report.ComputeSummary(result.Dependencies)
	case req.TopN > 0:
		deps, warnings := buildTopDependencies(repoPath, scanResult, req.TopN)
		result.Dependencies = deps
		result.Warnings = append(result.Warnings, warnings...)
		if len(deps) == 0 {
			result.Warnings = append(result.Warnings, "no dependency data available for top-N ranking")
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	default:
		result.Warnings = append(result.Warnings, "no dependency or top-N target provided")
	}

	return result, nil
}

func buildDependencyReport(repoPath string, dependency string, scanResult ScanResult) (report.DependencyReport, []string) {
	usedExports := make(map[string]struct{})
	counts := make(map[string]int)
	usedImports := make(map[string]*report.ImportUse)
	unusedImports := make(map[string]*report.ImportUse)
	var warnings []string
	var hasWildcard bool

	surface, err := resolveDependencyExports(repoPath, dependency)
	if err != nil {
		warnings = append(warnings, err.Error())
	} else {
		warnings = append(warnings, surface.Warnings...)
		if surface.IncludesWildcard {
			warnings = append(warnings, "dependency export surface includes wildcard re-exports")
		}
	}

	for _, file := range scanResult.Files {
		for _, imp := range file.Imports {
			if !matchesDependency(imp.Module, dependency) {
				continue
			}

			used := false
			switch imp.Kind {
			case ImportNamed:
				if count := file.IdentifierUsage[imp.LocalName]; count > 0 {
					used = true
					usedExports[imp.ExportName] = struct{}{}
					counts[imp.ExportName] += count
				}
			case ImportNamespace, ImportDefault:
				if props, ok := file.NamespaceUsage[imp.LocalName]; ok {
					for prop, count := range props {
						used = true
						usedExports[prop] = struct{}{}
						counts[prop] += count
					}
				}
				if count := file.IdentifierUsage[imp.LocalName]; count > 0 {
					used = true
					if imp.Kind == ImportDefault {
						usedExports["default"] = struct{}{}
						counts["default"] += count
					} else {
						usedExports["*"] = struct{}{}
						counts["*"] += count
					}
				}
			}

			if imp.ExportName == "*" || imp.ExportName == "default" {
				hasWildcard = true
			}

			entry := recordImportUse(imp)
			if used {
				addImportUse(usedImports, entry)
			} else {
				addImportUse(unusedImports, entry)
			}
		}
	}

	if len(usedExports) == 0 {
		warnings = append(warnings, fmt.Sprintf("no used exports found for dependency %q", dependency))
	}
	if hasWildcard {
		warnings = append(warnings, "default or namespace imports reduce export precision")
	}

	usedImportList := flattenImportUses(usedImports)
	unusedImportList := flattenImportUses(unusedImports)
	unusedImportList = removeOverlaps(unusedImportList, usedImportList)

	topSymbols := make([]report.SymbolUsage, 0, len(counts))
	for name, count := range counts {
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

	totalExports := len(surface.Names)
	unusedExports := buildUnusedExports(dependency, surface.Names, usedExports)
	usedPercent := 0.0
	if surface.IncludesWildcard {
		totalExports = 0
	} else if totalExports > 0 {
		usedCount := countUsedExports(surface.Names, usedExports)
		usedPercent = (float64(usedCount) / float64(totalExports)) * 100
	}

	usedExportCount := countUsedExports(surface.Names, usedExports)
	if usedExportCount == 0 && totalExports == 0 {
		usedExportCount = len(usedExports)
	}

	riskCues, riskWarnings := assessRiskCues(repoPath, dependency, surface)
	warnings = append(warnings, riskWarnings...)

	depReport := report.DependencyReport{
		Language:             "js-ts",
		Name:                 dependency,
		UsedExportsCount:     usedExportCount,
		TotalExportsCount:    totalExports,
		UsedPercent:          usedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       topSymbols,
		UsedImports:          usedImportList,
		UnusedImports:        unusedImportList,
		UnusedExports:        unusedExports,
		RiskCues:             riskCues,
	}
	depReport.Recommendations = buildRecommendations(dependency, depReport)
	return depReport, warnings
}

func recordImportUse(binding ImportBinding) report.ImportUse {
	return report.ImportUse{
		Name:      binding.ExportName,
		Module:    binding.Module,
		Locations: []report.Location{binding.Location},
	}
}

func addImportUse(dest map[string]*report.ImportUse, entry report.ImportUse) {
	key := fmt.Sprintf("%s:%s", entry.Module, entry.Name)
	current, ok := dest[key]
	if !ok {
		copyEntry := entry
		dest[key] = &copyEntry
		return
	}
	current.Locations = append(current.Locations, entry.Locations...)
}

func flattenImportUses(source map[string]*report.ImportUse) []report.ImportUse {
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

func removeOverlaps(unused []report.ImportUse, used []report.ImportUse) []report.ImportUse {
	usedKeys := make(map[string]struct{}, len(used))
	for _, entry := range used {
		usedKeys[fmt.Sprintf("%s:%s", entry.Module, entry.Name)] = struct{}{}
	}

	filtered := make([]report.ImportUse, 0, len(unused))
	for _, entry := range unused {
		key := fmt.Sprintf("%s:%s", entry.Module, entry.Name)
		if _, ok := usedKeys[key]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}

	return filtered
}

func buildUnusedExports(module string, surface map[string]struct{}, used map[string]struct{}) []report.SymbolRef {
	items := make([]report.SymbolRef, 0, len(surface))
	for name := range surface {
		if _, ok := used[name]; ok {
			continue
		}
		items = append(items, report.SymbolRef{Name: name, Module: module})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	return items
}

func countUsedExports(surface map[string]struct{}, used map[string]struct{}) int {
	count := 0
	for name := range used {
		if _, ok := surface[name]; ok {
			count++
		}
	}
	return count
}

func matchesDependency(module string, dependency string) bool {
	if module == dependency {
		return true
	}
	if strings.HasPrefix(module, dependency+"/") {
		return true
	}
	return false
}

func buildTopDependencies(repoPath string, scanResult ScanResult, topN int) ([]report.DependencyReport, []string) {
	dependencies, warnings := listDependencies(repoPath, scanResult)
	if len(dependencies) == 0 {
		return nil, warnings
	}

	reports := make([]report.DependencyReport, 0, len(dependencies))
	for _, dep := range dependencies {
		depReport, depWarnings := buildDependencyReport(repoPath, dep, scanResult)
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}

	sort.Slice(reports, func(i, j int) bool {
		iScore, iHas := wasteScore(reports[i])
		jScore, jHas := wasteScore(reports[j])
		if iHas != jHas {
			return iHas
		}
		if iScore == jScore {
			return reports[i].Name < reports[j].Name
		}
		return iScore > jScore
	})

	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}

	return reports, warnings
}

func wasteScore(dep report.DependencyReport) (float64, bool) {
	if dep.TotalExportsCount == 0 {
		return -1, false
	}
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 && dep.TotalExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	return 100 - usedPercent, true
}

func listDependencies(repoPath string, scanResult ScanResult) ([]string, []string) {
	set := make(map[string]struct{})
	missing := make(map[string]struct{})

	for _, file := range scanResult.Files {
		for _, imp := range file.Imports {
			dep := dependencyFromModule(imp.Module)
			if dep == "" {
				continue
			}
			if dependencyExists(repoPath, dep) {
				set[dep] = struct{}{}
			} else {
				missing[dep] = struct{}{}
			}
		}
	}

	deps := make([]string, 0, len(set))
	for dep := range set {
		deps = append(deps, dep)
	}
	sort.Strings(deps)

	warnings := make([]string, 0, len(missing))
	for dep := range missing {
		warnings = append(warnings, fmt.Sprintf("dependency not found in node_modules: %s", dep))
	}
	sort.Strings(warnings)

	return deps, warnings
}

func dependencyFromModule(module string) string {
	module = strings.TrimSpace(module)
	if module == "" {
		return ""
	}
	if strings.HasPrefix(module, ".") || strings.HasPrefix(module, "/") {
		return ""
	}

	if strings.HasPrefix(module, "@") {
		parts := strings.Split(module, "/")
		if len(parts) < 2 {
			return ""
		}
		if len(parts[0]) <= 1 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}

	parts := strings.Split(module, "/")
	return parts[0]
}

func dependencyExists(repoPath string, dependency string) bool {
	root, err := dependencyRoot(repoPath, dependency)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "package.json"))
	return err == nil && !info.IsDir()
}

func mapKeysSorted(values map[string]struct{}) []string {
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
