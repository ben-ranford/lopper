package report

import (
	"encoding/json"
	"math"
	"testing"
)

const (
	removeUnusedDependencyCode = "remove-unused-dependency"
	leftPadPackageName         = "left-pad"
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

func TestAnnotateReachabilityConfidenceAddsPerDependencyArtifact(t *testing.T) {
	reportData := Report{
		UsageUncertainty: &UsageUncertainty{ConfirmedImportUses: 5, UncertainImportUses: 1},
		Dependencies: []DependencyReport{
			{
				Name:              "lodash",
				UsedExportsCount:  2,
				TotalExportsCount: 4,
				UsedImports:       []ImportUse{{Name: "map", Module: "lodash"}},
				RuntimeUsage:      &RuntimeUsage{LoadCount: 3, Correlation: RuntimeCorrelationOverlap},
				RiskCues:          []RiskCue{{Code: "dynamic-loader", Severity: "medium", Message: "dynamic usage"}},
			},
		},
	}

	AnnotateReachabilityConfidence(&reportData)

	confidence := reportData.Dependencies[0].ReachabilityConfidence
	if confidence == nil {
		t.Fatalf("expected reachability confidence artifact")
	}
	if confidence.Model != reachabilityConfidenceModelV2 {
		t.Fatalf("expected reachability confidence model %q, got %q", reachabilityConfidenceModelV2, confidence.Model)
	}
	if confidence.Score <= 0 || confidence.Score > 100 {
		t.Fatalf("expected bounded confidence score, got %#v", confidence)
	}
	if len(confidence.Signals) != 6 {
		t.Fatalf("expected all v2 signals to be present, got %#v", confidence.Signals)
	}
	if len(confidence.RationaleCodes) != 6 {
		t.Fatalf("expected rationale code per signal, got %#v", confidence.RationaleCodes)
	}
	if confidence.Summary == "" {
		t.Fatalf("expected summary explanation on confidence artifact")
	}
}

func TestAnnotateReachabilityConfidenceNilReport(t *testing.T) {
	AnnotateReachabilityConfidence(nil)
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

func TestReachabilityConfidenceRuntimeOrdering(t *testing.T) {
	dep := DependencyReport{
		Name:              "dep",
		UsedExportsCount:  2,
		TotalExportsCount: 4,
		UsedImports:       []ImportUse{{Name: "map", Module: "dep"}},
	}

	overlapDep := DependencyReport{
		Name:              dep.Name,
		UsedExportsCount:  dep.UsedExportsCount,
		TotalExportsCount: dep.TotalExportsCount,
		UsedImports:       dep.UsedImports,
		RuntimeUsage:      &RuntimeUsage{LoadCount: 2, Correlation: RuntimeCorrelationOverlap},
	}
	overlap := buildReachabilityConfidence(overlapDep, nil)

	staticOnlyDep := DependencyReport{
		Name:              dep.Name,
		UsedExportsCount:  dep.UsedExportsCount,
		TotalExportsCount: dep.TotalExportsCount,
		UsedImports:       dep.UsedImports,
		RuntimeUsage:      &RuntimeUsage{LoadCount: 0, Correlation: RuntimeCorrelationStaticOnly},
	}
	staticOnly := buildReachabilityConfidence(staticOnlyDep, nil)

	runtimeOnlyDep := DependencyReport{
		Name:         dep.Name,
		RuntimeUsage: &RuntimeUsage{LoadCount: 2, Correlation: RuntimeCorrelationRuntimeOnly, RuntimeOnly: true},
	}
	runtimeOnly := buildReachabilityConfidence(runtimeOnlyDep, nil)

	if !(overlap.Score > staticOnly.Score && staticOnly.Score > runtimeOnly.Score) {
		t.Fatalf("expected overlap > static-only > runtime-only, got overlap=%f static=%f runtimeOnly=%f", overlap.Score, staticOnly.Score, runtimeOnly.Score)
	}
}

func TestReachabilityConfidenceUsageUncertaintyBounded(t *testing.T) {
	dep := DependencyReport{
		Name:              "dep",
		UsedExportsCount:  2,
		TotalExportsCount: 4,
		UsedImports:       []ImportUse{{Name: "map", Module: "dep"}},
	}

	clear := buildReachabilityConfidence(dep, nil)
	uncertain := buildReachabilityConfidence(dep, &UsageUncertainty{
		ConfirmedImportUses: 0,
		UncertainImportUses: 100,
	})

	if uncertain.Score >= clear.Score {
		t.Fatalf("expected repo uncertainty to reduce confidence, clear=%f uncertain=%f", clear.Score, uncertain.Score)
	}
	if delta := clear.Score - uncertain.Score; delta > 6.1 {
		t.Fatalf("expected bounded uncertainty penalty, got delta=%f", delta)
	}
}

func TestRuntimeUsageCorrelationFallbackBranches(t *testing.T) {
	if got := runtimeUsageCorrelation(nil); got != "" {
		t.Fatalf("expected nil runtime usage to produce empty correlation, got %q", got)
	}
	if got := runtimeUsageCorrelation(&RuntimeUsage{Correlation: RuntimeCorrelationOverlap}); got != RuntimeCorrelationOverlap {
		t.Fatalf("expected explicit correlation to win, got %q", got)
	}
	if got := runtimeUsageCorrelation(&RuntimeUsage{RuntimeOnly: true}); got != RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected runtimeOnly fallback correlation, got %q", got)
	}
	if got := runtimeUsageCorrelation(&RuntimeUsage{LoadCount: 2}); got != RuntimeCorrelationOverlap {
		t.Fatalf("expected loadCount fallback overlap correlation, got %q", got)
	}
	if got := runtimeUsageCorrelation(&RuntimeUsage{}); got != RuntimeCorrelationStaticOnly {
		t.Fatalf("expected zero-value runtime usage to be static-only, got %q", got)
	}
}

func TestReachabilityHelpersFallbackBranches(t *testing.T) {
	usageSignal := usageUncertaintyConfidenceSignal(&UsageUncertainty{UncertainImportUses: 3})
	if usageSignal.signal.Score <= 0 || usageSignal.signal.Score >= 100 {
		t.Fatalf("expected bounded uncertainty fallback score, got %#v", usageSignal)
	}

	summary := summarizeReachabilityConfidence([]evaluatedReachabilitySignal{
		{signal: ReachabilitySignal{Code: confidenceReasonNoRiskCues, Score: 100}, summary: "skip-perfect"},
		{signal: ReachabilitySignal{Code: confidenceReasonRuntimeOverlap, Score: 100}, summary: "runtime overlap"},
		{signal: ReachabilitySignal{Code: confidenceReasonWildcardImport, Score: 35}, summary: "wildcard import"},
		{signal: ReachabilitySignal{Code: confidenceReasonRepoUsageUncertainty, Score: 80}, summary: "repo uncertainty"},
		{signal: ReachabilitySignal{Code: confidenceReasonRiskMedium, Score: 65}, summary: "risk medium"},
	})
	if summary != "runtime overlap; wildcard import; repo uncertainty" {
		t.Fatalf("unexpected summarized confidence explanation: %q", summary)
	}

	rationale := reachabilityRationale(&ReachabilityConfidence{Summary: "summary-only"})
	if len(rationale) != 1 || rationale[0] != "summary-only" {
		t.Fatalf("expected summary fallback rationale, got %#v", rationale)
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
				{Code: removeUnusedDependencyCode, Priority: "high", Message: "remove"},
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

func TestAnnotateFindingConfidenceUsesReachabilityAlias(t *testing.T) {
	deps := []DependencyReport{
		{
			Name:                   leftPadPackageName,
			ReachabilityConfidence: &ReachabilityConfidence{Score: 61.4, RationaleCodes: []string{confidenceReasonRuntimeOnlyUsage, confidenceReasonMissingExportInventory}},
			UnusedExports:          []SymbolRef{{Name: "pad", Module: leftPadPackageName}},
			UnusedImports:          []ImportUse{{Name: "pad", Module: leftPadPackageName}},
			RiskCues:               []RiskCue{{Code: "dynamic-loader", Severity: "medium", Message: "dynamic"}},
			Recommendations:        []Recommendation{{Code: removeUnusedDependencyCode, Priority: "high", Message: "remove"}},
		},
	}

	AnnotateFindingConfidence(deps)

	dep := deps[0]
	if dep.UnusedExports[0].ConfidenceScore != 61.4 {
		t.Fatalf("expected finding confidence to mirror reachability score, got %#v", dep.UnusedExports[0])
	}
	if got := dep.UnusedExports[0].ConfidenceReasonCodes; len(got) != 2 || got[0] != confidenceReasonRuntimeOnlyUsage || got[1] != confidenceReasonMissingExportInventory {
		t.Fatalf("expected finding rationale codes to mirror reachability artifact, got %#v", got)
	}
}

func TestReachabilityConfidenceJSONCompatibility(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "dep",
				UsedExportsCount:  2,
				TotalExportsCount: 4,
				UsedImports:       []ImportUse{{Name: "map", Module: "dep"}},
				UnusedExports:     []SymbolRef{{Name: "filter", Module: "dep"}},
			},
		},
	}

	AnnotateReachabilityConfidence(&reportData)
	AnnotateFindingConfidence(reportData.Dependencies)
	AnnotateRemovalCandidateScores(reportData.Dependencies)

	payload, err := json.Marshal(reportData)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	dependencies := decoded["dependencies"].([]any)
	dep := dependencies[0].(map[string]any)
	reachability := dep["reachabilityConfidence"].(map[string]any)
	removalCandidate := dep["removalCandidate"].(map[string]any)
	unusedExports := dep["unusedExports"].([]any)
	unusedExport := unusedExports[0].(map[string]any)

	if removalCandidate["confidence"] != reachability["score"] {
		t.Fatalf("expected removal candidate confidence to mirror reachability score, removal=%v reachability=%v", removalCandidate["confidence"], reachability["score"])
	}
	if unusedExport["confidenceScore"] != reachability["score"] {
		t.Fatalf("expected finding confidence to mirror reachability score, finding=%v reachability=%v", unusedExport["confidenceScore"], reachability["score"])
	}
}

func TestFilterFindingsByConfidence(t *testing.T) {
	deps := []DependencyReport{
		{
			Name:          leftPadPackageName,
			UnusedExports: []SymbolRef{{Name: "pad", Module: leftPadPackageName}},
			UnusedImports: []ImportUse{{Name: "pad", Module: leftPadPackageName}},
			RiskCues:      []RiskCue{{Code: "runtime-only", Severity: "low", Message: "runtime only"}},
			Recommendations: []Recommendation{
				{Code: removeUnusedDependencyCode, Priority: "high", Message: "remove"},
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

func TestFilterFindingsByConfidenceNonPositiveNoop(t *testing.T) {
	deps := []DependencyReport{
		{
			Name:          leftPadPackageName,
			UnusedExports: []SymbolRef{{Name: "pad", Module: leftPadPackageName, ConfidenceScore: 10}},
		},
	}

	FilterFindingsByConfidence(deps, 0)

	if len(deps[0].UnusedExports) != 1 {
		t.Fatalf("expected non-positive threshold to leave findings unchanged, got %#v", deps[0].UnusedExports)
	}
}
