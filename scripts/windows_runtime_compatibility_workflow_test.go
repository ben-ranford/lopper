package scripts

import (
	"archive/tar"
	"bytes"
	"go/build"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestWindowsRuntimeCompatibilityProofRequiresPublishedBaseline(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	const jobName = "regression-proof-windows"
	const stepName = "Verify native Windows runtime path compatibility"
	step := workflowStepByName(t, workflow.Jobs, jobName, stepName)
	if step.If != "" || step.ContinueOnError || step.Shell != "pwsh" || step.Run != "./scripts/verify-windows-runtime-paths.ps1" {
		t.Fatal("runtime compatibility proof must run natively and fail closed for build titles too")
	}
	assertWorkflowStepOrder(t, workflow.Jobs[jobName], "Provision restricted regression proof runtime", "Test native Windows proof tools", "Verify restricted regression proof runtime", stepName, "Prove Windows regression tests for fix PRs")
	assertPinnedNodeConsumerEnvironment(t, step)
	if step.Env["REGRESSION_PROOF_GO_ROOT"] != "${{ steps.proof_runtime.outputs.go_root }}" {
		t.Fatal("runtime compatibility proof must select the authenticated compiler root")
	}
	index := workflowStepIndexByName(t, workflow.Jobs, jobName, stepName)
	verifier := workflowStepByName(t, workflow.Jobs, "verify-checks", "Verify restricted regression proof runtime")
	if index == 0 || !reflect.DeepEqual(workflow.Jobs[jobName].Steps[index-1], verifier) {
		t.Fatal("runtime compatibility proof must immediately follow the reviewed authenticated verifier")
	}
	script := readConfig(t, "scripts/verify-windows-runtime-paths.ps1")
	assertWindowsRuntimeProofBoundaries(t, script)
}

func assertWindowsRuntimeProofBoundaries(t *testing.T, script string) {
	t.Helper()
	for _, required := range []string{
		"if (-not $IsWindows)",
		"$baseline = 'c47703c6b1c7e0a9b813061beb44d98fb972b101'",
		"$baselineHash = 'e9ea580923770e10f4ec3e62d09c473993d068f2f58139ee0714ae4c1b6c54e2'",
		"git fetch --no-tags origin $baseline",
		"git -c core.autocrlf=false -c core.eol=lf archive --format=tar", "$baseline -- internal/runtime/capture_command.go",
		"(Get-FileHash -LiteralPath $oldSource -Algorithm SHA256).Hash.ToLowerInvariant() -ne $baselineHash",
		"& $go test '-overlay' $overlay ./internal/runtime -run '^$' -count=1",
		"if ($LASTEXITCODE -ne 0) { throw 'Baseline must compile before behavioral proof' }",
		"$oldExit -ne 1 -or $namedFailure.Count -ne 1 -or $admissionFailure.Count -eq 0",
		"$_.Action -eq 'fail' -and $_.Test -eq $testName",
		"$_.Test -eq $testName -and $_.Output -like '*valid root dropped:*'",
		"if ($headExit -ne 0)", "if ($passes.Count -ne 1)",
		"$_.Action -eq 'skip' -or $_.Action -eq 'fail'",
		"throw \"Runtime proof changed candidate source: $path\"",
		"finally {", "Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction Stop",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("native proof lost required custody or outcome boundary %q", required)
		}
	}
	if strings.Contains(script, "merge-base") || strings.Contains(script, "PR_BODY_FILE") || strings.Contains(script, "SilentlyContinue") {
		t.Fatal("runtime proof must use exact published source and preserve failures independently of PR metadata")
	}
}

func TestWindowsUNCFixtureRequiresExplicitIntegrationTag(t *testing.T) {
	context := build.Default
	context.GOOS = "windows"
	context.GOARCH = "amd64"
	context.CgoEnabled = false
	context.BuildTags = nil
	ordinary, err := context.ImportDir(repoPath(t, "internal/runtime"), 0)
	if err != nil {
		t.Fatal(err)
	}
	const fixture = "capture_searchdirs_unc_windows_test.go"
	if slices.Contains(ordinary.TestGoFiles, fixture) || !slices.Contains(ordinary.TestGoFiles, "capture_searchdirs_windows_test.go") {
		t.Fatalf("ordinary Windows selection must retain real drive checks without requiring a share: %v", ordinary.TestGoFiles)
	}
	context.BuildTags = []string{"lopper_native_windows_unc"}
	integration, err := context.ImportDir(repoPath(t, "internal/runtime"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(integration.TestGoFiles, fixture) {
		t.Fatal("explicit integration tag did not select the native UNC contract")
	}
	source := readConfig(t, "internal/runtime/"+fixture)
	if strings.Contains(source, "t.Skip") || !strings.Contains(source, `t.Fatal("native UNC fixture requires LOPPER_WINDOWS_TEST_UNC_DIR with an accessible share")`) {
		t.Fatal("explicit UNC integration must fail when its fixture is unavailable")
	}
}

func TestWindowsRuntimeBaselineArchivePreservesBlobBytes(t *testing.T) {
	t.Parallel()
	for _, conversion := range []string{"true", "false"} {
		t.Run(conversion, func(t *testing.T) {
			t.Parallel()
			repo := newHookFixture(t)
			const source = "internal/runtime/capture_command.go"
			writeFile(t, filepath.Join(repo, source), "package runtime\n\n// exact baseline bytes\n")
			writeFile(t, filepath.Join(repo, ".gitattributes"), "*.go text\n")
			runCommand(t, repo, "git", "add", source, ".gitattributes")
			runCommand(t, repo, "git", "commit", "-m", "archive fixture")
			raw, err := hookCommand(repo, "git", "show", "HEAD:"+source)
			if err != nil {
				t.Fatalf("read raw baseline blob: %v", err)
			}
			runCommand(t, repo, "git", "config", "--local", "core.autocrlf", conversion)
			runCommand(t, repo, "git", "config", "--local", "core.eol", "crlf")
			archive := filepath.Join(t.TempDir(), "baseline with spaces.tar")
			args := runtimeBaselineArchiveArgs(t, archive)
			if output, err := hookCommand(repo, "git", args...); err != nil {
				t.Fatalf("production baseline archive command: %v\n%s", err, output)
			}
			if got := runtimeArchiveSource(t, archive, source); !bytes.Equal(got, []byte(raw)) {
				t.Fatalf("production archive changed raw baseline blob bytes: got %d bytes, want %d", len(got), len(raw))
			}
		})
	}
}

func runtimeBaselineArchiveArgs(t *testing.T, archive string) []string {
	t.Helper()
	var commands []string
	for _, line := range strings.Split(readConfig(t, "scripts/verify-windows-runtime-paths.ps1"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "git ") && strings.Contains(line, " archive ") {
			commands = append(commands, line)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("expected exactly one production baseline archive command, got %d", len(commands))
	}
	args := strings.Fields(commands[0])[1:]
	for index, arg := range args {
		arg = strings.Trim(arg, "\"")
		arg = strings.ReplaceAll(arg, "$baseline", "HEAD")
		arg = strings.ReplaceAll(arg, "$archive", archive)
		if strings.Contains(arg, "$") {
			t.Fatalf("unhandled production archive argument: %s", arg)
		}
		args[index] = arg
	}
	return args
}

func runtimeArchiveSource(t *testing.T, archive, source string) []byte {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close baseline archive: %v", err)
		}
	}()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("read baseline archive entry: %v", err)
		}
		if header.Name == source {
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("extract baseline source: %v", err)
			}
			return data
		}
	}
}
