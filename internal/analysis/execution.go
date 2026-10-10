package analysis

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
)

// ErrIncompleteCoverage reports that an enforced analysis policy cannot trust partial dependency coverage.
var ErrIncompleteCoverage = errors.New("complete dependency coverage is required")

func (s *Service) runCandidates(ctx context.Context, req Request, repoPath string, candidates []language.Candidate, cache *analysisCache, trueRepoPathOverride ...string) ([]report.Report, []string, []string, error) {
	reports := make([]report.Report, 0, len(candidates))
	warnings := make([]string, 0)
	analyzedRoots := make([]string, 0)
	maven := newMavenEvidenceAccumulator(repoPath)
	lowConfidenceThreshold := resolveLowConfidenceWarningThreshold(req.LowConfidenceWarningPercent)
	for _, candidate := range candidates {
		warnings = append(warnings, lowConfidenceWarning(req.Language, candidate, lowConfidenceThreshold)...)
		candidateReports, candidateWarnings, candidateRoots, err := s.runCandidateOnRootsWithMaven(ctx, req, repoPath, candidate, cache, maven, trueRepoPathOverride...)
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

func (s *Service) runCandidateOnRoots(ctx context.Context, req Request, repoPath string, candidate language.Candidate, cache *analysisCache, trueRepoPathOverride ...string) ([]report.Report, []string, []string, error) {
	return s.runCandidateOnRootsWithMaven(ctx, req, repoPath, candidate, cache, newMavenEvidenceAccumulator(repoPath), trueRepoPathOverride...)
}
func (s *Service) runCandidateOnRootsWithMaven(ctx context.Context, req Request, repoPath string, candidate language.Candidate, cache *analysisCache, maven *mavenEvidenceAccumulator, trueRepoPathOverride ...string) ([]report.Report, []string, []string, error) {
	reports := make([]report.Report, 0)
	warnings := make([]string, 0)
	analyzedRoots := make([]string, 0)
	rootSeen := make(map[string]struct{})
	roots, rootWarnings := scopedCandidateRootsForRequest(req, candidate.Detection.Roots, repoPath)
	warnings = append(warnings, rootWarnings...)
	isolationRoots := normalizedIsolationRoots(repoPath, roots, req.ScopeMode)
	for _, root := range roots {
		normalizedRoot := normalizeCandidateRoot(repoPath, root)
		if normalizedRoot == "" {
			warnings = append(warnings, "skipping candidate root outside repo boundary: "+root)
			continue
		}
		if alreadySeenRoot(rootSeen, normalizedRoot) {
			continue
		}
		analyzedRoots = append(analyzedRoots, normalizedRoot)

		current, adapterFailure, err := s.runCandidateRoot(ctx, req, candidateRootScope{repoPath: repoPath, root: normalizedRoot, isolationRoots: isolationRoots}, candidate, cache, maven, trueRepoPathOverride...)
		if err != nil {
			if !adapterFailure || fatalCandidateError(req, err) {
				return nil, nil, nil, err
			}
			warnings = append(warnings, err.Error())
			continue
		}
		reports = append(reports, current)
	}
	return reports, warnings, analyzedRoots, nil
}

func fatalCandidateError(req Request, err error) bool {
	return errors.Is(err, model.ErrMavenEvidenceLimit) || shouldFailAdapterError(req) || !isMultiLanguage(req.Language)
}

// candidateRootScope keeps the candidate's root and isolation boundary tied to
// the repository used to normalise its reported locations.
type candidateRootScope struct {
	repoPath       string
	root           string
	isolationRoots []string
}

func (s *Service) runCandidateRoot(ctx context.Context, req Request, scope candidateRootScope, candidate language.Candidate, cache *analysisCache, maven *mavenEvidenceAccumulator, trueRepoPathOverride ...string) (report.Report, bool, error) {
	cacheEntry, cachedReport, hit := prepareAndLoadCachedReportWithIsolationRoots(req, cache, candidate.Adapter.ID(), scope.root, scope.isolationRoots, trueRepoPathOverride...)
	if hit {
		if err := maven.accept(cachedReport); err != nil {
			return report.Report{}, false, err
		}
		current, err := prepareCandidateReport(req, scope.repoPath, scope.root, candidate.Adapter.ID(), cachedReport)
		return current, false, err
	}
	exclusions := cache.cacheAnalysisExclusions(scope.root, req, trueRepoPathOverride...)
	current, err := candidate.Adapter.Analyse(ctx, language.AnalysisOptions{
		RepoPath:                          scope.root,
		ScopeMode:                         req.ScopeMode,
		IsolatedProjectRoots:              scope.isolationRoots,
		ExcludedPaths:                     exclusions.directories,
		ExcludedFiles:                     exclusions.files,
		Dependency:                        req.Dependency,
		TopN:                              req.TopN,
		SuggestOnly:                       req.SuggestOnly,
		RuntimeProfile:                    req.RuntimeProfile,
		Features:                          req.Features,
		MinUsagePercentForRecommendations: req.MinUsagePercentForRecommendations,
		RemovalCandidateWeights:           req.RemovalCandidateWeights,
		IncludeRegistryProvenance:         req.IncludeRegistryProvenance,
	})
	if err != nil {
		return report.Report{}, true, err
	}
	if candidate.Adapter.ID() == "jvm" {
		current.RepoPath = scope.root
	}
	if err := maven.accept(current); err != nil {
		return report.Report{}, false, err
	}
	storeCachedReport(cache, candidate.Adapter.ID(), scope.root, cacheEntry, current)
	current, err = prepareCandidateReport(req, scope.repoPath, scope.root, candidate.Adapter.ID(), current)
	return current, false, err
}
func prepareCandidateReport(req Request, repoPath, root, adapter string, current report.Report) (report.Report, error) {
	applyLanguageID(current.Dependencies, adapter)
	adjustRelativeLocations(repoPath, root, current.Dependencies)
	adjustRelativeCoverageGaps(repoPath, root, current.CoverageGaps)
	if err := incompleteCoverageReportError(req, adapter, root, current); err != nil {
		return report.Report{}, err
	}
	return current, nil
}

func shouldFailAdapterError(req Request) bool {
	return req.RequireCompleteCoverage
}

func incompleteCoverageReportError(req Request, adapterID, root string, reportData report.Report) error {
	if !req.RequireCompleteCoverage {
		return nil
	}
	if !req.DeferCoverageGapEnforcement {
		if paths := coverageGapPaths(reportData.CoverageGaps); len(paths) > 0 {
			return fmt.Errorf("%w: adapter %s at %s reported coverage gaps: %s", ErrIncompleteCoverage, adapterID, root, strings.Join(paths, ", "))
		}
	}
	dependencies := incompleteCoverageDependencies(reportData.Dependencies)
	if len(dependencies) == 0 {
		if reportData.UsageIncomplete {
			return fmt.Errorf("%w: adapter %s at %s reported incomplete usage coverage", ErrIncompleteCoverage, adapterID, root)
		}
		return nil
	}
	return fmt.Errorf("%w: adapter %s at %s reported incomplete usage for dependencies: %s", ErrIncompleteCoverage, adapterID, root, strings.Join(dependencies, ", "))
}

func coverageGapPaths(gaps []report.CoverageGap) []string {
	paths := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		path := strings.TrimSpace(gap.Path)
		if path == "" {
			path = strings.TrimSpace(gap.Code)
		}
		if path == "" {
			path = "<unknown>"
		}
		paths = append(paths, path)
	}
	return paths
}

func incompleteCoverageDependencies(dependencies []report.DependencyReport) []string {
	incomplete := make([]string, 0)
	for _, dependency := range dependencies {
		if !dependency.UsageIncomplete {
			continue
		}
		name := strings.TrimSpace(dependency.Name)
		if name == "" {
			name = "<unknown>"
		}
		if languageID := strings.TrimSpace(dependency.Language); languageID != "" {
			name = languageID + ":" + name
		}
		incomplete = append(incomplete, name)
	}
	return incomplete
}

func alreadySeenRoot(seen map[string]struct{}, normalizedRoot string) bool {
	if _, ok := seen[normalizedRoot]; ok {
		return true
	}
	seen[normalizedRoot] = struct{}{}
	return false
}

func prepareAndLoadCachedReport(req Request, cache *analysisCache, adapterID, normalizedRoot string, trueRepoPathOverride ...string) (cacheEntryDescriptor, report.Report, bool) {
	return prepareAndLoadCachedReportWithIsolationRoots(req, cache, adapterID, normalizedRoot, nil, trueRepoPathOverride...)
}

func prepareAndLoadCachedReportWithIsolationRoots(req Request, cache *analysisCache, adapterID, normalizedRoot string, isolationRoots []string, trueRepoPathOverride ...string) (cacheEntryDescriptor, report.Report, bool) {
	cacheEntry, err := cache.prepareEntryWithIsolationRoots(req, adapterID, normalizedRoot, isolationRoots, trueRepoPathOverride...)
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
	if hit && cachedReport.PythonManifestCatalog {
		// Catalog paths are relative to this adapter root, not a previous scoped checkout.
		cachedReport.RepoPath = normalizedRoot
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
		adjustImportLocations(prefix, dependencies[i].SuppressedUnusedImports)
	}
}

func adjustRelativeCoverageGaps(repoPath string, analyzedRoot string, gaps []report.CoverageGap) {
	prefix, err := filepath.Rel(repoPath, analyzedRoot)
	if err != nil || prefix == "." || prefix == "" {
		return
	}
	normalizedPrefix := normalizeCoverageGapLocationPath(prefix)
	for i := range gaps {
		if gaps[i].Path == "" {
			continue
		}
		normalizedPath := normalizeCoverageGapLocationPath(gaps[i].Path)
		if isAbsoluteCoverageGapLocationPath(gaps[i].Path) {
			gaps[i].Path = normalizedPath
			continue
		}
		gaps[i].Path = path.Clean(path.Join(normalizedPrefix, normalizedPath))
	}
}

func adjustImportLocations(prefix string, imports []report.ImportUse) {
	normalizedPrefix := normalizeLocationPath(prefix)
	for j := range imports {
		for k := range imports[j].Locations {
			location := &imports[j].Locations[k]
			normalizedFile := normalizeLocationPath(location.File)
			if isAbsoluteLocationPath(location.File) {
				location.File = normalizedFile
				continue
			}
			location.File = path.Clean(path.Join(normalizedPrefix, normalizedFile))
		}
	}
}

func normalizeLocationPath(value string) string {
	return path.Clean(strings.ReplaceAll(value, "\\", "/"))
}

func normalizeCoverageGapLocationPath(value string) string {
	return filepath.ToSlash(value)
}

func isAbsoluteCoverageGapLocationPath(value string) bool {
	return value != "" && filepath.IsAbs(value)
}

func isAbsoluteLocationPath(value string) bool {
	if value == "" {
		return false
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		drive := value[0]
		return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
	}
	return false
}

func normalizedIsolationRoots(repoPath string, roots []string, scopeMode string) []string {
	if normalizeScopeMode(scopeMode) == ScopeModeRepo {
		return nil
	}
	isolated := make([]string, 0, len(roots))
	for _, root := range roots {
		normalized := normalizeCandidateRoot(repoPath, root)
		if normalized != "" {
			isolated = append(isolated, normalized)
		}
	}
	return uniqueSorted(isolated)
}
