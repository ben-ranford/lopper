package scripts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestInlineSuppressionCheckRequiresComparableBase(t *testing.T) {
	t.Parallel()
	repoDir := newInlineSuppressionRepo(t)
	base := strings.TrimSpace(testutil.GitOutput(t, repoDir, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithComment("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "add suppression")
	writeFile(t, filepath.Join(repoDir, "README.md"), "Later documentation change\n")
	runCommand(t, repoDir, "git", "add", "README.md")
	runCommand(t, repoDir, "git", "commit", "-m", "add documentation")
	tree := strings.TrimSpace(testutil.GitOutput(t, repoDir, "rev-parse", "HEAD^{tree}"))
	unrelated := strings.TrimSpace(testutil.GitOutput(t, repoDir, "commit-tree", tree, "-m", "unrelated root"))
	for _, tc := range []struct{ name, base, want string }{
		{"earlier suppression", base, "Missing inline suppression tracking metadata"},
		{"missing", "missing/requested-base", "does not resolve to a commit"},
		{"non-commit", tree, "does not resolve to a commit"},
		{"unrelated", unrelated, "cannot establish a merge base with HEAD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_BASE="+tc.base)
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("expected failure containing %q; err=%v output:\n%s", tc.want, err, output)
			}
			if strings.Contains(output, "Inline suppression check passed") {
				t.Fatalf("invalid comparison reported success:\n%s", output)
			}
		})
	}
}

func TestInlineSuppressionCheckStagedChangesIgnoreMissingBranchBase(t *testing.T) {
	t.Parallel()
	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", mainGoPath)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_BASE=missing/requested-base", "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("staged check failed: %v\n%s", err, output)
	}
	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 || records.Suppressions[0].File != mainGoPath {
		t.Fatalf("staged tracking records = %#v", records.Suppressions)
	}
}
