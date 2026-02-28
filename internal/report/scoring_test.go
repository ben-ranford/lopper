package report

import (
	"math"
	"testing"
)

func TestAnnotateRemovalCandidateScoresDeterministic(t *testing.T) {
	deps := []DependencyReport{
		{Name: "alpha", UsedExportsCount: 5, TotalExportsCount: 10, UsedPercent: 50},
		{Name: "beta", UsedExportsCount: 1, TotalExportsCount: 10, UsedPercent: 10},
	}

	AnnotateRemovalCandidateScores(deps)

	if deps[0].RemovalCandidate == nil || deps[1].RemovalCandidate == nil {
		t.Fatalf("expected removal candidate scores to be populated")
	}
	if deps[1].RemovalCandidate.Score <= deps[0].RemovalCandidate.Score {
		t.Fatalf("expected lower-usage dependency to score higher, alpha=%f beta=%f", deps[0].RemovalCandidate.Score, deps[1].RemovalCandidate.Score)
	}
}

func TestAnnotateRemovalCandidateScoresConfidenceRationale(t *testing.T) {
	deps := []DependencyReport{
		{
			Name:              "wild",
			UsedExportsCount:  0,
			TotalExportsCount: 0,
			UsedImports:       []ImportUse{{Name: "*", Module: "wild"}},
			RiskCues:          []RiskCue{{Severity: "high"}},
			RuntimeUsage:      &RuntimeUsage{LoadCount: 2, RuntimeOnly: true},
		},
	}

	AnnotateRemovalCandidateScores(deps)
	candidate := deps[0].RemovalCandidate
	if candidate == nil {
		t.Fatalf("expected candidate score")
	}
	if candidate.Confidence >= 50 {
		t.Fatalf("expected confidence reduction for wildcard/risk/runtime-only cues, got %f", candidate.Confidence)
	}
	if len(candidate.Rationale) == 0 {
		t.Fatalf("expected rationale entries for confidence penalties")
	}
}

func TestRemovalCandidateScore(t *testing.T) {
	if _, ok := RemovalCandidateScore(DependencyReport{Name: "none"}); ok {
		t.Fatalf("expected missing score to return not-known")
	}
	score, ok := RemovalCandidateScore(DependencyReport{
		Name:             "scored",
		RemovalCandidate: &RemovalCandidate{Score: 42.5},
	})
	if !ok || score != 42.5 {
		t.Fatalf("unexpected score response: score=%f ok=%v", score, ok)
	}
}

func TestAnnotateRemovalCandidateScoresWithWeights(t *testing.T) {
	deps := []DependencyReport{
		{Name: "alpha", UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20},
		{Name: "beta", UsedExportsCount: 8, TotalExportsCount: 10, UsedPercent: 80},
	}
	AnnotateRemovalCandidateScoresWithWeights(deps, RemovalCandidateWeights{
		Usage:      1,
		Impact:     0,
		Confidence: 0,
	})
	if deps[0].RemovalCandidate == nil || deps[1].RemovalCandidate == nil {
		t.Fatalf("expected candidate scores")
	}
	if deps[0].RemovalCandidate.Score <= deps[1].RemovalCandidate.Score {
		t.Fatalf("expected high-usage-waste dependency to rank higher with usage-only weights")
	}
	if deps[0].RemovalCandidate.Weights.Usage != 1 || deps[0].RemovalCandidate.Weights.Impact != 0 || deps[0].RemovalCandidate.Weights.Confidence != 0 {
		t.Fatalf("expected normalized usage-only weights, got %#v", deps[0].RemovalCandidate.Weights)
	}
}

func TestNormalizeRemovalCandidateWeightsFallback(t *testing.T) {
	defaults := DefaultRemovalCandidateWeights()
	got := NormalizeRemovalCandidateWeights(RemovalCandidateWeights{Usage: -1, Impact: 0.5, Confidence: 0.5})
	if got != defaults {
		t.Fatalf("expected invalid weights to fall back to defaults, got %#v", got)
	}
	got = NormalizeRemovalCandidateWeights(RemovalCandidateWeights{})
	if got != defaults {
		t.Fatalf("expected empty weights to fall back to defaults, got %#v", got)
	}
}

func TestNormalizeRemovalCandidateWeightsRejectsNonFinite(t *testing.T) {
	defaults := DefaultRemovalCandidateWeights()
	if got := NormalizeRemovalCandidateWeights(RemovalCandidateWeights{Usage: math.NaN(), Impact: 1, Confidence: 1}); got != defaults {
		t.Fatalf("expected NaN weights to fall back to defaults, got %#v", got)
	}
	if got := NormalizeRemovalCandidateWeights(RemovalCandidateWeights{Usage: math.Inf(1), Impact: 1, Confidence: 1}); got != defaults {
		t.Fatalf("expected Inf weights to fall back to defaults, got %#v", got)
	}
	if got := NormalizeRemovalCandidateWeights(RemovalCandidateWeights{Usage: math.MaxFloat64, Impact: math.MaxFloat64, Confidence: 1}); got != defaults {
		t.Fatalf("expected infinite totals to fall back to defaults, got %#v", got)
	}
}

func TestAnnotateRemovalCandidateScoresWithWeightsEmptyInput(t *testing.T) {
	AnnotateRemovalCandidateScoresWithWeights(nil, RemovalCandidateWeights{Usage: 1, Impact: 0, Confidence: 0})
}

func TestDependencyConfidenceSignalSeverityBranches(t *testing.T) {
	high, _ := dependencyConfidenceSignal(DependencyReport{RiskCues: []RiskCue{{Severity: "high"}}})
	medium, _ := dependencyConfidenceSignal(DependencyReport{RiskCues: []RiskCue{{Severity: "medium"}}})
	low, _ := dependencyConfidenceSignal(DependencyReport{RiskCues: []RiskCue{{Severity: "low"}}})
	if !(high < medium && medium < low) {
		t.Fatalf("expected severity penalties high > medium > low, got high=%f medium=%f low=%f", high, medium, low)
	}
}

func TestClampBounds(t *testing.T) {
	if got := clamp(-2, 0, 100); got != 0 {
		t.Fatalf("expected clamp lower bound, got %f", got)
	}
	if got := clamp(120, 0, 100); got != 100 {
		t.Fatalf("expected clamp upper bound, got %f", got)
	}
}

func TestAnnotateFindingConfidence(t *testing.T) {
	deps := []DependencyReport{
		{
			Name:          "lodash",
			UnusedExports: []SymbolRef{{Name: "chunk", Module: "lodash"}},
			UnusedImports: []ImportUse{{Name: "chunk", Module: "lodash"}},
			RiskCues:      []RiskCue{{Code: "dynamic-require", Severity: "high", Message: "dynamic usage"}},
			Recommendations: []Recommendation{
				{Code: "remove-unused-dependency", Priority: "high", Message: "remove"},
			},
		},
	}

	AnnotateFindingConfidence(deps)

	dep := deps[0]
	if dep.UnusedExports[0].ConfidenceScore == 0 || len(dep.UnusedExports[0].ConfidenceReasonCodes) == 0 {
		t.Fatalf("expected confidence metadata on unused exports, got %#v", dep.UnusedExports[0])
	}
	if dep.UnusedImports[0].ConfidenceScore == 0 || len(dep.UnusedImports[0].ConfidenceReasonCodes) == 0 {
		t.Fatalf("expected confidence metadata on unused imports, got %#v", dep.UnusedImports[0])
	}
	if dep.RiskCues[0].ConfidenceScore == 0 || len(dep.RiskCues[0].ConfidenceReasonCodes) == 0 {
		t.Fatalf("expected confidence metadata on risk cues, got %#v", dep.RiskCues[0])
	}
	if dep.Recommendations[0].ConfidenceScore == 0 || len(dep.Recommendations[0].ConfidenceReasonCodes) == 0 {
		t.Fatalf("expected confidence metadata on recommendations, got %#v", dep.Recommendations[0])
	}
}

func TestFilterFindingsByConfidence(t *testing.T) {
	const packageName = "left-pad"

	deps := []DependencyReport{
		{
			Name:          packageName,
			UnusedExports: []SymbolRef{{Name: "pad", Module: packageName}},
			UnusedImports: []ImportUse{{Name: "pad", Module: packageName}},
			RiskCues:      []RiskCue{{Code: "runtime-only", Severity: "low", Message: "runtime only"}},
			Recommendations: []Recommendation{
				{Code: "remove-unused-dependency", Priority: "high", Message: "remove"},
			},
		},
	}

	AnnotateFindingConfidence(deps)
	FilterFindingsByConfidence(deps, 95)

	dep := deps[0]
	if len(dep.UnusedExports) != 0 {
		t.Fatalf("expected unused exports to be filtered, got %#v", dep.UnusedExports)
	}
	if len(dep.UnusedImports) != 0 {
		t.Fatalf("expected unused imports to be filtered, got %#v", dep.UnusedImports)
	}
	if len(dep.RiskCues) != 0 {
		t.Fatalf("expected risk cues to be filtered, got %#v", dep.RiskCues)
	}
	if len(dep.Recommendations) != 0 {
		t.Fatalf("expected recommendations to be filtered, got %#v", dep.Recommendations)
	}
}
