package scripts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionInlineSuppressionCleanScanWritesEmptyEvidence(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"detect", "track"} {
		for _, scope := range []string{"staged", "worktree", "branch", "unchanged"} {
			t.Run(mode+"/"+scope, func(t *testing.T) {
				t.Parallel()
				repoDir := newCleanSuppressionScanRepo(t, scope)
				assertCleanSuppressionEvidence(t, repoDir, mode)
			})
		}
	}
}

func newCleanSuppressionScanRepo(t *testing.T, scope string) string {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithoutComment())
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "clean source")
	runCommand(t, repoDir, "git", "branch", "scan-base")
	if scope != "unchanged" {
		writeFile(t, filepath.Join(repoDir, mainGoPath), strings.Replace(mainGoWithoutComment(), "_ = 1", "_ = 2", 1))
	}
	if scope == "staged" || scope == "branch" {
		runCommand(t, repoDir, "git", "add", mainGoPath)
	}
	if scope == "branch" {
		runCommand(t, repoDir, "git", "commit", "-m", "clean change")
	}
	return repoDir
}

func assertCleanSuppressionEvidence(t *testing.T, repoDir, mode string) {
	t.Helper()

	outputPath := seedSuppressionEvidence(t, repoDir)
	output, err := runSuppressionCheckWithEnv(repoDir,
		"SUPPRESSION_BASE=scan-base",
		"SUPPRESSION_TRACKING_MODE="+mode,
		"SUPPRESSION_TRACKING_OUTPUT="+outputPath,
		"GH_BIN="+filepath.Join(repoDir, "missing-gh"),
	)
	if err != nil {
		t.Fatalf("clean scan failed: %v\n%s", err, output)
	}
	records := readSuppressionRecords(t, outputPath)
	encoded, err := json.Marshal(records.Suppressions)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("clean scan must replace stale evidence with a JSON array, got %s (%v)", encoded, err)
	}
}

func TestRegressionInlineSuppressionFailureRemovesStaleEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*testing.T, string) []string
		want  string
	}{
		{
			name: "missing base without fallback",
			setup: func(_ *testing.T, _ string) []string {
				return []string{"SUPPRESSION_BASE=missing-base"}
			},
			want: "suppression base ref 'missing-base' not found",
		},
		{
			name: "missing base with available fallback",
			setup: func(t *testing.T, repoDir string) []string {
				runCommand(t, repoDir, "git", "commit", "--allow-empty", "-m", "fallback exists")
				return []string{"SUPPRESSION_BASE=missing-base"}
			},
			want: "suppression base ref 'missing-base' not found",
		},
		{
			name: "unrelated base",
			setup: func(t *testing.T, repoDir string) []string {
				runCommand(t, repoDir, "git", "checkout", "--orphan", "unrelated-base")
				runCommand(t, repoDir, "git", "commit", "-m", "unrelated root")
				runCommand(t, repoDir, "git", "checkout", "main")
				return []string{"SUPPRESSION_BASE=unrelated-base"}
			},
			want: "not related to HEAD",
		},
		{
			name: "invalid metadata",
			setup: func(t *testing.T, repoDir string) []string {
				writeFile(t, filepath.Join(repoDir, "first.go"), mainGoWithTrackedSuppression("nolint:staticcheck"))
				writeFile(t, filepath.Join(repoDir, "last.go"), mainGoWithComment("nolint:staticcheck"))
				runCommand(t, repoDir, "git", "add", "first.go", "last.go")
				return nil
			},
			want: "Missing inline suppression tracking metadata",
		},
		{
			name: "source read failure",
			setup: func(t *testing.T, repoDir string) []string {
				runCommand(t, repoDir, "git", "branch", "scan-base")
				writeFile(t, filepath.Join(repoDir, "new.go"), mainGoWithoutComment())
				runCommand(t, repoDir, "git", "add", "new.go")
				runCommand(t, repoDir, "git", "commit", "-m", "add source to scan")
				blob := strings.TrimSpace(runCommand(t, repoDir, "git", "rev-parse", "HEAD:new.go"))
				blobPath := filepath.Join(repoDir, ".git", "objects", blob[:2], blob[2:])
				if err := os.Remove(blobPath); err != nil {
					t.Fatalf("remove fixture source blob: %v", err)
				}
				return []string{"SUPPRESSION_BASE=scan-base"}
			},
			want: "unable to read",
		},
		{
			name: "tracking failure",
			setup: func(t *testing.T, repoDir string) []string {
				writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nolint:staticcheck"))
				runCommand(t, repoDir, "git", "add", mainGoPath)
				ghPath, _ := newMockGH(t)
				return []string{"SUPPRESSION_TRACKING_MODE=track", "GH_BIN=" + ghPath, "GH_MOCK_FAIL_CREATE=1"}
			},
			want: "Unable to create GitHub tracking issue",
		},
		{
			name: "unsupported mode on clean scan",
			setup: func(_ *testing.T, _ string) []string {
				return []string{"SUPPRESSION_BASE=HEAD", "SUPPRESSION_TRACKING_MODE=unknown"}
			},
			want: "Unsupported SUPPRESSION_TRACKING_MODE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repoDir := newInlineSuppressionRepo(t)
			env := tc.setup(t, repoDir)
			outputPath := seedSuppressionEvidence(t, repoDir)
			env = append(env, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
			output, err := runSuppressionCheckWithEnv(repoDir, env...)
			if err == nil {
				t.Fatalf("incomplete scan must fail: %s", output)
			}
			if !strings.Contains(output, tc.want) {
				t.Fatalf("expected %q in failure output: %s", tc.want, output)
			}
			if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
				t.Fatalf("failed scan must leave no usable evidence, stat error: %v", err)
			}
			files, err := os.ReadDir(filepath.Dir(outputPath))
			if err != nil || len(files) != 0 {
				t.Fatalf("failed scan must also remove temporary evidence: files=%v, error=%v", files, err)
			}
		})
	}
}

func TestInlineSuppressionTrackingPublishesEvidenceAfterSuccess(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", mainGoPath)
	ghPath, _ := newMockGH(t)
	outputPath := seedSuppressionEvidence(t, repoDir)
	output, err := runSuppressionCheckWithEnv(repoDir,
		"SUPPRESSION_TRACKING_MODE=track", "GH_BIN="+ghPath,
		"SUPPRESSION_TRACKING_OUTPUT="+outputPath,
	)
	if err != nil {
		t.Fatalf("tracking failed: %v\n%s", err, output)
	}
	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 || records.Suppressions[0].File != mainGoPath {
		t.Fatalf("expected current tracked evidence, got %#v", records.Suppressions)
	}
}

func seedSuppressionEvidence(t *testing.T, repoDir string) string {
	t.Helper()
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, outputPath, `{"schema":"lopper-inline-suppressions-v1","suppressions":[{"file":"stale.go","line":1}]}`)
	return outputPath
}
