package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type analysisPipeline struct {
	service          *Service
	request          Request
	repoPath         string
	analysisRepoPath string
	scopeWarnings    []string
	cleanupFn        func()
	candidates       []language.Candidate
	cache            *analysisCache
	reports          []report.Report
	warnings         []string
	analyzedRoots    []string
}

func (s *Service) newAnalysisPipeline(ctx context.Context, req Request) (*analysisPipeline, error) {
	repoPath, err := s.prepareAnalysis(req)
	if err != nil {
		return nil, err
	}

	req.ScopeMode = normalizeScopeMode(req.ScopeMode)
	analysisRepoPath, scopeWarnings, cleanupFn, err := applyPathScope(repoPath, req.IncludePatterns, req.ExcludePatterns)
	if err != nil {
		return nil, err
	}
	candidates, err := s.resolveCandidates(ctx, analysisRepoPath, req)
	if err != nil {
		cleanupFn()
		return nil, err
	}

	return &analysisPipeline{
		service:          s,
		request:          req,
		repoPath:         repoPath,
		analysisRepoPath: analysisRepoPath,
		scopeWarnings:    scopeWarnings,
		cleanupFn:        cleanupFn,
		candidates:       candidates,
		cache:            newAnalysisCache(req, analysisRepoPath),
	}, nil
}

func (p *analysisPipeline) cleanup() {
	if p.cleanupFn != nil {
		p.cleanupFn()
	}
}

func (p *analysisPipeline) execute(ctx context.Context) error {
	reports, warnings, analyzedRoots, err := p.service.runCandidates(ctx, p.request, p.analysisRepoPath, p.candidates, p.cache)
	if err != nil {
		return err
	}
	runtimeWarnings, runtimeTracePath := captureRuntimeTraceIfNeeded(ctx, p.request, p.repoPath, p.cache)
	p.reports = reports
	warnings = append(warnings, runtimeWarnings...)
	p.warnings = warnings
	p.analyzedRoots = analyzedRoots
	p.request.RuntimeTracePath = runtimeTracePath
	return nil
}

func (p *analysisPipeline) finalReport() (report.Report, error) {
	reportData := report.Report{
		RepoPath: p.repoPath,
		Warnings: p.collectWarnings(),
		Cache:    p.cacheMetadata(),
	}
	if len(p.reports) == 0 {
		reportData.Warnings = append(reportData.Warnings, "no language adapter produced results")
		return finalizeReport(p.request, p.repoPath, p.remappedAnalyzedRoots(), reportData)
	}

	merged := mergeReports(p.repoPath, p.reports)
	merged.Warnings = append(merged.Warnings, reportData.Warnings...)
	merged.Cache = reportData.Cache
	return finalizeReport(p.request, p.repoPath, p.remappedAnalyzedRoots(), merged)
}

func (p *analysisPipeline) collectWarnings() []string {
	warnings := append([]string(nil), p.scopeWarnings...)
	warnings = append(warnings, p.warnings...)
	if p.cache != nil {
		warnings = append(warnings, p.cache.takeWarnings()...)
	}
	return warnings
}

func (p *analysisPipeline) cacheMetadata() *report.CacheMetadata {
	if p.cache == nil {
		return nil
	}
	return p.cache.metadataSnapshot()
}

func (p *analysisPipeline) remappedAnalyzedRoots() []string {
	return remapAnalyzedRoots(p.analyzedRoots, p.analysisRepoPath, p.repoPath)
}

func finalizeReport(req Request, repoPath string, analyzedRoots []string, reportData report.Report) (report.Report, error) {
	var err error
	reportData, err = annotateRuntimeTraceIfPresent(req.RuntimeTracePath, req.Language, reportData)
	if err != nil {
		return report.Report{}, err
	}

	lowConfidenceThreshold := float64(resolveLowConfidenceWarningThreshold(req.LowConfidenceWarningPercent))
	report.AnnotateReachabilityConfidence(&reportData)
	report.AnnotateFindingConfidence(reportData.Dependencies)
	report.FilterFindingsByConfidence(reportData.Dependencies, lowConfidenceThreshold)
	report.NormalizeDependencyLicenses(reportData.Dependencies)
	report.ApplyLicensePolicy(reportData.Dependencies, req.LicenseDenyList)
	reportData.Scope = scopeMetadata(req.ScopeMode, repoPath, analyzedRoots)
	report.AnnotateRemovalCandidateScoresWithWeights(reportData.Dependencies, resolveRemovalCandidateWeights(req.RemovalCandidateWeights))
	reportData.Summary = report.ComputeSummary(reportData.Dependencies)
	reportData.LanguageBreakdown = report.ComputeLanguageBreakdown(reportData.Dependencies)
	reportData.SchemaVersion = report.SchemaVersion
	return reportData, nil
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

func supportsJSTraceLanguage(languageID string) bool {
	switch strings.TrimSpace(strings.ToLower(languageID)) {
	case "", "auto", language.All, "js-ts":
		return true
	default:
		return false
	}
}
