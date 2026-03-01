package analysis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/cpp"
	"github.com/ben-ranford/lopper/internal/lang/dotnet"
	"github.com/ben-ranford/lopper/internal/lang/elixir"
	"github.com/ben-ranford/lopper/internal/lang/golang"
	"github.com/ben-ranford/lopper/internal/lang/js"
	"github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/lang/php"
	"github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/lang/ruby"
	"github.com/ben-ranford/lopper/internal/lang/rust"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Analyser interface {
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
		err = registry.Register(cpp.NewAdapter())
	}
	if err == nil {
		err = registry.Register(jvm.NewAdapter())
	}
	if err == nil {
		err = registry.Register(golang.NewAdapter())
	}
	if err == nil {
		err = registry.Register(php.NewAdapter())
	}
	if err == nil {
		err = registry.Register(rust.NewAdapter())
	}
	if err == nil {
		err = registry.Register(ruby.NewAdapter())
	}
	if err == nil {
		err = registry.Register(dotnet.NewAdapter())
	}
	if err == nil {
		err = registry.Register(elixir.NewAdapter())
	}

	return &Service{
		Registry: registry,
		InitErr:  err,
	}
}

func (s *Service) Analyse(ctx context.Context, req Request) (report.Report, error) {
	repoPath, err := s.prepareAnalysis(req)
	if err != nil {
		return report.Report{}, err
	}
	req.ScopeMode = normalizeScopeMode(req.ScopeMode)
	analysisRepoPath, scopeWarnings, cleanupScope, err := applyPathScope(repoPath, req.IncludePatterns, req.ExcludePatterns)
	if err != nil {
		return report.Report{}, err
	}
	defer cleanupScope()
	candidates, err := s.resolveCandidates(ctx, analysisRepoPath, req.Language)
	if err != nil {
		return report.Report{}, err
	}
	cache := newAnalysisCache(req, analysisRepoPath)

	reports, warnings, analyzedRoots, err := s.runCandidates(ctx, req, analysisRepoPath, candidates, cache)
	if err != nil {
		return report.Report{}, err
	}
	analyzedRoots = remapAnalyzedRoots(analyzedRoots, analysisRepoPath, repoPath)
	warnings = append(scopeWarnings, warnings...)
	warnings = append(warnings, cache.takeWarnings()...)
	if len(reports) == 0 {
		reportData := report.Report{
			RepoPath: repoPath,
			Warnings: append(warnings, "no language adapter produced results"),
			Cache:    cache.metadataSnapshot(),
		}
		reportData, err = annotateRuntimeTraceIfPresent(req.RuntimeTracePath, req.Language, reportData)
		if err != nil {
			return report.Report{}, err
		}
		reportData.Scope = scopeMetadata(req.ScopeMode, repoPath, analyzedRoots)
		reportData.Summary = report.ComputeSummary(reportData.Dependencies)
		reportData.LanguageBreakdown = report.ComputeLanguageBreakdown(reportData.Dependencies)
		reportData.SchemaVersion = report.SchemaVersion
		return reportData, nil
	}

	reportData := mergeReports(repoPath, reports)
	reportData.Warnings = append(reportData.Warnings, warnings...)
	reportData.Cache = cache.metadataSnapshot()

	reportData, err = annotateRuntimeTraceIfPresent(req.RuntimeTracePath, req.Language, reportData)
	if err != nil {
		return report.Report{}, err
	}
	lowConfidenceThreshold := float64(resolveLowConfidenceWarningThreshold(req.LowConfidenceWarningPercent))
	report.AnnotateFindingConfidence(reportData.Dependencies)
	report.FilterFindingsByConfidence(reportData.Dependencies, lowConfidenceThreshold)
	reportData.Scope = scopeMetadata(req.ScopeMode, repoPath, analyzedRoots)
	report.AnnotateRemovalCandidateScoresWithWeights(reportData.Dependencies, resolveRemovalCandidateWeights(req.RemovalCandidateWeights))
	reportData.Summary = report.ComputeSummary(reportData.Dependencies)
	reportData.LanguageBreakdown = report.ComputeLanguageBreakdown(reportData.Dependencies)
	reportData.SchemaVersion = report.SchemaVersion
	return reportData, nil
}

func (s *Service) prepareAnalysis(req Request) (string, error) {
	if s.InitErr != nil {
		return "", s.InitErr
	}
	if s.Registry == nil {
		return "", errors.New("language registry is not configured")
	}
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return "", err
	}
	return repoPath, nil
}

func (s *Service) resolveCandidates(ctx context.Context, repoPath string, languageID string) ([]language.Candidate, error) {
	candidates, err := s.Registry.Resolve(ctx, repoPath, languageID)
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Service) runCandidates(ctx context.Context, req Request, repoPath string, candidates []language.Candidate, cache *analysisCache) ([]report.Report, []string, []string, error) {
	reports := make([]report.Report, 0, len(candidates))
	warnings := make([]string, 0)
	analyzedRoots := make([]string, 0)
	lowConfidenceThreshold := resolveLowConfidenceWarningThreshold(req.LowConfidenceWarningPercent)
	for _, candidate := range candidates {
		warnings = append(warnings, lowConfidenceWarning(req.Language, candidate, lowConfidenceThreshold)...)
		candidateReports, candidateWarnings, candidateRoots, err := s.runCandidateOnRoots(ctx, req, repoPath, candidate, cache)
		if err != nil {
			return nil, nil, nil, err
		}
		reports = append(reports, candidateReports...)
		warnings = append(warnings, candidateWarnings...)
		analyzedRoots = append(analyzedRoots, candidateRoots...)
	}
	return reports, warnings, uniqueSorted(analyzedRoots), nil
}

func lowConfidenceWarning(languageID string, candidate language.Candidate, lowConfidenceThreshold int) []string {
	if !isMultiLanguage(languageID) {
		return nil
	}
	if candidate.Detection.Confidence <= 0 || candidate.Detection.Confidence >= lowConfidenceThreshold {
		return nil
	}
	return []string{"low detection confidence for adapter " + candidate.Adapter.ID() + ": results may be partial"}
}

func (s *Service) runCandidateOnRoots(ctx context.Context, req Request, repoPath string, candidate language.Candidate, cache *analysisCache) ([]report.Report, []string, []string, error) {
	reports := make([]report.Report, 0)
	warnings := make([]string, 0)
	analyzedRoots := make([]string, 0)
	rootSeen := make(map[string]struct{})
	roots, rootWarnings := scopedCandidateRoots(req.ScopeMode, candidate.Detection.Roots, repoPath)
	warnings = append(warnings, rootWarnings...)
	for _, root := range roots {
		normalizedRoot := normalizeCandidateRoot(repoPath, root)
		if alreadySeenRoot(rootSeen, normalizedRoot) {
			continue
		}
		analyzedRoots = append(analyzedRoots, normalizedRoot)

		cacheEntry, cachedReport, hit := prepareAndLoadCachedReport(req, cache, candidate.Adapter.ID(), normalizedRoot)
		if hit {
			applyLanguageID(cachedReport.Dependencies, candidate.Adapter.ID())
			adjustRelativeLocations(repoPath, normalizedRoot, cachedReport.Dependencies)
			reports = append(reports, cachedReport)
			continue
		}

		current, err := candidate.Adapter.Analyse(ctx, language.Request{
			RepoPath:                          normalizedRoot,
			Dependency:                        req.Dependency,
			TopN:                              req.TopN,
			SuggestOnly:                       req.SuggestOnly,
			RuntimeProfile:                    req.RuntimeProfile,
			MinUsagePercentForRecommendations: req.MinUsagePercentForRecommendations,
			RemovalCandidateWeights:           req.RemovalCandidateWeights,
		})
		if err != nil {
			if isMultiLanguage(req.Language) {
				warnings = append(warnings, err.Error())
				continue
			}
			return nil, nil, nil, err
		}
		storeCachedReport(cache, candidate.Adapter.ID(), normalizedRoot, cacheEntry, current)
		applyLanguageID(current.Dependencies, candidate.Adapter.ID())
		adjustRelativeLocations(repoPath, normalizedRoot, current.Dependencies)
		reports = append(reports, current)
	}
	return reports, warnings, analyzedRoots, nil
}

func alreadySeenRoot(seen map[string]struct{}, normalizedRoot string) bool {
	if _, ok := seen[normalizedRoot]; ok {
		return true
	}
	seen[normalizedRoot] = struct{}{}
	return false
}

func prepareAndLoadCachedReport(req Request, cache *analysisCache, adapterID, normalizedRoot string) (cacheEntryDescriptor, report.Report, bool) {
	cacheEntry, err := cache.prepareEntry(req, adapterID, normalizedRoot)
	if err != nil {
		cache.warn("analysis cache skipped for " + adapterID + ":" + normalizedRoot + ": " + err.Error())
		return cacheEntryDescriptor{}, report.Report{}, false
	}
	if cacheEntry.KeyDigest == "" {
		return cacheEntry, report.Report{}, false
	}
	cachedReport, hit, lookupErr := cache.lookup(cacheEntry)
	if lookupErr != nil {
		cache.warn("analysis cache lookup failed for " + adapterID + ":" + normalizedRoot + ": " + lookupErr.Error())
		return cacheEntry, report.Report{}, false
	}
	return cacheEntry, cachedReport, hit
}

func storeCachedReport(cache *analysisCache, adapterID, normalizedRoot string, cacheEntry cacheEntryDescriptor, current report.Report) {
	if cacheEntry.KeyDigest == "" {
		return
	}
	if storeErr := cache.store(cacheEntry, current); storeErr != nil {
		cache.warn("analysis cache store failed for " + adapterID + ":" + normalizedRoot + ": " + storeErr.Error())
	}
}

func resolveRemovalCandidateWeights(weights *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if weights == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*weights)
}

func resolveLowConfidenceWarningThreshold(threshold *int) int {
	if threshold != nil {
		return *threshold
	}
	return thresholds.Defaults().LowConfidenceWarningPercent
}

func candidateRoots(roots []string, repoPath string) []string {
	if len(roots) == 0 {
		return []string{repoPath}
	}
	return roots
}

func normalizeScopeMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case ScopeModeRepo, ScopeModeChangedPackages:
		return mode
	default:
		return ScopeModePackage
	}
}

func scopedCandidateRoots(scopeMode string, roots []string, repoPath string) ([]string, []string) {
	switch normalizeScopeMode(scopeMode) {
	case ScopeModeRepo:
		return []string{repoPath}, nil
	case ScopeModeChangedPackages:
		baseRoots := candidateRoots(roots, repoPath)
		changedFiles, err := workspace.ChangedFiles(repoPath)
		if err != nil {
			return baseRoots, []string{"unable to resolve changed packages; falling back to package scope: " + err.Error()}
		}
		return changedRoots(baseRoots, repoPath, changedFiles), nil
	default:
		return candidateRoots(roots, repoPath), nil
	}
}

func changedRoots(roots []string, repoPath string, changedFiles []string) []string {
	absoluteChangedFiles := make([]string, 0, len(changedFiles))
	for _, file := range changedFiles {
		absoluteChangedFiles = append(absoluteChangedFiles, filepath.Join(repoPath, file))
	}
	changed := make([]string, 0, len(roots))
	for _, root := range roots {
		for _, file := range absoluteChangedFiles {
			if rootContainsFile(root, file) {
				changed = append(changed, root)
				break
			}
		}
	}
	return uniqueSorted(changed)
}

func rootContainsFile(root, file string) bool {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func scopeMetadata(mode, repoPath string, roots []string) *report.ScopeMetadata {
	packages := make([]string, 0, len(roots))
	for _, root := range uniqueSorted(roots) {
		rel, err := filepath.Rel(repoPath, root)
		if err != nil {
			continue
		}
		if rel == "" {
			rel = "."
		}
		packages = append(packages, filepath.ToSlash(rel))
	}
	return &report.ScopeMetadata{
		Mode:     normalizeScopeMode(mode),
		Packages: packages,
	}
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	items := append([]string(nil), values...)
	sort.Strings(items)
	unique := items[:1]
	for i := 1; i < len(items); i++ {
		if items[i] != items[i-1] {
			unique = append(unique, items[i])
		}
	}
	return unique
}

func normalizeCandidateRoot(repoPath, root string) string {
	if filepath.IsAbs(root) {
		return root
	}
	return filepath.Join(repoPath, root)
}

func remapAnalyzedRoots(roots []string, fromRepoPath, toRepoPath string) []string {
	if fromRepoPath == toRepoPath || len(roots) == 0 {
		return roots
	}
	remapped := make([]string, 0, len(roots))
	for _, root := range roots {
		rel, err := filepath.Rel(fromRepoPath, root)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			remapped = append(remapped, root)
			continue
		}
		remapped = append(remapped, filepath.Join(toRepoPath, rel))
	}
	return uniqueSorted(remapped)
}

func annotateRuntimeTraceIfPresent(runtimeTracePath string, languageID string, reportData report.Report) (report.Report, error) {
	if runtimeTracePath == "" {
		return reportData, nil
	}
	traceData, err := runtime.Load(runtimeTracePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			reportData.Warnings = append(reportData.Warnings, "runtime trace file not found; continuing with static analysis")
			return reportData, nil
		}
		return report.Report{}, err
	}
	return runtime.Annotate(reportData, traceData, runtime.AnnotateOptions{
		IncludeRuntimeOnlyRows: supportsJSTraceLanguage(languageID),
	}), nil
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
		adjustImportLocations(prefix, dependencies[i].UsedImports)
		adjustImportLocations(prefix, dependencies[i].UnusedImports)
	}
}

func adjustImportLocations(prefix string, imports []report.ImportUse) {
	for j := range imports {
		for k := range imports[j].Locations {
			location := &imports[j].Locations[k]
			if filepath.IsAbs(location.File) {
				continue
			}
			location.File = filepath.Clean(filepath.Join(prefix, location.File))
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
		result.UsageUncertainty = mergeUsageUncertainty(result.UsageUncertainty, current.UsageUncertainty)
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

func mergeUsageUncertainty(left, right *report.UsageUncertainty) *report.UsageUncertainty {
	if left == nil {
		if right == nil {
			return nil
		}
		copyRight := *right
		copyRight.Samples = cappedSampleCopy(right.Samples)
		return &copyRight
	}
	if right == nil {
		copyLeft := *left
		copyLeft.Samples = cappedSampleCopy(left.Samples)
		return &copyLeft
	}

	merged := &report.UsageUncertainty{
		ConfirmedImportUses: left.ConfirmedImportUses + right.ConfirmedImportUses,
		UncertainImportUses: left.UncertainImportUses + right.UncertainImportUses,
	}
	merged.Samples = append(merged.Samples, left.Samples...)
	if len(merged.Samples) < 5 {
		remaining := 5 - len(merged.Samples)
		if remaining > len(right.Samples) {
			remaining = len(right.Samples)
		}
		merged.Samples = append(merged.Samples, right.Samples[:remaining]...)
	}
	return merged
}

func cappedSampleCopy(samples []report.Location) []report.Location {
	if len(samples) > 5 {
		samples = samples[:5]
	}
	return append([]report.Location{}, samples...)
}

func mergeDependency(left, right report.DependencyReport) report.DependencyReport {
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
	merged.Codemod = mergeCodemodReport(left.Codemod, right.Codemod)
	merged.TopUsedSymbols = mergeTopSymbols(left.TopUsedSymbols, right.TopUsedSymbols)
	merged.RuntimeUsage = mergeRuntimeUsage(left.RuntimeUsage, right.RuntimeUsage)

	return merged
}

func filterUsedOverlaps(unused, used []report.ImportUse) []report.ImportUse {
	if len(unused) == 0 || len(used) == 0 {
		return unused
	}

	usedLookup := make(map[string]struct{}, len(used))
	for i := range used {
		usedLookup[importUseKey(used[i])] = struct{}{}
	}

	filtered := unused[:0]
	for i := range unused {
		item := unused[i]
		if _, exists := usedLookup[importUseKey(item)]; exists {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func importUseKey(item report.ImportUse) string {
	return item.Module + "\x00" + item.Name
}

func runtimeUsageSignals(usage *report.RuntimeUsage) (hasStatic bool, hasRuntime bool) {
	if usage == nil {
		return false, false
	}
	switch usage.Correlation {
	case report.RuntimeCorrelationOverlap:
		return true, true
	case report.RuntimeCorrelationRuntimeOnly:
		return false, true
	case report.RuntimeCorrelationStaticOnly:
		return true, false
	}
	if usage.RuntimeOnly {
		return false, usage.LoadCount > 0
	}
	return true, usage.LoadCount > 0
}

func mergeRuntimeCorrelation(hasStatic, hasRuntime bool) report.RuntimeCorrelation {
	switch {
	case hasStatic && hasRuntime:
		return report.RuntimeCorrelationOverlap
	case hasRuntime:
		return report.RuntimeCorrelationRuntimeOnly
	default:
		return report.RuntimeCorrelationStaticOnly
	}
}

func supportsJSTraceLanguage(languageID string) bool {
	switch strings.TrimSpace(strings.ToLower(languageID)) {
	case "", "auto", language.All, "js-ts":
		return true
	default:
		return false
	}
}

func mergeRuntimeModuleUsage(left, right []report.RuntimeModuleUsage) []report.RuntimeModuleUsage {
	merged := make(map[string]report.RuntimeModuleUsage)
	for _, item := range append(append([]report.RuntimeModuleUsage{}, left...), right...) {
		if current, ok := merged[item.Module]; ok {
			current.Count += item.Count
			merged[item.Module] = current
			continue
		}
		merged[item.Module] = item
	}
	items := make([]report.RuntimeModuleUsage, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Module < items[j].Module
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func mergeRuntimeSymbolUsage(left, right []report.RuntimeSymbolUsage) []report.RuntimeSymbolUsage {
	merged := make(map[string]report.RuntimeSymbolUsage)
	for _, item := range append(append([]report.RuntimeSymbolUsage{}, left...), right...) {
		key := item.Module + "\x00" + item.Symbol
		if current, ok := merged[key]; ok {
			current.Count += item.Count
			merged[key] = current
			continue
		}
		merged[key] = item
	}
	items := make([]report.RuntimeSymbolUsage, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			if items[i].Module == items[j].Module {
				return items[i].Symbol < items[j].Symbol
			}
			return items[i].Module < items[j].Module
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}

func mergeSymbolRefs(left []report.SymbolRef, right []report.SymbolRef) []report.SymbolRef {
	return mergeUniqueSorted(left, right, symbolRefKey, sortSymbolRefs)
}

func mergeRiskCues(left []report.RiskCue, right []report.RiskCue) []report.RiskCue {
	return mergeUniqueSorted(left, right, riskCueKey, sortRiskCues)
}

func mergeRecommendations(left []report.Recommendation, right []report.Recommendation) []report.Recommendation {
	return mergeUniqueSorted(left, right, recommendationKey, sortRecommendations)
}

func mergeCodemodReport(left, right *report.CodemodReport) *report.CodemodReport {
	if left == nil && right == nil {
		return nil
	}
	if left == nil {
		copyRight := *right
		return &copyRight
	}
	if right == nil {
		copyLeft := *left
		return &copyLeft
	}

	mode := left.Mode
	if strings.TrimSpace(mode) == "" {
		mode = right.Mode
	}
	suggestions := mergeUniqueSorted(left.Suggestions, right.Suggestions, codemodSuggestionKey, sortCodemodSuggestions)
	skips := mergeUniqueSorted(left.Skips, right.Skips, codemodSkipKey, sortCodemodSkips)
	return &report.CodemodReport{
		Mode:        mode,
		Suggestions: suggestions,
		Skips:       skips,
	}
}

func mergedSuggestionSortKey(item report.CodemodSuggestion) string {
	return fmt.Sprintf("%s|%09d|%s|%s", item.File, item.Line, item.ImportName, item.ToModule)
}

func mergedSkipSortKey(item report.CodemodSkip) string {
	return fmt.Sprintf("%s|%09d|%s|%s", item.File, item.Line, item.ReasonCode, item.ImportName)
}

func symbolRefKey(item report.SymbolRef) string {
	return item.Module + "\x00" + item.Name
}

func sortSymbolRefs(items []report.SymbolRef) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
}

func riskCueKey(item report.RiskCue) string {
	return item.Code + "\x00" + item.Severity
}

func sortRiskCues(items []report.RiskCue) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Code < items[j].Code
	})
}

func recommendationKey(item report.Recommendation) string {
	return item.Code
}

func sortRecommendations(items []report.Recommendation) {
	sort.Slice(items, func(i, j int) bool {
		return recommendationLess(items[i], items[j])
	})
}

func codemodSuggestionKey(item report.CodemodSuggestion) string {
	return item.File + "\x00" + fmt.Sprintf("%d", item.Line) + "\x00" + item.ImportName + "\x00" + item.ToModule
}

func sortCodemodSuggestions(items []report.CodemodSuggestion) {
	sort.Slice(items, func(i, j int) bool {
		return mergedSuggestionSortKey(items[i]) < mergedSuggestionSortKey(items[j])
	})
}

func codemodSkipKey(item report.CodemodSkip) string {
	return item.File + "\x00" + fmt.Sprintf("%d", item.Line) + "\x00" + item.ImportName + "\x00" + item.ReasonCode
}

func sortCodemodSkips(items []report.CodemodSkip) {
	sort.Slice(items, func(i, j int) bool {
		return mergedSkipSortKey(items[i]) < mergedSkipSortKey(items[j])
	})
}

func mergeUniqueSorted[T any](left []T, right []T, keyFn func(T) string, sortFn func([]T)) []T {
	merged := make(map[string]T)
	for _, elem := range left {
		merged[keyFn(elem)] = elem
	}
	for _, elem := range right {
		merged[keyFn(elem)] = elem
	}
	items := make([]T, 0, len(merged))
	for _, elem := range merged {
		items = append(items, elem)
	}
	sortFn(items)
	return items
}

func mergeRuntimeUsage(left, right *report.RuntimeUsage) *report.RuntimeUsage {
	if left == nil && right == nil {
		return nil
	}
	loadCount := 0
	hasStatic := false
	hasRuntime := false
	leftModules := []report.RuntimeModuleUsage(nil)
	leftSymbols := []report.RuntimeSymbolUsage(nil)
	rightModules := []report.RuntimeModuleUsage(nil)
	rightSymbols := []report.RuntimeSymbolUsage(nil)
	if left != nil {
		loadCount += left.LoadCount
		leftHasStatic, leftHasRuntime := runtimeUsageSignals(left)
		hasStatic = hasStatic || leftHasStatic
		hasRuntime = hasRuntime || leftHasRuntime
		leftModules = left.Modules
		leftSymbols = left.TopSymbols
	}
	if right != nil {
		loadCount += right.LoadCount
		rightHasStatic, rightHasRuntime := runtimeUsageSignals(right)
		hasStatic = hasStatic || rightHasStatic
		hasRuntime = hasRuntime || rightHasRuntime
		rightModules = right.Modules
		rightSymbols = right.TopSymbols
	}
	correlation := mergeRuntimeCorrelation(hasStatic, hasRuntime)
	return &report.RuntimeUsage{
		LoadCount:   loadCount,
		Correlation: correlation,
		RuntimeOnly: correlation == report.RuntimeCorrelationRuntimeOnly,
		Modules:     mergeRuntimeModuleUsage(leftModules, rightModules),
		TopSymbols:  mergeRuntimeSymbolUsage(leftSymbols, rightSymbols),
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

func recommendationLess(left, right report.Recommendation) bool {
	if left.Priority == right.Priority {
		return left.Code < right.Code
	}
	return recommendationPriorityRank(left.Priority) < recommendationPriorityRank(right.Priority)
}

func mergeImportUses(left, right []report.ImportUse) []report.ImportUse {
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
