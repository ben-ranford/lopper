package analysis

import (
	"context"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

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
			Features:                          req.Features,
			MinUsagePercentForRecommendations: req.MinUsagePercentForRecommendations,
			RemovalCandidateWeights:           req.RemovalCandidateWeights,
			IncludeRegistryProvenance:         req.IncludeRegistryProvenance,
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
