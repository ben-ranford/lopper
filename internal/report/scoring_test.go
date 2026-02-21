package report

import "testing"

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
