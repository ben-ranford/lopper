package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

type captureAsyncOutcome struct {
	result CaptureResult
	err    error
}

func TestDefaultTracePath(t *testing.T) {
	repo := "/tmp/repo"
	if got := DefaultTracePath(repo); got != filepath.Join(repo, defaultTraceRelPath) {
		t.Fatalf("unexpected default trace path: %q", got)
	}
}

func TestCapture(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	err := Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   makeVersionCommand,
	})
	if err != nil {
		t.Fatalf("capture runtime trace: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(tracePath)); err != nil {
		t.Fatalf("expected trace directory to exist: %v", err)
	}
}

func assertCaptureValidatedTraceResolvedPathError(t *testing.T, command string, wantErrContains string) {
	t.Helper()

	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	wantTracePath, err := ResolveTracePathForRepo(repo, tracePath)
	if err != nil {
		t.Fatalf("resolve canonical trace path: %v", err)
	}

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   command,
	})
	if err == nil || !strings.Contains(err.Error(), wantErrContains) {
		t.Fatalf("expected validation error containing %q with resolved trace path, got result=%#v err=%v", wantErrContains, result, err)
	}
	if result.TracePath != wantTracePath {
		t.Fatalf("expected resolved trace path %q, got %q", wantTracePath, result.TracePath)
	}
	if result.TraceProduced || result.Snapshot != nil {
		t.Fatalf("expected validation failure to return no runtime snapshot, got %#v", result)
	}
}

func TestCaptureValidatedTraceReturnsNoSnapshotWhenCommandProducesNoTrace(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	wantTracePath, err := ResolveTracePathForRepo(repo, tracePath)
	if err != nil {
		t.Fatalf("resolve canonical trace path: %v", err)
	}
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nexit 0\n"))

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   npmTestCommand,
	})
	if err != nil {
		t.Fatalf("capture validated trace without output: %v", err)
	}
	if result.TracePath != wantTracePath {
		t.Fatalf("expected capture result trace path %q, got %q", wantTracePath, result.TracePath)
	}
	if result.TraceProduced || result.Snapshot != nil {
		t.Fatalf("expected successful command without runtime trace output, got %#v", result)
	}
	if _, err := os.Stat(filepath.Dir(tracePath)); err != nil {
		t.Fatalf("expected trace directory to be prepared, stat err=%v", err)
	}
	if _, err := os.Stat(tracePath); !os.IsNotExist(err) {
		t.Fatalf("expected missing runtime trace file after successful no-output command, stat err=%v", err)
	}
}

func TestCaptureValidatedTraceReturnsResolvedTracePathOnCommandValidationError(t *testing.T) {
	assertCaptureValidatedTraceResolvedPathError(t, "foobar test", "unsupported runtime test executable")
}

func TestCaptureValidatedTraceReturnsResolvedTracePathOnCommandSyntaxValidationError(t *testing.T) {
	assertCaptureValidatedTraceResolvedPathError(t, "npm test && echo bad", "indirect command execution operators")
}

func TestCaptureValidatedTraceExplicitPreservesOriginalTraceOnCommandConstructionFailure(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:             repo,
		TracePath:            tracePath,
		TracePathExplicit:    true,
		Command:              "foobar test",
		Provider:             CaptureProviderNode,
		PythonRunnerProfiles: false,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime test executable") {
		t.Fatalf("expected explicit trace construction failure, got result=%#v err=%v", result, err)
	}
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestCaptureValidatedTraceExplicitPreservesOriginalTraceOnExecutableResolutionFailure(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	t.Setenv(runtimeBinDirsEnvKey, t.TempDir())

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:          repo,
		TracePath:         tracePath,
		TracePathExplicit: true,
		Command:           npmTestCommand,
	})
	if err == nil || !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("expected explicit trace executable resolution failure, got result=%#v err=%v", result, err)
	}
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestCaptureValidatedTraceExplicitPreservesOriginalTraceOnEnvSetupFailure(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nexit 0\n"))
	stageTempDir := testutil.SecureHomeTempDir(t, "runtime-env-stage-temp-")
	t.Setenv("TMPDIR", stageTempDir)

	runtimeTraceEnvBuilder = func(base []string, tracePath string, provider CaptureProvider) ([]string, error) {
		return nil, errors.New("env setup failed")
	}
	t.Cleanup(func() {
		runtimeTraceEnvBuilder = withRuntimeTraceEnv
	})

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:          repo,
		TracePath:         tracePath,
		TracePathExplicit: true,
		Command:           npmTestCommand,
	})
	if err == nil || !strings.Contains(err.Error(), "env setup failed") {
		t.Fatalf("expected explicit trace env setup failure, got result=%#v err=%v", result, err)
	}
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
	assertNoRuntimeExecutableStages(t, stageTempDir)
}

func TestCaptureValidatedTraceExplicitPreservesOriginalTraceAcrossOutputOutcomes(t *testing.T) {
	testCases := []struct {
		name            string
		script          string
		wantErrContains string
	}{
		{
			name:            "command failure",
			script:          "#!/bin/sh\nexit 3\n",
			wantErrContains: "runtime test command failed",
		},
		{
			name:   "missing output",
			script: "#!/bin/sh\nexit 0\n",
		},
		{
			name:            "validation failure",
			script:          "#!/bin/sh\nprintf '{not-json}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n",
			wantErrContains: "validate runtime trace",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runExplicitTraceOutputOutcomeCase(t, tc.script, tc.wantErrContains)
		})
	}
}

func TestCaptureUsesAbsoluteNodeHookPaths(t *testing.T) {
	repo := t.TempDir()
	nodeOptionsPath := filepath.Join(repo, "node-options.txt")
	t.Setenv("LOPPER_CAPTURE_NODE_OPTIONS", nodeOptionsPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf '%s' \"$NODE_OPTIONS\" > \"$LOPPER_CAPTURE_NODE_OPTIONS\"\n"))

	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
	})
	if err != nil {
		t.Fatalf("capture runtime trace: %v", err)
	}

	gotBytes, err := os.ReadFile(nodeOptionsPath)
	if err != nil {
		t.Fatalf("read node options: %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "./scripts/runtime/") {
		t.Fatalf("expected node hook paths to resolve from lopper, got %q", got)
	}

	requirePath, loaderPath, err := runtimeHookPaths()
	if err != nil {
		t.Fatalf("runtime hook paths: %v", err)
	}
	if !strings.Contains(got, "--require="+requirePath) {
		t.Fatalf("expected absolute require hook path, got %q", got)
	}
	if !strings.Contains(got, "--loader="+loaderPath) {
		t.Fatalf("expected absolute loader hook path, got %q", got)
	}
}

func TestCapturePythonRuntimeImports(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	sitePackages := filepath.Join(t.TempDir(), "lib", "python3.12", "site-packages")
	if err := os.MkdirAll(filepath.Join(sitePackages, "thirdparty"), 0o750); err != nil {
		t.Fatalf("mkdir thirdparty package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sitePackages, "thirdparty", "__init__.py"), []byte("VALUE = 1\n"), 0o600); err != nil {
		t.Fatalf("write thirdparty package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "localmod.py"), []byte("VALUE = 1\n"), 0o600); err != nil {
		t.Fatalf("write local module: %v", err)
	}

	t.Setenv("LOPPER_TEST_PYTHON", pythonPath)
	t.Setenv("PYTHONPATH", sitePackages)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "pytest", "#!/bin/sh\nexec \"$LOPPER_TEST_PYTHON\" -c 'import thirdparty; import localmod'\n"))

	err = Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   "pytest",
		Provider:  CaptureProviderPython,
	})
	if err != nil {
		t.Fatalf("capture python runtime trace: %v", err)
	}

	content, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read python runtime trace: %v", err)
	}
	if !strings.Contains(string(content), `"language":"python"`) || !strings.Contains(string(content), `"module":"thirdparty"`) {
		t.Fatalf("expected third-party python import event, got %s", content)
	}
	if strings.Contains(string(content), "localmod") {
		t.Fatalf("expected local module import to be filtered, got %s", content)
	}

	trace, err := Load(tracePath)
	if err != nil {
		t.Fatalf("load captured python runtime trace: %v", err)
	}
	key := DependencyKey{Language: runtimeLanguagePython, Name: "thirdparty"}
	if trace.DependencyLoadsByLanguage[key] == 0 {
		t.Fatalf("expected thirdparty load in parsed trace, got %#v", trace.DependencyLoadsByLanguage)
	}
}

func TestCaptureCommandFailure(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{RepoPath: repo, Command: "make __missing_target__"}, "runtime test command failed")
}

func TestCaptureUnsupportedCommand(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{RepoPath: repo, Command: "foobar test"}, "unsupported runtime test executable")
}

func TestCaptureValidationErrors(t *testing.T) {
	if Capture(context.Background(), CaptureRequest{Command: npmTestCommand}) == nil {
		t.Fatalf("expected missing repo path error")
	}
	if Capture(context.Background(), CaptureRequest{RepoPath: t.TempDir()}) == nil {
		t.Fatalf("expected missing command error")
	}
}

func TestCaptureExecutableNotFound(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, t.TempDir())
	repo := t.TempDir()
	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
	})
	if err == nil {
		t.Fatalf("expected executable-not-found capture error")
	}
	if !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("unexpected capture executable-not-found error: %v", err)
	}
}

func TestCaptureTracePathSetupErrors(t *testing.T) {
	t.Run("create trace directory", func(t *testing.T) {
		repo := t.TempDir()
		blocker := filepath.Join(repo, "blocked")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		err := Capture(context.Background(), CaptureRequest{
			RepoPath:  repo,
			TracePath: filepath.Join(blocker, runtimeTraceNDJSON),
			Command:   makeVersionCommand,
		})
		if err == nil || (!strings.Contains(err.Error(), "create runtime trace directory") && !strings.Contains(err.Error(), "runtime trace path")) {
			t.Fatalf("expected trace path setup error, got %v", err)
		}
	})

	t.Run("remove previous runtime trace", func(t *testing.T) {
		repo := t.TempDir()
		tracePath := filepath.Join(repo, "traces", runtimeTraceNDJSON)
		if err := os.MkdirAll(tracePath, 0o750); err != nil {
			t.Fatalf("mkdir trace path: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tracePath, "keep.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write trace child: %v", err)
		}

		err := Capture(context.Background(), CaptureRequest{
			RepoPath:  repo,
			TracePath: tracePath,
			Command:   makeVersionCommand,
		})
		if err == nil || !strings.Contains(err.Error(), "remove previous runtime trace") {
			t.Fatalf("expected trace cleanup error, got %v", err)
		}
	})
}

func TestCaptureRejectsExplicitExternalTracePathBeforeCommandLaunch(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	tracePath := filepath.Join(outside, runtimeTraceNDJSON)
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	testutil.MustWriteFile(t, tracePath, "outside-trace\n")
	testutil.MustWriteFile(t, sentinelPath, "keep\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\ncount=$(cat \"$LOPPER_RUNTIME_COUNTER\" 2>/dev/null || echo 0)\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$LOPPER_RUNTIME_COUNTER\"\n"))

	traceBeforeBytes, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read outside trace before capture: %v", err)
	}
	sentinelBeforeBytes, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel before capture: %v", err)
	}
	traceBefore := string(traceBeforeBytes)
	sentinelBefore := string(sentinelBeforeBytes)

	err = Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   npmTestCommand,
	})
	if err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected explicit external trace path rejection, got %v", err)
	}
	if _, statErr := os.Stat(counterPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected runtime command not to start, stat err=%v", statErr)
	}
	traceAfter, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read outside trace after capture: %v", err)
	}
	if got := string(traceAfter); got != traceBefore {
		t.Fatalf("expected outside trace to remain unchanged, before=%q after=%q", traceBefore, got)
	}
	sentinelAfter, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel after capture: %v", err)
	}
	if got := string(sentinelAfter); got != sentinelBefore {
		t.Fatalf("expected outside sentinel to remain unchanged, before=%q after=%q", sentinelBefore, got)
	}
}

func TestResolveTracePathForRepoUsesRealRepoPathForRelativeTrace(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("repo symlink regression is Unix-specific")
	}

	realRepo := t.TempDir()
	linkedRepo := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(realRepo, linkedRepo); err != nil {
		t.Fatalf("symlink repo path: %v", err)
	}

	tracePath, err := ResolveTracePathForRepo(linkedRepo, filepath.Join(".artifacts", runtimeTraceNDJSON))
	if err != nil {
		t.Fatalf("resolve trace path for symlinked repo: %v", err)
	}
	realRepoPath, err := resolveRealRepoPath(realRepo)
	if err != nil {
		t.Fatalf("resolve real repo path: %v", err)
	}
	want := filepath.Join(realRepoPath, ".artifacts", runtimeTraceNDJSON)
	if tracePath != want {
		t.Fatalf("expected resolved trace path %q, got %q", want, tracePath)
	}
}

func TestResolveRealRepoPathResolvesSymlinkedRepo(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("repo symlink regression is Unix-specific")
	}

	realRepo := t.TempDir()
	linkedRepo := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(realRepo, linkedRepo); err != nil {
		t.Fatalf("symlink repo path: %v", err)
	}

	got, err := resolveRealRepoPath(linkedRepo)
	if err != nil {
		t.Fatalf("resolve symlinked repo path: %v", err)
	}
	want, err := filepath.EvalSymlinks(realRepo)
	if err != nil {
		t.Fatalf("eval real repo path: %v", err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("expected real repo path %q, got %q", filepath.Clean(want), got)
	}
}

func TestResolveTracePathForRepoRejectsNonDirectoryRepoPath(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	testutil.MustWriteFile(t, repoFile, "not-a-dir\n")

	if _, err := ResolveTracePathForRepo(repoFile, runtimeTraceNDJSON); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected non-directory repo path rejection, got %v", err)
	}
}

func TestResolveTracePathForRepoRejectsExternalPath(t *testing.T) {
	repo := t.TempDir()
	outsideTrace := filepath.Join(t.TempDir(), runtimeTraceNDJSON)

	if _, err := ResolveTracePathForRepo(repo, outsideTrace); err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected external trace path rejection, got %v", err)
	}
}

func TestResolveTracePathForRepoRejectsSymlinkLeafWithoutMutatingTarget(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink regression is Unix-specific")
	}

	repo := t.TempDir()
	targetPath := filepath.Join(repo, "package.json")
	testutil.MustWriteFile(t, targetPath, "{\"name\":\"keep\"}\n")

	traceDir := filepath.Join(repo, ".artifacts")
	if err := os.MkdirAll(traceDir, 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	tracePath := filepath.Join(traceDir, runtimeTraceNDJSON)
	if err := os.Symlink("../package.json", tracePath); err != nil {
		t.Fatalf("symlink trace path: %v", err)
	}

	before, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target before resolve: %v", err)
	}
	if err := Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   npmTestCommand,
	}); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink leaf rejection, got %v", err)
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target after resolve: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("expected symlink target to remain unchanged, before=%q after=%q", before, after)
	}
	info, err := os.Lstat(tracePath)
	if err != nil {
		t.Fatalf("lstat trace symlink after rejection: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected trace path symlink to remain in place, mode=%v", info.Mode())
	}
}

func TestResolveTracePathForRepoRejectsBrokenRepoSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink resolution regression is Unix-specific")
	}

	linkPath := filepath.Join(t.TempDir(), "repo-link")
	missingTarget := filepath.Join(t.TempDir(), "missing-repo")
	if err := os.Symlink(missingTarget, linkPath); err != nil {
		t.Fatalf("symlink broken repo path: %v", err)
	}

	if _, err := ResolveTracePathForRepo(linkPath, runtimeTraceNDJSON); err == nil {
		t.Fatal("expected broken repo symlink resolution to fail")
	}
}

func TestResolveTracePathForRepoUsesDefaultPath(t *testing.T) {
	repo := t.TempDir()

	tracePath, err := ResolveTracePathForRepo(repo, "")
	if err != nil {
		t.Fatalf("resolve default trace path: %v", err)
	}
	realRepoPath, err := resolveRealRepoPath(repo)
	if err != nil {
		t.Fatalf("resolve real repo path: %v", err)
	}
	want := filepath.Join(realRepoPath, defaultTraceRelPath)
	if tracePath != want {
		t.Fatalf("expected default trace path %q, got %q", want, tracePath)
	}
}

func TestResolveRealRepoPathRejectsMissingRepo(t *testing.T) {
	missingRepo := filepath.Join(t.TempDir(), "missing")

	if _, err := resolveRealRepoPath(missingRepo); err == nil {
		t.Fatal("expected missing repo path resolution to fail")
	}
}

func TestResolveRealRepoPathRejectsFile(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	if _, err := resolveRealRepoPath(repoFile); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected file repo path rejection, got %v", err)
	}
}

func TestResolveRealRepoPathSurfacesAbsFailureWhenCWDRemoved(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, err := resolveRealRepoPath("."); err == nil || !strings.Contains(err.Error(), "resolve repo path") {
		t.Fatalf("expected removed-cwd repo path resolution failure, got %v", err)
	}
}

func TestResolveRealRepoPathSurfacesAbsFailureForInvalidPath(t *testing.T) {
	if _, err := resolveRealRepoPath(string([]byte{0})); err == nil || !strings.Contains(err.Error(), "resolve repo path") {
		t.Fatalf("expected invalid repo path resolution failure, got %v", err)
	}
}

func TestPrepareCapturePathWithoutExplicitStageUsesResolvedTracePath(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)

	gotPath, stage, err := prepareCapturePath(capturePlan{tracePath: tracePath})
	if err != nil {
		t.Fatalf("prepare non-explicit capture path: %v", err)
	}
	if gotPath != tracePath || stage != nil {
		t.Fatalf("expected direct trace path %q without stage, got path=%q stage=%#v", tracePath, gotPath, stage)
	}
	if _, err := os.Stat(filepath.Dir(tracePath)); err != nil {
		t.Fatalf("expected non-explicit trace directory creation: %v", err)
	}
}

func TestPrepareCapturePathWithExplicitStageReturnsTempPath(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)

	gotPath, stage, err := prepareCapturePath(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit capture path: %v", err)
	}
	if stage == nil || gotPath != stage.tempPath {
		t.Fatalf("expected explicit staged temp path, got path=%q stage=%#v", gotPath, stage)
	}
	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup explicit capture stage: %v", err)
	}
}

func TestPrepareCapturePathReturnsExplicitStageError(t *testing.T) {
	repo := t.TempDir()
	blocker := filepath.Join(repo, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write non-directory blocker: %v", err)
	}

	_, stage, err := prepareCapturePath(capturePlan{
		tracePath:         filepath.Join(blocker, runtimeTraceNDJSON),
		tracePathExplicit: true,
	})
	if err == nil || stage != nil {
		t.Fatalf("expected explicit stage preparation failure, stage=%#v err=%v", stage, err)
	}
}

func TestResolveExistingOrPlannedRuntimePath(t *testing.T) {
	existingTrace := writeTraceFixture(t)
	resolvedExisting, err := resolveExistingOrPlannedRuntimePath(existingTrace)
	if err != nil {
		t.Fatalf("resolve existing runtime trace path: %v", err)
	}
	existingInfo, existingErr := os.Stat(existingTrace)
	resolvedInfo, resolvedErr := os.Stat(resolvedExisting)
	if existingErr != nil || resolvedErr != nil || !os.SameFile(existingInfo, resolvedInfo) {
		t.Fatalf("expected resolved existing trace path to preserve identity, existing err=%v resolved err=%v", existingErr, resolvedErr)
	}

	plannedTraceRoot := t.TempDir()
	plannedTrace := filepath.Join(plannedTraceRoot, ".artifacts", "nested", runtimeTraceNDJSON)
	resolvedPlanned, err := resolveExistingOrPlannedRuntimePath(plannedTrace)
	if err != nil {
		t.Fatalf("resolve planned runtime trace path: %v", err)
	}
	plannedRootPath, err := resolveRealRepoPath(plannedTraceRoot)
	if err != nil {
		t.Fatalf("resolve planned trace root path: %v", err)
	}
	wantPlanned := filepath.Join(plannedRootPath, ".artifacts", "nested", runtimeTraceNDJSON)
	if resolvedPlanned != wantPlanned {
		t.Fatalf("expected planned runtime trace path %q, got %q", wantPlanned, resolvedPlanned)
	}
}

func TestResolveExistingOrPlannedRuntimePathPreservesExistingRegularFile(t *testing.T) {
	tracePath := writeTraceFixture(t)
	wantPath, err := filepath.EvalSymlinks(tracePath)
	if err != nil {
		t.Fatalf("eval existing runtime trace path: %v", err)
	}

	resolvedPath, err := resolveExistingOrPlannedRuntimePath(tracePath)
	if err != nil {
		t.Fatalf("resolve existing runtime trace path: %v", err)
	}
	if resolvedPath != filepath.Clean(wantPath) {
		t.Fatalf("expected existing regular trace path %q, got %q", filepath.Clean(wantPath), resolvedPath)
	}
}

func TestResolveExistingOrPlannedRuntimePathRejectsSymlinkLeaf(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink regression is Unix-specific")
	}

	baseDir := t.TempDir()
	targetPath := filepath.Join(baseDir, "package.json")
	testutil.MustWriteFile(t, targetPath, "{\"name\":\"keep\"}\n")
	tracePath := filepath.Join(baseDir, ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	if err := os.Symlink("../package.json", tracePath); err != nil {
		t.Fatalf("symlink trace path: %v", err)
	}

	if _, err := resolveExistingOrPlannedRuntimePath(tracePath); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected direct symlink leaf rejection, got %v", err)
	}
}

func TestResolveExistingOrPlannedRuntimePathRejectsBrokenSymlinkAncestor(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("broken symlink regression is Unix-specific")
	}

	linkPath := filepath.Join(t.TempDir(), "trace-link")
	missingTarget := filepath.Join(t.TempDir(), "missing-trace")
	if err := os.Symlink(missingTarget, linkPath); err != nil {
		t.Fatalf("symlink broken trace path: %v", err)
	}

	if _, err := resolveExistingOrPlannedRuntimePath(linkPath); err == nil {
		t.Fatal("expected broken trace symlink resolution to fail")
	}
}

func TestRuntimePathWithinRoot(t *testing.T) {
	root := t.TempDir()

	within, err := runtimePathWithinRoot(root, filepath.Join(root, ".artifacts", runtimeTraceNDJSON))
	if err != nil {
		t.Fatalf("runtimePathWithinRoot inside root: %v", err)
	}
	if !within {
		t.Fatal("expected in-repo trace path to be accepted")
	}

	within, err = runtimePathWithinRoot(root, filepath.Join(t.TempDir(), runtimeTraceNDJSON))
	if err != nil {
		t.Fatalf("runtimePathWithinRoot outside root: %v", err)
	}
	if within {
		t.Fatal("expected out-of-repo trace path to be rejected")
	}
}

func TestResolveCapturePlanNormalizesRelativeTracePathUnderRealRepo(t *testing.T) {
	repo := t.TempDir()
	wantRepoPath, err := resolveRealRepoPath(repo)
	if err != nil {
		t.Fatalf("resolve real repo path: %v", err)
	}

	plan, err := resolveCapturePlan(CaptureRequest{
		RepoPath:  repo,
		TracePath: filepath.Join(".artifacts", runtimeTraceNDJSON),
		Command:   npmTestCommand,
		Provider:  CaptureProviderNode,
	})
	if err != nil {
		t.Fatalf("resolve capture plan: %v", err)
	}
	if plan.repoPath != wantRepoPath {
		t.Fatalf("expected canonical repo path %q, got %q", wantRepoPath, plan.repoPath)
	}
	wantTracePath := filepath.Join(wantRepoPath, ".artifacts", runtimeTraceNDJSON)
	if plan.tracePath != wantTracePath {
		t.Fatalf("expected normalized trace path %q, got %q", wantTracePath, plan.tracePath)
	}
}

func TestResolveCapturePlanRejectsUnsupportedProvider(t *testing.T) {
	repo := t.TempDir()

	if _, err := resolveCapturePlan(CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
		Provider: CaptureProvider("ruby"),
	}); err == nil || !strings.Contains(err.Error(), "unsupported runtime capture provider") {
		t.Fatalf("expected unsupported provider rejection, got %v", err)
	}
}

func TestResolveCapturePlanRejectsEscapingRelativeTracePath(t *testing.T) {
	repo := t.TempDir()

	if _, err := resolveCapturePlan(CaptureRequest{
		RepoPath:  repo,
		TracePath: filepath.Join("..", "outside", runtimeTraceNDJSON),
		Command:   npmTestCommand,
		Provider:  CaptureProviderNode,
	}); err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected escaping relative trace path rejection, got %v", err)
	}
}

func TestResolveCapturePlanSurfacesRemovedCWDFailure(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, err := resolveCapturePlan(CaptureRequest{
		RepoPath: ".",
		Command:  npmTestCommand,
		Provider: CaptureProviderNode,
	}); err == nil || !strings.Contains(err.Error(), "resolve repo path") {
		t.Fatalf("expected removed-cwd capture plan failure, got %v", err)
	}
}

func TestResolveTracePathForRealRepoRejectsEscapingRelativePath(t *testing.T) {
	repo := t.TempDir()
	realRepoPath, err := resolveRealRepoPath(repo)
	if err != nil {
		t.Fatalf("resolve real repo path: %v", err)
	}

	if _, err := resolveTracePathForRealRepo(realRepoPath, filepath.Join("..", "outside", runtimeTraceNDJSON)); err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected escaping trace path rejection, got %v", err)
	}
}

func TestResolveRuntimePathUnderRepoSurfacesRemovedCWDFailure(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, err := resolveRuntimePathUnderRepo(".", "runtime.ndjson", "", "runtime trace path"); err == nil || !strings.Contains(err.Error(), "runtime trace path") {
		t.Fatalf("expected removed-cwd runtime path failure, got %v", err)
	}
}

func TestResolveRuntimePathUnderRepoSurfacesAbsFailureForInvalidConfiguredPath(t *testing.T) {
	repo := t.TempDir()
	realRepoPath, err := resolveRealRepoPath(repo)
	if err != nil {
		t.Fatalf("resolve real repo path: %v", err)
	}

	if _, err := resolveRuntimePathUnderRepo(realRepoPath, string([]byte{0}), "", "runtime trace path"); err == nil || !strings.Contains(err.Error(), "runtime trace path") {
		t.Fatalf("expected invalid configured runtime path failure, got %v", err)
	}
}

func TestCaptureCommandFailureWithoutOutput(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "make", "#!/bin/sh\nexit 3\n"))

	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  "make test",
	})
	if err == nil {
		t.Fatalf("expected silent command failure")
	}
	if !strings.Contains(err.Error(), "runtime test command failed") {
		t.Fatalf("expected runtime command failure error, got %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("expected silent failure without command output, got %v", err)
	}
}

func TestCaptureHonorsContextCancellation(t *testing.T) {
	testCases := []struct {
		name                 string
		tool                 string
		command              string
		provider             CaptureProvider
		pythonRunnerProfiles bool
		explicitTrace        bool
	}{
		{name: "existing command", tool: "make", command: "make test"},
		{name: "enabled uv runner profile", tool: "uv", command: "uv run pytest", provider: CaptureProviderPython, pythonRunnerProfiles: true},
		{name: "explicit trace", tool: "npm", command: npmTestCommand, explicitTrace: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runCaptureContextCancellationCase(t, tc)
		})
	}
}

func TestCaptureRunnerProfileUsesRepoWorkingDirectory(t *testing.T) {
	repo := t.TempDir()
	workingDirectoryPath := filepath.Join(t.TempDir(), "working-directory.txt")
	t.Setenv("LOPPER_CAPTURE_WORKING_DIRECTORY", workingDirectoryPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "uv", "#!/bin/sh\npwd > \"$LOPPER_CAPTURE_WORKING_DIRECTORY\"\n"))

	err := Capture(context.Background(), CaptureRequest{
		RepoPath:             repo,
		Command:              "uv run pytest",
		Provider:             CaptureProviderPython,
		PythonRunnerProfiles: true,
	})
	if err != nil {
		t.Fatalf("capture runner profile: %v", err)
	}
	content, err := os.ReadFile(workingDirectoryPath)
	if err != nil {
		t.Fatalf("read captured working directory: %v", err)
	}
	got := strings.TrimSpace(string(content))
	wantInfo, wantErr := os.Stat(repo)
	gotInfo, gotErr := os.Stat(got)
	if wantErr != nil || gotErr != nil || !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("expected runner profile working directory %q, got %q (want err=%v, got err=%v)", repo, got, wantErr, gotErr)
	}
}

func TestCaptureValidatedTraceExplicitPreservesOriginalTraceOnPublishFailures(t *testing.T) {
	testCases := []struct {
		name      string
		configure func()
		want      string
	}{
		{
			name: "write failure",
			configure: func() {
				explicitTracePublishWriteHook = func(root safeio.Root, targetPath string, data []byte, perm os.FileMode) error {
					return errors.New("write failed")
				}
			},
			want: "write failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			tracePath, before := writeExplicitTraceFixture(t, repo)
			t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))
			explicitTracePublishWriteHook = safeio.PublishFileWithinRoot
			tc.configure()
			t.Cleanup(func() {
				explicitTracePublishWriteHook = safeio.PublishFileWithinRoot
			})

			result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
				RepoPath:          repo,
				TracePath:         tracePath,
				TracePathExplicit: true,
				Command:           npmTestCommand,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected explicit publish failure %q, got result=%#v err=%v", tc.want, result, err)
			}
			assertExplicitTracePreserved(t, tracePath, before)
			assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
		})
	}
}

func TestCaptureValidatedTraceExplicitPublishesValidatedBytesAcrossSwap(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	trustedBytes := []byte("{\"module\":\"lodash/map\"}\n")
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))

	swapPath := tracePath + ".swap"
	if err := os.WriteFile(swapPath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
		t.Fatalf("write swap trace: %v", err)
	}
	stagedValidatedPath := tracePath + ".validated"
	stagedTempPath := installTrackedExplicitTraceTempFileCreator(t, tracePath)
	installExplicitTracePublishSwapPathsHook(t, stagedTempPath, swapPath, stagedValidatedPath)

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:          repo,
		TracePath:         tracePath,
		TracePathExplicit: true,
		Command:           npmTestCommand,
	})
	if err != nil {
		t.Fatalf("capture validated explicit trace across publish swap: %v", err)
	}
	if !result.TraceProduced || result.Snapshot == nil {
		t.Fatalf("expected validated explicit trace snapshot, got %#v", result)
	}

	after, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read explicit trace after swapped publish: %v", err)
	}
	assertPublishedExplicitTraceMatchesTrustedBytes(t, after, trustedBytes, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestCaptureValidatedTraceExplicitRejectsRenamedTraceDirectoryDuringCommand(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	traceDir := filepath.Dir(tracePath)
	relocatedDir := traceDir + ".relocated"
	readyPath := filepath.Join(repo, "capture-ready")
	continuePath := filepath.Join(repo, "capture-continue")
	trustedBytes := []byte("{\"module\":\"lodash/map\"}\n")

	t.Setenv("LOPPER_CAPTURE_READY", readyPath)
	t.Setenv("LOPPER_CAPTURE_CONTINUE", continuePath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf ready > \"$LOPPER_CAPTURE_READY\"\nwhile [ ! -f \"$LOPPER_CAPTURE_CONTINUE\" ]; do sleep 0.01; done\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))
	setAmbientValidatedRuntimeTraceLoaderHook(t, func(capturePath string) (*ValidatedTraceSnapshot, error) {
		return nil, fmt.Errorf("ambient runtime trace load attempted for explicit stage: %s", capturePath)
	})

	outcomeCh := make(chan captureAsyncOutcome, 1)
	go func() {
		result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
			RepoPath:          repo,
			TracePath:         tracePath,
			TracePathExplicit: true,
			Command:           npmTestCommand,
		})
		outcomeCh <- captureAsyncOutcome{result: result, err: err}
	}()

	waitForRuntimeCaptureFile(t, readyPath)
	if err := os.Rename(traceDir, relocatedDir); err != nil {
		t.Fatalf("relocate explicit trace dir: %v", err)
	}
	if err := os.MkdirAll(traceDir, 0o750); err != nil {
		t.Fatalf("replace explicit trace dir: %v", err)
	}
	if err := os.WriteFile(continuePath, []byte("go"), 0o600); err != nil {
		t.Fatalf("signal capture continue: %v", err)
	}

	outcome := waitForCaptureOutcome(t, outcomeCh)
	if outcome.err == nil || !strings.Contains(outcome.err.Error(), "trace directory changed during capture") {
		t.Fatalf("expected renamed trace directory rejection, got result=%#v err=%v", outcome.result, outcome.err)
	}
	if outcome.result.TraceProduced || outcome.result.Snapshot != nil {
		t.Fatalf("expected renamed trace directory capture to fail closed, got %#v", outcome.result)
	}
	assertExplicitTracePreserved(t, filepath.Join(relocatedDir, runtimeTraceNDJSON), before)
	assertNoRuntimeTempLeaks(t, relocatedDir)
	replacementTemp := requireSingleRuntimeTempFile(t, traceDir)
	replacementData, err := os.ReadFile(replacementTemp)
	if err != nil {
		t.Fatalf("read replacement staged temp: %v", err)
	}
	if string(replacementData) != string(trustedBytes) {
		t.Fatalf("expected replacement path temp to contain command output %q, got %q", trustedBytes, replacementData)
	}
}

func TestCaptureValidatedTraceExplicitRejectsSymlinkSwappedTraceDirectoryDuringCommand(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink swap regression is Unix-specific")
	}

	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	traceDir := filepath.Dir(tracePath)
	relocatedDir := traceDir + ".relocated"
	outsideDir := filepath.Join(repo, "outside-trace")
	readyPath := filepath.Join(repo, "capture-ready")
	continuePath := filepath.Join(repo, "capture-continue")
	trustedBytes := []byte("{\"module\":\"lodash/map\"}\n")

	t.Setenv("LOPPER_CAPTURE_READY", readyPath)
	t.Setenv("LOPPER_CAPTURE_CONTINUE", continuePath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf ready > \"$LOPPER_CAPTURE_READY\"\nwhile [ ! -f \"$LOPPER_CAPTURE_CONTINUE\" ]; do sleep 0.01; done\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))
	setAmbientValidatedRuntimeTraceLoaderHook(t, func(capturePath string) (*ValidatedTraceSnapshot, error) {
		return nil, fmt.Errorf("ambient runtime trace load attempted for explicit stage: %s", capturePath)
	})

	outcomeCh := make(chan captureAsyncOutcome, 1)
	go func() {
		result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
			RepoPath:          repo,
			TracePath:         tracePath,
			TracePathExplicit: true,
			Command:           npmTestCommand,
		})
		outcomeCh <- captureAsyncOutcome{result: result, err: err}
	}()

	waitForRuntimeCaptureFile(t, readyPath)
	if err := os.Rename(traceDir, relocatedDir); err != nil {
		t.Fatalf("relocate explicit trace dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o750); err != nil {
		t.Fatalf("create swapped trace target: %v", err)
	}
	if err := os.Symlink(outsideDir, traceDir); err != nil {
		t.Fatalf("swap explicit trace dir for symlink: %v", err)
	}
	if err := os.WriteFile(continuePath, []byte("go"), 0o600); err != nil {
		t.Fatalf("signal capture continue: %v", err)
	}

	outcome := waitForCaptureOutcome(t, outcomeCh)
	if outcome.err == nil || !strings.Contains(outcome.err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink swap rejection, got result=%#v err=%v", outcome.result, outcome.err)
	}
	if outcome.result.TraceProduced || outcome.result.Snapshot != nil {
		t.Fatalf("expected symlink-swapped trace directory capture to fail closed, got %#v", outcome.result)
	}
	assertExplicitTracePreserved(t, filepath.Join(relocatedDir, runtimeTraceNDJSON), before)
	assertNoRuntimeTempLeaks(t, relocatedDir)
	replacementTemp := requireSingleRuntimeTempFile(t, outsideDir)
	replacementData, err := os.ReadFile(replacementTemp)
	if err != nil {
		t.Fatalf("read symlink-swapped staged temp: %v", err)
	}
	if string(replacementData) != string(trustedBytes) {
		t.Fatalf("expected symlink-swapped temp to contain command output %q, got %q", trustedBytes, replacementData)
	}
}

func TestPrepareExplicitTraceCaptureStageSkipsImplicitTracePaths(t *testing.T) {
	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON),
		tracePathExplicit: false,
	})
	if err != nil {
		t.Fatalf("prepare implicit trace stage: %v", err)
	}
	if stage != nil {
		t.Fatalf("expected implicit trace stage to be skipped, got %#v", stage)
	}
}

func TestPrepareExplicitTraceCaptureStagePropagatesTempCreateAndCloseFailures(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	plan := capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	}

	t.Run("create temp failure", func(t *testing.T) {
		explicitTraceTempFileCreator = func(root safeio.Root, dir string, perm os.FileMode) (string, safeio.File, error) {
			return "", nil, errors.New("create temp failed")
		}
		t.Cleanup(func() {
			explicitTraceTempFileCreator = safeio.CreateTempFileWithinRoot
		})

		stage, err := prepareExplicitTraceCaptureStage(plan)
		if err == nil || !strings.Contains(err.Error(), "create runtime trace temp file") {
			t.Fatalf("expected temp creation failure, got stage=%#v err=%v", stage, err)
		}
		assertExplicitTracePreserved(t, tracePath, before)
		assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
	})

	t.Run("close temp failure", func(t *testing.T) {
		explicitTraceTempFileCloseHook = func(file safeio.File) error {
			return errors.New("close temp failed")
		}
		t.Cleanup(func() {
			explicitTraceTempFileCloseHook = closeExplicitTracePublishFile
		})

		stage, err := prepareExplicitTraceCaptureStage(plan)
		if err == nil || !strings.Contains(err.Error(), "close runtime trace temp file") {
			t.Fatalf("expected temp close failure, got stage=%#v err=%v", stage, err)
		}
		assertExplicitTracePreserved(t, tracePath, before)
		assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
	})
}

func TestExplicitTraceCaptureStagePublishReplacesTraceOnSuccessfulRenameCommit(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	validatedData := []byte("{\"module\":\"lodash/map\"}\n")
	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit trace stage: %v", err)
	}
	if err := os.WriteFile(stage.tempPath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write staged explicit trace: %v", err)
	}
	if err := stage.publish(validatedData); err != nil {
		t.Fatalf("publish explicit trace stage: %v", err)
	}
	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup explicit trace stage: %v", err)
	}

	after, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read published explicit trace: %v", err)
	}
	if string(after) == before {
		t.Fatalf("expected explicit trace publish to replace original content, still %q", after)
	}
	if string(after) != string(validatedData) {
		t.Fatalf("expected published explicit trace content, got %q", after)
	}
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestExplicitTraceCaptureStagePublishUsesValidatedBytesAcrossRenameSwap(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	validatedData := []byte("{\"module\":\"lodash/map\"}\n")
	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit trace stage: %v", err)
	}
	if err := os.WriteFile(stage.tempPath, validatedData, 0o600); err != nil {
		t.Fatalf("write staged explicit trace: %v", err)
	}
	swapPath := tracePath + ".swap"
	if err := os.WriteFile(swapPath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
		t.Fatalf("write swap trace: %v", err)
	}
	renamedValidatedPath := tracePath + ".validated"
	restoreSwap := installExplicitTracePublishSwapHook(t, stage, swapPath, renamedValidatedPath)
	defer func() {
		if err := restoreSwap(); err != nil {
			t.Fatalf("restore explicit trace after publish swap: %v", err)
		}
	}()

	if err := stage.publish(validatedData); err != nil {
		t.Fatalf("publish explicit trace stage with swapped temp path: %v", err)
	}
	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup explicit trace stage: %v", err)
	}

	after, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read published explicit trace: %v", err)
	}
	if string(after) != string(validatedData) {
		t.Fatalf("expected published explicit trace to equal validated bytes %q, got %q", validatedData, after)
	}
	if string(after) == before {
		t.Fatalf("expected validated explicit trace to replace original content, still %q", after)
	}
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestExplicitTraceCaptureStageLoadValidatedRuntimeTraceRejectsReplacedStagedTempPath(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	validatedData := []byte("{\"module\":\"lodash/map\"}\n")
	replacementData := []byte("{\"module\":\"chalk/index\"}\n")
	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit trace stage: %v", err)
	}
	if err := os.WriteFile(stage.tempPath, validatedData, 0o600); err != nil {
		t.Fatalf("write staged explicit trace: %v", err)
	}
	replacementPath := stage.tempPath + ".replacement"
	if err := os.WriteFile(replacementPath, replacementData, 0o600); err != nil {
		t.Fatalf("write replacement staged explicit trace: %v", err)
	}
	displacedPath := stage.tempPath + ".original"
	if err := os.Rename(stage.tempPath, displacedPath); err != nil {
		t.Fatalf("displace original staged explicit trace: %v", err)
	}
	if err := os.Rename(replacementPath, stage.tempPath); err != nil {
		t.Fatalf("replace staged explicit trace path: %v", err)
	}

	snapshot, err := stage.loadValidatedRuntimeTrace()
	if err == nil || !strings.Contains(err.Error(), "staged temp file changed during capture") {
		t.Fatalf("expected staged temp identity rejection, got snapshot=%#v err=%v", snapshot, err)
	}
	if snapshot != nil {
		t.Fatalf("expected replaced staged temp path to fail closed, got %#v", snapshot)
	}

	loadedReplacementData, err := os.ReadFile(stage.tempPath)
	if err != nil {
		t.Fatalf("read replacement staged explicit trace: %v", err)
	}
	if string(loadedReplacementData) != string(replacementData) {
		t.Fatalf("expected replacement staged explicit trace content %q, got %q", replacementData, loadedReplacementData)
	}
	loadedOriginalData, err := os.ReadFile(displacedPath)
	if err != nil {
		t.Fatalf("read displaced original staged explicit trace: %v", err)
	}
	if string(loadedOriginalData) != string(validatedData) {
		t.Fatalf("expected displaced original staged explicit trace content %q, got %q", validatedData, loadedOriginalData)
	}
	if err := os.Remove(displacedPath); err != nil {
		t.Fatalf("remove displaced original staged explicit trace: %v", err)
	}
	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup explicit trace stage: %v", err)
	}
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestExplicitTraceCaptureStageCleanupRemovesStagedTempFile(t *testing.T) {
	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit trace stage: %v", err)
	}
	if _, err := os.Stat(stage.tempPath); err != nil {
		t.Fatalf("stat staged explicit temp file: %v", err)
	}
	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup explicit trace stage: %v", err)
	}
	if _, err := os.Stat(stage.tempPath); !os.IsNotExist(err) {
		t.Fatalf("expected staged explicit temp file cleanup, stat err=%v", err)
	}
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestExplicitTraceCaptureStageCleanupRetriesAfterTransientRemoveFailure(t *testing.T) {
	root := &stubRoot{
		removeSteps: map[string][]error{
			"temp.ndjson": {os.ErrPermission, nil},
		},
	}
	stage := &explicitTraceCaptureStage{
		root:     root,
		tempRel:  "temp.ndjson",
		tempPath: "/tmp/temp.ndjson",
	}

	if err := stage.cleanupTempOnly(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected transient cleanup failure, got %v", err)
	}
	if stage.tempRel != "temp.ndjson" || stage.tempPath != "/tmp/temp.ndjson" {
		t.Fatalf("expected staged temp identity to be retained for retry, got %#v", stage)
	}

	if err := stage.cleanup(); err != nil {
		t.Fatalf("expected deferred cleanup retry to succeed, got %v", err)
	}
	if stage.tempRel != "" || stage.tempPath != "" || stage.tempInfo != nil {
		t.Fatalf("expected staged temp identity cleared after successful retry, got %#v", stage)
	}
	if !root.closed {
		t.Fatal("expected cleanup to close the pinned root")
	}
}

func TestCaptureValidatedTraceDefersTransientPublishedTempCleanupFailure(t *testing.T) {
	repo := t.TempDir()
	tracePath, _ := writeExplicitTraceFixture(t, repo)
	stagedTempPath := installTrackedExplicitTraceTempFileCreator(t, tracePath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))

	firstRemoveErr := errors.New("first staged temp remove failure")
	removeCalls := 0
	explicitTraceTempCleanupHook = func(root safeio.Root, tempRel string, tempFile safeio.File) error {
		removeCalls++
		if removeCalls == 1 {
			return firstRemoveErr
		}
		return safeio.CleanupTempFileWithinRoot(root, tempRel, tempFile)
	}
	t.Cleanup(func() {
		explicitTraceTempCleanupHook = safeio.CleanupTempFileWithinRoot
	})

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:          repo,
		TracePath:         tracePath,
		TracePathExplicit: true,
		Command:           npmTestCommand,
	})
	if err != nil {
		t.Fatalf("expected deferred cleanup retry to recover, got %v", err)
	}
	if !result.TraceProduced || result.Snapshot == nil {
		t.Fatalf("expected published snapshot after cleanup retry, got %#v", result)
	}
	if removeCalls != 2 {
		t.Fatalf("expected immediate cleanup and one deferred retry, got %d calls", removeCalls)
	}
	if *stagedTempPath == "" {
		t.Fatal("expected tracked staged temp path")
	}
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestCaptureValidatedTraceJoinsPublishedTempCleanupFailuresAfterFinalRetry(t *testing.T) {
	repo := t.TempDir()
	tracePath, _ := writeExplicitTraceFixture(t, repo)
	stagedTempPath := installTrackedExplicitTraceTempFileCreator(t, tracePath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))

	firstRemoveErr := errors.New("first staged temp remove failure")
	finalRemoveErr := errors.New("final staged temp remove failure")
	removeCalls := 0
	explicitTraceTempCleanupHook = func(root safeio.Root, tempRel string, tempFile safeio.File) error {
		removeCalls++
		if removeCalls == 1 {
			return firstRemoveErr
		}
		return finalRemoveErr
	}
	t.Cleanup(func() {
		explicitTraceTempCleanupHook = safeio.CleanupTempFileWithinRoot
		if *stagedTempPath != "" {
			if removeErr := os.Remove(*stagedTempPath); removeErr != nil && !os.IsNotExist(removeErr) {
				t.Errorf("remove leaked staged temp after expected final failure: %v", removeErr)
			}
		}
	})

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:          repo,
		TracePath:         tracePath,
		TracePathExplicit: true,
		Command:           npmTestCommand,
	})
	if !errors.Is(err, firstRemoveErr) || !errors.Is(err, finalRemoveErr) {
		t.Fatalf("expected joined first and final cleanup failures, got %v", err)
	}
	if !result.TraceProduced || result.Snapshot == nil {
		t.Fatalf("expected publication result to remain produced, got %#v", result)
	}
	if removeCalls != 2 {
		t.Fatalf("expected immediate cleanup and one final retry, got %d calls", removeCalls)
	}
	after, readErr := os.ReadFile(tracePath)
	if readErr != nil {
		t.Fatalf("read published trace after cleanup failures: %v", readErr)
	}
	if string(after) != "{\"module\":\"lodash/map\"}\n" {
		t.Fatalf("expected published trace despite cleanup failures, got %q", after)
	}
}

func TestCloseExplicitTracePublishFile(t *testing.T) {
	file := &runtimePublishFileStub{closeErr: errors.New("close failed")}
	if err := closeExplicitTracePublishFile(file); !errors.Is(err, file.closeErr) {
		t.Fatalf("expected close helper error %v, got %v", file.closeErr, err)
	}
}

func TestExplicitTraceCaptureStagePublishOpenFailureAndNoop(t *testing.T) {
	var nilStage *explicitTraceCaptureStage
	if err := nilStage.publish([]byte("{}\n")); err != nil {
		t.Fatalf("expected nil publish stage to no-op, got %v", err)
	}
	stage := &explicitTraceCaptureStage{}
	if err := stage.publish([]byte("{}\n")); err != nil {
		t.Fatalf("expected empty publish stage to no-op, got %v", err)
	}

	stage = &explicitTraceCaptureStage{
		root:     &stubRoot{},
		target:   "trace.ndjson",
		tempRel:  "temp.ndjson",
		tempPath: "temp.ndjson",
	}
	explicitTracePublishWriteHook = func(root safeio.Root, targetPath string, data []byte, perm os.FileMode) error {
		return errors.New("write failed")
	}
	t.Cleanup(func() {
		explicitTracePublishWriteHook = safeio.PublishFileWithinRoot
	})
	if err := stage.publish([]byte("{}\n")); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected staged publish open failure, got %v", err)
	}
	if stage.tempRel != "" || stage.tempPath != "" {
		t.Fatalf("expected staged publish open failure cleanup, got %#v", stage)
	}
}

func TestExplicitTraceCaptureStagePublishRejectsEmptyValidatedBytesAndCleanupNilNoop(t *testing.T) {
	if err := (*explicitTraceCaptureStage)(nil).cleanup(); err != nil {
		t.Fatalf("expected nil cleanup to no-op, got %v", err)
	}
	if err := (*explicitTraceCaptureStage)(nil).cleanupTempOnly(); err != nil {
		t.Fatalf("expected nil temp cleanup to no-op, got %v", err)
	}

	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit trace stage: %v", err)
	}
	if err := stage.publish(nil); err == nil || !strings.Contains(err.Error(), "validated runtime trace is empty") {
		t.Fatalf("expected empty validated data rejection, got %v", err)
	}
	if stage.tempRel != "" || stage.tempPath != "" {
		t.Fatalf("expected empty validated data cleanup, got %#v", stage)
	}
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func TestStableRuntimeTraceFileSnapshotReturnsSnapshot(t *testing.T) {
	assertRuntimeTraceSnapshotData(t, stableRuntimeTraceFileSnapshot, "stable runtime trace file snapshot")
}

func TestStableRuntimeTraceFileSnapshotSurfacesSecondSnapshotError(t *testing.T) {
	tracePath := writeTraceFixture(t)
	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove trace before second snapshot: %v", err)
		}
	})

	if _, err := stableRuntimeTraceFileSnapshot(tracePath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected second snapshot error to surface, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileSurfacesOpenErrorAfterPathSnapshot(t *testing.T) {
	tracePath := writeTraceFixture(t)
	removeTrace := func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove trace before open: %v", err)
		}
	}
	setRuntimeTraceSnapshotOpenHooks(t, removeTrace, nil)

	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected trace open error to surface, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileSurfacesTraceRootOpenError(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	tracePath := filepath.Join(parentFile, runtimeTraceNDJSON)

	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil {
		t.Fatalf("expected trace root open error")
	}
}

func TestSnapshotRuntimeTraceFileRejectsBrokenSymlinkAncestor(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("broken symlink regression is Unix-specific")
	}

	baseDir := t.TempDir()
	brokenLink := filepath.Join(baseDir, "broken")
	if err := os.Symlink(filepath.Join(baseDir, "missing"), brokenLink); err != nil {
		t.Fatalf("symlink broken snapshot ancestor: %v", err)
	}

	if _, err := snapshotRuntimeTraceFile(filepath.Join(brokenLink, runtimeTraceNDJSON)); err == nil {
		t.Fatal("expected broken snapshot ancestor to fail")
	}
}

func TestSnapshotRuntimeTraceFileRelativePathFailsWhenCWDRemoved(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, err := snapshotRuntimeTraceFile("runtime.ndjson"); err == nil {
		t.Fatal("expected relative snapshot path to fail when cwd is removed")
	}
}

func TestSnapshotRuntimeTraceFileRejectsSymlinkedParentDirectory(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink parent regression is Unix-specific")
	}

	baseDir := t.TempDir()
	realDir := filepath.Join(baseDir, "real", ".artifacts")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("mkdir real trace parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, runtimeTraceNDJSON), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace under real parent: %v", err)
	}

	linkPath := filepath.Join(baseDir, "linked")
	if err := os.Symlink(filepath.Join(baseDir, "real"), linkPath); err != nil {
		t.Fatalf("symlink trace parent: %v", err)
	}

	tracePath := filepath.Join(linkPath, ".artifacts", runtimeTraceNDJSON)
	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlinked trace parent rejection, got %v", err)
	}
}

func TestAllowsSystemRuntimeTraceRootAlias(t *testing.T) {
	allowed := allowsSystemRuntimeTraceRootAlias("/var/folders/demo", "/private/var/folders/demo")
	if goruntime.GOOS == "darwin" {
		if !allowed {
			t.Fatal("expected darwin /var alias to be allowed")
		}
	} else if allowed {
		t.Fatal("expected non-darwin system alias check to fail closed")
	}

	if allowsSystemRuntimeTraceRootAlias("/var/folders/demo", "/private/var/folders/other") {
		t.Fatal("expected nested symlink divergence to be rejected")
	}
	if allowsSystemRuntimeTraceRootAlias("/tmp/demo", "/private/tmp/demo/child") {
		t.Fatal("expected non-identical /tmp alias mapping to be rejected")
	}
}

func TestAllowsSystemRuntimeTraceRootAliasExactRoots(t *testing.T) {
	testCases := []struct {
		path         string
		resolvedPath string
	}{
		{path: "/tmp", resolvedPath: "/private/tmp"},
		{path: "/var", resolvedPath: "/private/var"},
	}
	for _, testCase := range testCases {
		got := allowsSystemRuntimeTraceRootAlias(testCase.path, testCase.resolvedPath)
		want := goruntime.GOOS == "darwin"
		if got != want {
			t.Fatalf("allowsSystemRuntimeTraceRootAlias(%q, %q)=%v, want %v", testCase.path, testCase.resolvedPath, got, want)
		}
	}
	if allowsSystemRuntimeTraceRootAlias("/tmp", "/tmp") {
		t.Fatal("expected identical exact root paths not to be treated as aliases")
	}
}

func TestRuntimeTraceRootPath(t *testing.T) {
	t.Run("returns cleaned stable path", testRuntimeTraceRootPathReturnsCleanedStablePath)
	t.Run("rejects symlinked parent", testRuntimeTraceRootPathRejectsSymlinkedParent)
	t.Run("surfaces missing path error", testRuntimeTraceRootPathSurfacesMissingPathError)
}

func TestSnapshotRuntimeTraceFileReturnsSnapshot(t *testing.T) {
	assertRuntimeTraceSnapshotData(t, snapshotRuntimeTraceFile, "snapshot runtime trace file")
}

func TestOpenRuntimeTraceRootSurfacesRelativePathFailureWhenCWDRemoved(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, _, err := openRuntimeTraceRoot("runtime.ndjson"); err == nil || !strings.Contains(err.Error(), "hash runtime trace") {
		t.Fatalf("expected removed-cwd runtime trace root path failure, got %v", err)
	}
}

func assertRuntimeTraceSnapshotData(t *testing.T, loadSnapshot func(string) (runtimeTraceSnapshot, error), operation string) {
	t.Helper()

	tracePath := writeTraceFixture(t)
	traceData := []byte("{\"module\":\"lodash/map\"}\n")
	if err := os.WriteFile(tracePath, traceData, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	snapshot, err := loadSnapshot(tracePath)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if string(snapshot.data) != string(traceData) {
		t.Fatalf("expected snapshot data %q, got %q", traceData, snapshot.data)
	}
}

func testRuntimeTraceRootPathReturnsCleanedStablePath(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	got, err := runtimeTraceRootPath(dir)
	if err != nil {
		t.Fatalf("runtime trace root path: %v", err)
	}
	want := filepath.Clean(dir)
	if goruntime.GOOS == "darwin" {
		if resolved, resolveErr := filepath.EvalSymlinks(want); resolveErr == nil && allowsSystemRuntimeTraceRootAlias(want, resolved) {
			want = resolved
		}
	}
	if got != want {
		t.Fatalf("expected runtime trace root path %q, got %q", want, got)
	}
}

func testRuntimeTraceRootPathRejectsSymlinkedParent(t *testing.T) {
	t.Helper()

	if os.PathSeparator == '\\' {
		t.Skip("symlink regression is Unix-specific")
	}

	baseDir := t.TempDir()
	realDir := filepath.Join(baseDir, "real")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("mkdir real root: %v", err)
	}
	linkDir := filepath.Join(baseDir, "linked")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink runtime root: %v", err)
	}
	if _, err := runtimeTraceRootPath(linkDir); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlinked runtime root rejection, got %v", err)
	}
}

func testRuntimeTraceRootPathSurfacesMissingPathError(t *testing.T) {
	t.Helper()

	missingDir := filepath.Join(t.TempDir(), "missing")
	if _, err := runtimeTraceRootPath(missingDir); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing runtime root error, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileSurfacesPathDisappearingAfterOpen(t *testing.T) {
	tracePath := writeTraceFixture(t)
	setRuntimeTraceSnapshotOpenHooks(t, nil, func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove trace path after open: %v", err)
		}
	})

	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected current path stat error to surface, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileRejectsOversizedDescriptorAfterOpen(t *testing.T) {
	tracePath := writeTraceFixture(t)
	line := "{\"module\":\"lodash/map\"}\n"
	repeat := int(maxRuntimeTraceBytes)/len(line) + 1
	setRuntimeTraceSnapshotOpenHooks(t, nil, func() {
		if err := os.WriteFile(tracePath, []byte(strings.Repeat(line, repeat)), 0o600); err != nil {
			t.Fatalf("grow trace path after open: %v", err)
		}
	})

	if _, err := snapshotRuntimeTraceFile(tracePath); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized descriptor to be rejected, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileRejectsCurrentPathDirectoryReplacement(t *testing.T) {
	tracePath := writeTraceFixture(t)
	relocatedPath := tracePath + ".relocated"
	setRuntimeTraceSnapshotOpenHooks(t, nil, func() {
		if err := os.Rename(tracePath, relocatedPath); err != nil {
			t.Fatalf("relocate trace path after open: %v", err)
		}
		if err := os.Mkdir(tracePath, 0o750); err != nil {
			t.Fatalf("replace trace path with directory: %v", err)
		}
	})

	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil || !strings.Contains(err.Error(), "must be regular") {
		t.Fatalf("expected current path directory replacement to be rejected, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileRejectsDescriptorPathMismatchAfterRead(t *testing.T) {
	tracePath := writeTraceFixture(t)
	replacementPath := tracePath + ".replacement"
	if err := os.WriteFile(replacementPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write replacement trace: %v", err)
	}
	setRuntimeTraceSnapshotOpenHooks(t, nil, func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove opened trace path: %v", err)
		}
		if err := os.Rename(replacementPath, tracePath); err != nil {
			t.Fatalf("replace trace path with new file: %v", err)
		}
	})

	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected descriptor/path mismatch to be rejected, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileSurfacesCurrentPathLookupErrorAfterRead(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("renaming an opened file path is Unix-specific")
	}

	tracePath := writeTraceFixture(t)
	relocatedPath := tracePath + ".relocated"
	setRuntimeTraceSnapshotOpenHooks(t, nil, func() {
		if err := os.Rename(tracePath, relocatedPath); err != nil {
			t.Fatalf("relocate opened trace path: %v", err)
		}
	})

	if _, err := snapshotRuntimeTraceFile(tracePath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected current trace path lookup error after read, got %v", err)
	}
}

func TestPrepareTracePathRejectsSymlinkedImplicitDefaultArtifactsBeforeMutation(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink parent regression is Unix-specific")
	}

	repo := t.TempDir()
	outside := t.TempDir()
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	tracePath := DefaultTracePath(repo)
	testTracePath := filepath.Join(outside, runtimeTraceNDJSON)

	if err := os.WriteFile(sentinelPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	if err := os.WriteFile(testTracePath, []byte("outside-trace\n"), 0o600); err != nil {
		t.Fatalf("write outside trace: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".artifacts")); err != nil {
		t.Fatalf("symlink implicit artifacts dir: %v", err)
	}

	traceBefore, err := os.ReadFile(testTracePath)
	if err != nil {
		t.Fatalf("read outside trace before prepare: %v", err)
	}
	sentinelBefore, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel before prepare: %v", err)
	}

	err = prepareTracePath(tracePath)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlinked implicit default rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Dir(tracePath)); statErr != nil {
		t.Fatalf("expected symlinked artifacts path to remain present, stat err=%v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Dir(tracePath)); statErr != nil {
		t.Fatalf("expected symlinked artifacts path to remain unchanged, lstat err=%v", statErr)
	}
	traceAfter, err := os.ReadFile(testTracePath)
	if err != nil {
		t.Fatalf("read outside trace after prepare: %v", err)
	}
	if string(traceAfter) != string(traceBefore) {
		t.Fatalf("expected outside trace to remain unchanged, before=%q after=%q", traceBefore, traceAfter)
	}
	sentinelAfter, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel after prepare: %v", err)
	}
	if string(sentinelAfter) != string(sentinelBefore) {
		t.Fatalf("expected outside sentinel to remain unchanged, before=%q after=%q", sentinelBefore, sentinelAfter)
	}
}

func TestExistingRuntimeTraceAncestor(t *testing.T) {
	baseDir := t.TempDir()
	targetDir := filepath.Join(baseDir, ".artifacts", "nested")

	ancestor, missingParts, err := existingRuntimeTraceAncestor(targetDir)
	if err != nil {
		t.Fatalf("existing runtime trace ancestor: %v", err)
	}
	if ancestor != baseDir {
		t.Fatalf("expected ancestor %q, got %q", baseDir, ancestor)
	}
	if got, want := strings.Join(missingParts, "/"), ".artifacts/nested"; got != want {
		t.Fatalf("expected missing parts %q, got %q", want, got)
	}
}

func TestExistingRuntimeTraceAncestorReturnsExistingDirectoryWithoutMissingParts(t *testing.T) {
	baseDir := t.TempDir()

	ancestor, missingParts, err := existingRuntimeTraceAncestor(baseDir)
	if err != nil {
		t.Fatalf("existing runtime trace ancestor for existing dir: %v", err)
	}
	if ancestor != baseDir {
		t.Fatalf("expected ancestor %q, got %q", baseDir, ancestor)
	}
	if len(missingParts) != 0 {
		t.Fatalf("expected no missing parts for existing dir, got %#v", missingParts)
	}
}

func TestExistingRuntimeTraceAncestorSurfacesInvalidPath(t *testing.T) {
	if _, _, err := existingRuntimeTraceAncestor(string([]byte{0})); err == nil {
		t.Fatal("expected invalid path error")
	}
}

func TestExistingRuntimeTraceAncestorSurfacesNonDirectoryAncestorError(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	testutil.MustWriteFile(t, filePath, "x")

	if _, _, err := existingRuntimeTraceAncestor(filepath.Join(filePath, "child")); err == nil {
		t.Fatal("expected non-directory ancestor error")
	}
}

func TestOpenPreparedTraceRootCreatesMissingInRepoArtifactsDirectory(t *testing.T) {
	repo := t.TempDir()
	traceDir := filepath.Join(repo, ".artifacts")

	root, err := openPreparedTraceRoot(traceDir)
	if err != nil {
		t.Fatalf("open prepared trace root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close prepared trace root: %v", closeErr)
		}
	}()

	if _, err := os.Stat(traceDir); err != nil {
		t.Fatalf("expected missing trace dir to be created, stat err=%v", err)
	}
	if err := root.Mkdir("child", 0o750); err != nil {
		t.Fatalf("expected confined prepared trace root to allow in-root mutation, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(traceDir, "child")); err != nil {
		t.Fatalf("expected child directory under prepared trace root, stat err=%v", err)
	}
}

func TestOpenPreparedTraceRootReturnsExistingDirectory(t *testing.T) {
	repo := t.TempDir()
	traceDir := filepath.Join(repo, ".artifacts")
	if err := os.MkdirAll(traceDir, 0o750); err != nil {
		t.Fatalf("mkdir existing trace dir: %v", err)
	}

	root, err := openPreparedTraceRoot(traceDir)
	if err != nil {
		t.Fatalf("open prepared existing trace root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close existing prepared trace root: %v", closeErr)
		}
	}()

	if _, err := os.Stat(traceDir); err != nil {
		t.Fatalf("expected existing trace dir to remain present, stat err=%v", err)
	}
}

func TestOpenPreparedTraceRootPropagatesAncestorLookupError(t *testing.T) {
	if _, err := openPreparedTraceRoot(string([]byte{0})); err == nil {
		t.Fatal("expected ancestor lookup error")
	}
}

func TestOpenPreparedTraceRootRejectsSymlinkedArtifactsDirectory(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink parent regression is Unix-specific")
	}

	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, ".artifacts")); err != nil {
		t.Fatalf("symlink artifacts dir: %v", err)
	}

	if _, err := openPreparedTraceRoot(filepath.Join(repo, ".artifacts")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlinked trace root rejection, got %v", err)
	}
}

func TestOpenPreparedTraceRootRejectsBrokenSymlinkAncestor(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("broken symlink regression is Unix-specific")
	}

	baseDir := t.TempDir()
	brokenLink := filepath.Join(baseDir, "broken")
	if err := os.Symlink(filepath.Join(baseDir, "missing"), brokenLink); err != nil {
		t.Fatalf("symlink broken ancestor: %v", err)
	}

	if _, err := openPreparedTraceRoot(filepath.Join(brokenLink, ".artifacts")); err == nil {
		t.Fatal("expected broken symlink ancestor to fail")
	}
}

func TestOpenPreparedTraceRootSurfacesOpenRootFailureForFileAncestor(t *testing.T) {
	traceParent := filepath.Join(t.TempDir(), "not-a-directory")
	testutil.MustWriteFile(t, traceParent, "x")

	if _, err := openPreparedTraceRoot(filepath.Join(traceParent, ".artifacts")); err == nil {
		t.Fatal("expected file ancestor open-root failure")
	}
}

func assertOpenPreparedTraceRootChildCreatesDirectory(t *testing.T, root safeio.Root, rootDir string) {
	t.Helper()

	childRoot, err := openPreparedTraceRootChild(root, "created", 0o750)
	if err != nil {
		t.Fatalf("open prepared trace root child: %v", err)
	}
	if err := childRoot.Close(); err != nil {
		t.Fatalf("close created child root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "created")); err != nil {
		t.Fatalf("expected child directory to exist, stat err=%v", err)
	}
}

func assertOpenPreparedTraceRootChildRejectsSymlink(t *testing.T, root safeio.Root, rootDir string) {
	t.Helper()

	if os.PathSeparator == '\\' {
		t.Skip("symlink child regression is Unix-specific")
	}
	target := filepath.Join(rootDir, "target")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("mkdir symlink target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(rootDir, "linked")); err != nil {
		t.Fatalf("symlink child: %v", err)
	}
	if _, err := openPreparedTraceRootChild(root, "linked", 0o750); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink child rejection, got %v", err)
	}
}

func assertOpenPreparedTraceRootChildRejectsFile(t *testing.T, root safeio.Root, rootDir string) {
	t.Helper()

	filePath := filepath.Join(rootDir, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write child file: %v", err)
	}
	if _, err := openPreparedTraceRootChild(root, "file", 0o750); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected file child rejection, got %v", err)
	}
}

func TestOpenPreparedTraceRootChild(t *testing.T) {
	rootDir := t.TempDir()
	rootPath, err := runtimeTraceRootPath(rootDir)
	if err != nil {
		t.Fatalf("runtime trace root path: %v", err)
	}
	root, err := safeio.OpenRootNoFollow(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	t.Run("creates missing child directory", func(t *testing.T) {
		assertOpenPreparedTraceRootChildCreatesDirectory(t, root, rootDir)
	})
	t.Run("rejects symlink child", func(t *testing.T) {
		assertOpenPreparedTraceRootChildRejectsSymlink(t, root, rootDir)
	})
	t.Run("rejects non-directory child", func(t *testing.T) {
		assertOpenPreparedTraceRootChildRejectsFile(t, root, rootDir)
	})
}

func TestRemovePreparedTracePath(t *testing.T) {
	rootDir := t.TempDir()
	rootPath, err := runtimeTraceRootPath(rootDir)
	if err != nil {
		t.Fatalf("runtime trace root path: %v", err)
	}
	root, err := safeio.OpenRootNoFollow(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := removePreparedTracePath(root, "missing.ndjson"); err != nil {
		t.Fatalf("expected missing path removal to be ignored, got %v", err)
	}

	regularPath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(regularPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write regular trace: %v", err)
	}
	if err := removePreparedTracePath(root, "trace.ndjson"); err != nil {
		t.Fatalf("remove regular trace path: %v", err)
	}
	if _, err := os.Stat(regularPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected regular trace path removal, stat err=%v", err)
	}

	if os.PathSeparator == '\\' {
		return
	}
	target := filepath.Join(rootDir, "symlink-target")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(rootDir, "trace-link.ndjson")); err != nil {
		t.Fatalf("symlink trace path: %v", err)
	}
	if err := removePreparedTracePath(root, "trace-link.ndjson"); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink trace path rejection, got %v", err)
	}
}

func TestCreatePreparedTraceRootClosesTransferredRootsOnLaterFailure(t *testing.T) {
	wantErr := errors.New("boom")
	firstDir := filepath.Join(t.TempDir(), "first")
	if err := os.MkdirAll(firstDir, 0o750); err != nil {
		t.Fatalf("mkdir first dir: %v", err)
	}
	firstInfo, err := os.Stat(firstDir)
	if err != nil {
		t.Fatalf("stat first dir: %v", err)
	}
	firstChild := &stubRoot{
		selfInfo: firstInfo,
		lstatErr: map[string]error{"second": os.ErrNotExist},
		mkdirErr: map[string]error{"second": wantErr},
	}
	root := &stubRoot{
		lstatInfo: map[string]fs.FileInfo{"first": firstInfo},
		openRoots: map[string]safeio.Root{"first": firstChild},
	}

	_, err = createPreparedTraceRoot(root, []string{"first", "second"}, 0o750)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected createPreparedTraceRoot to return %v, got %v", wantErr, err)
	}
	if !firstChild.closed {
		t.Fatal("expected intermediate child root to close on later failure")
	}
	if !root.closed {
		t.Fatal("expected original root to close after ownership transfers to its child")
	}
}

func TestCreatePreparedTraceRootClosesParentOnSuccessfulTransfer(t *testing.T) {
	childDir := filepath.Join(t.TempDir(), "child")
	if err := os.MkdirAll(childDir, 0o750); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}
	childInfo, err := os.Stat(childDir)
	if err != nil {
		t.Fatalf("stat child dir: %v", err)
	}
	child := &stubRoot{selfInfo: childInfo}
	root := &stubRoot{
		lstatInfo: map[string]fs.FileInfo{"child": childInfo},
		openRoots: map[string]safeio.Root{"child": child},
	}

	got, err := createPreparedTraceRoot(root, []string{"child"}, 0o750)
	if err != nil {
		t.Fatalf("create prepared trace root: %v", err)
	}
	if got != child {
		t.Fatalf("expected child root transfer, got %#v", got)
	}
	if !root.closed {
		t.Fatal("expected parent root to close after successful ownership transfer")
	}
	if child.closed {
		t.Fatal("expected transferred child root to remain open")
	}
	if err := got.Close(); err != nil {
		t.Fatalf("close transferred child root: %v", err)
	}
}

func TestCreatePreparedTraceRootClosesRootOnInitialFailure(t *testing.T) {
	wantErr := errors.New("boom")
	root := &stubRoot{
		lstatErr: map[string]error{"first": os.ErrNotExist},
		mkdirErr: map[string]error{"first": wantErr},
	}

	_, err := createPreparedTraceRoot(root, []string{"first"}, 0o750)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected createPreparedTraceRoot to return %v, got %v", wantErr, err)
	}
	if !root.closed {
		t.Fatal("expected original root to close on initial failure")
	}
}

func TestCreatePreparedTraceRootClosesNewRootWhenIntermediateCloseFails(t *testing.T) {
	wantErr := errors.New("close intermediate")
	firstDir := filepath.Join(t.TempDir(), "first")
	if err := os.MkdirAll(firstDir, 0o750); err != nil {
		t.Fatalf("mkdir first dir: %v", err)
	}
	secondDir := filepath.Join(t.TempDir(), "second")
	if err := os.MkdirAll(secondDir, 0o750); err != nil {
		t.Fatalf("mkdir second dir: %v", err)
	}
	firstInfo, err := os.Stat(firstDir)
	if err != nil {
		t.Fatalf("stat first dir: %v", err)
	}
	secondInfo, err := os.Stat(secondDir)
	if err != nil {
		t.Fatalf("stat second dir: %v", err)
	}
	secondChild := &stubRoot{selfInfo: secondInfo}
	firstChild := &stubRoot{
		selfInfo:  firstInfo,
		closeErr:  wantErr,
		lstatInfo: map[string]fs.FileInfo{"second": secondInfo},
		openRoots: map[string]safeio.Root{"second": secondChild},
	}
	root := &stubRoot{
		lstatInfo: map[string]fs.FileInfo{"first": firstInfo},
		openRoots: map[string]safeio.Root{"first": firstChild},
	}

	_, err = createPreparedTraceRoot(root, []string{"first", "second"}, 0o750)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected createPreparedTraceRoot to return %v, got %v", wantErr, err)
	}
	if !secondChild.closed {
		t.Fatal("expected newly opened root to close when intermediate close fails")
	}
}

func TestOpenPreparedTraceRootChildRejectsChangedDirectoryWhileOpening(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(sourceDir, 0o750); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	replacementDir := filepath.Join(t.TempDir(), "replacement")
	if err := os.MkdirAll(replacementDir, 0o750); err != nil {
		t.Fatalf("mkdir replacement dir: %v", err)
	}
	sourceInfo, err := os.Stat(sourceDir)
	if err != nil {
		t.Fatalf("stat source dir: %v", err)
	}
	replacementInfo, err := os.Stat(replacementDir)
	if err != nil {
		t.Fatalf("stat replacement dir: %v", err)
	}

	childRoot := &stubRoot{selfInfo: replacementInfo}
	root := &stubRoot{
		lstatInfo: map[string]fs.FileInfo{"child": sourceInfo},
		openRoots: map[string]safeio.Root{"child": childRoot},
	}

	if _, err := openPreparedTraceRootChild(root, "child", 0o750); err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("expected changed-while-opening rejection, got %v", err)
	}
	if !childRoot.closed {
		t.Fatal("expected changed child root to close on rejection")
	}
}

func TestOpenPreparedTraceRootChildPropagatesMkdirError(t *testing.T) {
	wantErr := errors.New("mkdir failed")
	root := &stubRoot{
		lstatErr: map[string]error{"child": os.ErrNotExist},
		mkdirErr: map[string]error{"child": wantErr},
	}

	if _, err := openPreparedTraceRootChild(root, "child", 0o750); !errors.Is(err, wantErr) {
		t.Fatalf("expected mkdir error %v, got %v", wantErr, err)
	}
}

func TestOpenPreparedTraceRootChildHandlesMkdirExistAndPropagatesOpenErrors(t *testing.T) {
	childDir := filepath.Join(t.TempDir(), "child")
	if err := os.MkdirAll(childDir, 0o750); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}
	childInfo, err := os.Stat(childDir)
	if err != nil {
		t.Fatalf("stat child dir: %v", err)
	}

	t.Run("mkdir exist retries lstat", func(t *testing.T) {
		childRoot := &stubRoot{selfInfo: childInfo}
		root := &stubRoot{
			lstatErr:  map[string]error{"child": os.ErrNotExist},
			mkdirErr:  map[string]error{"child": fs.ErrExist},
			lstatInfo: map[string]fs.FileInfo{"child": childInfo},
			openRoots: map[string]safeio.Root{"child": childRoot},
		}
		next, err := openPreparedTraceRootChild(root, "child", 0o750)
		if err != nil {
			t.Fatalf("expected mkdir-exist retry to succeed, got %v", err)
		}
		if err := next.Close(); err != nil {
			t.Fatalf("close retried child root: %v", err)
		}
	})

	t.Run("open root error", func(t *testing.T) {
		wantErr := errors.New("open root failed")
		root := &stubRoot{
			lstatInfo:   map[string]fs.FileInfo{"child": childInfo},
			openRootErr: map[string]error{"child": wantErr},
		}
		if _, err := openPreparedTraceRootChild(root, "child", 0o750); !errors.Is(err, wantErr) {
			t.Fatalf("expected open root error %v, got %v", wantErr, err)
		}
	})

	t.Run("child lstat error closes root", func(t *testing.T) {
		wantErr := errors.New("lstat child root failed")
		childRoot := &stubRoot{lstatErr: map[string]error{".": wantErr}}
		root := &stubRoot{
			lstatInfo: map[string]fs.FileInfo{"child": childInfo},
			openRoots: map[string]safeio.Root{"child": childRoot},
		}
		if _, err := openPreparedTraceRootChild(root, "child", 0o750); !errors.Is(err, wantErr) {
			t.Fatalf("expected child lstat error %v, got %v", wantErr, err)
		}
		if !childRoot.closed {
			t.Fatal("expected child root to close after lstat error")
		}
	})

	t.Run("retry lstat error after mkdir exist", func(t *testing.T) {
		wantErr := errors.New("retry lstat failed")
		root := &stubRoot{
			lstatSteps: map[string][]stubLstatResult{
				"child": {
					{err: os.ErrNotExist},
					{err: wantErr},
				},
			},
			mkdirErr: map[string]error{"child": fs.ErrExist},
		}
		if _, err := openPreparedTraceRootChild(root, "child", 0o750); !errors.Is(err, wantErr) {
			t.Fatalf("expected retry lstat error %v, got %v", wantErr, err)
		}
	})
}

func TestRemovePreparedTracePathPropagatesRemoveError(t *testing.T) {
	traceFile := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(traceFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace file: %v", err)
	}
	traceInfo, err := os.Stat(traceFile)
	if err != nil {
		t.Fatalf("stat trace file: %v", err)
	}
	wantErr := errors.New("remove failed")
	root := &stubRoot{
		lstatInfo: map[string]fs.FileInfo{"trace.ndjson": traceInfo},
		removeErr: map[string]error{"trace.ndjson": wantErr},
	}

	if err := removePreparedTracePath(root, "trace.ndjson"); !errors.Is(err, wantErr) {
		t.Fatalf("expected remove error %v, got %v", wantErr, err)
	}
}

func TestRemovePreparedTracePathPropagatesLstatError(t *testing.T) {
	wantErr := errors.New("lstat failed")
	root := &stubRoot{lstatErr: map[string]error{"trace.ndjson": wantErr}}

	if err := removePreparedTracePath(root, "trace.ndjson"); !errors.Is(err, wantErr) {
		t.Fatalf("expected lstat error %v, got %v", wantErr, err)
	}
}

func TestBuildRuntimeCommandRejectsUnsupportedAllowlistedPath(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "foobar", "#!/bin/sh\nexit 0\n"))

	if _, err := buildRuntimeCommand(context.Background(), "foobar test"); err == nil || !strings.Contains(err.Error(), "unsupported runtime test executable") {
		t.Fatalf("expected allowlist rejection for unsupported executable, got %v", err)
	}
}

func TestRuntimeModuleFromResolvedPathIgnoresTrailingNodeModules(t *testing.T) {
	if got := runtimeModuleFromResolvedPath("/tmp/node_modules/", "lodash"); got != "" {
		t.Fatalf("expected empty runtime module for trailing node_modules path, got %q", got)
	}
}

func writeTraceFixture(t *testing.T) string {
	t.Helper()

	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace parent: %v", err)
	}
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace file: %v", err)
	}
	return tracePath
}

func writeExplicitTraceFixture(t *testing.T, repo string) (string, string) {
	t.Helper()

	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir explicit trace parent: %v", err)
	}
	before := "{\"module\":\"chalk/index\"}\n"
	if err := os.WriteFile(tracePath, []byte(before), 0o600); err != nil {
		t.Fatalf("write explicit trace fixture: %v", err)
	}
	return tracePath, before
}

func assertExplicitTracePreserved(t *testing.T, tracePath string, before string) {
	t.Helper()

	after, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read explicit trace after capture: %v", err)
	}
	if string(after) != before {
		t.Fatalf("expected explicit trace to remain unchanged, before=%q after=%q", before, after)
	}
}

func assertNoRuntimeTempLeaks(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read runtime trace dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".safeio-atomic-") {
			t.Fatalf("expected staged runtime trace temp cleanup, found %q", entry.Name())
		}
	}
}

func assertNoRuntimeExecutableStages(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read runtime executable stage dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), runtimeExecutableStagePrefix) {
			t.Fatalf("expected runtime executable stage cleanup, found %q", entry.Name())
		}
	}
}

func waitForRuntimeCaptureFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat runtime capture marker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for runtime capture marker %q", path)
}

func waitForCaptureOutcome(t *testing.T, outcomeCh <-chan captureAsyncOutcome) captureAsyncOutcome {
	t.Helper()

	select {
	case outcome := <-outcomeCh:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runtime capture outcome")
		return captureAsyncOutcome{}
	}
}

func requireSingleRuntimeTempFile(t *testing.T, dir string) string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, ".safeio-atomic-*"))
	if err != nil {
		t.Fatalf("glob runtime temp files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one runtime temp file in %s, got %#v", dir, matches)
	}
	return matches[0]
}

func runExplicitTraceOutputOutcomeCase(t *testing.T, script string, wantErrContains string) {
	t.Helper()

	repo := t.TempDir()
	tracePath, before := writeExplicitTraceFixture(t, repo)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", script))

	result, err := CaptureValidatedTrace(context.Background(), CaptureRequest{
		RepoPath:          repo,
		TracePath:         tracePath,
		TracePathExplicit: true,
		Command:           npmTestCommand,
	})
	assertExplicitTraceOutputOutcome(t, result, err, wantErrContains)
	assertExplicitTracePreserved(t, tracePath, before)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func assertExplicitTraceOutputOutcome(t *testing.T, result CaptureResult, err error, wantErrContains string) {
	t.Helper()

	if wantErrContains != "" {
		if err == nil || !strings.Contains(err.Error(), wantErrContains) {
			t.Fatalf("expected explicit trace error containing %q, got result=%#v err=%v", wantErrContains, result, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("capture explicit trace without output: %v", err)
	}
	if result.TraceProduced || result.Snapshot != nil {
		t.Fatalf("expected explicit trace without output to skip publishing, got %#v", result)
	}
}

func runCaptureContextCancellationCase(t *testing.T, tc struct {
	name                 string
	tool                 string
	command              string
	provider             CaptureProvider
	pythonRunnerProfiles bool
	explicitTrace        bool
}) {
	t.Helper()

	repo := t.TempDir()
	markerPath := filepath.Join(repo, "started.txt")
	t.Setenv("LOPPER_CAPTURE_MARKER", markerPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, tc.tool, "#!/bin/sh\nsleep 5\nprintf started > \"$LOPPER_CAPTURE_MARKER\"\n"))

	tracePath, traceBefore := prepareOptionalExplicitTraceFixture(t, repo, tc.explicitTrace)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Capture(ctx, CaptureRequest{
		RepoPath:             repo,
		TracePath:            tracePath,
		TracePathExplicit:    tc.explicitTrace,
		Command:              tc.command,
		Provider:             tc.provider,
		PythonRunnerProfiles: tc.pythonRunnerProfiles,
	})
	assertCaptureCancellationState(t, err, start, markerPath)
	assertOptionalExplicitTraceStillPreserved(t, tc.explicitTrace, tracePath, traceBefore)
}

func prepareOptionalExplicitTraceFixture(t *testing.T, repo string, explicitTrace bool) (string, string) {
	t.Helper()

	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	if !explicitTrace {
		return tracePath, ""
	}
	return writeExplicitTraceFixture(t, repo)
}

func assertCaptureCancellationState(t *testing.T, err error, start time.Time, markerPath string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected capture cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("expected cancelled command to stop quickly, took %v", elapsed)
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected cancelled command to stop before creating marker, stat err = %v", statErr)
	}
}

func assertOptionalExplicitTraceStillPreserved(t *testing.T, explicitTrace bool, tracePath string, traceBefore string) {
	t.Helper()

	if !explicitTrace {
		return
	}
	assertExplicitTracePreserved(t, tracePath, traceBefore)
	assertNoRuntimeTempLeaks(t, filepath.Dir(tracePath))
}

func installTrackedExplicitTraceTempFileCreator(t *testing.T, tracePath string) *string {
	t.Helper()

	var stagedTempPath string
	explicitTraceTempFileCreator = func(root safeio.Root, dir string, perm os.FileMode) (string, safeio.File, error) {
		tempRel, tempFile, err := safeio.CreateTempFileWithinRoot(root, dir, perm)
		if err == nil && stagedTempPath == "" {
			stagedTempPath = filepath.Join(filepath.Dir(tracePath), tempRel)
		}
		return tempRel, tempFile, err
	}
	t.Cleanup(func() {
		explicitTraceTempFileCreator = safeio.CreateTempFileWithinRoot
	})
	return &stagedTempPath
}

func installExplicitTracePublishSwapPathsHook(t *testing.T, stagedTempPath *string, swapPath string, stagedValidatedPath string) {
	t.Helper()

	explicitTracePublishWriteHook = func(root safeio.Root, targetPath string, data []byte, perm os.FileMode) error {
		if *stagedTempPath == "" {
			t.Fatal("expected staged temp path before publication")
		}
		if err := os.Rename(*stagedTempPath, stagedValidatedPath); err != nil {
			return err
		}
		if err := os.Rename(swapPath, *stagedTempPath); err != nil {
			return err
		}
		return safeio.PublishFileWithinRoot(root, targetPath, data, perm)
	}
	t.Cleanup(func() {
		explicitTracePublishWriteHook = safeio.PublishFileWithinRoot
		restoreSwappedExplicitTracePaths(t, *stagedTempPath, swapPath, stagedValidatedPath)
	})
}

func restoreSwappedExplicitTracePaths(t *testing.T, stagedTempPath string, swapPath string, stagedValidatedPath string) {
	t.Helper()

	restoreExplicitTracePathIfPresent(t, stagedTempPath, swapPath, "restore swapped explicit trace temp path")
	restoreExplicitTracePathIfPresent(t, stagedValidatedPath, stagedTempPath, "restore validated explicit trace temp path")
}

func restoreExplicitTracePathIfPresent(t *testing.T, from string, to string, label string) {
	t.Helper()

	if _, err := os.Stat(from); err == nil {
		if err := os.Rename(from, to); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
}

func assertPublishedExplicitTraceMatchesTrustedBytes(t *testing.T, after []byte, trustedBytes []byte, before string) {
	t.Helper()

	if string(after) != string(trustedBytes) {
		t.Fatalf("expected published explicit trace to equal trusted bytes %q, got %q", trustedBytes, after)
	}
	if string(after) == before {
		t.Fatalf("expected validated explicit trace to replace original content, still %q", after)
	}
}

func installExplicitTracePublishSwapHook(t *testing.T, stage *explicitTraceCaptureStage, swapPath string, displacedPath string) func() error {
	t.Helper()

	originalWriteHook := explicitTracePublishWriteHook
	tempPath := stage.tempPath
	swapped := false
	explicitTracePublishWriteHook = func(root safeio.Root, targetPath string, data []byte, perm os.FileMode) error {
		if err := swapExplicitTracePublishPaths(tempPath, swapPath, displacedPath, &swapped); err != nil {
			return err
		}
		return originalWriteHook(root, targetPath, data, perm)
	}

	return func() error {
		explicitTracePublishWriteHook = originalWriteHook
		return restoreSwappedExplicitTracePublishPaths(tempPath, swapPath, displacedPath, swapped)
	}
}

func swapExplicitTracePublishPaths(tempPath string, swapPath string, displacedPath string, swapped *bool) error {
	if *swapped {
		return nil
	}
	if err := os.Rename(tempPath, displacedPath); err != nil {
		return err
	}
	if err := os.Rename(swapPath, tempPath); err != nil {
		return err
	}
	*swapped = true
	return nil
}

func restoreSwappedExplicitTracePublishPaths(tempPath string, swapPath string, displacedPath string, swapped bool) error {
	if !swapped {
		return nil
	}
	if err := renameIfPresent(tempPath, swapPath); err != nil {
		return err
	}
	return renameIfPresent(displacedPath, tempPath)
}

func renameIfPresent(from string, to string) error {
	if _, err := os.Stat(from); err == nil {
		return os.Rename(from, to)
	}
	return nil
}

type runtimePublishFileStub struct {
	closeErr error
}

func (f *runtimePublishFileStub) Read(p []byte) (int, error)  { return 0, nil }
func (f *runtimePublishFileStub) Write(p []byte) (int, error) { return len(p), nil }
func (f *runtimePublishFileStub) Close() error                { return f.closeErr }
func (f *runtimePublishFileStub) Stat() (fs.FileInfo, error)  { return nil, nil }
func (f *runtimePublishFileStub) Chmod(os.FileMode) error     { return nil }

func setHashRuntimeTraceMutationHook(t *testing.T, hook func()) {
	t.Helper()
	stableRuntimeTraceFileAfterFirstSnapshotHook = hook
	t.Cleanup(func() {
		stableRuntimeTraceFileAfterFirstSnapshotHook = nil
	})
}

func setAmbientValidatedRuntimeTraceLoaderHook(t *testing.T, hook func(capturePath string) (*ValidatedTraceSnapshot, error)) {
	t.Helper()
	ambientValidatedRuntimeTraceLoader = hook
	t.Cleanup(func() {
		ambientValidatedRuntimeTraceLoader = loadValidatedRuntimeTrace
	})
}

func setRuntimeTraceSnapshotOpenHooks(t *testing.T, afterPathSnapshot func(), afterOpen func()) {
	t.Helper()
	snapshotRuntimeTraceFileAfterPathSnapshotHook = afterPathSnapshot
	snapshotRuntimeTraceFileAfterOpenHook = afterOpen
	t.Cleanup(func() {
		snapshotRuntimeTraceFileAfterPathSnapshotHook = nil
		snapshotRuntimeTraceFileAfterOpenHook = nil
	})
}

func runWithRuntimeTraceRenameSwapRenameBack(t *testing.T, tracePath string, operation func() error) error {
	t.Helper()

	originalPath := tracePath + ".original"
	swapPath := tracePath + ".swap"
	if err := os.WriteFile(swapPath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
		t.Fatalf("write swap trace: %v", err)
	}

	stage := 0
	swapTracePath := func(stagedPath, displacedPath string, expectedStage int) {
		if stage != expectedStage {
			return
		}
		if err := os.Rename(tracePath, displacedPath); err != nil {
			t.Fatalf("rename active trace aside: %v", err)
		}
		if err := os.Rename(stagedPath, tracePath); err != nil {
			t.Fatalf("move staged trace into path: %v", err)
		}
		stage++
	}
	setRuntimeTraceSnapshotOpenHooks(t, func() { swapTracePath(swapPath, originalPath, 0) }, func() {
		swapTracePath(originalPath, swapPath, 1)
	})

	err := operation()
	if stage != 2 {
		t.Fatalf("expected deterministic swap fixture to complete, stage=%d", stage)
	}
	return err
}

type stubRoot struct {
	lstatInfo   map[string]fs.FileInfo
	lstatErr    map[string]error
	lstatSteps  map[string][]stubLstatResult
	openRoots   map[string]safeio.Root
	openRootErr map[string]error
	mkdirErr    map[string]error
	removeErr   map[string]error
	removeSteps map[string][]error
	selfInfo    fs.FileInfo
	closeErr    error
	closed      bool
}

type stubLstatResult struct {
	info fs.FileInfo
	err  error
}

func (r *stubRoot) Open(name string) (safeio.File, error) {
	return nil, fmt.Errorf("unexpected Open(%q)", name)
}

func (r *stubRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	return nil, fmt.Errorf("unexpected OpenFile(%q)", name)
}

func (r *stubRoot) OpenRoot(name string) (safeio.Root, error) {
	if root, ok := r.openRoots[name]; ok {
		return root, nil
	}
	if err, ok := r.openRootErr[name]; ok {
		return nil, err
	}
	return nil, fmt.Errorf("unexpected OpenRoot(%q)", name)
}

func (r *stubRoot) Lstat(name string) (fs.FileInfo, error) {
	if steps, ok := r.lstatSteps[name]; ok && len(steps) > 0 {
		step := steps[0]
		r.lstatSteps[name] = steps[1:]
		if step.info != nil {
			return step.info, nil
		}
		if step.err != nil {
			return nil, step.err
		}
		return nil, os.ErrNotExist
	}
	if name == "." && r.selfInfo != nil {
		return r.selfInfo, nil
	}
	if info, ok := r.lstatInfo[name]; ok {
		return info, nil
	}
	if err, ok := r.lstatErr[name]; ok {
		return nil, err
	}
	return nil, os.ErrNotExist
}

func (r *stubRoot) Mkdir(name string, perm os.FileMode) error {
	if err, ok := r.mkdirErr[name]; ok {
		return err
	}
	return nil
}

func (r *stubRoot) Chmod(name string, perm os.FileMode) error {
	return fmt.Errorf("unexpected Chmod(%q)", name)
}

func (r *stubRoot) MkdirAll(name string, perm os.FileMode) error {
	return fmt.Errorf("unexpected MkdirAll(%q)", name)
}

func (r *stubRoot) Link(oldName, newName string) error {
	return fmt.Errorf("unexpected Link(%q, %q)", oldName, newName)
}

func (r *stubRoot) Rename(oldName, newName string) error {
	return fmt.Errorf("unexpected Rename(%q, %q)", oldName, newName)
}

func (r *stubRoot) Remove(name string) error {
	if steps, ok := r.removeSteps[name]; ok && len(steps) > 0 {
		err := steps[0]
		r.removeSteps[name] = steps[1:]
		return err
	}
	if err, ok := r.removeErr[name]; ok {
		return err
	}
	return nil
}

func (r *stubRoot) Close() error {
	r.closed = true
	return r.closeErr
}
