package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestBuildPRReviewArtifactPropagatesRevisionWarningsDeterministically(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)

	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		repoPath: "/repo",
		baseSHA:  baseSHA,
		headSHA:  headSHA,
		baseReport: report.Report{
			Warnings: []string{
				"identity manifest parse failed for package-lock.json: invalid JSON",
				"shared warning",
			},
		},
		headReport: report.Report{
			Warnings: []string{
				"identity manifest parse failed for package-lock.json: invalid JSON",
				"head only warning",
			},
		},
		req: PRReviewRequest{},
		now: time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC),
		warnings: []string{
			"pr-review disables git hooks for temporary worktree checkouts and does not run package-manager or runtime test commands",
			"pr-review uses explicit base/head SHAs; merge-base inference is intentionally disabled",
			"pr-review uses explicit base/head SHAs; merge-base inference is intentionally disabled",
		},
	})

	want := []string{
		"base " + baseSHA[:12] + ": identity manifest parse failed for package-lock.json: invalid JSON",
		"base " + baseSHA[:12] + ": shared warning",
		"head " + headSHA[:12] + ": head only warning",
		"head " + headSHA[:12] + ": identity manifest parse failed for package-lock.json: invalid JSON",
		"pr-review disables git hooks for temporary worktree checkouts and does not run package-manager or runtime test commands",
		"pr-review uses explicit base/head SHAs; merge-base inference is intentionally disabled",
	}
	if !reflect.DeepEqual(artifact.Warnings, want) {
		t.Fatalf("unexpected artifact warnings:\n got: %#v\nwant: %#v", artifact.Warnings, want)
	}
}

func TestRecordPRReviewCleanupErrorPreservesErrorsJoinBehavior(t *testing.T) {
	primaryErr := errors.New("primary failure")
	cleanupErr := errors.New("cleanup failed")

	resultErr := primaryErr
	recordPRReviewCleanupError(&resultErr, cleanupErr, "remove pr-review workspace")

	if !errors.Is(resultErr, primaryErr) {
		t.Fatalf("expected primary error in chain, got %v", resultErr)
	}
	if !errors.Is(resultErr, cleanupErr) {
		t.Fatalf("expected cleanup error in chain, got %v", resultErr)
	}
}
