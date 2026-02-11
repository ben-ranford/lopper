package js

import (
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestBuildRecommendationsBranchesAndOrdering(t *testing.T) {
	dep := report.DependencyReport{
		Name:              "lodash",
		UsedExportsCount:  1,
		TotalExportsCount: 10,
		UsedPercent:       10,
		UsedImports: []report.ImportUse{
			{Name: "default", Module: "lodash"},
		},
		UnusedImports: []report.ImportUse{
			{Name: "map", Module: "lodash"},
		},
	}

	recs := buildRecommendations("lodash", dep, thresholds.Defaults().MinUsagePercentForRecommendations)
	if len(recs) < 3 {
		t.Fatalf("expected multiple recommendations, got %#v", recs)
	}
	codes := make([]string, 0, len(recs))
	for _, rec := range recs {
		codes = append(codes, rec.Code)
	}
	for _, want := range []string{
		"avoid-wildcard-default-imports",
		"consider-replacement",
		"prefer-subpath-imports",
	} {
		if !slices.Contains(codes, want) {
			t.Fatalf("expected recommendation %q, got %#v", want, codes)
		}
	}
	if recommendationPriorityRank(recs[0].Priority) > recommendationPriorityRank(recs[len(recs)-1].Priority) {
		t.Fatalf("expected recommendations sorted by priority, got %#v", recs)
	}
}

func TestBuildRecommendationsNoUsedImportsRemoval(t *testing.T) {
	dep := report.DependencyReport{
		Name:              "moment",
		UsedExportsCount:  0,
		TotalExportsCount: 5,
		UsedPercent:       0,
		UsedImports:       nil,
		UnusedImports: []report.ImportUse{
			{Name: "default", Module: "moment"},
		},
	}
	recs := buildRecommendations("moment", dep, thresholds.Defaults().MinUsagePercentForRecommendations)
	codes := make([]string, 0, len(recs))
	for _, rec := range recs {
		codes = append(codes, rec.Code)
	}
	if !slices.Contains(codes, "remove-unused-dependency") {
		t.Fatalf("expected remove-unused-dependency recommendation, got %#v", codes)
	}
}

func TestRecommendationHelperFunctions(t *testing.T) {
	root, subpath, wildcard := importUsageFlags("lodash", report.DependencyReport{
		UsedImports: []report.ImportUse{
			{Name: "map", Module: "lodash/map"},
		},
		UnusedImports: []report.ImportUse{
			{Name: "*", Module: "lodash"},
		},
	})
	if !root || !subpath || !wildcard {
		t.Fatalf("expected root/subpath/wildcard flags to be true, got root=%v subpath=%v wildcard=%v", root, subpath, wildcard)
	}
	if recommendationPriorityRank("high") != 0 || recommendationPriorityRank("medium") != 1 || recommendationPriorityRank("other") != 2 {
		t.Fatalf("unexpected recommendation priority rank mapping")
	}
}
