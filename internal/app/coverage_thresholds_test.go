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

	"github.com/ben-ranford/lopper/internal/analysis"
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

func TestRetainedAnalyseViewRejectsMismatchedAndUncapturedState(t *testing.T) {
	repo := t.TempDir()
	view := openAnalysisViewWithoutMetadata(t, repo)
	uncaptured := AnalyseRequest{repositoryView: view}
	assertUncapturedRetainedAnalyseViewErrors(t, repo, view, uncaptured)
	assertMetadataRetainedAnalyseViewErrors(t, uncaptured)
	assertFilteredRetainedAnalyseViewErrors(t, uncaptured)
	assertBorrowedRetainedViewMismatch(t, repo, view)
}

func openAnalysisViewWithoutMetadata(t *testing.T, repo string) *analysis.RepositoryView {
	t.Helper()
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := analysis.OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})
	return view
}

func openAnalysisViewWithMetadata(t *testing.T, repo string) *analysis.RepositoryView {
	t.Helper()
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := analysis.OpenTrustedRepositoryWithGitMetadata(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open metadata repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close metadata repository view: %v", err)
		}
	})
	return view
}

func assertUncapturedRetainedAnalyseViewErrors(t *testing.T, repo string, view *analysis.RepositoryView, uncaptured AnalyseRequest) {
	t.Helper()
	if _, err := resolvePreparedLockfileDrift(context.Background(), repo, uncaptured); err == nil || !strings.Contains(err.Error(), "was not captured") {
		t.Fatalf("expected uncaptured lockfile state rejection, got %v", err)
	}
	uncaptured.ApplyCodemod = true
	if err := validateCodemodApplyPreconditions(context.Background(), repo, uncaptured); err == nil || !strings.Contains(err.Error(), "was not captured") {
		t.Fatalf("expected uncaptured codemod state rejection, got %v", err)
	}
	if key := currentBaselineKeyForAnalyse(repo, uncaptured); key != "" {
		t.Fatalf("uncaptured retained-view baseline key = %q, want empty", key)
	}
	if err := ensureCleanRepositoryViewForCodemod(view, true); err != nil {
		t.Fatalf("allow-dirty retained view returned error: %v", err)
	}
	if err := ensureCleanRepositoryViewForCodemod(view, false); err == nil || !strings.Contains(err.Error(), "metadata was not captured") {
		t.Fatalf("expected uncaptured retained-view cleanliness rejection, got %v", err)
	}
	if _, err := evaluateLockfileDriftPolicyWithRepositoryView(context.Background(), view, "warn", uncaptured.Features); err == nil || !strings.Contains(err.Error(), "metadata was not captured") {
		t.Fatalf("expected uncaptured retained-view lockfile rejection, got %v", err)
	}
	filterErr := repositoryViewLockfileFilterError([]analysis.RepositoryGitFilter{{Path: "package.json", Driver: "demo"}}, []string{"package.json"})
	if filterErr == nil || !strings.Contains(filterErr.Error(), "package.json (demo)") {
		t.Fatalf("expected captured filter ambiguity, got %v", filterErr)
	}
	if err := ensureCleanWorktreeForCodemod(context.Background(), repo, true); err != nil {
		t.Fatalf("allow-dirty live precondition returned error: %v", err)
	}
}

func assertMetadataRetainedAnalyseViewErrors(t *testing.T, uncaptured AnalyseRequest) {
	t.Helper()
	metadataRepo := t.TempDir()
	writeFile(t, filepath.Join(metadataRepo, goModManifestName), oversizedGoModManifestBody())
	metadataView := openAnalysisViewWithMetadata(t, metadataRepo)
	if err := ensureCleanRepositoryViewForCodemod(metadataView, false); err != nil {
		t.Fatalf("clean non-Git metadata view returned error: %v", err)
	}
	if _, err := evaluateLockfileDriftPolicyWithRepositoryView(context.Background(), metadataView, "fail", uncaptured.Features); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected retained-view oversized manifest failure, got %v", err)
	}
	if _, err := applyCodemodIfNeededWithRepository(context.Background(), report.Report{}, "", AnalyseRequest{ApplyCodemod: true}, time.Now(), nil); err == nil {
		t.Fatal("expected invalid retained codemod target rejection")
	}
}

func assertFilteredRetainedAnalyseViewErrors(t *testing.T, uncaptured AnalyseRequest) {
	t.Helper()
	filteredRepo := t.TempDir()
	writeFile(t, filepath.Join(filteredRepo, ".gitattributes"), "*.json filter=demo\n")
	writeFile(t, filepath.Join(filteredRepo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(filteredRepo, lockfileName), demoPackageJSON)
	initGitRepo(t, filteredRepo)
	runGit(t, filteredRepo, "config", "filter.demo.clean", "cat")
	filteredView := openAnalysisViewWithMetadata(t, filteredRepo)
	if _, err := evaluateLockfileDriftPolicyWithRepositoryView(context.Background(), filteredView, "warn", uncaptured.Features); err == nil || !strings.Contains(err.Error(), "active custom git filter") {
		t.Fatalf("expected retained-view filter rejection, got %v", err)
	}
}

func assertBorrowedRetainedViewMismatch(t *testing.T, repo string, view *analysis.RepositoryView) {
	t.Helper()
	otherRepository, err := analysis.ResolveTrustedRepository(t.TempDir())
	if err != nil {
		t.Fatalf("authorize other repository: %v", err)
	}
	req := DefaultRequest()
	req.RepoPath = repo
	req.Analyse.repository = otherRepository
	req.Analyse.repositoryView = view
	if _, _, err := openAnalyseRepositoryView(context.Background(), req); err == nil {
		t.Fatal("expected mismatched borrowed repository view rejection")
	}
}

func TestPersistAnalyseOutputRetainedViewGuards(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "reports"), 0o750); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, "directory-target"), 0o750); err != nil {
		t.Fatalf("mkdir directory target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "blocked"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("write blocked parent: %v", err)
	}
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := analysis.OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	directoryStylePath := filepath.Join(repo, "reports") + string(os.PathSeparator)
	if _, err := persistAnalyseOutput("{}", directoryStylePath, repo, view); err == nil || !strings.Contains(err.Error(), "must name a file") {
		t.Fatalf("expected directory-style output rejection, got %v", err)
	}
	rootOutput := filepath.Join(repo, "report.json")
	if _, err := persistAnalyseOutput("root\n", rootOutput, repo, view); err != nil {
		t.Fatalf("write repository-root output: %v", err)
	}
	nestedOutput := filepath.Join(repo, "reports", "analyse.json")
	if _, err := persistAnalyseOutput("nested\n", nestedOutput, repo, view); err != nil {
		t.Fatalf("write output through existing repository directory: %v", err)
	}
	if _, err := persistAnalyseOutput("{}", filepath.Join(repo, "directory-target"), repo, view); err == nil {
		t.Fatal("expected retained-view write to reject a directory target")
	}
	if _, err := persistAnalyseOutput("{}", filepath.Join(repo, "blocked", "nested", "analyse.json"), repo, view); err == nil {
		t.Fatal("expected retained-view output inspection error below a file")
	}
}

func TestAnalyseRepositoryBoundaryErrorBranches(t *testing.T) {
	captureAnalyseGitSensitiveState(context.Background(), nil, nil)

	req := DefaultRequest()
	req.RepoPath = filepath.Join(t.TempDir(), "missing")
	if _, err := (&App{Analyzer: &fakeAnalyzer{}, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req); err == nil {
		t.Fatal("expected missing repository authorization failure")
	}

	repo := t.TempDir()
	current, err := (&App{}).applyBaselineIfNeededWithRepository(report.Report{}, repo, AnalyseRequest{}, nil)
	if err != nil {
		t.Fatalf("apply disabled baseline without repository view: %v", err)
	}
	if len(current.Dependencies) != 0 {
		t.Fatalf("disabled baseline changed report: %#v", current)
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
