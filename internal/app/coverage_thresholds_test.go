package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	goModManifestName  = "go.mod"
	poetryConfigFile   = "[tool.poetry]\n"
	poetryLockfileName = "poetry.lock"
	poetryManifestName = "Poetry configuration in pyproject.toml"
)

func TestPrepareRuntimeTraceUsesProvidedPathWithoutCapture(t *testing.T) {
	req := DefaultRequest()
	req.RepoPath = ""
	req.Analyse.RuntimeTracePath = filepath.Join(t.TempDir(), testRuntimeTracePath)
	req.Analyse.RuntimeTestCommand = missingRuntimeMakeTarget

	var warnings []string
	var tracePath string
	err := withRemovedWorkingDir(t, func() error {
		warnings, tracePath = prepareRuntimeTrace(context.Background(), req)
		return nil
	})
	if err != nil {
		t.Fatalf("prepareRuntimeTrace setup: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected runtime trace planning to avoid capture warnings, got %#v", warnings)
	}
	if tracePath != req.Analyse.RuntimeTracePath {
		t.Fatalf("expected explicit trace path to be retained, got %q", tracePath)
	}
}

func TestPrepareAnalyseExecutionPropagatesCodemodPreconditionErrors(t *testing.T) {
	repo := t.TempDir()
	initCodemodGitRepo(t, repo)
	writeTextFile(t, filepath.Join(repo, "tracked.txt"), "tracked\n", 0o644)
	writeTextFile(t, filepath.Join(repo, "dirty.txt"), "dirty\n", 0o644)

	req := DefaultRequest()
	req.RepoPath = repo
	req.Analyse.ApplyCodemod = true
	req.Analyse.Thresholds.LockfileDriftPolicy = "off"

	if _, err := prepareAnalyseExecution(context.Background(), req); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected dirty-worktree error, got %v", err)
	}
}

func TestCodemodHelpersNoOpWhenNoCodemodReportIsPresent(t *testing.T) {
	if err := validateCodemodApplyPreconditions(context.Background(), t.TempDir(), AnalyseRequest{}); err != nil {
		t.Fatalf("expected disabled codemod preconditions to pass, got %v", err)
	}

	phaseContext, shouldApply, err := beginCodemodApplyPhase(&report.Report{}, t.TempDir(), "lodash")
	if err != nil {
		t.Fatalf("beginCodemodApplyPhase without codemod: %v", err)
	}
	if shouldApply || phaseContext.codemod != nil {
		t.Fatalf("expected no codemod apply phase context, got shouldApply=%v context=%#v", shouldApply, phaseContext)
	}

	updated, err := applyCodemodIfNeeded(context.Background(), report.Report{}, t.TempDir(), AnalyseRequest{ApplyCodemod: true}, time.Now())
	if err != nil {
		t.Fatalf("applyCodemodIfNeeded without codemod: %v", err)
	}
	if len(updated.Dependencies) != 0 {
		t.Fatalf("expected unchanged report when no codemod is present, got %#v", updated)
	}
}

func TestCodemodHelpersRejectBlankRepoPaths(t *testing.T) {
	req := AnalyseRequest{ApplyCodemod: true}

	if err := validateCodemodApplyPreconditions(context.Background(), "", req); err == nil {
		t.Fatalf("expected repo path validation to fail for blank path")
	}

	if _, _, err := beginCodemodApplyPhase(&report.Report{}, "", "lodash"); err == nil {
		t.Fatalf("expected codemod phase setup to fail for blank path")
	}
}

func TestAnalyseFormatterKeepsOriginalErrorWhenFallbackFormattingFails(t *testing.T) {
	decorateAnalyseReport(nil, preparedAnalyseExecution{})

	application := &App{Formatter: report.NewFormatter()}
	originalErr := errors.New("original failure")
	formatted, err := application.formatReportWithOriginalError(report.Report{}, report.Format("bogus"), originalErr)
	if formatted != "" {
		t.Fatalf("expected empty formatted output on formatter failure, got %q", formatted)
	}
	if !errors.Is(err, originalErr) {
		t.Fatalf("expected original error to be preserved, got %v", err)
	}
}

func withRemovedWorkingDir(t *testing.T, fn func() error) error {
	t.Helper()

	originalWD, err := os.Getwd()
	if err != nil {
		return err
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore wd %s: %v", originalWD, chdirErr)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		return err
	}
	if err := os.Chdir(deadDir); err != nil {
		return err
	}
	if err := os.RemoveAll(deadDir); err != nil {
		return err
	}

	return fn()
}

func TestMissingLockfileReadErrorsPropagate(t *testing.T) {
	snapshot := newMissingGoModSnapshot(t)
	rule := newGoModulesRule()

	_, _, err := evaluateMissingOrStaleLockfile(snapshot, rule, true, nil)
	assertErrorContains(t, err, "read go.mod for lockfile drift detection")
}

func TestManifestMatcherErrorsPropagateFromSkipChecks(t *testing.T) {
	_, snapshot := newPoetrySnapshot(t, false)
	rule := newPoetryRule(func(string, string) (bool, error) {
		return false, errors.New("match failed")
	})
	_, err := shouldSkipMissingLockfile(snapshot, rule)
	assertErrorContains(t, err, "match failed")
}

func TestEvaluateLockfileDirPropagatesRuleErrors(t *testing.T) {
	snapshot := newMissingGoModSnapshot(t)

	_, err := evaluateLockfileDir(snapshot, lockfileGitContext{})
	assertErrorContains(t, err, "read go.mod for lockfile drift detection")
}

func TestProcessLockfileDirToleratesNilVisitor(t *testing.T) {
	repo := t.TempDir()
	subdir := filepath.Join(repo, "pkg")
	mustMkdirAll(t, subdir)

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}

	for _, entry := range entries {
		if entry.Name() != "pkg" {
			continue
		}
		if err := processLockfileDir(context.Background(), subdir, entry, nil, lockfileWalkState{repoPath: repo}); err != nil {
			t.Fatalf("expected nil visitor branch to return nil, got %v", err)
		}
	}
}

func TestManifestMismatchWithLockfileReportsStaleLockfile(t *testing.T) {
	_, snapshot := newPoetrySnapshot(t, true)
	rule := newPoetryLockfileRule(func(string, string) (bool, error) {
		return false, nil
	})
	rule.manifestLabel = poetryManifestName

	finding, ok, err := evaluateLockfileRule(snapshot, rule, lockfileGitContext{})
	if err != nil {
		t.Fatalf("evaluateLockfileRule: %v", err)
	}
	if !ok || finding.kind != lockfileDriftStaleLockfile {
		t.Fatalf("expected stale lockfile finding, got ok=%v finding=%#v", ok, finding)
	}
}

func TestEvaluateLockfileRulePropagatesManifestMatcherErrors(t *testing.T) {
	_, snapshot := newPoetrySnapshot(t, true)
	rule := newPoetryLockfileRule(func(string, string) (bool, error) {
		return false, errors.New("manifest mismatch")
	})

	_, _, err := evaluateLockfileRule(snapshot, rule, lockfileGitContext{})
	assertErrorContains(t, err, "manifest mismatch")
}

func TestDetectDriftForRulePropagatesEvaluationErrors(t *testing.T) {
	repo, snapshot := newPoetrySnapshot(t, false)
	rule := newPoetryRule(func(string, string) (bool, error) {
		return false, errors.New("detect failed")
	})

	_, err := detectDriftForRule(repo, repo, snapshot.files, rule, nil, false)
	assertErrorContains(t, err, "detect failed")
}

func TestWarningHelpersCoverEmptyAndDefaultPaths(t *testing.T) {
	if warnings := buildLockfileDriftWarnings(nil); len(warnings) != 0 {
		t.Fatalf("expected no warnings for empty findings, got %#v", warnings)
	}
	if warning := buildLockfileDriftWarning(lockfileDriftFinding{
		rule:   lockfileRule{manager: "uv", manifest: pyprojectManifestName},
		relDir: ".",
	}); !strings.Contains(warning, "unable to classify lockfile drift") {
		t.Fatalf("expected default warning message, got %q", warning)
	}
	if got := manifestDescription(lockfileRule{manifest: pyprojectManifestName, manifestLabel: poetryManifestName}); got != poetryManifestName {
		t.Fatalf("expected manifest label to be preferred, got %q", got)
	}
}

func TestPyprojectMatcherReadErrorsAreWrapped(t *testing.T) {
	matcher := pyprojectSectionMatcher("tool.poetry")
	_, err := matcher(t.TempDir(), t.TempDir())
	assertErrorContains(t, err, "read pyproject.toml for tool.poetry lockfile drift detection")
}

func newGoModulesRule() lockfileRule {
	return lockfileRule{
		manager:   "Go modules",
		manifest:  goModManifestName,
		lockfiles: []string{"go.sum"},
	}
}

func newPoetryRule(matcher func(string, string) (bool, error)) lockfileRule {
	return lockfileRule{
		manager:  "Poetry",
		manifest: pyprojectManifestName,
		manifestMatcher: func(repoPath, manifestPath string) (bool, error) {
			if matcher == nil {
				return true, nil
			}
			return matcher(repoPath, manifestPath)
		},
	}
}

func newPoetryLockfileRule(matcher func(string, string) (bool, error)) lockfileRule {
	rule := newPoetryRule(matcher)
	rule.lockfiles = []string{poetryLockfileName}
	return rule
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}

func newMissingGoModSnapshot(t *testing.T) lockfileDirSnapshot {
	t.Helper()

	repo := t.TempDir()
	manifestPath := filepath.Join(repo, goModManifestName)
	writeTextFile(t, manifestPath, "module example.com/test\n", 0o644)
	manifestInfo := mustStatFile(t, manifestPath)
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}

	return lockfileDirSnapshot{
		repoPath: repo,
		path:     repo,
		relDir:   ".",
		files:    map[string]fs.FileInfo{goModManifestName: manifestInfo},
	}
}

func newPoetrySnapshot(t *testing.T, withLockfile bool) (string, lockfileDirSnapshot) {
	t.Helper()

	repo := t.TempDir()
	manifestPath := filepath.Join(repo, pyprojectManifestName)
	writeTextFile(t, manifestPath, poetryConfigFile, 0o644)

	files := map[string]fs.FileInfo{
		pyprojectManifestName: mustStatFile(t, manifestPath),
	}

	if withLockfile {
		lockfilePath := filepath.Join(repo, poetryLockfileName)
		writeTextFile(t, lockfilePath, "content\n", 0o644)
		files[poetryLockfileName] = mustStatFile(t, lockfilePath)
	}

	return repo, lockfileDirSnapshot{
		repoPath: repo,
		path:     repo,
		relDir:   ".",
		files:    files,
	}
}

func mustStatFile(t *testing.T, path string) fs.FileInfo {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}

	return info
}
