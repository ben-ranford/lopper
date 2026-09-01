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
)

// ErrIncompleteCoverage reports that an enforced analysis policy cannot trust partial dependency coverage.
var ErrIncompleteCoverage = errors.New("complete dependency coverage is required")

func (s *Service) runCandidates(ctx context.Context, req Request, repoPath string, candidates []language.Candidate, cache *analysisCache, trueRepoPathOverride ...string) ([]report.Report, []string, []string, error) {
	reports := make([]report.Report, 0, len(candidates))
	warnings := make([]string, 0)
	analyzedRoots := make([]string, 0)
	lowConfidenceThreshold := resolveLowConfidenceWarningThreshold(req.LowConfidenceWarningPercent)
	for _, candidate := range candidates {
		warnings = append(warnings, lowConfidenceWarning(req.Language, candidate, lowConfidenceThreshold)...)
		candidateReports, candidateWarnings, candidateRoots, err := s.runCandidateOnRoots(ctx, req, repoPath, candidate, cache, trueRepoPathOverride...)
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
	reports := make([]report.Report, 0)
	warnings := make([]string, 0)
	analyzedRoots := make([]string, 0)
	rootSeen := make(map[string]struct{})
	roots, rootWarnings := scopedCandidateRootsForRequest(req, candidate.Detection.Roots, repoPath)
	warnings = append(warnings, rootWarnings...)
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

		cacheEntry, cachedReport, hit := prepareAndLoadCachedReport(req, cache, candidate.Adapter.ID(), normalizedRoot, trueRepoPathOverride...)
		if hit {
			applyLanguageID(cachedReport.Dependencies, candidate.Adapter.ID())
			adjustRelativeLocations(repoPath, normalizedRoot, cachedReport.Dependencies)
			if err := incompleteCoverageReportError(req, candidate.Adapter.ID(), normalizedRoot, cachedReport); err != nil {
				return nil, nil, nil, err
			}
			reports = append(reports, cachedReport)
			continue
		}

		exclusions := cache.cacheAnalysisExclusions(normalizedRoot, req, trueRepoPathOverride...)
		current, err := candidate.Adapter.Analyse(ctx, language.AnalysisOptions{
			RepoPath:                          normalizedRoot,
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
			if shouldFailAdapterError(req) {
				return nil, nil, nil, err
			}
			if isMultiLanguage(req.Language) {
				warnings = append(warnings, err.Error())
				continue
			}
			return nil, nil, nil, err
		}
		storeCachedReport(cache, candidate.Adapter.ID(), normalizedRoot, cacheEntry, current)
		applyLanguageID(current.Dependencies, candidate.Adapter.ID())
		adjustRelativeLocations(repoPath, normalizedRoot, current.Dependencies)
		if err := incompleteCoverageReportError(req, candidate.Adapter.ID(), normalizedRoot, current); err != nil {
			return nil, nil, nil, err
		}
		reports = append(reports, current)
	}
	return reports, warnings, analyzedRoots, nil
}

func shouldFailAdapterError(req Request) bool {
	return req.RequireCompleteCoverage
}

func incompleteCoverageReportError(req Request, adapterID, root string, reportData report.Report) error {
	if !req.RequireCompleteCoverage {
		return nil
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
	cacheEntry, err := cache.prepareEntry(req, adapterID, normalizedRoot, trueRepoPathOverride...)
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
