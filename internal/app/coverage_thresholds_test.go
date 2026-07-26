package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
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

	target, shouldApply, err := resolveCodemodApplyTarget(&report.Report{}, t.TempDir(), "lodash")
	if err != nil {
		t.Fatalf("resolveCodemodApplyTarget without codemod: %v", err)
	}
	if shouldApply || target.codemod != nil {
		t.Fatalf("expected no codemod apply target, got shouldApply=%v target=%#v", shouldApply, target)
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

	if _, _, err := resolveCodemodApplyTarget(&report.Report{}, "", "lodash"); err == nil {
		t.Fatalf("expected codemod target resolution to fail for blank path")
	}
	if _, err := applyCodemodIfNeeded(context.Background(), report.Report{}, "", req, time.Time{}); err == nil {
		t.Fatalf("expected codemod apply to propagate blank repo path validation")
	}
}

func TestAnalyseFormatterKeepsOriginalErrorWhenFallbackFormattingFails(t *testing.T) {
	decorateAnalyseReport(nil, preparedAnalyseExecution{})

	application := &App{Formatter: report.NewFormatter()}
	originalErr := errors.New("original failure")
	formatted, err := application.completeAnalyseExecution(context.Background(), "", AnalyseRequest{Format: report.Format("bogus")}, report.Report{}, originalErr)
	if formatted != "" {
		t.Fatalf("expected empty formatted output on formatter failure, got %q", formatted)
	}
	if !errors.Is(err, originalErr) {
		t.Fatalf("expected original error to be preserved, got %v", err)
	}
}

func TestAnalyseFormatterReturnsFormatterErrorWithoutOriginalError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}

	formatted, err := application.completeAnalyseExecution(context.Background(), "", AnalyseRequest{Format: report.Format("bogus")}, report.Report{}, nil)
	if formatted != "" {
		t.Fatalf("expected empty formatted output on formatter failure, got %q", formatted)
	}
	if err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("expected formatter error, got %v", err)
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
	warnings := buildLockfileDriftWarnings([]lockfileDriftFinding{{
		kind:   lockfileDriftMissingLockfile,
		rule:   lockfileRule{manager: "Go modules", manifest: goModManifestName, lockfiles: []string{"go.sum"}, remedy: "run go mod tidy"},
		relDir: ".",
	}})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Go modules in .: go.mod exists but no matching lockfile") {
		t.Fatalf("expected finding to produce a lockfile warning, got %#v", warnings)
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

func TestAppendManifestIfPresentGuardsDuplicatesAndMissingFiles(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, goModManifestName)
	writeTextFile(t, manifestPath, "module example.com/test\n", 0o644)

	files := map[string]fs.FileInfo{
		goModManifestName: mustStatFile(t, manifestPath),
	}
	seen := make(map[string]struct{})

	manifests := appendManifestIfPresent(nil, seen, files, goModManifestName)
	if len(manifests) != 1 || manifests[0] != goModManifestName {
		t.Fatalf("expected manifest to be appended once, got %#v", manifests)
	}

	manifests = appendManifestIfPresent(manifests, seen, files, goModManifestName)
	if len(manifests) != 1 {
		t.Fatalf("expected duplicate manifest to be ignored, got %#v", manifests)
	}

	manifests = appendManifestIfPresent(manifests, seen, files, "missing.toml")
	if len(manifests) != 1 {
		t.Fatalf("expected missing manifest to be ignored, got %#v", manifests)
	}
}

func TestNormalizedManifestExtsDropsBlankEntries(t *testing.T) {
	got := normalizedManifestExts([]string{" .csproj ", "", "  ", ".FSPROJ"})
	want := []string{".csproj", ".fsproj"}
	if len(got) != len(want) {
		t.Fatalf("expected %d extensions, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected normalized extensions %#v, got %#v", want, got)
		}
	}
}

func TestPyprojectMatcherReadErrorsAreWrapped(t *testing.T) {
	matcher := pyprojectSectionMatcher("tool.poetry")
	_, err := matcher(t.TempDir(), t.TempDir())
	assertErrorContains(t, err, "read pyproject.toml for tool.poetry lockfile drift detection")
}

func TestLockfileManifestIOFromNilContextUsesDefaults(t *testing.T) {
	var nilContext context.Context
	manifestIO := lockfileManifestIOFromContext(nilContext)
	if manifestIO.readFileUnder == nil || manifestIO.readFileUnderLimit == nil {
		t.Fatalf("expected nil context to use default manifest I/O, got %#v", manifestIO)
	}
}

func TestEvaluatePreparedReplayRuleWithoutReplay(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)

	finding, found, err := evaluatePreparedReplayRule(dir, lockfilePreparedRule{}, lockfileGitContext{}, cache)
	assertNoPreparedReplayFinding(t, finding, found, err)
}

func TestEvaluatePreparedReplayRuleWithoutInputs(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)
	prepared := lockfilePreparedRule{replay: &lockfilePreparedRuleReplay{
		rule: lockfileRule{manager: "custom", manifest: "custom.toml"},
	}}

	finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, cache)
	assertNoPreparedReplayFinding(t, finding, found, err)
}

func TestEvaluatePreparedReplayRulePropagatesMatcherError(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)
	matchErr := errors.New("match manifest")
	prepared := newPreparedReplayRule(func(string, string) (bool, error) {
		return false, matchErr
	})

	finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, cache)
	if !errors.Is(err, matchErr) || found || finding.kind != 0 {
		t.Fatalf("expected matcher error without finding, got found=%v finding=%#v err=%v", found, finding, err)
	}
}

func TestEvaluatePreparedReplayRuleDetectsStaleLockfile(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)
	prepared := newPreparedReplayRule(func(string, string) (bool, error) {
		return false, nil
	})

	finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, cache)
	if err != nil {
		t.Fatalf("evaluate prepared stale replay: %v", err)
	}
	if !found || finding.kind != lockfileDriftStaleLockfile || len(finding.lockfiles) != 1 || finding.lockfiles[0].name != "custom.lock" {
		t.Fatalf("expected stale custom lockfile finding, got found=%v finding=%#v", found, finding)
	}
}

func TestEvaluatePreparedReplayRuleAcceptsMatchingManifest(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)
	prepared := newPreparedReplayRule(func(string, string) (bool, error) {
		return true, nil
	})

	finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, cache)
	assertNoPreparedReplayFinding(t, finding, found, err)
}

func TestAppendPreparedReplayRuleRecordsRecoverableReadError(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)
	prepared := newPreparedReplayRule(func(string, string) (bool, error) {
		return false, safeio.ErrFileTooLarge
	})
	result := lockfileDriftResult{}

	if !result.appendPreparedReplayRule(dir, prepared, lockfileGitContext{}, cache) {
		t.Fatal("expected recoverable manifest read error to allow replay to continue")
	}
	if !errors.Is(result.err, safeio.ErrFileTooLarge) || len(result.orderedWarnings) != 1 {
		t.Fatalf("expected recoverable error and warning to be retained, got %#v", result)
	}
}

func TestAppendPreparedReplayRuleContinuesWithoutFinding(t *testing.T) {
	dir, cache := newPreparedReplayTestContext(t)
	prepared := lockfilePreparedRule{replay: &lockfilePreparedRuleReplay{
		rule: lockfileRule{manager: "custom", manifest: "custom.toml"},
	}}
	result := lockfileDriftResult{}

	if !result.appendPreparedReplayRule(dir, prepared, lockfileGitContext{}, cache) {
		t.Fatal("expected replay without inputs to continue")
	}
	if result.err != nil || len(result.findings) != 0 || len(result.orderedWarnings) != 0 {
		t.Fatalf("expected replay without inputs to leave result unchanged, got %#v", result)
	}
}

func TestRecoverablePreparedLockfileRuleRejectsOrdinaryErrors(t *testing.T) {
	prepared, recoverable := recoverablePreparedLockfileRule(lockfileDirSnapshot{repoPath: t.TempDir()}, lockfileRule{manager: "custom", manifest: "custom.toml"}, fs.ErrPermission, &lockfileManifestReadErrors{})
	if recoverable || prepared.replay != nil || prepared.manifestChange != nil || prepared.manifestReadErr != nil {
		t.Fatalf("expected ordinary read error to remain fatal, got recoverable=%v prepared=%#v", recoverable, prepared)
	}
	if isPureLockfileManifestReadSizeError(nil) {
		t.Fatal("expected nil error not to be classified as a size error")
	}
}

func TestPreparedLockfileNamesPreservesOrder(t *testing.T) {
	got := preparedLockfileNames([]presentLockfile{{name: "a.lock"}, {name: "b.lock"}})
	want := []string{"a.lock", "b.lock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected prepared lockfile names %#v, got %#v", want, got)
	}
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

func newPreparedReplayTestContext(t *testing.T) (lockfilePreparedDir, *lockfileManifestCache) {
	t.Helper()

	repo := t.TempDir()
	snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: "."}
	return lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}, newLockfileManifestCache(snapshot)
}

func newPreparedReplayRule(matcher func(string, string) (bool, error)) lockfilePreparedRule {
	return lockfilePreparedRule{replay: &lockfilePreparedRuleReplay{
		rule: lockfileRule{
			manager:         "custom",
			manifest:        "custom.toml",
			lockfiles:       []string{"custom.lock"},
			manifestMatcher: matcher,
		},
		manifests: []string{"custom.toml"},
		lockfiles: []string{"custom.lock"},
	}}
}

func assertNoPreparedReplayFinding(t *testing.T, finding lockfileDriftFinding, found bool, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("evaluate prepared replay: %v", err)
	}
	if found || finding.kind != 0 || finding.manifest != "" || finding.relDir != "" || len(finding.lockfiles) != 0 {
		t.Fatalf("expected no prepared replay finding, got found=%v finding=%#v", found, finding)
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
