package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/js"
	"github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Analyzer interface {
	Analyse(ctx context.Context, req Request) (report.Report, error)
}

type Service struct {
	Registry *language.Registry
	InitErr  error
}

func NewService() *Service {
	registry := language.NewRegistry()
	err := registry.Register(js.NewAdapter())
	if err == nil {
		err = registry.Register(python.NewAdapter())
	}
	if err == nil {
		err = registry.Register(jvm.NewAdapter())
	}

	return &Service{
		Registry: registry,
		InitErr:  err,
	}
}

func (s *Service) Analyse(ctx context.Context, req Request) (report.Report, error) {
	if s.InitErr != nil {
		return report.Report{}, s.InitErr
	}
	if s.Registry == nil {
		return report.Report{}, errors.New("language registry is not configured")
	}

	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	candidates, err := s.Registry.Resolve(ctx, repoPath, req.Language)
	if err != nil {
		return report.Report{}, err
	}

	reports := make([]report.Report, 0, len(candidates))
	var warnings []string

	for _, candidate := range candidates {
		if isMultiLanguage(req.Language) && candidate.Detection.Confidence > 0 && candidate.Detection.Confidence < 40 {
			warnings = append(
				warnings,
				"low detection confidence for adapter "+candidate.Adapter.ID()+": results may be partial",
			)
		}
		roots := candidate.Detection.Roots
		if len(roots) == 0 {
			roots = []string{repoPath}
		}
		rootSeen := make(map[string]struct{})
		for _, root := range roots {
			normalizedRoot := root
			if !filepath.IsAbs(normalizedRoot) {
				normalizedRoot = filepath.Join(repoPath, normalizedRoot)
			}
			if _, ok := rootSeen[normalizedRoot]; ok {
				continue
			}
			rootSeen[normalizedRoot] = struct{}{}

			current, err := candidate.Adapter.Analyse(ctx, language.Request{
				RepoPath:   normalizedRoot,
				Dependency: req.Dependency,
				TopN:       req.TopN,
			})
			if err != nil {
				if isMultiLanguage(req.Language) {
					warnings = append(warnings, err.Error())
					continue
				}
				return report.Report{}, err
			}
			applyLanguageID(current.Dependencies, candidate.Adapter.ID())
			adjustRelativeLocations(repoPath, normalizedRoot, current.Dependencies)
			reports = append(reports, current)
		}
	}

	if len(reports) == 0 {
		return report.Report{
			SchemaVersion: report.SchemaVersion,
			RepoPath:      repoPath,
			Warnings:      append(warnings, "no language adapter produced results"),
		}, nil
	}

	reportData := mergeReports(repoPath, reports)
	reportData.Warnings = append(reportData.Warnings, warnings...)

	if req.RuntimeTracePath != "" {
		traceData, err := runtime.Load(req.RuntimeTracePath)
		if err != nil {
			return report.Report{}, err
		}
		reportData = runtime.Annotate(reportData, traceData)
	}

	reportData.SchemaVersion = report.SchemaVersion
	return reportData, nil
}

func isMultiLanguage(languageID string) bool {
	languageID = strings.TrimSpace(strings.ToLower(languageID))
	return languageID == language.All
}

func applyLanguageID(dependencies []report.DependencyReport, languageID string) {
	for i := range dependencies {
		if dependencies[i].Language == "" {
			dependencies[i].Language = languageID
		}
	}
}

func adjustRelativeLocations(repoPath string, analyzedRoot string, dependencies []report.DependencyReport) {
	prefix, err := filepath.Rel(repoPath, analyzedRoot)
	if err != nil || prefix == "." || prefix == "" {
		return
	}
	for i := range dependencies {
		for j := range dependencies[i].UsedImports {
			for k := range dependencies[i].UsedImports[j].Locations {
				location := &dependencies[i].UsedImports[j].Locations[k]
				if filepath.IsAbs(location.File) {
					continue
				}
				location.File = filepath.Clean(filepath.Join(prefix, location.File))
			}
		}
		for j := range dependencies[i].UnusedImports {
			for k := range dependencies[i].UnusedImports[j].Locations {
				location := &dependencies[i].UnusedImports[j].Locations[k]
				if filepath.IsAbs(location.File) {
					continue
				}
				location.File = filepath.Clean(filepath.Join(prefix, location.File))
			}
		}
	}
}

func mergeReports(repoPath string, reports []report.Report) report.Report {
	result := report.Report{
		RepoPath: repoPath,
	}
	mergedByKey := make(map[string]report.DependencyReport)
	orderedKeys := make([]string, 0)

	for _, current := range reports {
		result.Warnings = append(result.Warnings, current.Warnings...)
		if current.GeneratedAt.After(result.GeneratedAt) {
			result.GeneratedAt = current.GeneratedAt
		}
		for _, dep := range current.Dependencies {
			key := dep.Language + "\x00" + dep.Name
			if existing, ok := mergedByKey[key]; ok {
				mergedByKey[key] = mergeDependency(existing, dep)
				continue
			}
			mergedByKey[key] = dep
			orderedKeys = append(orderedKeys, key)
		}
	}

	sort.Strings(orderedKeys)
	result.Dependencies = make([]report.DependencyReport, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		result.Dependencies = append(result.Dependencies, mergedByKey[key])
	}
	result.Summary = report.ComputeSummary(result.Dependencies)
	result.LanguageBreakdown = report.ComputeLanguageBreakdown(result.Dependencies)
	return result
}

func mergeDependency(left report.DependencyReport, right report.DependencyReport) report.DependencyReport {
	merged := left
	merged.UsedExportsCount += right.UsedExportsCount
	merged.TotalExportsCount += right.TotalExportsCount
	if merged.TotalExportsCount > 0 {
		merged.UsedPercent = (float64(merged.UsedExportsCount) / float64(merged.TotalExportsCount)) * 100
	}
	merged.EstimatedUnusedBytes += right.EstimatedUnusedBytes

	merged.UsedImports = mergeImportUses(left.UsedImports, right.UsedImports)
	merged.UnusedImports = mergeImportUses(left.UnusedImports, right.UnusedImports)
	merged.UnusedImports = filterUsedOverlaps(merged.UnusedImports, merged.UsedImports)
	merged.UnusedExports = mergeSymbolRefs(left.UnusedExports, right.UnusedExports)
	merged.RiskCues = mergeRiskCues(left.RiskCues, right.RiskCues)
	merged.Recommendations = mergeRecommendations(left.Recommendations, right.Recommendations)
	merged.TopUsedSymbols = mergeTopSymbols(left.TopUsedSymbols, right.TopUsedSymbols)

	if left.RuntimeUsage != nil || right.RuntimeUsage != nil {
		loadCount := 0
		runtimeOnly := true
		if left.RuntimeUsage != nil {
			loadCount += left.RuntimeUsage.LoadCount
			runtimeOnly = runtimeOnly && left.RuntimeUsage.RuntimeOnly
		}
		if right.RuntimeUsage != nil {
			loadCount += right.RuntimeUsage.LoadCount
			runtimeOnly = runtimeOnly && right.RuntimeUsage.RuntimeOnly
		}
		merged.RuntimeUsage = &report.RuntimeUsage{
			LoadCount:   loadCount,
			RuntimeOnly: runtimeOnly,
		}
	}

	return merged
}

func mergeImportUses(left []report.ImportUse, right []report.ImportUse) []report.ImportUse {
	merged := make(map[string]report.ImportUse)
	for _, item := range append(append([]report.ImportUse{}, left...), right...) {
		key := item.Module + "\x00" + item.Name
		if current, ok := merged[key]; ok {
			current.Locations = append(current.Locations, item.Locations...)
			merged[key] = current
			continue
		}
		merged[key] = item
	}
	items := make([]report.ImportUse, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
	return items
}

func filterUsedOverlaps(unused []report.ImportUse, used []report.ImportUse) []report.ImportUse {
	usedKeys := make(map[string]struct{}, len(used))
	for _, item := range used {
		usedKeys[item.Module+"\x00"+item.Name] = struct{}{}
	}
	filtered := make([]report.ImportUse, 0, len(unused))
	for _, item := range unused {
		if _, ok := usedKeys[item.Module+"\x00"+item.Name]; ok {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func mergeSymbolRefs(left []report.SymbolRef, right []report.SymbolRef) []report.SymbolRef {
	merged := make(map[string]report.SymbolRef)
	for _, item := range append(append([]report.SymbolRef{}, left...), right...) {
		merged[item.Module+"\x00"+item.Name] = item
	}
	items := make([]report.SymbolRef, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
	return items
}

func mergeRiskCues(left []report.RiskCue, right []report.RiskCue) []report.RiskCue {
	merged := make(map[string]report.RiskCue)
	for _, item := range append(append([]report.RiskCue{}, left...), right...) {
		merged[item.Code+"\x00"+item.Severity] = item
	}
	items := make([]report.RiskCue, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Code < items[j].Code
	})
	return items
}

func mergeRecommendations(left []report.Recommendation, right []report.Recommendation) []report.Recommendation {
	merged := make(map[string]report.Recommendation)
	for _, item := range append(append([]report.Recommendation{}, left...), right...) {
		merged[item.Code] = item
	}
	items := make([]report.Recommendation, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority == items[j].Priority {
			return items[i].Code < items[j].Code
		}
		return recommendationPriorityRank(items[i].Priority) < recommendationPriorityRank(items[j].Priority)
	})
	return items
}

func recommendationPriorityRank(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

func mergeTopSymbols(left []report.SymbolUsage, right []report.SymbolUsage) []report.SymbolUsage {
	merged := make(map[string]report.SymbolUsage)
	for _, item := range append(append([]report.SymbolUsage{}, left...), right...) {
		key := item.Module + "\x00" + item.Name
		if current, ok := merged[key]; ok {
			current.Count += item.Count
			merged[key] = current
			continue
		}
		merged[key] = item
	}
	items := make([]report.SymbolUsage, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Name < items[j].Name
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}
