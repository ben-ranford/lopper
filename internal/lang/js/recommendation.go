package js

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

var replacementHints = map[string]string{
	"lodash": "Prefer per-method imports (`lodash/<method>`) or native JS methods when possible.",
	"moment": "Consider `date-fns` or `dayjs` for a smaller date utility footprint.",
	"axios":  "If browser/runtime support allows, consider native `fetch`.",
}

func buildRecommendations(
	dependency string,
	dep report.DependencyReport,
) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 4)

	if dep.UsedExportsCount == 0 && len(dep.UsedImports) == 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dependency),
			Rationale: "Unused dependencies increase install size and maintenance surface.",
		})
	}

	rootImportUsed := false
	subpathImportUsed := false
	usesWildcardLike := false
	for _, imp := range append(append([]report.ImportUse{}, dep.UsedImports...), dep.UnusedImports...) {
		if imp.Module == dependency {
			rootImportUsed = true
		}
		if strings.HasPrefix(imp.Module, dependency+"/") {
			subpathImportUsed = true
		}
		if imp.Name == "*" || imp.Name == "default" {
			usesWildcardLike = true
		}
	}

	knownExportSurface := dep.TotalExportsCount > 0
	if knownExportSurface && rootImportUsed && !subpathImportUsed && dep.UsedPercent > 0 && dep.UsedPercent < 40 {
		recs = append(recs, report.Recommendation{
			Code:      "prefer-subpath-imports",
			Priority:  "medium",
			Message:   fmt.Sprintf("Only %.1f%% of %q exports are used; prefer subpath imports for used APIs.", dep.UsedPercent, dependency),
			Rationale: "Subpath imports can reduce bundled/transpiled dependency surface.",
		})
	}

	if usesWildcardLike {
		recs = append(recs, report.Recommendation{
			Code:      "avoid-wildcard-default-imports",
			Priority:  "medium",
			Message:   "Default/namespace imports were detected; switch to named imports for better precision.",
			Rationale: "Named imports improve static analysis and often improve tree-shaking outcomes.",
		})
	}

	if hint, ok := replacementHints[dependency]; ok {
		if dep.UsedPercent > 0 && dep.UsedPercent <= 35 || usesWildcardLike {
			recs = append(recs, report.Recommendation{
				Code:      "consider-replacement",
				Priority:  "low",
				Message:   hint,
				Rationale: fmt.Sprintf("%q currently has relatively low measured usage in this repo.", dependency),
			})
		}
	}

	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Priority == recs[j].Priority {
			return recs[i].Code < recs[j].Code
		}
		return recommendationPriorityRank(recs[i].Priority) < recommendationPriorityRank(recs[j].Priority)
	})
	return recs
}

func recommendationPriorityRank(priority string) int {
	switch priority {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}
