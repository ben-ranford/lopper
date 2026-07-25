package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
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
		if err == nil || !strings.Contains(err.Error(), "create runtime trace directory") {
			t.Fatalf("expected trace directory creation error, got %v", err)
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
			if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "signal: killed") {
				t.Fatalf("expected context cancellation error, got %v", err)
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

func TestCaptureReuseDoesNotBypassRunnerProfileGate(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "python", "#!/bin/sh\n: > \"$LOPPER_RUNTIME_TRACE\"\n"))

	request := CaptureRequest{
		RepoPath:             repo,
		TracePath:            tracePath,
		Command:              "python -m unittest",
		Provider:             CaptureProviderPython,
		TrustedInputDigest:   "digest-a",
		PythonRunnerProfiles: true,
	}
	if err := Capture(context.Background(), request); err != nil {
		t.Fatalf("capture enabled runner profile: %v", err)
	}

	request.PythonRunnerProfiles = false
	request.ReuseIfUnchanged = true
	err := Capture(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), PythonRunnerProfilesFeature) {
		t.Fatalf("expected disabled runner profile gate to reject cached trace reuse, got %v", err)
	}
}

func TestCaptureReuseIfUnchangedSkipsRepeatedCommand(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	counterPath := filepath.Join(repo, "counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\ncount=$(cat \"$LOPPER_RUNTIME_COUNTER\" 2>/dev/null || echo 0)\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$LOPPER_RUNTIME_COUNTER\"\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))

	first := CaptureRequest{
		RepoPath:           repo,
		TracePath:          tracePath,
		Command:            npmTestCommand,
		TrustedInputDigest: "digest-a",
	}
	if err := Capture(context.Background(), first); err != nil {
		t.Fatalf("capture first run: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first capture execution count 1, got %d", got)
	}

	second := first
	second.ReuseIfUnchanged = true
	if err := Capture(context.Background(), second); err != nil {
		t.Fatalf("capture second run: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 1 {
		t.Fatalf("expected second capture reuse without rerun, got %d", got)
	}

	third := second
	third.Provider = CaptureProviderPython
	if err := Capture(context.Background(), third); err != nil {
		t.Fatalf("capture third run provider change: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 2 {
		t.Fatalf("expected changed provider to rerun capture, got %d", got)
	}

	fourth := third
	if err := Capture(context.Background(), fourth); err != nil {
		t.Fatalf("capture fourth run: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 2 {
		t.Fatalf("expected repeated provider-specific capture to reuse trace, got %d", got)
	}

	fifth := second
	fifth.Command = "npm run test"
	if err := Capture(context.Background(), fifth); err != nil {
		t.Fatalf("capture fifth run command change: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 3 {
		t.Fatalf("expected changed command to rerun capture, got %d", got)
	}
}

func TestReuseRuntimeTraceIfPossibleMissingTraceSkipsReuse(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	assertDefaultRuntimeReuseResult(t, tracePath, npmTestCommand, "digest-a", false)
}

func TestReuseRuntimeTraceIfPossibleDirectoryTraceSkipsReuse(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(tracePath, 0o750); err != nil {
		t.Fatalf("mkdir trace path dir: %v", err)
	}
	assertDefaultRuntimeReuseResult(t, tracePath, npmTestCommand, "digest-a", false)
}

func TestReuseRuntimeTraceIfPossibleMissingStateSkipsReuse(t *testing.T) {
	tracePath := writeTraceFixture(t)
	assertDefaultRuntimeReuseResult(t, tracePath, npmTestCommand, "digest-a", false)
}

func TestReuseRuntimeTraceIfPossibleInvalidStateSkipsReuse(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := os.WriteFile(runtimeTraceStatePath(tracePath), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid state file: %v", err)
	}
	assertDefaultRuntimeReuseResult(t, tracePath, npmTestCommand, "digest-a", false)
}

func TestParseRuntimeTraceStateValidation(t *testing.T) {
	if _, ok := parseRuntimeTraceState([]byte(`{"schema":"wrong","command":"npm test"}`)); ok {
		t.Fatalf("expected wrong runtime trace schema to be rejected")
	}
	if _, ok := parseRuntimeTraceState([]byte(`{"schema":"v3","command":"npm test","provider":"node","traceHash":"abc"}`)); ok {
		t.Fatalf("expected missing trusted input hash to be rejected")
	}
}

func TestParseRuntimeTraceStateRejectsBlankCommand(t *testing.T) {
	if _, ok := parseRuntimeTraceState([]byte(`{"schema":"v3","command":"  ","provider":"node","trustedInputHash":"digest-a","traceHash":"digest-b"}`)); ok {
		t.Fatalf("expected blank runtime trace command to be rejected")
	}
}

func TestParseRuntimeTraceStateRejectsUnsupportedProvider(t *testing.T) {
	if _, ok := parseRuntimeTraceState([]byte(`{"schema":"v3","command":"npm test","provider":"ruby","trustedInputHash":"digest-a","traceHash":"digest-b"}`)); ok {
		t.Fatalf("expected unsupported runtime trace provider to be rejected")
	}
}

func TestParseRuntimeTraceStateReturnsTrimmedValidState(t *testing.T) {
	state, ok := parseRuntimeTraceState([]byte(`{"schema":" v3 ","command":" npm test ","provider":"node","trustedInputHash":" digest-a ","traceHash":" digest-b "}`))
	if !ok {
		t.Fatalf("expected valid runtime trace state to parse")
	}
	if strings.TrimSpace(state.Command) != npmTestCommand {
		t.Fatalf("expected command to round-trip, got %q", state.Command)
	}
	if strings.TrimSpace(state.TrustedInputHash) != "digest-a" {
		t.Fatalf("expected trusted input hash to round-trip, got %q", state.TrustedInputHash)
	}
	if strings.TrimSpace(state.TraceHash) != "digest-b" {
		t.Fatalf("expected trace hash to round-trip, got %q", state.TraceHash)
	}
}

func TestWriteRuntimeTraceStateAndReuseChecks(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := writeRuntimeTraceState(tracePath, "  npm test  ", CaptureProviderNode, "digest-a"); err != nil {
		t.Fatalf("write runtime trace state: %v", err)
	}

	assertRuntimeReuseResult(t, tracePath, npmTestCommand, CaptureProviderNode, "digest-a", true)
	assertRuntimeReuseResult(t, tracePath, npmTestCommand, CaptureProviderPython, "digest-a", false)
	assertRuntimeReuseResult(t, tracePath, "npm run test", CaptureProviderNode, "digest-a", false)
	assertRuntimeReuseResult(t, tracePath, npmTestCommand, CaptureProviderNode, "digest-b", false)
}

func TestWriteRuntimeTraceStateSkipsWhenTraceMissing(t *testing.T) {
	missingParentTrace := filepath.Join(t.TempDir(), "missing", runtimeTraceNDJSON)
	if err := writeRuntimeTraceState(missingParentTrace, npmTestCommand, CaptureProviderNode, "digest-a"); err != nil {
		t.Fatalf("expected missing trace to skip runtime trace state write, got %v", err)
	}
}

func TestWriteRuntimeTraceStateAndSnapshotReturnsSnapshot(t *testing.T) {
	tracePath := writeTraceFixture(t)
	traceData := []byte("{\"module\":\"lodash/map\"}\n")
	if err := os.WriteFile(tracePath, traceData, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	snapshot, err := writeRuntimeTraceStateAndSnapshot(tracePath, npmTestCommand, CaptureProviderNode, "digest-a")
	if err != nil {
		t.Fatalf("write runtime trace state and snapshot: %v", err)
	}
	if snapshot == nil {
		t.Fatalf("expected snapshot")
	}
	if string(snapshot.data) != string(traceData) {
		t.Fatalf("expected snapshot data %q, got %q", traceData, snapshot.data)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(traceData))
	if snapshot.digest != wantDigest {
		t.Fatalf("expected digest %q, got %q", wantDigest, snapshot.digest)
	}
}

func TestWriteRuntimeTraceStateAndSnapshotSkipsDirectoryTrace(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(tracePath, 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}

	snapshot, err := writeRuntimeTraceStateAndSnapshot(tracePath, npmTestCommand, CaptureProviderNode, "digest-a")
	if err != nil {
		t.Fatalf("expected directory trace to be skipped, got %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected no snapshot for directory trace")
	}
}

func TestWriteRuntimeTraceStateAndSnapshotReturnsStateWriteError(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := os.MkdirAll(runtimeTraceStatePath(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace state dir: %v", err)
	}

	if _, err := writeRuntimeTraceStateAndSnapshot(tracePath, npmTestCommand, CaptureProviderNode, "digest-a"); err == nil {
		t.Fatalf("expected runtime trace state write error")
	}
}

func TestWriteRuntimeTraceStateAndSnapshotSurfacesStatError(t *testing.T) {
	if _, err := writeRuntimeTraceStateAndSnapshot(string([]byte{0}), npmTestCommand, CaptureProviderNode, "digest-a"); err == nil {
		t.Fatalf("expected trace stat error")
	}
}

func TestReuseRuntimeTraceIfPossibleSurfacesStatError(t *testing.T) {
	reused, err := reuseRuntimeTraceIfPossible(string([]byte{0}), npmTestCommand, CaptureProviderNode, "digest-a")
	if err == nil || reused {
		t.Fatalf("expected invalid trace path stat error, reused=%v err=%v", reused, err)
	}
}

func TestReuseRuntimeTraceIfPossibleSurfacesStateReadError(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := os.MkdirAll(runtimeTraceStatePath(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace state dir: %v", err)
	}

	reused, err := reuseRuntimeTraceIfPossible(tracePath, npmTestCommand, CaptureProviderNode, "digest-a")
	if err == nil || reused {
		t.Fatalf("expected runtime trace state read error, reused=%v err=%v", reused, err)
	}
}

func TestReuseRuntimeTraceIfPossibleRequiresTrustedInputDigest(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := writeRuntimeTraceState(tracePath, npmTestCommand, CaptureProviderNode, "digest-a"); err != nil {
		t.Fatalf("write runtime trace state: %v", err)
	}
	assertRuntimeReuseResult(t, tracePath, npmTestCommand, CaptureProviderNode, "", false)
}

func TestReuseRuntimeTraceIfPossibleSkipsTamperedTraceOrState(t *testing.T) {
	t.Run("trace changed after trusted capture", func(t *testing.T) {
		tracePath := writeTraceFixture(t)
		if err := writeRuntimeTraceState(tracePath, npmTestCommand, CaptureProviderNode, "digest-a"); err != nil {
			t.Fatalf("write runtime trace state: %v", err)
		}
		if err := os.WriteFile(tracePath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
			t.Fatalf("tamper trace file: %v", err)
		}
		assertRuntimeReuseResult(t, tracePath, npmTestCommand, CaptureProviderNode, "digest-a", false)
	})

	t.Run("forged trace and state without trusted binding", func(t *testing.T) {
		tracePath := writeTraceFixture(t)
		if err := os.WriteFile(tracePath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
			t.Fatalf("write forged trace: %v", err)
		}
		state := `{"schema":"v3","command":"npm test","provider":"node","trustedInputHash":"forged","traceHash":"forged"}`
		if err := os.WriteFile(runtimeTraceStatePath(tracePath), []byte(state), 0o600); err != nil {
			t.Fatalf("write forged state: %v", err)
		}
		assertRuntimeReuseResult(t, tracePath, npmTestCommand, CaptureProviderNode, "digest-a", false)
	})
}

func TestHashRuntimeTraceFileRejectsOversizedTrace(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	line := "{\"module\":\"lodash/map\"}\n"
	repeat := int(maxRuntimeTraceBytes)/len(line) + 1
	if err := os.WriteFile(tracePath, []byte(strings.Repeat(line, repeat)), 0o600); err != nil {
		t.Fatalf("write oversized trace: %v", err)
	}

	if _, err := hashRuntimeTraceFile(tracePath); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized trace hash to fail with ErrFileTooLarge, got %v", err)
	}
}

func TestHashRuntimeTraceFileRejectsNonRegularTrace(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(tracePath, 0o750); err != nil {
		t.Fatalf("mkdir non-regular trace path: %v", err)
	}

	if _, err := hashRuntimeTraceFile(tracePath); err == nil || !strings.Contains(err.Error(), "must be regular") {
		t.Fatalf("expected non-regular trace hash rejection, got %v", err)
	}
}

func TestHashRuntimeTraceFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.ndjson")
	tracePath := filepath.Join(dir, "trace.ndjson")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, tracePath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := hashRuntimeTraceFile(tracePath); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink trace hash rejection, got %v", err)
	}
}

func TestHashRuntimeTraceFileReturnsDigest(t *testing.T) {
	tracePath := writeTraceFixture(t)
	traceData := []byte("{\"module\":\"lodash/map\"}\n")
	if err := os.WriteFile(tracePath, traceData, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	digest, err := hashRuntimeTraceFile(tracePath)
	if err != nil {
		t.Fatalf("hash runtime trace file: %v", err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(traceData))
	if digest != wantDigest {
		t.Fatalf("expected digest %q, got %q", wantDigest, digest)
	}
}

func TestHashRuntimeTraceFileRejectsConcurrentSameInodeMutation(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	original := []byte("trace-line-alpha\n")
	mutated := []byte("trace-line-omega\n")
	if len(original) != len(mutated) {
		t.Fatalf("expected same-size mutation fixture")
	}
	if err := os.WriteFile(tracePath, original, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	setHashRuntimeTraceMutationHook(t, func() {
		file, err := os.OpenFile(tracePath, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			t.Fatalf("open trace for mutation: %v", err)
		}
		if _, err := file.Write(mutated); err != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close trace after failed mutation write: %v", closeErr)
			}
			t.Fatalf("mutate trace contents: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close mutated trace: %v", err)
		}
	})

	if _, err := hashRuntimeTraceFile(tracePath); err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected same-inode mutation to be rejected, got %v", err)
	}
}

func TestWriteRuntimeTraceStateRejectsConcurrentSameInodeMutation(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	if err := os.WriteFile(tracePath, []byte("trace-line-alpha\n"), 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.WriteFile(tracePath, []byte("trace-line-omega\n"), 0o600); err != nil {
			t.Fatalf("mutate trace contents: %v", err)
		}
	})

	if err := writeRuntimeTraceState(tracePath, npmTestCommand, CaptureProviderNode, "digest-a"); err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected concurrent mutation to reject runtime trace state write, got %v", err)
	}
	if _, err := os.Stat(runtimeTraceStatePath(tracePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rejected state write to leave no state file, got stat err %v", err)
	}
}

func TestReuseRuntimeTraceIfPossibleRejectsConcurrentSameInodeMutation(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	if err := os.WriteFile(tracePath, []byte("trace-line-alpha\n"), 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}
	if err := writeRuntimeTraceState(tracePath, npmTestCommand, CaptureProviderNode, "digest-a"); err != nil {
		t.Fatalf("write runtime trace state: %v", err)
	}

	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.WriteFile(tracePath, []byte("trace-line-omega\n"), 0o600); err != nil {
			t.Fatalf("mutate trace contents: %v", err)
		}
	})

	reused, err := reuseRuntimeTraceIfPossible(tracePath, npmTestCommand, CaptureProviderNode, "digest-a")
	if err == nil || reused || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected concurrent mutation to reject runtime trace reuse, reused=%v err=%v", reused, err)
	}
}

func TestReuseRuntimeTraceIfPossibleRejectsRenameSwapRenameBackDuringDescriptorOpen(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := writeRuntimeTraceState(tracePath, npmTestCommand, CaptureProviderNode, "digest-a"); err != nil {
		t.Fatalf("write runtime trace state: %v", err)
	}

	originalPath := tracePath + ".original"
	swapPath := tracePath + ".swap"
	if err := os.WriteFile(swapPath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
		t.Fatalf("write swap trace: %v", err)
	}

	stage := 0
	beforeOpenHook := func() {
		if stage != 0 {
			return
		}
		if err := os.Rename(tracePath, originalPath); err != nil {
			t.Fatalf("rename original trace aside: %v", err)
		}
		if err := os.Rename(swapPath, tracePath); err != nil {
			t.Fatalf("swap trace into path: %v", err)
		}
		stage = 1
	}
	afterOpenHook := func() {
		if stage != 1 {
			return
		}
		if err := os.Rename(tracePath, swapPath); err != nil {
			t.Fatalf("rename swap trace aside: %v", err)
		}
		if err := os.Rename(originalPath, tracePath); err != nil {
			t.Fatalf("restore original trace path: %v", err)
		}
		stage = 2
	}
	setRuntimeTraceSnapshotOpenHooks(t, beforeOpenHook, afterOpenHook)

	reused, err := reuseRuntimeTraceIfPossible(tracePath, npmTestCommand, CaptureProviderNode, "digest-a")
	if err == nil || reused || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("expected rename/swap/rename-back to reject runtime trace reuse, reused=%v err=%v", reused, err)
	}
	if stage != 2 {
		t.Fatalf("expected deterministic swap fixture to complete, stage=%d", stage)
	}
}

func TestStableRuntimeTraceFileSnapshotReturnsSnapshot(t *testing.T) {
	tracePath := writeTraceFixture(t)
	traceData := []byte("{\"module\":\"lodash/map\"}\n")
	if err := os.WriteFile(tracePath, traceData, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	snapshot, err := stableRuntimeTraceFileSnapshot(tracePath)
	if err != nil {
		t.Fatalf("stable runtime trace file snapshot: %v", err)
	}
	if string(snapshot.data) != string(traceData) {
		t.Fatalf("expected snapshot data %q, got %q", traceData, snapshot.data)
	}
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

func TestSnapshotRuntimeTraceFileReturnsSnapshot(t *testing.T) {
	tracePath := writeTraceFixture(t)
	traceData := []byte("{\"module\":\"lodash/map\"}\n")
	if err := os.WriteFile(tracePath, traceData, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	snapshot, err := snapshotRuntimeTraceFile(tracePath)
	if err != nil {
		t.Fatalf("snapshot runtime trace file: %v", err)
	}
	if string(snapshot.data) != string(traceData) {
		t.Fatalf("expected snapshot data %q, got %q", traceData, snapshot.data)
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

func TestPrepareTracePathFailsWhenStaleStateIsDirectory(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), ".artifacts", runtimeTraceNDJSON)
	statePath := runtimeTraceStatePath(tracePath)
	if err := os.MkdirAll(statePath, 0o750); err != nil {
		t.Fatalf("mkdir stale trace state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(statePath, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write stale trace state child: %v", err)
	}
	if err := prepareTracePath(tracePath); err == nil || !strings.Contains(err.Error(), "remove previous runtime trace state") {
		t.Fatalf("expected stale trace state cleanup error, got %v", err)
	}
}

func TestBuildRuntimeCommandRejectsUnsupportedAllowlistedPath(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "foobar", "#!/bin/sh\nexit 0\n"))

	if _, err := buildRuntimeCommand(context.Background(), "foobar test"); err == nil || !strings.Contains(err.Error(), "unsupported runtime test executable") {
		t.Fatalf("expected allowlist rejection for unsupported executable, got %v", err)
	}
}

func TestCaptureSurfacesTraceStateWriteFailure(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	script := "#!/bin/sh\nmkdir -p \"$LOPPER_RUNTIME_TRACE.state.json\"\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", script))

	err := Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   npmTestCommand,
	})
	if err == nil || !strings.Contains(err.Error(), "write runtime trace state") {
		t.Fatalf("expected runtime trace state write error, got %v", err)
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

func assertDefaultRuntimeReuseResult(t *testing.T, tracePath, command string, trustedInputDigest string, wantReused bool) {
	t.Helper()

	assertRuntimeReuseResult(t, tracePath, command, CaptureProviderNode, trustedInputDigest, wantReused)
}

func assertRuntimeReuseResult(t *testing.T, tracePath, command string, provider CaptureProvider, trustedInputDigest string, wantReused bool) {
	t.Helper()

	reused, err := reuseRuntimeTraceIfPossible(tracePath, command, provider, trustedInputDigest)
	if err != nil {
		t.Fatalf("reuse runtime trace: %v", err)
	}
	if reused != wantReused {
		t.Fatalf("expected reused=%v, got %v", wantReused, reused)
	}
}

func readCaptureCounter(t *testing.T, counterPath string) int {
	t.Helper()
	content, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read capture counter: %v", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		t.Fatalf("parse capture counter: %v", err)
	}
	return value
}

func setHashRuntimeTraceMutationHook(t *testing.T, hook func()) {
	t.Helper()
	hashRuntimeTraceFileAfterFirstSnapshotHook = hook
	t.Cleanup(func() {
		hashRuntimeTraceFileAfterFirstSnapshotHook = nil
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
