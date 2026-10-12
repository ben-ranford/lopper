package analysis

import (
	"context"
	"slices"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type pendingCacheReport struct {
	entry  cacheEntryDescriptor
	report report.Report
}

func selectedGradleIdentityLimit(candidates []language.Candidate) int {
	for _, candidate := range candidates {
		if candidate.Adapter.ID() == "jvm" || candidate.Adapter.ID() == kotlinAndroidLanguageName {
			return maxIdentityDiscoveryFiles
		}
	}
	return 0
}

func (p *analysisPipeline) finalizeAndPublish(ctx context.Context, current report.Report) (report.Report, error) {
	result, err := finalizeReportWithIdentityPolicy(ctx, p.request, p.repoPath, p.analysisRepoPath, p.remappedAnalyzedRoots(), current, p.identityPolicy)
	if err != nil {
		return report.Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return report.Report{}, err
	}
	if p.cache != nil {
		p.cache.publishPending()
		result.Warnings = uniqueSorted(append(result.Warnings, p.cache.takeWarnings()...))
		result.Cache = p.cacheMetadata()
	}
	return result, nil
}

func (c *analysisCache) publishPending() {
	defer func() { c.pending = nil }()
	for _, item := range c.pending {
		if err := c.store(item.entry, item.report); err != nil {
			c.warn("analysis cache store failed for " + item.entry.AdapterID + ":" + item.entry.RootPath + ": " + err.Error())
		}
	}
}

// Pending cache reports borrow immutable Maven documents. Detach only fields
// that candidate preparation, merging or finalization can mutate in place.
func detachPendingReport(current report.Report) report.Report {
	current.Dependencies = slices.Clone(current.Dependencies)
	current.CoverageGaps = slices.Clone(current.CoverageGaps)
	current.Warnings = slices.Clone(current.Warnings)
	for i := range current.Dependencies {
		detachPendingDependency(&current.Dependencies[i])
	}
	return current
}

func detachPendingDependency(dep *report.DependencyReport) {
	dep.UsedImports = detachPendingImports(dep.UsedImports)
	dep.UnusedImports = detachPendingImports(dep.UnusedImports)
	dep.SuppressedUnusedImports = detachPendingImports(dep.SuppressedUnusedImports)
	dep.UnusedExports = slices.Clone(dep.UnusedExports)
	dep.RiskCues = slices.Clone(dep.RiskCues)
	dep.Recommendations = slices.Clone(dep.Recommendations)
	if dep.RuntimeUsage != nil {
		value := *dep.RuntimeUsage
		dep.RuntimeUsage = &value
	}
	if dep.License != nil {
		value := *dep.License
		dep.License = &value
	}
}

func detachPendingImports(imports []report.ImportUse) []report.ImportUse {
	result := slices.Clone(imports)
	for i := range result {
		result[i].Locations = slices.Clone(result[i].Locations)
	}
	return result
}
