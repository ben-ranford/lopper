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
	wantTracePath := filepath.Join(plan.repoPath, ".artifacts", runtimeTraceNDJSON)
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
	}{
		{name: "existing command", tool: "make", command: "make test"},
		{name: "enabled uv runner profile", tool: "uv", command: "uv run pytest", provider: CaptureProviderPython, pythonRunnerProfiles: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			markerPath := filepath.Join(repo, "started.txt")
			t.Setenv("LOPPER_CAPTURE_MARKER", markerPath)
			t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, tc.tool, "#!/bin/sh\nsleep 5\nprintf started > \"$LOPPER_CAPTURE_MARKER\"\n"))

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			start := time.Now()
			err := Capture(ctx, CaptureRequest{
				RepoPath:             repo,
				Command:              tc.command,
				Provider:             tc.provider,
				PythonRunnerProfiles: tc.pythonRunnerProfiles,
			})
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

func setHashRuntimeTraceMutationHook(t *testing.T, hook func()) {
	t.Helper()
	stableRuntimeTraceFileAfterFirstSnapshotHook = hook
	t.Cleanup(func() {
		stableRuntimeTraceFileAfterFirstSnapshotHook = nil
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

func (r *stubRoot) Rename(oldName, newName string) error {
	return fmt.Errorf("unexpected Rename(%q, %q)", oldName, newName)
}

func (r *stubRoot) Remove(name string) error {
	if err, ok := r.removeErr[name]; ok {
		return err
	}
	return nil
}

func (r *stubRoot) Close() error {
	r.closed = true
	return r.closeErr
}
