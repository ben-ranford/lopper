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

func TestAnalyseAdditionalErrorBranches(t *testing.T) {
	t.Run("prepare runtime trace falls back when repo normalization fails", func(t *testing.T) {
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
		if len(warnings) != 2 {
			t.Fatalf("expected normalization and runtime warnings, got %#v", warnings)
		}
		if !strings.Contains(warnings[0], "runtime trace setup: using raw repo path due to normalization error:") {
			t.Fatalf("expected normalization warning first, got %#v", warnings)
		}
		if !strings.Contains(warnings[1], "runtime trace command failed; continuing with static analysis:") {
			t.Fatalf("expected runtime warning second, got %#v", warnings)
		}
		if tracePath != req.Analyse.RuntimeTracePath {
			t.Fatalf("expected explicit trace path to be retained, got %q", tracePath)
		}
	})

	t.Run("prepare analyse execution propagates codemod precondition errors", func(t *testing.T) {
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
	})

	t.Run("codemod helpers no-op when no codemod report is present", func(t *testing.T) {
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
	})

	t.Run("codemod helpers reject blank repo paths", func(t *testing.T) {
		req := AnalyseRequest{ApplyCodemod: true}

		if err := validateCodemodApplyPreconditions(context.Background(), "", req); err == nil {
			t.Fatalf("expected repo path validation to fail for blank path")
		}

		if _, _, err := beginCodemodApplyPhase(&report.Report{}, "", "lodash"); err == nil {
			t.Fatalf("expected codemod phase setup to fail for blank path")
		}
	})

	t.Run("analyse formatter keeps original error when fallback formatting fails", func(t *testing.T) {
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
	})
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

func TestLockfileDriftAdditionalBranchCoverage(t *testing.T) {
	t.Run("missing lockfile read errors propagate", func(t *testing.T) {
		repo := t.TempDir()
		manifestPath := filepath.Join(repo, "go.mod")
		writeTextFile(t, manifestPath, "module example.com/test\n", 0o644)
		manifestInfo, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}
		if err := os.Remove(manifestPath); err != nil {
			t.Fatalf("remove manifest: %v", err)
		}

		snapshot := lockfileDirSnapshot{
			repoPath: repo,
			path:     repo,
			relDir:   ".",
			files:    map[string]fs.FileInfo{"go.mod": manifestInfo},
		}
		rule := lockfileRule{
			manager:   "Go modules",
			manifest:  "go.mod",
			lockfiles: []string{"go.sum"},
		}
		_, _, err = evaluateMissingOrStaleLockfile(snapshot, rule, true, nil)
		if err == nil || !strings.Contains(err.Error(), "read go.mod for lockfile drift detection") {
			t.Fatalf("expected missing-manifest read error, got %v", err)
		}
	})

	t.Run("manifest matcher errors propagate from skip checks", func(t *testing.T) {
		repo := t.TempDir()
		manifestPath := filepath.Join(repo, pyprojectManifestName)
		writeTextFile(t, manifestPath, "[tool.poetry]\n", 0o644)

		manifestInfo, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}

		snapshot := lockfileDirSnapshot{
			repoPath: repo,
			path:     repo,
			relDir:   ".",
			files:    map[string]fs.FileInfo{pyprojectManifestName: manifestInfo},
		}
		rule := lockfileRule{
			manager:  "Poetry",
			manifest: pyprojectManifestName,
			manifestMatcher: func(string, string) (bool, error) {
				return false, errors.New("match failed")
			},
		}
		_, err = shouldSkipMissingLockfile(snapshot, rule)
		if err == nil || !strings.Contains(err.Error(), "match failed") {
			t.Fatalf("expected manifest matcher error, got %v", err)
		}
	})

	t.Run("evaluate lockfile dir propagates rule errors", func(t *testing.T) {
		repo := t.TempDir()
		manifestPath := filepath.Join(repo, "go.mod")
		writeTextFile(t, manifestPath, "module example.com/test\n", 0o644)
		manifestInfo, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}
		if err := os.Remove(manifestPath); err != nil {
			t.Fatalf("remove manifest: %v", err)
		}

		snapshot := lockfileDirSnapshot{
			repoPath: repo,
			path:     repo,
			relDir:   ".",
			files:    map[string]fs.FileInfo{"go.mod": manifestInfo},
		}
		_, err = evaluateLockfileDir(snapshot, lockfileGitContext{})
		if err == nil || !strings.Contains(err.Error(), "read go.mod for lockfile drift detection") {
			t.Fatalf("expected evaluateLockfileDir to propagate read error, got %v", err)
		}
	})

	t.Run("process lockfile dir tolerates nil visitor", func(t *testing.T) {
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
	})

	t.Run("manifest mismatch with lockfile reports stale lockfile", func(t *testing.T) {
		repo := t.TempDir()
		manifestPath := filepath.Join(repo, pyprojectManifestName)
		lockfilePath := filepath.Join(repo, "poetry.lock")
		writeTextFile(t, manifestPath, "[tool.poetry]\n", 0o644)
		writeTextFile(t, lockfilePath, "content\n", 0o644)

		manifestInfo, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}
		lockfileInfo, err := os.Stat(lockfilePath)
		if err != nil {
			t.Fatalf("stat lockfile: %v", err)
		}

		snapshot := lockfileDirSnapshot{
			repoPath: repo,
			path:     repo,
			relDir:   ".",
			files: map[string]fs.FileInfo{
				pyprojectManifestName: manifestInfo,
				"poetry.lock":         lockfileInfo,
			},
		}
		rule := lockfileRule{
			manager:       "Poetry",
			manifest:      pyprojectManifestName,
			lockfiles:     []string{"poetry.lock"},
			manifestLabel: "Poetry configuration in pyproject.toml",
			manifestMatcher: func(string, string) (bool, error) {
				return false, nil
			},
		}
		finding, ok, err := evaluateLockfileRule(snapshot, rule, lockfileGitContext{})
		if err != nil {
			t.Fatalf("evaluateLockfileRule: %v", err)
		}
		if !ok || finding.kind != lockfileDriftStaleLockfile {
			t.Fatalf("expected stale lockfile finding, got ok=%v finding=%#v", ok, finding)
		}
	})

	t.Run("evaluate lockfile rule propagates manifest matcher errors", func(t *testing.T) {
		repo := t.TempDir()
		manifestPath := filepath.Join(repo, pyprojectManifestName)
		lockfilePath := filepath.Join(repo, "poetry.lock")
		writeTextFile(t, manifestPath, "[tool.poetry]\n", 0o644)
		writeTextFile(t, lockfilePath, "content\n", 0o644)

		manifestInfo, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}
		lockfileInfo, err := os.Stat(lockfilePath)
		if err != nil {
			t.Fatalf("stat lockfile: %v", err)
		}

		snapshot := lockfileDirSnapshot{
			repoPath: repo,
			path:     repo,
			relDir:   ".",
			files: map[string]fs.FileInfo{
				pyprojectManifestName: manifestInfo,
				"poetry.lock":         lockfileInfo,
			},
		}
		rule := lockfileRule{
			manager:   "Poetry",
			manifest:  pyprojectManifestName,
			lockfiles: []string{"poetry.lock"},
			manifestMatcher: func(string, string) (bool, error) {
				return false, errors.New("manifest mismatch")
			},
		}
		_, _, err = evaluateLockfileRule(snapshot, rule, lockfileGitContext{})
		if err == nil || !strings.Contains(err.Error(), "manifest mismatch") {
			t.Fatalf("expected manifest matcher error, got %v", err)
		}
	})

	t.Run("detect drift for rule propagates evaluation errors", func(t *testing.T) {
		repo := t.TempDir()
		manifestPath := filepath.Join(repo, pyprojectManifestName)
		writeTextFile(t, manifestPath, "[tool.poetry]\n", 0o644)

		manifestInfo, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}

		rule := lockfileRule{
			manager:  "Poetry",
			manifest: pyprojectManifestName,
			manifestMatcher: func(string, string) (bool, error) {
				return false, errors.New("detect failed")
			},
		}
		_, err = detectDriftForRule(repo, repo, map[string]fs.FileInfo{pyprojectManifestName: manifestInfo}, rule, nil, false)
		if err == nil || !strings.Contains(err.Error(), "detect failed") {
			t.Fatalf("expected detectDriftForRule to propagate matcher error, got %v", err)
		}
	})

	t.Run("warning helpers cover empty and default paths", func(t *testing.T) {
		if warnings := buildLockfileDriftWarnings(nil); len(warnings) != 0 {
			t.Fatalf("expected no warnings for empty findings, got %#v", warnings)
		}
		if warning := buildLockfileDriftWarning(lockfileDriftFinding{
			rule:   lockfileRule{manager: "uv", manifest: pyprojectManifestName},
			relDir: ".",
		}); !strings.Contains(warning, "unable to classify lockfile drift") {
			t.Fatalf("expected default warning message, got %q", warning)
		}
		if got := manifestDescription(lockfileRule{manifest: pyprojectManifestName, manifestLabel: "Poetry configuration in pyproject.toml"}); got != "Poetry configuration in pyproject.toml" {
			t.Fatalf("expected manifest label to be preferred, got %q", got)
		}
	})

	t.Run("pyproject matcher read errors are wrapped", func(t *testing.T) {
		matcher := pyprojectSectionMatcher("tool.poetry")
		_, err := matcher(t.TempDir(), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "read pyproject.toml for tool.poetry lockfile drift detection") {
			t.Fatalf("expected pyproject matcher read error, got %v", err)
		}
	})
}
