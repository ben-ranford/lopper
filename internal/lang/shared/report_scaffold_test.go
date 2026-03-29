package shared

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

func TestNewReportInitializesNormalizedRepoPath(t *testing.T) {
	now := time.Date(2026, time.March, 30, 12, 0, 0, 0, time.UTC)

	repoPath, got, err := NewReport(".", func() time.Time { return now })
	if err != nil {
		t.Fatalf("new report: %v", err)
	}

	wantRepoPath, err := workspace.NormalizeRepoPath(".")
	if err != nil {
		t.Fatalf("normalize repo path: %v", err)
	}
	if repoPath != wantRepoPath {
		t.Fatalf("repo path = %q, want %q", repoPath, wantRepoPath)
	}
	if got.RepoPath != wantRepoPath {
		t.Fatalf("report repo path = %q, want %q", got.RepoPath, wantRepoPath)
	}
	if !got.GeneratedAt.Equal(now) {
		t.Fatalf("generated at = %v, want %v", got.GeneratedAt, now)
	}
}

func TestWalkContextErrBranches(t *testing.T) {
	walkErr := errors.New("walk failed")
	if err := WalkContextErr(context.Background(), walkErr); !errors.Is(err, walkErr) {
		t.Fatalf("expected walk error, got %v", err)
	}

	if err := WalkContextErr(nil, nil); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WalkContextErr(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}
}

func TestResolveSharedReportHelpers(t *testing.T) {
	if got := ResolveMinUsageRecommendationThreshold(nil); got != thresholds.Defaults().MinUsagePercentForRecommendations {
		t.Fatalf("default recommendation threshold = %d", got)
	}

	customThreshold := 17
	if got := ResolveMinUsageRecommendationThreshold(&customThreshold); got != customThreshold {
		t.Fatalf("custom recommendation threshold = %d, want %d", got, customThreshold)
	}

	defaultWeights := report.DefaultRemovalCandidateWeights()
	if got := ResolveRemovalCandidateWeights(nil); got != defaultWeights {
		t.Fatalf("default removal candidate weights = %#v, want %#v", got, defaultWeights)
	}

	customWeights := report.RemovalCandidateWeights{Usage: 2, Impact: 1, Confidence: 1}
	wantWeights := report.NormalizeRemovalCandidateWeights(customWeights)
	if got := ResolveRemovalCandidateWeights(&customWeights); got != wantWeights {
		t.Fatalf("resolved removal candidate weights = %#v, want %#v", got, wantWeights)
	}
}

func TestRecommendationPriorityRank(t *testing.T) {
	if got := RecommendationPriorityRank("high"); got != 0 {
		t.Fatalf("high rank = %d, want 0", got)
	}
	if got := RecommendationPriorityRank("medium"); got != 1 {
		t.Fatalf("medium rank = %d, want 1", got)
	}
	if got := RecommendationPriorityRank("low"); got != 2 {
		t.Fatalf("default rank = %d, want 2", got)
	}
}

func TestSortRiskCues(t *testing.T) {
	cues := []report.RiskCue{
		{Code: "z-last"},
		{Code: "a-first"},
		{Code: "m-middle"},
	}

	SortRiskCues(cues)

	got := []string{cues[0].Code, cues[1].Code, cues[2].Code}
	want := []string{"a-first", "m-middle", "z-last"}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted risk cues = %#v, want %#v", got, want)
	}
}
