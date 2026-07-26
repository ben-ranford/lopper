package analysis

import (
	"context"
	"errors"
	"fmt"
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

const (
	pythonRuntimeTraceFeature   = "python-runtime-trace"
	pythonRuntimeCaptureFeature = "python-runtime-capture"
)

type analysisPipeline struct {
	service           *Service
	request           Request
	repoPath          string
	executionRepoPath string
	analysisRepoPath  string
	repositoryView    *RepositoryView
	ownsRepository    bool
	scopeWarnings     []string
	cleanupFn         func()
	candidates        []language.Candidate
	cache             *analysisCache
	reports           []report.Report
	warnings          []string
	analyzedRoots     []string
}

func (s *Service) newAnalysisPipeline(ctx context.Context, req Request) (*analysisPipeline, error) {
	repoPath, err := s.prepareAnalysis(req)
	if err != nil {
		return nil, err
	}
	repository, err := resolvePipelineRepository(repoPath, req)
	if err != nil {
		return nil, err
	}
	req.Repository = repository
	req.Cache, err = normalizePipelineCacheOptions(repoPath, req)
	if err != nil {
		return nil, err
	}
	repositoryView, ownsRepository, err := resolvePipelineRepositoryView(ctx, repoPath, req)
	if err != nil {
		return nil, err
	}
	executionRepoPath := repositoryView.ExecutionPath()
	req.RepositoryView = repositoryView
	req.RepoPath = repositoryView.StablePath(executionRepoPath)
	req.ConfigCachePath = repositoryView.StablePath(req.ConfigPath)
	req.RuntimeTraceCachePath = repositoryView.StablePath(req.RuntimeTracePath)
	req.ConfigPath, err = repositoryView.SnapshotPath(req.ConfigPath)
	if err != nil {
		if ownsRepository {
			err = errors.Join(err, repositoryView.Close())
		}
		return nil, err
	}
	if req.RuntimeTracePathExplicit {
		req.RuntimeTracePath, err = repositoryView.SnapshotPath(req.RuntimeTracePath)
		if err != nil {
			if ownsRepository {
				err = errors.Join(err, repositoryView.Close())
			}
			return nil, err
		}
	}

	req.ScopeMode = normalizeScopeMode(req.ScopeMode)
	analysisRepoPath, scopeWarnings, cleanupFn, err := applyPathScope(executionRepoPath, req.IncludePatterns, req.ExcludePatterns, pinnedScopedCacheRoot(req.Cache))
	if err != nil {
		if ownsRepository {
			err = errors.Join(err, repositoryView.Close())
		}
		return nil, err
	}
	candidates, err := s.resolveCandidates(ctx, analysisRepoPath, req)
	if err != nil {
		cleanupFn()
		if ownsRepository {
			err = errors.Join(err, repositoryView.Close())
		}
		return nil, err
	}

	return &analysisPipeline{
		service:           s,
		request:           req,
		repoPath:          repoPath,
		executionRepoPath: executionRepoPath,
		analysisRepoPath:  analysisRepoPath,
		repositoryView:    repositoryView,
		ownsRepository:    ownsRepository,
		scopeWarnings:     scopeWarnings,
		cleanupFn:         cleanupFn,
		candidates:        candidates,
		cache:             newAnalysisCache(req, analysisRepoPath, repositoryView),
	}, nil
}

func (p *analysisPipeline) cleanup() error {
	if p.cleanupFn != nil {
		p.cleanupFn()
	}
	if p.ownsRepository && p.repositoryView != nil {
		return p.repositoryView.Close()
	}
	return nil
}

func normalizePipelineCacheOptions(repoPath string, req Request) (*CacheOptions, error) {
	if req.Cache == nil {
		return ResolveTrustedDefaultCacheOptionsForRepository(req.Repository, false)
	}
	if !req.Cache.Enabled {
		return req.Cache, nil
	}
	if strings.TrimSpace(req.Cache.Path) == "" && !req.Cache.hasTrustedPin() {
		return ResolveTrustedDefaultCacheOptionsForRepository(req.Repository, req.Cache.ReadOnly)
	}
	cacheOptions, err := resolvePipelineCacheOptions(repoPath, req)
	if err != nil {
		return nil, err
	}
	if err := ValidateTrustedScopedCacheOptions(cacheOptions, req.IncludePatterns, req.ExcludePatterns); err != nil {
		return nil, err
	}
	return cacheOptions, nil
}

func resolvePipelineCacheOptions(repoPath string, req Request) (*CacheOptions, error) {
	if req.Repository == nil {
		repository, err := resolvePipelineRepository(repoPath, req)
		if err != nil {
			return nil, err
		}
		req.Repository = repository
	} else if err := useTrustedRepository(repoPath, req.Repository); err != nil {
		return nil, err
	}
	if req.Cache.hasTrustedPin() {
		if req.Cache.trustedPin.repositoryState != req.Repository.authorizationState() {
			return nil, errors.New("trusted cache pin does not match repository authorization")
		}
		return useTrustedCacheOptions(repoPath, req.Cache)
	}
	cacheOptions, err := ResolveTrustedCacheOptionsForRepository(req.Repository, req.Cache)
	if err == nil {
		return cacheOptions, nil
	}
	if AuthenticatedExternalCacheOptions(cacheOptions, err) {
		return cacheOptions, nil
	}
	return nil, err
}

func resolvePipelineRepository(repoPath string, req Request) (*RepositoryAuthorization, error) {
	if req.Repository != nil {
		if err := useTrustedRepository(repoPath, req.Repository); err != nil {
			return nil, err
		}
		return req.Repository, nil
	}
	if req.Cache.hasTrustedPin() {
		repository := newRepositoryAuthorization(req.Cache.trustedPin.repositoryState)
		if err := useTrustedRepository(repoPath, repository); err != nil {
			return nil, err
		}
		return repository, nil
	}
	return ResolveTrustedRepository(repoPath)
}

func resolvePipelineRepositoryView(ctx context.Context, repoPath string, req Request) (*RepositoryView, bool, error) {
	if req.RepositoryView != nil {
		if !req.RepositoryView.matches(req.Repository) {
			return nil, false, errors.New("trusted repository view does not match repository authorization")
		}
		return req.RepositoryView, false, nil
	}
	view, err := OpenTrustedRepository(ctx, req.Repository, repoPath, req.Cache)
	if err != nil {
		return nil, false, err
	}
	return view, true, nil
}

// ValidateTrustedScopedCacheOptions rejects authenticated in-repository cache
// roots that would otherwise be copied into a scoped workspace.
func ValidateTrustedScopedCacheOptions(cacheOptions *CacheOptions, includePatterns, excludePatterns []string) error {
	if !patternsUseScopedWorkspace(includePatterns, excludePatterns) || cacheOptions == nil || !cacheOptions.Enabled {
		return nil
	}
	if !scopedCacheTargetsRepositoryRoot(cacheOptions) {
		return nil
	}
	return fmt.Errorf("scoped analysis does not allow cachePath at the repository root because cache keys/objects would be copied into the scoped workspace")
}

func scopedCacheTargetsRepositoryRoot(cacheOptions *CacheOptions) bool {
	return InRepoCacheOptions(cacheOptions) && cacheOptions.trustedPin.repoRelativePath == "."
}

func requestUsesScopedWorkspace(req Request) bool {
	return patternsUseScopedWorkspace(req.IncludePatterns, req.ExcludePatterns)
}

func patternsUseScopedWorkspace(includePatterns, excludePatterns []string) bool {
	return len(normalizePatterns(includePatterns)) > 0 || len(normalizePatterns(excludePatterns)) > 0
}

func pinnedScopedCacheRoot(cache *CacheOptions) string {
	if cache == nil || !cache.Enabled || !InRepoCacheOptions(cache) {
		return ""
	}
	relativePath := cache.trustedPin.repoRelativePath
	if relativePath == "." {
		return ""
	}
	return filepath.Clean(relativePath)
}

func (p *analysisPipeline) execute(ctx context.Context) error {
	reports, warnings, analyzedRoots, err := p.service.runCandidates(ctx, p.request, p.analysisRepoPath, p.candidates, p.cache)
	if err != nil {
		return err
	}
	runtimeWarnings, runtimeTracePath, pythonRuntimeTraceCaptured := captureRuntimeTraceIfNeeded(ctx, p.request, p.executionRepoPath, p.cache, p.candidates)
	persistWarnings, err := p.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		return err
	}
	p.reports = reports
	warnings = append(warnings, runtimeWarnings...)
	warnings = append(warnings, persistWarnings...)
	p.warnings = warnings
	p.analyzedRoots = analyzedRoots
	p.request.RuntimeTracePath = runtimeTracePath
	p.request.PythonRuntimeTraceCaptured = pythonRuntimeTraceCaptured
	return nil
}

func (p *analysisPipeline) persistRuntimeTrace(runtimeTracePath string) ([]string, error) {
	if p == nil || p.repositoryView == nil || strings.TrimSpace(runtimeTracePath) == "" || strings.TrimSpace(p.request.RuntimeTestCommand) == "" {
		return nil, nil
	}
	relativePath, inRepository := p.repositoryView.RepositoryRelativePath(runtimeTracePath)
	if !inRepository || !isSafeRepositoryRelativePath(relativePath, false) {
		return nil, nil
	}
	data, err := p.repositoryView.ReadExecutionFile(relativePath)
	if err != nil {
		if p.request.RuntimeTracePathExplicit {
			return nil, fmt.Errorf("persist requested runtime trace %s: %w", p.repositoryView.DisplayPath(relativePath), err)
		}
		return nil, nil
	}
	if err := p.repositoryView.WriteFile(relativePath, data, 0o600); err != nil {
		if p.request.RuntimeTracePathExplicit {
			return nil, fmt.Errorf("persist requested runtime trace %s: %w", p.repositoryView.DisplayPath(relativePath), err)
		}
		return nil, nil
	}
	stateRelativePath := relativePath + ".state.json"
	stateData, err := p.repositoryView.ReadExecutionFile(stateRelativePath)
	if err == nil {
		if err := p.repositoryView.WriteFile(stateRelativePath, stateData, 0o600); err != nil {
			if !p.request.RuntimeTracePathExplicit {
				return nil, nil
			}
			return []string{fmt.Sprintf("runtime trace state was not persisted to %s: %v", p.repositoryView.DisplayPath(stateRelativePath), err)}, nil
		}
		return nil, nil
	}
	if errors.Is(err, os.ErrNotExist) || !p.request.RuntimeTracePathExplicit {
		return nil, nil
	}
	return []string{fmt.Sprintf("runtime trace state was not persisted to %s: %v", p.repositoryView.DisplayPath(stateRelativePath), err)}, nil
}

func (p *analysisPipeline) finalReport() (report.Report, error) {
	reportData := report.Report{
		RepoPath: p.repoPath,
		Warnings: p.collectWarnings(),
		Cache:    p.cacheMetadata(),
	}
	if len(p.reports) == 0 {
		reportData.Warnings = append(reportData.Warnings, "no language adapter produced results")
		return finalizeReport(p.request, p.repoPath, p.analysisRepoPath, p.remappedAnalyzedRoots(), reportData)
	}

	merged := mergeReports(p.repoPath, p.reports)
	merged.Warnings = append(merged.Warnings, reportData.Warnings...)
	merged.Cache = reportData.Cache
	return finalizeReport(p.request, p.repoPath, p.analysisRepoPath, p.remappedAnalyzedRoots(), merged)
}

func (p *analysisPipeline) collectWarnings() []string {
	warnings := append([]string(nil), p.scopeWarnings...)
	if p.repositoryView != nil {
		warnings = append(warnings, p.repositoryView.snapshotWarnings()...)
	}
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

func finalizeReport(req Request, repoPath string, identityRepoPath string, analyzedRoots []string, reportData report.Report) (report.Report, error) {
	var err error
	pythonRuntimeTraceEnabled := req.Features.Enabled(pythonRuntimeTraceFeature) ||
		(req.PythonRuntimeTraceCaptured && req.Features.Enabled(pythonRuntimeCaptureFeature))
	reportData, err = annotateRuntimeTraceIfPresent(req.RuntimeTracePath, req.Language, reportData, pythonRuntimeTraceEnabled)
	if err != nil {
		return report.Report{}, err
	}

	lowConfidenceThreshold := float64(resolveLowConfidenceWarningThreshold(req.LowConfidenceWarningPercent))
	annotateDerivedDependencyMetrics(reportData.Dependencies)
	if identityPreviewEnabled(req) {
		annotateDependencyIdentities(identityRepoPath, &reportData)
	}
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
	return scopedCandidateRootsForRequest(Request{ScopeMode: scopeMode}, roots, repoPath)
}

func scopedCandidateRootsForRequest(req Request, roots []string, repoPath string) ([]string, []string) {
	switch normalizeScopeMode(req.ScopeMode) {
	case ScopeModeRepo:
		return []string{repoPath}, nil
	case ScopeModeChangedPackages:
		baseRoots := candidateRoots(roots, repoPath)
		if req.ChangedFilesExplicit {
			return changedRoots(baseRoots, repoPath, req.ChangedFiles), nil
		}
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
	if len(roots) == 0 || len(changedFiles) == 0 {
		return nil
	}
	rootIndex := changedRootIndex(roots, repoPath)
	if len(rootIndex) == 0 {
		return nil
	}

	changed := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, file := range changedFiles {
		changed = appendChangedRootAncestors(changed, seen, rootIndex, absoluteChangedPath(repoPath, file))
	}
	return uniqueSorted(changed)
}

func appendChangedRootAncestors(changed []string, seen map[string]struct{}, rootIndex map[string][]string, path string) []string {
	for current := path; ; {
		changed = appendChangedRoots(changed, seen, rootIndex[current])
		parent := filepath.Dir(current)
		if parent == current {
			return changed
		}
		current = parent
	}
}

func appendChangedRoots(changed []string, seen map[string]struct{}, roots []string) []string {
	for _, root := range roots {
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		changed = append(changed, root)
	}
	return changed
}

func changedRootIndex(roots []string, repoPath string) map[string][]string {
	index := make(map[string][]string, len(roots))
	for _, root := range roots {
		path := absoluteRootPath(repoPath, root)
		index[path] = append(index[path], root)
	}
	return index
}

func absoluteRootPath(repoPath, root string) string {
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	return filepath.Clean(filepath.Join(repoPath, root))
}

func absoluteChangedPath(repoPath, file string) string {
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	return filepath.Clean(filepath.Join(repoPath, file))
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
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			remapped = append(remapped, root)
			continue
		}
		if rel == "." {
			remapped = append(remapped, toRepoPath)
			continue
		}
		remapped = append(remapped, filepath.Join(toRepoPath, rel))
	}
	return uniqueSorted(remapped)
}

func annotateRuntimeTraceIfPresent(runtimeTracePath string, languageID string, reportData report.Report, pythonRuntimeTraceEnabled bool) (report.Report, error) {
	if runtimeTracePath == "" {
		return reportData, nil
	}
	supportedLanguages := supportedRuntimeTraceLanguages(languageID, pythonRuntimeTraceEnabled)
	if len(supportedLanguages) == 0 {
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
		IncludeRuntimeOnlyRows: true,
		SupportedLanguages:     supportedLanguages,
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

func supportedRuntimeTraceLanguages(languageID string, pythonRuntimeTraceEnabled bool) []string {
	supported := make([]string, 0, 2)
	if supportsJSTraceLanguage(languageID) {
		supported = append(supported, "js-ts")
	}
	if pythonRuntimeTraceEnabled && supportsPythonTraceLanguage(languageID) {
		supported = append(supported, "python")
	}
	return supported
}

func supportsPythonTraceLanguage(languageID string) bool {
	switch strings.TrimSpace(strings.ToLower(languageID)) {
	case "", "auto", language.All, "python", "py":
		return true
	default:
		return false
	}
}
