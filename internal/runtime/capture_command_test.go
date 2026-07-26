package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestBuildRuntimeCommandAllowlist(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeTools(t))

	commands := []string{
		npmTestCommand,
		"pnpm test",
		"yarn test",
		"bun test",
		"npx vitest",
		"node -v",
		"vitest run",
		"jest --runInBand",
		"mocha",
		"ava",
		"deno test",
		"make test",
		"pytest",
		"pytest tests -q",
		"python -m pytest",
		"python3 -m pytest tests",
	}

	for _, command := range commands {
		cmd, err := buildRuntimeCommand(context.Background(), command)
		if err != nil {
			t.Fatalf("expected %q to be allowlisted: %v", command, err)
		}
		if cmd.Path == "" || !filepath.IsAbs(cmd.Path) {
			t.Fatalf("expected executable path for command %q", command)
		}
		registerRuntimeCommandCleanup(t, cmd)
	}
}

func TestBuildRuntimeCommandAllowsPythonRunnerProfiles(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeTools(t))
	options := CommandOptions{PythonRunnerProfiles: true}
	commands := []string{
		"python -m unittest",
		"python3 -m unittest discover -s tests",
		"uv run pytest",
		"uv run -- pytest tests -q",
		"uv run python -m pytest tests",
		"uv run python3 -m pytest tests",
		"uv run python -m unittest discover",
		"uv run -- python3 -m unittest tests.test_api",
	}

	for _, command := range commands {
		cmd, err := buildRuntimeCommand(context.Background(), command, options)
		if err != nil {
			t.Fatalf("expected enabled runner profile %q to be allowlisted: %v", command, err)
		}
		if cmd.Path == "" || !filepath.IsAbs(cmd.Path) {
			t.Fatalf("expected executable path for command %q", command)
		}
		registerRuntimeCommandCleanup(t, cmd)
	}
}

func TestBuildRuntimeCommandPreservesParsedArgs(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeTools(t))

	testCases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "quoted args",
			command: `node -r "console.log('hello world')"`,
			want:    []string{"node", "-r", "console.log('hello world')"},
		},
		{
			name:    "single quoted args",
			command: `node -r 'console.log("hello")'`,
			want:    []string{"node", "-r", `console.log("hello")`},
		},
		{
			name:    "escaped whitespace",
			command: `make test\ target`,
			want:    []string{"make", "test target"},
		},
		{
			name:    "forwarded separator",
			command: `uv run -- pytest "tests/integration suite" -- -k smoke`,
			want:    []string{"uv", "run", "--", "pytest", "tests/integration suite", "--", "-k", "smoke"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := buildRuntimeCommand(context.Background(), tc.command, CommandOptions{PythonRunnerProfiles: true})
			if err != nil {
				t.Fatalf("build runtime command: %v", err)
			}
			if !slices.Equal(cmd.Args[1:], tc.want[1:]) {
				t.Fatalf("expected args %q, got %q", tc.want[1:], cmd.Args[1:])
			}
			if got := filepath.Base(cmd.Path); got != tc.want[0] {
				t.Fatalf("expected executable %q, got %q", tc.want[0], got)
			}
			registerRuntimeCommandCleanup(t, cmd)
		})
	}
}

func TestBuildRuntimeCommandRequiresInput(t *testing.T) {
	if _, err := buildRuntimeCommand(context.Background(), " "); err == nil {
		t.Fatalf("expected empty command error")
	}
}

func TestBuildRuntimeCommandRejectsInvalidInput(t *testing.T) {
	testCases := []struct {
		name    string
		command string
		wantErr string
	}{
		{
			name:    "unfinished escape",
			command: `npm test\`,
			wantErr: "unfinished escape sequence",
		},
		{
			name:    "unterminated quote",
			command: `node -e "console.log('hello world')`,
			wantErr: "unterminated quote",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildRuntimeCommand(context.Background(), tc.command)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestBuildRuntimeCommandRejectsUnsafeSyntaxAndFlags(t *testing.T) {
	checkRejects := func(command, wantErr string) {
		t.Helper()

		_, err := buildRuntimeCommand(context.Background(), command)
		if err == nil {
			t.Fatalf("expected error containing %q", wantErr)
		}
		if !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("expected error containing %q, got %v", wantErr, err)
		}
	}

	checkRejects(`npm test && echo bad`, "indirect command execution operators")
	checkRejects(`node -e 'console.log("hi")'`, "unsafe executable flag")
	checkRejects(`python -c 'print("hi")'`, "may only run '-m pytest'")
	checkRejects(`python -m pip install pytest`, "may only run '-m pytest'")
	checkRejects(`python3 -m unittest`, PythonRunnerProfilesFeature)
	checkRejects(`PYTHONPATH=/tmp python -m pytest`, "inline environment assignment")
}

func TestBuildRuntimeCommandRejectsUnsafePythonRunnerProfileShapes(t *testing.T) {
	options := CommandOptions{PythonRunnerProfiles: true}
	commands := []string{
		"uv --directory /tmp run pytest",
		"uv run --isolated pytest",
		"uv run --python 3.13 pytest",
		"uv run ruff check",
		"uv tool run pytest",
		"uv run python -c 'print(1)'",
		"uv run python -m pip install pytest",
		"uv run --",
		"python -I -m unittest",
	}

	for _, command := range commands {
		_, err := buildRuntimeCommand(context.Background(), command, options)
		if err == nil {
			t.Fatalf("expected unsafe runner profile %q to be rejected", command)
		}
		if !strings.Contains(err.Error(), "may only") {
			t.Fatalf("expected profile-boundary error for %q, got %v", command, err)
		}
	}
}

func TestValidateCommand(t *testing.T) {
	if err := ValidateCommand(" "); err != nil {
		t.Fatalf("expected blank command to be ignored, got %v", err)
	}
	if err := ValidateCommand("npm test"); err != nil {
		t.Fatalf("expected safe command to validate, got %v", err)
	}
	if err := ValidateCommand("python3 -m pytest tests"); err != nil {
		t.Fatalf("expected python pytest command to validate, got %v", err)
	}
	if err := ValidateCommand("python3 -m unittest tests"); err == nil || !strings.Contains(err.Error(), PythonRunnerProfilesFeature) {
		t.Fatalf("expected disabled runner profile error, got %v", err)
	}
	if err := ValidateCommand("python3 -m unittest tests", CommandOptions{PythonRunnerProfiles: true}); err != nil {
		t.Fatalf("expected enabled unittest profile to validate, got %v", err)
	}
	if err := ValidateCommand("uv run pytest -- -k smoke", CommandOptions{PythonRunnerProfiles: true}); err != nil {
		t.Fatalf("expected enabled uv profile to validate, got %v", err)
	}

	err := ValidateCommand(`node -e 'console.log("hi")'`)
	if err == nil || !strings.Contains(err.Error(), "unsafe executable flag") {
		t.Fatalf("expected unsafe executable flag rejection, got %v", err)
	}
}

func TestIsPythonTestCommand(t *testing.T) {
	testCases := []struct {
		command        string
		runnerProfiles bool
		want           bool
	}{
		{command: "pytest", want: true},
		{command: "pytest tests -q", want: true},
		{command: "python -m pytest", want: true},
		{command: "python3 -m pytest tests", want: true},
		{command: "python -m unittest", runnerProfiles: true, want: true},
		{command: "python3 -m unittest discover", runnerProfiles: true, want: true},
		{command: "uv run pytest", runnerProfiles: true, want: true},
		{command: "uv run -- python -m unittest", runnerProfiles: true, want: true},
		{command: "python -m unittest", want: false},
		{command: "uv run pytest", want: false},
		{command: "npm test", want: false},
		{command: "python -m pip", want: false},
		{command: `python -m "pytest`, want: false},
	}

	for _, tc := range testCases {
		options := CommandOptions{PythonRunnerProfiles: tc.runnerProfiles}
		if got := IsPythonTestCommand(tc.command, options); got != tc.want {
			t.Fatalf("IsPythonTestCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestParseRuntimeCommandUnixArgumentConventions(t *testing.T) {
	setRuntimeOSTest(t, "linux")
	got, err := parseRuntimeCommand(`uv run pytest 'tests/integration suite' -- -k smoke`)
	if err != nil {
		t.Fatalf("parse Unix runtime command: %v", err)
	}
	want := []string{"uv", "run", "pytest", "tests/integration suite", "--", "-k", "smoke"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected Unix args %q, got %q", want, got)
	}
}

func TestParseRuntimeCommandWindowsArgumentConventions(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	testCases := []struct {
		command string
		want    []string
	}{
		{
			command: `python -m unittest C:\repo\tests`,
			want:    []string{"python", "-m", "unittest", `C:\repo\tests`},
		},
		{
			command: `uv run pytest "C:\repo\tests suite" -- -k smoke`,
			want:    []string{"uv", "run", "pytest", `C:\repo\tests suite`, "--", "-k", "smoke"},
		},
		{
			command: `uv run pytest "C:\repo\quoted\\\"name.py"`,
			want:    []string{"uv", "run", "pytest", `C:\repo\quoted\"name.py`},
		},
		{
			command: `uv run pytest "C:\repo\\"`,
			want:    []string{"uv", "run", "pytest", `C:\repo\`},
		},
		{
			command: `uv run pytest C:\repo\\tests`,
			want:    []string{"uv", "run", "pytest", `C:\repo\\tests`},
		},
		{
			command: `uv run pytest ""`,
			want:    []string{"uv", "run", "pytest", ""},
		},
	}

	for _, tc := range testCases {
		got, err := parseRuntimeCommand(tc.command)
		if err != nil {
			t.Fatalf("parse Windows runtime command %q: %v", tc.command, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("expected Windows args %q, got %q", tc.want, got)
		}
	}

	if _, err := parseRuntimeCommand(`uv run pytest "C:\repo\tests`); err == nil || !strings.Contains(err.Error(), "unterminated quote") {
		t.Fatalf("expected unterminated Windows quote error, got %v", err)
	}
	if fields, err := parseRuntimeCommand("   "); err != nil || len(fields) != 0 {
		t.Fatalf("expected blank Windows command to produce no fields, fields=%v err=%v", fields, err)
	}
}

func TestInlineEnvironmentAssignmentShape(t *testing.T) {
	for _, token := range []string{"PYTHONPATH=/tmp", "_LOPPER_2=value"} {
		if !isInlineEnvironmentAssignment(token) {
			t.Fatalf("expected %q to be recognized as an environment assignment", token)
		}
	}
	for _, token := range []string{"=value", "2PYTHON=value", "python3"} {
		if isInlineEnvironmentAssignment(token) {
			t.Fatalf("expected %q not to be recognized as an environment assignment", token)
		}
	}
}

func TestContainsUnsafeRuntimeCommandSyntax(t *testing.T) {
	if containsUnsafeRuntimeCommandSyntax(`node -r 'const value = "a\\b"'`) {
		t.Fatalf("expected backslashes in single quotes to stay safe")
	}
	if containsUnsafeRuntimeCommandSyntax(`node -r 'console.log("a && b")'`) {
		t.Fatalf("expected shell operators in single quotes to stay safe")
	}
	if containsUnsafeRuntimeCommandSyntax(`node -r '$(pwd)'`) {
		t.Fatalf("expected subshell syntax in single quotes to stay safe")
	}
	if !containsUnsafeRuntimeCommandSyntax(`node -r $(pwd)`) {
		t.Fatalf("expected subshell syntax outside quotes to be rejected")
	}
}

func TestContainsUnsafeRuntimeCommandSyntaxUsesWindowsQuoting(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	if containsUnsafeRuntimeCommandSyntax(`python -m unittest C:\repo\tests`) {
		t.Fatal("expected Windows path backslashes to remain literal")
	}
	if !containsUnsafeRuntimeCommandSyntax(`python -m unittest 'tests & more'`) {
		t.Fatal("expected Windows single quotes not to hide indirect operators")
	}
}

func TestResolveRuntimeExecutablePathSkipsNonExecutableCandidate(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("non-executable mode bit checks are Unix-specific")
	}

	firstDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-first-"), "first")
	secondDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-second-"), "second")
	if err := os.MkdirAll(firstDir, 0o755); err != nil {
		t.Fatalf("mkdir first dir: %v", err)
	}
	if err := os.MkdirAll(secondDir, 0o755); err != nil {
		t.Fatalf("mkdir second dir: %v", err)
	}

	firstPath := filepath.Join(firstDir, "npm")
	if err := os.WriteFile(firstPath, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write non-executable tool: %v", err)
	}
	secondPath := filepath.Join(secondDir, "npm")
	if err := os.WriteFile(secondPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write executable tool: %v", err)
	}

	got, err := resolveRuntimeExecutablePath("npm", []string{firstDir, secondDir})
	if err != nil {
		t.Fatalf("resolve runtime executable path: %v", err)
	}
	if got != secondPath {
		t.Fatalf("expected executable path %q, got %q", secondPath, got)
	}
}

func TestResolveRuntimeExecutablePathSkipsWritableCandidate(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("writable mode checks are Unix-specific")
	}

	firstDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-first-"), "first")
	secondDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-second-"), "second")
	if err := os.MkdirAll(firstDir, 0o755); err != nil {
		t.Fatalf("mkdir first dir: %v", err)
	}
	if err := os.MkdirAll(secondDir, 0o755); err != nil {
		t.Fatalf("mkdir second dir: %v", err)
	}
	firstPath := filepath.Join(firstDir, "python3")
	if err := os.WriteFile(firstPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write writable tool: %v", err)
	}
	if err := os.Chmod(firstPath, 0o777); err != nil {
		t.Fatalf("make tool writable: %v", err)
	}
	secondPath := filepath.Join(secondDir, "python3")
	if err := os.WriteFile(secondPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write trusted tool: %v", err)
	}

	got, err := resolveRuntimeExecutablePath("python3", []string{firstDir, secondDir})
	if err != nil {
		t.Fatalf("resolve runtime executable path: %v", err)
	}
	if got != secondPath {
		t.Fatalf("expected trusted executable path %q, got %q", secondPath, got)
	}
}

func TestResolveRuntimeExecutablePathRejectsSwappedSymlinkCandidate(t *testing.T) {
	trustedDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-trusted-"), "trusted")
	outsideDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-outside-"), "outside")
	if err := os.MkdirAll(trustedDir, 0o755); err != nil {
		t.Fatalf("mkdir trusted dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	outsidePath := runtimeToolPathForTest(t, outsideDir, "node")
	if err := os.WriteFile(outsidePath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write outside tool: %v", err)
	}

	symlinkPath := filepath.Join(trustedDir, "npm")
	if err := os.Symlink(outsidePath, symlinkPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := resolveRuntimeExecutablePath("npm", []string{trustedDir})
	if err == nil {
		t.Fatal("expected symlink candidate to be rejected")
	}
	if !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("expected trusted-dir rejection for symlink candidate, got %v", err)
	}
}

func TestResolveRuntimeExecutablePathCanonicalizesHomebrewStyleSymlinkCandidate(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix symlink canonicalization is covered here")
	}

	trustedDir, canonicalPath := createHomebrewStyleNodeToolFixture(t, "npm", "npm-cli.js")
	assertRuntimeExecutableCanonicalPath(t, "npm", trustedDir, canonicalPath)
}

func TestResolveRuntimeExecutablePathCanonicalizesNVMStyleSymlinkCandidate(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix symlink canonicalization is covered here")
	}

	currentDir, canonicalPath := createNVMCurrentToolFixture(t, "npx", "npx-cli.js")
	assertRuntimeExecutableCanonicalPath(t, "npx", currentDir, canonicalPath)
}

func TestBuildRuntimeCommandUsesTrustedNodeForCanonicalCLIScripts(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix npm and npx launcher mediation is covered here")
	}

	for _, tc := range runtimeNodeCLITestCases() {
		t.Run(tc.name, func(t *testing.T) {
			exerciseTrustedNodeForCanonicalCLIScript(t, tc)
		})
	}
}

func TestBuildRuntimeCommandStagesCanonicalCLIForNodeExecution(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix CLI staging behavior is covered here")
	}

	searchDir, cliPath := createHomebrewStyleNodeToolFixture(t, "npm", "npm-cli.js")
	nodePath, err := resolveRuntimeExecutablePath("node", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve trusted node fixture: %v", err)
	}
	if err := os.Chmod(nodePath, 0o700); err != nil {
		t.Fatalf("make trusted node fixture writable: %v", err)
	}
	writeRuntimeTestExecutable(t, nodePath, "#!/bin/sh\ncat \"$1\"\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "npm test")
	if err != nil {
		t.Fatalf("build staged CLI runtime command: %v", err)
	}
	stagedCLIPath := cmd.Args[1]
	if stagedCLIPath == cliPath {
		t.Fatalf("expected staged CLI path instead of canonical %q", cliPath)
	}

	originalCLIPath := cliPath + ".original"
	if err := os.Rename(cliPath, originalCLIPath); err != nil {
		t.Fatalf("move canonical CLI after command construction: %v", err)
	}
	if err := os.WriteFile(cliPath, []byte("poisoned\n"), 0o700); err != nil {
		t.Fatalf("replace canonical CLI after command construction: %v", err)
	}

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run staged CLI runtime command: %v", err)
	}
	if got := string(output); !strings.Contains(got, "process.stdout.write('npm-cli.js") {
		t.Fatalf("expected staged original CLI content, got %q", got)
	}
	if _, err := os.Stat(stagedCLIPath); !os.IsNotExist(err) {
		t.Fatalf("expected staged CLI cleanup after run, stat err=%v", err)
	}
}

func TestBuildRuntimeCommandRejectsCrossInstallationLauncherAndNodePoison(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix npm and npx installation identity is covered here")
	}

	for _, tc := range runtimeNodeCLIPoisonTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			exerciseCrossInstallationLauncherAndNodePoisonRejection(t, tc)
		})
	}
}

func TestNodeCLIInstallationIdentityRejectsMalformedLayouts(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix npm and npx installation identity is covered here")
	}

	for _, tc := range malformedNodeCLIInstallationCases() {
		t.Run(tc.name, tc.run)
	}
}

func TestNewTrustedRuntimeCommandRejectsUntrustedPathAtLaunchBoundary(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable mode trust is covered here")
	}

	executablePath := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-untrusted-launch-"), "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(executablePath, 0o777); err != nil {
		t.Fatalf("chmod untrusted runtime executable: %v", err)
	}

	resolution := resolvedRuntimeExecutable{path: executablePath}
	cmd, err := newTrustedRuntimeCommand(context.Background(), "node", &resolution, []string{"--version"})
	if err == nil {
		t.Fatalf("expected untrusted launch path rejection, got command %#v", cmd)
	}
	if !strings.Contains(err.Error(), "not trusted at launch boundary") {
		t.Fatalf("expected launch-boundary trust error, got %v", err)
	}
}

func TestNewTrustedRuntimeCommandRevalidatesCanonicalCLIAtLaunchBoundary(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable mode trust is covered here")
	}

	searchDir, cliPath := createNVMCurrentToolFixture(t, "npx", "npx-cli.js")
	resolution, err := resolveRuntimeExecutable("npx", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve trusted CLI fixture: %v", err)
	}
	if err := os.Chmod(cliPath, 0o777); err != nil {
		t.Fatalf("make canonical CLI untrusted: %v", err)
	}

	cmd, err := newTrustedRuntimeCommand(context.Background(), "npx", &resolution, []string{"vitest"})
	if err == nil {
		t.Fatalf("expected untrusted canonical CLI rejection, got command %#v", cmd)
	}
	if !strings.Contains(err.Error(), "validate canonical CLI script") ||
		!strings.Contains(err.Error(), "not trusted at launch boundary") {
		t.Fatalf("expected canonical CLI launch-boundary trust error, got %v", err)
	}
}

func TestNewTrustedRuntimeCommandCleansNodeStageWhenCLIStagingFails(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix fixture coverage is exercised here")
	}

	searchDir, cliPath := createHomebrewStyleNodeToolFixture(t, "npm", "npm-cli.js")
	resolution, err := resolveRuntimeExecutable("npm", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve trusted CLI fixture: %v", err)
	}
	if err := resolution.closeSource(); err != nil {
		t.Fatalf("close original CLI source: %v", err)
	}

	stageErr := errors.New("cli stage read failed")
	resolution.source = &runtimeExecutableSource{
		path: cliPath,
		file: &runtimeStageFileStub{reader: &runtimeErrorReader{err: stageErr}},
		root: &stubRoot{},
		info: runtimeExecutableInfoForTest(t, 1),
	}

	cmd, err := newTrustedRuntimeCommand(context.Background(), "npm", &resolution, []string{"test"})
	if cmd != nil || !errors.Is(err, stageErr) {
		t.Fatalf("expected CLI staging failure identity, cmd=%#v err=%v", cmd, err)
	}
}

func TestNewTrustedRuntimeCommandPreservesArgvAndCancellation(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixture and cancellation are covered here")
	}

	searchDir := testutil.SecureHomeTempDir(t, "runtime-trusted-launch-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nexit 0\n")
	resolution, err := resolveRuntimeExecutable("node", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve trusted runtime executable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := newTrustedRuntimeCommand(ctx, "node", &resolution, []string{"--eval", "value with spaces"})
	if err != nil {
		t.Fatalf("construct trusted runtime command: %v", err)
	}
	wantArgs := []string{executablePath, "--eval", "value with spaces"}
	assertRuntimeCommandUsesPrivateImage(t, cmd, executablePath)
	if !slices.Equal(cmd.Args, wantArgs) {
		t.Fatalf("expected argv %q, got %q", wantArgs, cmd.Args)
	}

	cancel()
	if err := cmd.Run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected retained command context cancellation, got %v", err)
	}
	if _, err := os.Stat(cmd.Path); !os.IsNotExist(err) {
		t.Fatalf("expected canceled command stage cleanup, stat err=%v", err)
	}
}

func TestCapturePreservesCancellationAndCleanupFailureIdentities(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixtures are covered here")
	}

	repo := t.TempDir()
	markerPath := filepath.Join(repo, "started.txt")
	t.Setenv("LOPPER_CAPTURE_MARKER", markerPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nsleep 5\nprintf started > \"$LOPPER_CAPTURE_MARKER\"\n"))

	cleanupFailure := errors.New("injected staged cleanup failure")
	cmdBuilder := runtimeCommandBuilder
	runtimeCommandBuilder = func(ctx context.Context, command string, requestedOptions ...CommandOptions) (*runtimeCommand, error) {
		cmd, err := cmdBuilder(ctx, command, requestedOptions...)
		if err != nil {
			return nil, err
		}
		cleanup := cmd.cleanupFn
		cmd.cleanupFn = func() error {
			return errors.Join(cleanup(), cleanupFailure)
		}
		return cmd, nil
	}
	t.Cleanup(func() {
		runtimeCommandBuilder = cmdBuilder
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := Capture(ctx, CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
	})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("expected cancellation and cleanup identities, got %v", err)
	}
}

func TestRuntimeCommandRunsPinnedImageAfterCanonicalTargetSwap(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixture behavior is covered here")
	}

	searchDir := testutil.SecureHomeTempDir(t, "runtime-canonical-swap-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'original\\n'\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node --version")
	if err != nil {
		t.Fatalf("build trusted runtime command: %v", err)
	}
	launchPath := cmd.Path
	if launchPath == executablePath {
		t.Fatalf("expected private launch image, got canonical source path %q", launchPath)
	}

	originalPath := executablePath + ".original"
	if err := os.Rename(executablePath, originalPath); err != nil {
		t.Fatalf("move original canonical executable: %v", err)
	}
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'replacement\\n'\n")

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run pinned runtime image: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "original" {
		t.Fatalf("expected pinned original image output, got %q", got)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("expected private launch image cleanup, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(launchPath)); !os.IsNotExist(err) {
		t.Fatalf("expected private launch directory cleanup, stat err=%v", err)
	}
}

func TestRuntimeCommandStagesResolvedIdentityAfterCanonicalTargetSwap(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixture behavior is covered here")
	}

	searchDir := testutil.SecureHomeTempDir(t, "runtime-resolved-swap-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'resolved-original\\n'\n")
	resolution, err := resolvePinnedRuntimeExecutable("node", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve and pin trusted runtime executable: %v", err)
	}
	if resolution.source == nil {
		t.Fatal("expected resolution to retain its validated executable handle")
	}

	originalPath := executablePath + ".original"
	if err := os.Rename(executablePath, originalPath); err != nil {
		t.Fatalf("move canonical executable after resolution: %v", err)
	}
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'resolved-replacement\\n'\n")

	cmd, err := newTrustedRuntimeCommand(context.Background(), "node", &resolution, nil)
	if err != nil {
		t.Fatalf("construct command from pinned resolution: %v", err)
	}
	if resolution.source != nil {
		t.Fatal("expected command construction to consume the resolved executable handle")
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run command from pinned resolution: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "resolved-original" {
		t.Fatalf("expected originally resolved executable output, got %q", got)
	}
}

func TestRuntimeCommandSealsStagedExecutableAgainstReplacement(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix staging permissions are covered here")
	}

	searchDir := testutil.SecureHomeTempDir(t, "runtime-stage-replacement-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'original\\n'\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build trusted runtime command: %v", err)
	}
	launchPath := cmd.Path
	if launchPath == executablePath {
		t.Fatalf("expected private launch image, got canonical source path %q", launchPath)
	}

	replacementPath := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-stage-poison-"), "node")
	writeRuntimeTestExecutable(t, replacementPath, "#!/bin/sh\nprintf 'replacement\\n'\n")
	if err := os.Rename(replacementPath, launchPath); err == nil {
		t.Fatal("expected sealed launch image replacement to fail")
	}
	launchParent := filepath.Dir(launchPath)
	movedParent := launchParent + ".moved"
	if err := os.Rename(launchParent, movedParent); err == nil {
		if restoreErr := os.Rename(movedParent, launchParent); restoreErr != nil {
			t.Fatalf("staged launch parent was replaceable and could not be restored: %v", restoreErr)
		}
		t.Fatal("expected sealed launch parent replacement to fail")
	}

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run sealed runtime image: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "original" {
		t.Fatalf("expected sealed original image output, got %q", got)
	}
}

func TestPinRuntimeExecutableRejectsReplacedStageIdentity(t *testing.T) {
	stageDir := testutil.SecureHomeTempDir(t, "runtime-stage-pin-")
	executablePath := runtimeToolPathForTest(t, stageDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'original\\n'\n")
	originalInfo, err := os.Lstat(executablePath)
	if err != nil {
		t.Fatalf("lstat original staged executable: %v", err)
	}
	if err := os.Rename(executablePath, executablePath+".original"); err != nil {
		t.Fatalf("move original staged executable: %v", err)
	}
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'replacement\\n'\n")

	root, err := safeio.OpenRootNoFollow(stageDir)
	if err != nil {
		t.Fatalf("open staged executable root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close staged executable root: %v", err)
		}
	})

	pin, err := pinRuntimeExecutable(root, filepath.Base(executablePath), executablePath, stageDir, originalInfo)
	if err == nil {
		if closeErr := pin.Close(); closeErr != nil {
			t.Fatalf("unexpected staged executable pin succeeded and failed to close: %v", closeErr)
		}
		t.Fatal("expected replaced staged executable identity to be rejected")
	}
	if !strings.Contains(err.Error(), "changed before pinning") {
		t.Fatalf("expected staged identity mismatch error, got %v", err)
	}
}

func TestRuntimeCommandCleansStageAfterStartFailure(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixture behavior is covered here")
	}

	searchDir := testutil.SecureHomeTempDir(t, "runtime-start-failure-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nexit 0\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build trusted runtime command: %v", err)
	}
	launchPath := cmd.Path
	stageDir := filepath.Dir(filepath.Dir(launchPath))
	cmd.Path = launchPath + ".missing"

	err = cmd.Start()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing staged executable error, got %v", err)
	}
	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		t.Fatalf("expected stage cleanup after Start failure, stat err=%v", err)
	}
	if err := cmd.finish(nil); err != nil {
		t.Fatalf("expected successful one-shot cleanup after Start failure, got %v", err)
	}
}

func TestRuntimeCommandPreservesInstallationRelativeLayout(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix installation-relative executable behavior is covered here")
	}

	installationRoot := testutil.SecureHomeTempDir(t, "runtime-stage-layout-")
	searchDir := filepath.Join(installationRoot, "bin")
	libDir := filepath.Join(installationRoot, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture library directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "result"), []byte("layout-preserved\n"), 0o500); err != nil {
		t.Fatalf("write fixture library result: %v", err)
	}
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\ncat \"$(dirname \"$0\")/../lib/result\"\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build layout-sensitive runtime command: %v", err)
	}
	if filepath.Base(filepath.Dir(cmd.Path)) != "bin" {
		t.Fatalf("expected staged executable to retain bin layout, got %q", cmd.Path)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run layout-sensitive runtime command: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "layout-preserved" {
		t.Fatalf("expected preserved installation layout output, got %q", got)
	}
}

func TestRuntimeCommandRetainsStageThroughStartAndCleansAfterWait(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixture behavior is covered here")
	}

	fixtureDir := testutil.SecureHomeTempDir(t, "runtime-stage-lifecycle-")
	searchDir := filepath.Join(fixtureDir, "tools")
	readyPath := filepath.Join(fixtureDir, "ready")
	continuePath := filepath.Join(fixtureDir, "continue")
	t.Setenv("LOPPER_STAGE_READY", readyPath)
	t.Setenv("LOPPER_STAGE_CONTINUE", continuePath)
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf ready > \"$LOPPER_STAGE_READY\"\nwhile [ ! -f \"$LOPPER_STAGE_CONTINUE\" ]; do sleep 0.01; done\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build trusted runtime command: %v", err)
	}
	launchPath := cmd.Path
	if err := cmd.Start(); err != nil {
		t.Fatalf("start trusted runtime command: %v", err)
	}
	waitForRuntimeCaptureFile(t, readyPath)
	if _, err := os.Stat(launchPath); err != nil {
		t.Fatalf("expected staged image to remain pinned through Start: %v", err)
	}
	if err := os.WriteFile(continuePath, []byte("continue"), 0o600); err != nil {
		t.Fatalf("release staged runtime command: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for staged runtime command: %v", err)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("expected staged image cleanup after Wait, stat err=%v", err)
	}
}

func TestRuntimeCommandCleanupFailurePreservesErrors(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixtures are covered here")
	}

	t.Run("successful process", func(t *testing.T) {
		cmd, cleanupCalls, cleanupFailure := runtimeCommandWithCleanupFailure(t, "#!/bin/sh\nexit 0\n")
		if err := cmd.Run(); !errors.Is(err, cleanupFailure) {
			t.Fatalf("expected cleanup failure identity, got %v", err)
		}
		if *cleanupCalls != 1 {
			t.Fatalf("expected one cleanup attempt, got %d", *cleanupCalls)
		}
		if err := cmd.finish(nil); !errors.Is(err, cleanupFailure) {
			t.Fatalf("expected retained one-shot cleanup failure, got %v", err)
		}
		if *cleanupCalls != 1 {
			t.Fatalf("expected cleanup to remain one-shot, got %d attempts", *cleanupCalls)
		}
	})

	t.Run("failed process", func(t *testing.T) {
		cmd, _, cleanupFailure := runtimeCommandWithCleanupFailure(t, "#!/bin/sh\nexit 7\n")
		err := cmd.Run()
		if !errors.Is(err, cleanupFailure) {
			t.Fatalf("expected joined cleanup failure identity, got %v", err)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected process exit error identity, got %v", err)
		}
	})
}

func TestRuntimeCommandOutputContractsCleanStages(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix executable fixture behavior is covered here")
	}

	t.Run("combined output", testRuntimeCommandCombinedOutput)

	for _, tc := range runtimeCommandOutputContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertRuntimeCommandOutputContract(t, tc)
		})
	}
}

type runtimeCommandOutputContractCase struct {
	name      string
	configure func(*runtimeCommand)
	run       func(*runtimeCommand) error
	want      string
}

type runtimeNodeCLITestCase struct {
	name       string
	executable string
	command    string
	wantArgs   []string
	setup      func(*testing.T) (string, string)
}

type runtimeNamedTestCase struct {
	name string
	run  func(*testing.T)
}

func runtimeCommandOutputContractCases() []runtimeCommandOutputContractCase {
	return []runtimeCommandOutputContractCase{
		{
			name: "output stdout already set",
			configure: func(cmd *runtimeCommand) {
				cmd.Stdout = &strings.Builder{}
			},
			run: func(cmd *runtimeCommand) error {
				_, err := cmd.Output()
				return err
			},
			want: "exec: Stdout already set",
		},
		{
			name: "combined stdout already set",
			configure: func(cmd *runtimeCommand) {
				cmd.Stdout = &strings.Builder{}
			},
			run: func(cmd *runtimeCommand) error {
				_, err := cmd.CombinedOutput()
				return err
			},
			want: "exec: Stdout already set",
		},
		{
			name: "combined stderr already set",
			configure: func(cmd *runtimeCommand) {
				cmd.Stderr = &strings.Builder{}
			},
			run: func(cmd *runtimeCommand) error {
				_, err := cmd.CombinedOutput()
				return err
			},
			want: "exec: Stderr already set",
		},
	}
}

func runtimeNodeCLITestCases() []runtimeNodeCLITestCase {
	return []runtimeNodeCLITestCase{
		{
			name:       "Homebrew npm",
			executable: "npm",
			command:    `npm test -- --grep "integration suite"`,
			wantArgs:   []string{"test", "--", "--grep", "integration suite"},
			setup: func(t *testing.T) (string, string) {
				return createHomebrewStyleNodeToolFixture(t, "npm", "npm-cli.js")
			},
		},
		{
			name:       "NVM npx",
			executable: "npx",
			command:    `npx vitest --run "suite name"`,
			wantArgs:   []string{"vitest", "--run", "suite name"},
			setup: func(t *testing.T) (string, string) {
				return createNVMCurrentToolFixture(t, "npx", "npx-cli.js")
			},
		},
	}
}

func runtimeNodeCLIPoisonTestCases() []runtimeNodeCLITestCase {
	return []runtimeNodeCLITestCase{
		{
			name:       "Homebrew npm",
			executable: "npm",
			command:    `npm test -- --grep "homebrew suite"`,
			wantArgs:   []string{"test", "--", "--grep", "homebrew suite"},
			setup: func(t *testing.T) (string, string) {
				return createHomebrewStyleNodeToolFixture(t, "npm", "npm-cli.js")
			},
		},
		{
			name:       "NVM npx",
			executable: "npx",
			command:    `npx vitest --run "nvm suite"`,
			wantArgs:   []string{"vitest", "--run", "nvm suite"},
			setup: func(t *testing.T) (string, string) {
				return createNVMCurrentToolFixture(t, "npx", "npx-cli.js")
			},
		},
	}
}

func malformedNodeCLIInstallationCases() []runtimeNamedTestCase {
	return []runtimeNamedTestCase{
		{name: "missing launcher identity", run: testMissingLauncherIdentityRejected},
		{name: "same node search directory without node", run: testSameNodeSearchDirectoryWithoutNodeRejected},
		{name: "non CLI canonical target", run: testNonCLICanonicalTargetRejected},
		{name: "missing Homebrew launcher directory", run: testMissingHomebrewLauncherDirectoryRejected},
		{name: "regular Homebrew launcher", run: testRegularHomebrewLauncherRejected},
		{name: "non Homebrew installation layout", run: testNonHomebrewInstallationLayoutRejected},
		{name: "Homebrew intermediate resolves elsewhere", run: testHomebrewIntermediateMismatchRejected},
		{name: "Homebrew launcher outside prefix bin", run: testHomebrewLauncherOutsidePrefixBinRejected},
		{name: "NVM launcher outside installation bin", run: testNVMLauncherOutsideInstallationBinRejected},
		{name: "NVM installation root is writable", run: testWritableNVMInstallationRootRejected},
		{name: "invalid node executable layout", run: testInvalidNodeExecutableLayoutRejected},
		{name: "missing canonical installation root", run: testMissingCanonicalInstallationRootRejected},
	}
}

func testRuntimeCommandCombinedOutput(t *testing.T) {
	searchDir := testutil.SecureHomeTempDir(t, "runtime-combined-output-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf stdout\nprintf stderr >&2\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build combined-output command: %v", err)
	}
	launchPath := cmd.Path
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run combined-output command: %v", err)
	}
	if got := string(output); got != "stdoutstderr" {
		t.Fatalf("expected combined stdout and stderr, got %q", got)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("expected combined-output stage cleanup, stat err=%v", err)
	}
}

func assertRuntimeCommandOutputContract(t *testing.T, tc runtimeCommandOutputContractCase) {
	searchDir := testutil.SecureHomeTempDir(t, "runtime-output-contract-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nexit 0\n")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build output-contract command: %v", err)
	}
	launchPath := cmd.Path
	tc.configure(cmd)
	if err := tc.run(cmd); err == nil || !strings.Contains(err.Error(), tc.want) {
		t.Fatalf("expected %q error, got %v", tc.want, err)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("expected output-contract stage cleanup, stat err=%v", err)
	}
}

func exerciseTrustedNodeForCanonicalCLIScript(t *testing.T, tc runtimeNodeCLITestCase) {
	searchDir, cliPath := tc.setup(t)
	assertCanonicalCLIScript(t, cliPath)
	nodePath := assertRuntimeNodeCLIResolution(t, tc.executable, searchDir, cliPath)
	poisonNodePath, poisonMarker := configureAmbientPoisonNode(t, searchDir)

	ambientNodePath, err := resolveRuntimeExecutablePath("node", runtimeSearchDirs())
	if err != nil {
		t.Fatalf("resolve poisoned ambient node fixture: %v", err)
	}
	if ambientNodePath != poisonNodePath {
		t.Fatalf("expected vetted poisoned node %q to win ambient search, got %q", poisonNodePath, ambientNodePath)
	}

	cmd, err := buildRuntimeCommand(context.Background(), tc.command)
	if err != nil {
		t.Fatalf("build canonical CLI command: %v", err)
	}
	assertNodeCLICommandArgs(t, cmd, nodePath, cliPath, tc.wantArgs)
	swapTrustedNodeExecutable(t, nodePath, "#!/bin/sh\nprintf 'replacement\\n'\nprintf 'poisoned\\n' > \"$LOPPER_POISON_NODE_MARKER\"\n")
	assertTrustedNodeCommandOutput(t, cmd, poisonMarker, "expected poisoned ambient node not to run")
}

func exerciseCrossInstallationLauncherAndNodePoisonRejection(t *testing.T, tc runtimeNodeCLITestCase) {
	launcherRoot, cliPath := tc.setup(t)
	trustedNodePath, err := resolveRuntimeExecutablePath("node", []string{launcherRoot})
	if err != nil {
		t.Fatalf("resolve trusted installation node: %v", err)
	}
	poisonMarker := configureCrossInstallationPoisonNode(t, tc.executable, launcherRoot, cliPath)

	cmd, err := buildRuntimeCommand(context.Background(), tc.command)
	if err != nil {
		t.Fatalf("build command after rejecting poison launcher: %v", err)
	}
	assertNodeCLICommandArgs(t, cmd, trustedNodePath, cliPath, tc.wantArgs)
	assertTrustedNodeCommandOutput(t, cmd, poisonMarker, "expected cross-install poison node not to run")
}

func assertCanonicalCLIScript(t *testing.T, cliPath string) {
	cliContent, err := os.ReadFile(cliPath)
	if err != nil {
		t.Fatalf("read canonical CLI script: %v", err)
	}
	if !strings.HasPrefix(string(cliContent), "#!/usr/bin/env node\n") {
		t.Fatalf("expected real env-node shebang, got %q", cliContent)
	}
}

func assertRuntimeNodeCLIResolution(t *testing.T, executable, searchDir, cliPath string) string {
	nodePath, err := resolveRuntimeExecutablePath("node", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve trusted node fixture: %v", err)
	}
	launcherResolution, err := resolveRuntimeExecutable(executable, []string{searchDir})
	if err != nil {
		t.Fatalf("resolve launcher fixture: %v", err)
	}
	if launcherResolution.path != cliPath || launcherResolution.selectedLauncherRoot != searchDir {
		t.Fatalf("unexpected launcher resolution: %#v", launcherResolution)
	}
	wantInstallationRoot := filepath.Dir(filepath.Dir(nodePath))
	if launcherResolution.canonicalInstallationRoot != wantInstallationRoot {
		t.Fatalf("expected launcher installation %q, got %#v", wantInstallationRoot, launcherResolution)
	}
	return nodePath
}

func configureAmbientPoisonNode(t *testing.T, searchDir string) (string, string) {
	poisonDir := testutil.SecureHomeTempDir(t, "runtime-vetted-poison-")
	poisonMarker := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-poison-marker-"), "poisoned-node-ran")
	poisonNodePath := filepath.Join(poisonDir, "node")
	writeRuntimeTestExecutable(t, poisonNodePath, "#!/bin/sh\nprintf 'poisoned\\n' > \"$LOPPER_POISON_NODE_MARKER\"\n")
	t.Setenv("LOPPER_POISON_NODE_MARKER", poisonMarker)
	unsetRuntimeBinDirsTest(t)
	t.Setenv("PATH", strings.Join([]string{poisonDir, searchDir}, string(os.PathListSeparator)))
	return poisonNodePath, poisonMarker
}

func configureCrossInstallationPoisonNode(t *testing.T, executable, launcherRoot, cliPath string) string {
	poisonInstallationRoot := testutil.SecureHomeTempDir(t, "runtime-cross-install-poison-")
	poisonBin := filepath.Join(poisonInstallationRoot, "bin")
	if err := os.MkdirAll(poisonBin, 0o755); err != nil {
		t.Fatalf("mkdir poison bin: %v", err)
	}
	if err := os.Symlink(cliPath, filepath.Join(poisonBin, executable)); err != nil {
		t.Fatalf("symlink poison launcher directly to canonical CLI: %v", err)
	}
	poisonMarker := filepath.Join(poisonInstallationRoot, "poison-node-ran")
	writeRuntimeTestExecutable(t, filepath.Join(poisonBin, "node"), "#!/bin/sh\nprintf 'poisoned\\n' > \"$LOPPER_POISON_NODE_MARKER\"\n")
	unsetRuntimeBinDirsTest(t)
	t.Setenv("LOPPER_POISON_NODE_MARKER", poisonMarker)
	t.Setenv("PATH", strings.Join([]string{poisonBin, launcherRoot}, string(os.PathListSeparator)))
	return poisonMarker
}

func assertNodeCLICommandArgs(t *testing.T, cmd *runtimeCommand, nodePath, cliPath string, wantArgs []string) {
	assertRuntimeCommandUsesPrivateImage(t, cmd, nodePath)
	if len(cmd.Args) != len(wantArgs)+2 {
		t.Fatalf("expected node argv plus staged CLI and args, got %q", cmd.Args)
	}
	if cmd.Args[1] == cliPath {
		t.Fatalf("expected staged CLI path instead of canonical path %q", cliPath)
	}
	if filepath.Base(cmd.Args[1]) != filepath.Base(cliPath) {
		t.Fatalf("expected staged CLI basename %q, got %q", filepath.Base(cliPath), cmd.Args[1])
	}
	if !slices.Equal(cmd.Args[2:], wantArgs) || cmd.Args[0] != nodePath {
		t.Fatalf("expected command args prefix %q and tail %q, got %q", nodePath, wantArgs, cmd.Args)
	}
}

func swapTrustedNodeExecutable(t *testing.T, nodePath string, script string) {
	originalNodePath := nodePath + ".original"
	if err := os.Rename(nodePath, originalNodePath); err != nil {
		t.Fatalf("move canonical node after command construction: %v", err)
	}
	writeRuntimeTestExecutable(t, nodePath, script)
}

func assertTrustedNodeCommandOutput(t *testing.T, cmd *runtimeCommand, poisonMarker string, markerMessage string) {
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run trusted node command: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "trusted-node" {
		t.Fatalf("expected trusted node output, got %q", got)
	}
	if _, err := os.Stat(poisonMarker); !os.IsNotExist(err) {
		t.Fatalf("%s, stat err=%v", markerMessage, err)
	}
}

func testMissingLauncherIdentityRejected(t *testing.T) {
	cliPath := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-cli-no-identity-"), "npm-cli.js")
	resolution := resolvedRuntimeExecutable{path: cliPath}
	cmd, err := newTrustedRuntimeCommand(context.Background(), "npm", &resolution, []string{"test"})
	if err == nil || !strings.Contains(err.Error(), "installation identity is unavailable") {
		t.Fatalf("expected missing installation identity rejection, cmd=%v err=%v", cmd, err)
	}
}

func testSameNodeSearchDirectoryWithoutNodeRejected(t *testing.T) {
	installationRoot := testutil.SecureHomeTempDir(t, "runtime-cli-no-node-")
	binDir := filepath.Join(installationRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir installation bin: %v", err)
	}
	_, err := resolveTrustedRuntimeNodeForCLI(resolvedRuntimeExecutable{
		selectedLauncherRoot:      binDir,
		canonicalInstallationRoot: installationRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "trusted node interpreter not found") {
		t.Fatalf("expected same-install node lookup rejection, got %v", err)
	}
}

func testNonCLICanonicalTargetRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-non-cli-target-")
	if _, ok := runtimeNodeCLIInstallationRoot("npm", filepath.Join(root, "bin", "npm"), filepath.Join(root, "bin", "npm")); ok {
		t.Fatal("expected non-CLI canonical target to have no installation identity")
	}
}

func testMissingHomebrewLauncherDirectoryRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-homebrew-missing-launcher-")
	if _, ok := runtimeHomebrewNodeCLIInstallationRoot("npm", filepath.Join(root, "missing", "npm"), filepath.Join(root, "lib", "node_modules", "npm", "bin", "npm-cli.js")); ok {
		t.Fatal("expected missing Homebrew launcher directory to be rejected")
	}
}

func testRegularHomebrewLauncherRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-homebrew-regular-launcher-")
	launcherPath := filepath.Join(root, "bin", "npm")
	writeRuntimeTestExecutable(t, launcherPath, "#!/bin/sh\nexit 0\n")
	if _, ok := runtimeHomebrewNodeCLIInstallationRoot("npm", launcherPath, filepath.Join(root, "lib", "node_modules", "npm", "bin", "npm-cli.js")); ok {
		t.Fatal("expected non-symlink Homebrew launcher to be rejected")
	}
}

func testNonHomebrewInstallationLayoutRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-non-homebrew-layout-")
	launcherDir := filepath.Join(root, "bin")
	installationBin := filepath.Join(root, "versions", "node", "v24.0.0", "bin")
	mustMkdirAllRuntimeTestDirs(t, launcherDir, installationBin)
	launcherPath := filepath.Join(launcherDir, "npm")
	if err := os.Symlink(filepath.Join(installationBin, "npm"), launcherPath); err != nil {
		t.Fatalf("symlink malformed Homebrew launcher: %v", err)
	}
	if _, ok := runtimeHomebrewNodeCLIInstallationRoot("npm", launcherPath, filepath.Join(root, "lib", "node_modules", "npm", "bin", "npm-cli.js")); ok {
		t.Fatal("expected non-Homebrew installation layout to be rejected")
	}
}

func testHomebrewIntermediateMismatchRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-homebrew-intermediate-mismatch-")
	prefix := filepath.Join(root, "opt", "homebrew")
	launcherDir := filepath.Join(prefix, "bin")
	installationRoot := filepath.Join(prefix, "Cellar", "node", "24.0.0")
	installationBin := filepath.Join(installationRoot, "bin")
	canonicalCLI := filepath.Join(prefix, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	alternateCLI := filepath.Join(prefix, "alternate", "npm-cli.js")
	mustMkdirAllRuntimeTestDirs(t, launcherDir, installationBin)
	writeRuntimeTestExecutable(t, canonicalCLI, "#!/usr/bin/env node\n")
	writeRuntimeTestExecutable(t, alternateCLI, "#!/usr/bin/env node\n")
	intermediate := filepath.Join(installationBin, "npm")
	if err := os.Symlink(alternateCLI, intermediate); err != nil {
		t.Fatalf("symlink mismatched Homebrew intermediate: %v", err)
	}
	launcherPath := filepath.Join(launcherDir, "npm")
	if err := os.Symlink(intermediate, launcherPath); err != nil {
		t.Fatalf("symlink Homebrew launcher: %v", err)
	}
	if _, ok := runtimeHomebrewNodeCLIInstallationRoot("npm", launcherPath, canonicalCLI); ok {
		t.Fatal("expected mismatched Homebrew intermediate target to be rejected")
	}
}

func testHomebrewLauncherOutsidePrefixBinRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-homebrew-wrong-launcher-root-")
	prefix := filepath.Join(root, "opt", "homebrew")
	installationRoot := filepath.Join(prefix, "Cellar", "node", "24.0.0")
	cliPath := filepath.Join(prefix, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	if runtimeHomebrewInstallationLayoutMatches(filepath.Join(prefix, "sbin"), installationRoot, cliPath) {
		t.Fatal("expected Homebrew launcher outside prefix bin to be rejected")
	}
}

func testNVMLauncherOutsideInstallationBinRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-nvm-wrong-launcher-root-")
	launcherDir := filepath.Join(root, "tools")
	if err := os.MkdirAll(launcherDir, 0o755); err != nil {
		t.Fatalf("mkdir NVM launcher dir: %v", err)
	}
	if _, ok := runtimeNVMNodeCLIInstallationRoot(filepath.Join(launcherDir, "npx"), filepath.Join(root, "lib", "node_modules", "npm", "bin", "npx-cli.js")); ok {
		t.Fatal("expected NVM launcher outside installation bin to be rejected")
	}
}

func testWritableNVMInstallationRootRejected(t *testing.T) {
	installationRoot := testutil.SecureHomeTempDir(t, "runtime-nvm-writable-installation-")
	binDir := filepath.Join(installationRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir NVM installation bin: %v", err)
	}
	if err := os.Chmod(installationRoot, 0o777); err != nil {
		t.Fatalf("chmod NVM installation root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(installationRoot, 0o700); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore NVM installation permissions: %v", err)
		}
	})
	if _, ok := runtimeNVMNodeCLIInstallationRoot(filepath.Join(binDir, "npx"), filepath.Join(installationRoot, "lib", "node_modules", "npm", "bin", "npx-cli.js")); ok {
		t.Fatal("expected writable NVM installation root to be rejected")
	}
}

func testInvalidNodeExecutableLayoutRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-invalid-node-layout-")
	if _, ok := runtimeNodeExecutableInstallationRoot(filepath.Join(root, "sbin", "node")); ok {
		t.Fatal("expected Node outside installation bin to be rejected")
	}
}

func testMissingCanonicalInstallationRootRejected(t *testing.T) {
	root := testutil.SecureHomeTempDir(t, "runtime-missing-installation-root-")
	if _, ok := canonicalRuntimeInstallationRoot(filepath.Join(root, "missing")); ok {
		t.Fatal("expected missing canonical installation root to be rejected")
	}
}

func mustMkdirAllRuntimeTestDirs(t *testing.T, dirs ...string) {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir runtime fixture dir %q: %v", dir, err)
		}
	}
}

func TestRuntimeCommandWindowsStageSurvivesPoisonedPathextAndSourceDisappearance(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := testutil.SecureHomeTempDir(t, "runtime-windows-stage-")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	t.Setenv(runtimeBinDirsEnvKey, searchDir)
	t.Setenv("PATHEXT", ".EVIL")

	executablePath := filepath.Join(searchDir, "node.exe")
	writeRuntimeTestExecutable(t, executablePath, "#!/bin/sh\nprintf 'original\\n'\n")
	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build trusted Windows runtime command: %v", err)
	}
	launchPath := cmd.Path
	if strings.EqualFold(launchPath, executablePath) {
		t.Fatalf("expected private Windows launch image, got canonical source path %q", launchPath)
	}
	if !strings.EqualFold(filepath.Ext(launchPath), ".exe") {
		t.Fatalf("expected exact trusted executable extension, got %q", launchPath)
	}
	if err := os.Remove(launchPath); err == nil {
		t.Fatal("expected pinned staged executable removal to fail")
	}

	if err := os.Remove(executablePath); err != nil {
		t.Fatalf("remove canonical executable after construction: %v", err)
	}
	poisonMarker := filepath.Join(searchDir, "poison-ran")
	t.Setenv("LOPPER_POISON_MARKER", poisonMarker)
	writeRuntimeTestExecutable(t, executablePath+".evil", "#!/bin/sh\nprintf poison > \"$LOPPER_POISON_MARKER\"\nprintf 'replacement\\n'\n")

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run pinned Windows-style runtime image: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "original" {
		t.Fatalf("expected pinned original image output, got %q", got)
	}
	if _, err := os.Stat(poisonMarker); !os.IsNotExist(err) {
		t.Fatalf("expected poisoned PATHEXT sibling not to run, stat err=%v", err)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("expected pinned Windows-style stage cleanup, stat err=%v", err)
	}
}

func TestTrustedRuntimeExecutableRejectsEmptyCapability(t *testing.T) {
	cmd, err := newTrustedRuntimeExecCommand(context.Background(), nil, []string{"test"})
	if err == nil {
		t.Fatalf("expected empty trusted capability rejection, got command %#v", cmd)
	}
	if !strings.Contains(err.Error(), "trusted runtime executable is unavailable") {
		t.Fatalf("expected unavailable capability error, got %v", err)
	}
}

func TestSameRuntimeExecutablePathUsesWindowsCaseFolding(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	if !sameRuntimeExecutablePath(`C:\Program Files\nodejs\node.exe`, `c:\program files\NODEJS\NODE.EXE`) {
		t.Fatal("expected Windows runtime paths to compare case-insensitively")
	}
}

func TestResolveRuntimeExecutablePathCanonicalizesVersionedPython3Target(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix symlink canonicalization is covered here")
	}

	trustedDir, canonicalPath := createHomebrewStylePythonFixture(t)
	assertRuntimeExecutableCanonicalPath(t, "python3", trustedDir, canonicalPath)
}

func TestResolveRuntimeExecutablePathCanonicalizesDirectRegularExecutableUnderAliasSearchRoot(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix symlink canonicalization is covered here")
	}

	currentDir, canonicalPath := createNVMCurrentToolFixture(t, "node", "node")
	assertRuntimeExecutableCanonicalPath(t, "node", currentDir, canonicalPath)
}

func assertRuntimeExecutableCanonicalPath(t *testing.T, executable, searchDir, canonicalPath string) {
	t.Helper()
	got, err := resolveRuntimeExecutablePath(executable, []string{searchDir})
	if err != nil {
		t.Fatalf("resolve runtime executable path: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(canonicalPath)
	if err != nil {
		t.Fatalf("eval canonical runtime executable path: %v", err)
	}
	if got != wantPath {
		t.Fatalf("expected canonical executable path %q, got %q", wantPath, got)
	}
}

func TestResolveRuntimeExecutablePathRejectsSymlinkCandidateResolvingToWritableCanonicalTarget(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix permission trust checks are covered here")
	}

	trustedDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-trusted-"), "trusted")
	canonicalDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-canonical-"), "canonical")
	if err := os.MkdirAll(trustedDir, 0o755); err != nil {
		t.Fatalf("mkdir trusted dir: %v", err)
	}
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical dir: %v", err)
	}
	canonicalPath := filepath.Join(canonicalDir, "python3")
	if err := os.WriteFile(canonicalPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write writable canonical tool: %v", err)
	}
	if err := os.Chmod(canonicalPath, 0o777); err != nil {
		t.Fatalf("chmod writable canonical tool: %v", err)
	}
	symlinkPath := filepath.Join(trustedDir, "python3")
	if err := os.Symlink(canonicalPath, symlinkPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := resolveRuntimeExecutablePath("python3", []string{trustedDir})
	if err == nil || !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("expected writable canonical target to be rejected, got %v", err)
	}
}

func TestResolveRuntimeExecutablePathRejectsSymlinkCandidateResolvingThroughWritableCanonicalAncestor(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix permission trust checks are covered here")
	}

	trustedDir, canonicalPath := createHomebrewStyleNodeToolFixture(t, "npm", "npm-cli.js")
	canonicalParent := filepath.Dir(filepath.Dir(filepath.Dir(canonicalPath)))
	if err := os.Chmod(canonicalParent, 0o777); err != nil {
		t.Fatalf("chmod writable canonical ancestor: %v", err)
	}

	_, err := resolveRuntimeExecutablePath("npm", []string{trustedDir})
	if err == nil || !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("expected writable canonical parent chain to be rejected, got %v", err)
	}
}

func TestResolveRuntimeExecutablePathRejectsAliasSearchRootWithWritableAncestor(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix permission trust checks are covered here")
	}

	currentDir, _ := createNVMCurrentToolFixture(t, "node", "node")
	aliasAncestor := filepath.Dir(filepath.Dir(currentDir))
	if err := os.Chmod(aliasAncestor, 0o777); err != nil {
		t.Fatalf("chmod writable alias ancestor: %v", err)
	}

	_, err := resolveRuntimeExecutablePath("node", []string{currentDir})
	if err == nil || !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("expected writable alias ancestor to be rejected, got %v", err)
	}
}

func TestTrustedSearchDirsKeepsSymlinkSearchRootAliases(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix symlink canonicalization is covered here")
	}

	currentDir, _ := createNVMCurrentToolFixture(t, "node", "node")

	got := trustedSearchDirs(currentDir)
	want := []string{currentDir}
	if !slices.Equal(got, want) {
		t.Fatalf("expected alias search dir %v, got %v", want, got)
	}
}

func TestBuildRuntimeCommandExecutesCanonicalPathAfterAliasSwap(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix alias-swap execution proof is covered here")
	}

	currentDir, canonicalPath := createNVMCurrentToolFixture(t, "node", "node")
	t.Setenv(runtimeBinDirsEnvKey, currentDir)
	replacementVersionDir := filepath.Join(t.TempDir(), "versions", "node", "v25.0.0", "bin")
	replacementPath := filepath.Join(replacementVersionDir, "node")
	if err := os.MkdirAll(replacementVersionDir, 0o755); err != nil {
		t.Fatalf("mkdir replacement version dir: %v", err)
	}
	writeRuntimeTestExecutable(t, replacementPath, "#!/bin/sh\nprintf 'replacement\\n'\n")

	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build runtime command: %v", err)
	}
	assertRuntimeCommandUsesPrivateImage(t, cmd, canonicalPath)

	currentLink := filepath.Dir(currentDir)
	if err := os.Remove(currentLink); err != nil {
		t.Fatalf("remove current symlink: %v", err)
	}
	if err := os.Symlink(filepath.Dir(replacementVersionDir), currentLink); err != nil {
		t.Fatalf("swap current symlink: %v", err)
	}

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run canonical command after alias swap: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "node" {
		t.Fatalf("expected canonical executable output, got %q", got)
	}
}

func TestRuntimeSearchDirsPrefersTrustedPATHSelection(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix PATH trust checks are covered here")
	}

	selectedDir := testutil.SecureHomeTempDir(t, "runtime-path-selected-")
	fallbackDir := testutil.SecureHomeTempDir(t, "runtime-path-fallback-")
	t.Setenv(runtimeBinDirsEnvKey, "")
	t.Setenv("PATH", strings.Join([]string{selectedDir, fallbackDir}, string(os.PathListSeparator)))

	got := runtimeSearchDirs()
	if len(got) < 2 || got[0] != selectedDir || got[1] != fallbackDir {
		t.Fatalf("expected PATH order to lead runtime search dirs, got %v", got)
	}
}

func TestBuildRuntimeCommandUsesPATHSelectedPython(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix PATH-selected executable behavior is covered here")
	}

	selectedDir := testutil.SecureHomeTempDir(t, "runtime-path-python-")
	selectedPython := filepath.Join(selectedDir, "python3")
	if err := os.WriteFile(selectedPython, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write PATH-selected python: %v", err)
	}
	t.Setenv(runtimeBinDirsEnvKey, "")
	t.Setenv("PATH", selectedDir+string(os.PathListSeparator)+"/usr/bin:/bin")

	cmd, err := buildRuntimeCommand(context.Background(), "python3 -m pytest")
	if err != nil {
		t.Fatalf("build PATH-selected Python command: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(selectedPython)
	if err != nil {
		t.Fatalf("eval PATH-selected python: %v", err)
	}
	assertRuntimeCommandUsesPrivateImage(t, cmd, wantPath)
	registerRuntimeCommandCleanup(t, cmd)
}

func TestRuntimeExecutableCanonicalTargetAllowedRestrictsPython3Suffixes(t *testing.T) {
	tests := []struct {
		name          string
		canonicalBase string
		want          bool
	}{
		{name: "bare python3", canonicalBase: "python3", want: true},
		{name: "numeric suffix", canonicalBase: "python3.12", want: true},
		{name: "multiple numeric suffixes", canonicalBase: "python3.12.4", want: true},
		{name: "config helper", canonicalBase: "python3-config"},
		{name: "config suffix", canonicalBase: "python3.12-config"},
		{name: "backup suffix", canonicalBase: "python3.12.bak"},
		{name: "text suffix", canonicalBase: "python3.alpha"},
		{name: "empty suffix segment", canonicalBase: "python3.12."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeExecutableCanonicalTargetAllowed("python3", tc.canonicalBase); got != tc.want {
				t.Fatalf("expected python3 canonical target %q to be %t, got %t", tc.canonicalBase, tc.want, got)
			}
		})
	}
}

func TestRuntimeExecutableCandidatesWindowsUsesFixedExtensions(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	t.Setenv("PATHEXT", ".PS1;.EVIL")
	dir := t.TempDir()
	got := runtimeExecutableCandidates("npm", dir)
	want := []string{
		filepath.Join(dir, "npm") + ".com",
		filepath.Join(dir, "npm") + ".exe",
		filepath.Join(dir, "npm") + ".bat",
		filepath.Join(dir, "npm") + ".cmd",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("expected candidates %v, got %v", want, got)
	}
}

func TestRuntimeExecutableCandidatesWindowsRejectsUntrustedExplicitExtension(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	t.Setenv("PATHEXT", ".PS1")

	if got := runtimeExecutableCandidates("npm.ps1", t.TempDir()); len(got) != 0 {
		t.Fatalf("expected untrusted explicit extension to produce no candidates, got %v", got)
	}
}

func TestRuntimeWindowsExecutableBasenameIgnoresPoisonedPathext(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	t.Setenv("PATHEXT", ".PS1;.EVIL")

	testCases := []struct {
		name          string
		executable    string
		canonicalBase string
		want          bool
	}{
		{name: "exe", executable: "npm", canonicalBase: "npm.exe", want: true},
		{name: "cmd case insensitive", executable: "npm", canonicalBase: "NPM.CMD", want: true},
		{name: "bare extensionless", executable: "npm", canonicalBase: "npm"},
		{name: "poisoned extension", executable: "npm", canonicalBase: "npm.ps1"},
		{name: "unknown extension", executable: "npm", canonicalBase: "npm.evil"},
		{name: "trusted explicit extension", executable: "npm.cmd", canonicalBase: "NPM.CMD", want: true},
		{name: "untrusted explicit extension", executable: "npm.ps1", canonicalBase: "npm.ps1"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeExecutableCanonicalTargetAllowed(tc.executable, tc.canonicalBase); got != tc.want {
				t.Fatalf("expected basename trust for %q -> %q to be %t, got %t", tc.executable, tc.canonicalBase, tc.want, got)
			}
		})
	}
}

func TestRuntimeExecutableCandidatesWindowsKeepsExplicitExtension(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	dir := t.TempDir()
	got := runtimeExecutableCandidates("npm.cmd", dir)
	want := []string{filepath.Join(dir, "npm.cmd")}
	if !slices.Equal(got, want) {
		t.Fatalf("expected explicit-extension candidates %v, got %v", want, got)
	}
}

func TestIsTrustedRuntimeExecutableOnWindowsIgnoresModeBits(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	path := filepath.Join(t.TempDir(), "npm.cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\n"), 0o600); err != nil {
		t.Fatalf("write tool script: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat tool script: %v", err)
	}
	if !isTrustedRuntimeExecutable(info) {
		t.Fatalf("expected windows executable trust check to accept regular files")
	}
}

func TestIsTrustedRuntimeExecutableRejectsNonRegularFile(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if isTrustedRuntimeExecutable(info) {
		t.Fatal("expected non-regular executable candidate to be rejected")
	}
}

func TestWindowsRuntimeTrustUsesOSRootsAndIgnoresPoisonedEnvironment(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	validProgramFilesNodejs := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	validSystem32 := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-system-root-"), "System32")
	for _, root := range []string{validProgramFilesNodejs, validSystem32} {
		if err := os.MkdirAll(root, 0o777); err != nil {
			t.Fatalf("mkdir valid Windows executable root %q: %v", root, err)
		}
	}
	setRuntimeWindowsExecutableRootsTest(t, validProgramFilesNodejs, validSystem32)

	poisonedProgramFiles := testutil.SecureHomeTempDir(t, "runtime-poisoned-program-files-")
	poisonedProgramFilesX86 := testutil.SecureHomeTempDir(t, "runtime-poisoned-program-files-x86-")
	poisonedSystemRoot := testutil.SecureHomeTempDir(t, "runtime-poisoned-system-root-")
	poisonedWindowsDir := testutil.SecureHomeTempDir(t, "runtime-poisoned-windir-")
	poisonedRoots := []string{
		filepath.Join(poisonedProgramFiles, "nodejs"),
		filepath.Join(poisonedProgramFilesX86, "nodejs"),
		filepath.Join(poisonedSystemRoot, "System32"),
		filepath.Join(poisonedWindowsDir, "System32"),
	}
	for _, root := range poisonedRoots {
		if err := os.MkdirAll(root, 0o777); err != nil {
			t.Fatalf("mkdir poisoned Windows executable root %q: %v", root, err)
		}
	}
	t.Setenv("ProgramFiles", poisonedProgramFiles)
	t.Setenv("ProgramFiles(x86)", poisonedProgramFilesX86)
	t.Setenv("SystemRoot", poisonedSystemRoot)
	t.Setenv("windir", poisonedWindowsDir)

	wantDefaults := []string{validProgramFilesNodejs, validSystem32}
	if got := defaultTrustedRuntimeBinDirEntries(); !slices.Equal(got, wantDefaults) {
		t.Fatalf("expected only OS-derived Windows roots %v, got %v", wantDefaults, got)
	}
	for _, poisonedRoot := range poisonedRoots {
		if got := trustedSearchDirs(poisonedRoot); len(got) != 0 {
			t.Fatalf("expected poisoned environment root %q to be rejected, got %v", poisonedRoot, got)
		}
	}
}

func TestTrustedSearchDirsOnWindowsRejectsUnvettedAmbientPathRoot(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	setRuntimeWindowsExecutableRootsTest(t)

	trustedDir := testutil.SecureHomeTempDir(t, "runtime-trusted-")
	if err := os.Chmod(trustedDir, 0o777); err != nil {
		t.Fatalf("chmod trusted dir: %v", err)
	}

	got := trustedSearchDirs(trustedDir)
	want := []string(nil)
	if !slices.Equal(got, want) {
		t.Fatalf("expected trusted dirs %v, got %v", want, got)
	}
}

func TestTrustedSearchDirsOnWindowsAcceptsExactVettedExecutableRoots(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	programFilesNodejs := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	system32 := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-system-root-"), "System32")
	want := []string{programFilesNodejs, system32}
	for _, root := range want {
		if err := os.MkdirAll(root, 0o777); err != nil {
			t.Fatalf("mkdir vetted Windows executable root %q: %v", root, err)
		}
	}
	setRuntimeWindowsExecutableRootsTest(t, want...)

	got := trustedSearchDirs(strings.Join(want, string(os.PathListSeparator)))
	if !slices.Equal(got, want) {
		t.Fatalf("expected trusted dirs %v, got %v", want, got)
	}
}

func TestTrustedSearchDirsOnWindowsAcceptsVettedRootDescendants(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	programFilesNodejs := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	descendant := filepath.Join(programFilesNodejs, "tools", "bin")
	if err := os.MkdirAll(descendant, 0o777); err != nil {
		t.Fatalf("mkdir vetted Windows executable root descendant: %v", err)
	}
	setRuntimeWindowsExecutableRootsTest(t, programFilesNodejs)

	if got := trustedSearchDirs(descendant); !slices.Equal(got, []string{descendant}) {
		t.Fatalf("expected trusted descendant %q to be accepted, got %v", descendant, got)
	}
}

func TestTrustedSearchDirsOnWindowsAcceptsConfiguredDirsOutsideOSRoots(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	setRuntimeWindowsExecutableRootsTest(t)

	configured := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-venv-"), "Scripts")
	if err := os.MkdirAll(configured, 0o777); err != nil {
		t.Fatalf("mkdir configured runtime dir: %v", err)
	}
	t.Setenv(runtimeBinDirsEnvKey, configured)

	if got := runtimeSearchDirs(); !slices.Equal(got, []string{configured}) {
		t.Fatalf("expected configured trusted dirs %q, got %v", configured, got)
	}
}

func TestTrustedSearchDirsOnWindowsRejectsTrustedRootAncestors(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	programFilesNodejs := filepath.Join("C:", "Program Files", "nodejs")
	configured := filepath.Join("D:", "venv", "Scripts")
	setRuntimeWindowsExecutableRootsTest(t, programFilesNodejs)
	t.Setenv(runtimeBinDirsEnvKey, configured)

	tests := []string{
		"C:",
		filepath.Join("C:", "Program Files"),
		filepath.Dir(configured),
	}
	for _, candidate := range tests {
		if got := trustedRuntimeWindowsTrustedRoot(candidate); got != "" {
			t.Fatalf("expected trusted root ancestor %q to be rejected, got %q", candidate, got)
		}
		if got := trustedSearchDirs(candidate); len(got) != 0 {
			t.Fatalf("expected trusted root ancestor %q to be rejected by search dirs, got %v", candidate, got)
		}
	}
}

func TestBuildRuntimeCommandUsesConfiguredTrustedWindowsPythonDir(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	setRuntimeWindowsExecutableRootsTest(t)

	configured := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-python-venv-"), "Scripts")
	if err := os.MkdirAll(configured, 0o777); err != nil {
		t.Fatalf("mkdir configured Python dir: %v", err)
	}
	writeRuntimeTestExecutable(t, filepath.Join(configured, "python.exe"), "@echo off\r\n")
	t.Setenv(runtimeBinDirsEnvKey, configured)

	cmd, err := buildRuntimeCommand(context.Background(), "python -m pytest")
	if err != nil {
		t.Fatalf("build configured trusted Windows Python command: %v", err)
	}
	if !strings.EqualFold(filepath.Base(cmd.Path), "python.exe") {
		t.Fatalf("expected staged Python executable, got %q", cmd.Path)
	}
	registerRuntimeCommandCleanup(t, cmd)
}

func TestResolveRuntimeExecutablePathMediatesWindowsNPMCMDToCanonicalCLI(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	cliPath := writeWindowsNodeJSFixture(t, searchDir, "npm")

	got, err := resolveRuntimeExecutablePath("npm", []string{searchDir})
	if err != nil {
		t.Fatalf("resolve mediated Windows npm path: %v", err)
	}
	if got != cliPath {
		t.Fatalf("expected canonical CLI path %q, got %q", cliPath, got)
	}
}

func TestBuildRuntimeCommandMediatesWindowsNPMCMDLayout(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	cliPath := writeWindowsNodeJSFixture(t, searchDir, "npm")
	t.Setenv(runtimeBinDirsEnvKey, searchDir)

	cmd, err := buildRuntimeCommand(context.Background(), "npm test")
	if err != nil {
		t.Fatalf("build mediated Windows npm command: %v", err)
	}
	if !strings.EqualFold(filepath.Base(cmd.Path), "node.exe") {
		t.Fatalf("expected mediated node executable, got %q", cmd.Path)
	}
	if len(cmd.Args) < 2 || strings.EqualFold(cmd.Args[1], cliPath) || filepath.Base(cmd.Args[1]) != filepath.Base(cliPath) {
		t.Fatalf("expected staged CLI argument replacing %q, got %q", cliPath, cmd.Args)
	}
	registerRuntimeCommandCleanup(t, cmd)
}

func TestTrustedRuntimeWindowsExecutableRootMatchesExactRootOnly(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	programFilesNodejs := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, programFilesNodejs)

	if got := trustedRuntimeWindowsExecutableRoot(programFilesNodejs); got != programFilesNodejs {
		t.Fatalf("expected exact Windows executable root %q, got %q", programFilesNodejs, got)
	}
	if got := trustedRuntimeWindowsExecutableRoot(filepath.Join(filepath.Dir(programFilesNodejs), "Python313")); got != "" {
		t.Fatalf("expected descendant to miss exact-root helper, got %q", got)
	}
}

func TestTrustedRuntimeWindowsConfiguredRootsSanitizeEntries(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	valid := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-configured-"), "Scripts")
	t.Setenv(runtimeBinDirsEnvKey, strings.Join([]string{"", "relative", valid, strings.ToUpper(valid)}, string(os.PathListSeparator)))

	if got := trustedRuntimeWindowsConfiguredRoots(); !slices.Equal(got, []string{valid}) {
		t.Fatalf("expected sanitized configured Windows roots %q, got %v", valid, got)
	}
}

func TestRuntimeWindowsBaseRoot(t *testing.T) {
	programFiles := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "Program Files")
	systemRoot := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-system-root-"), "System32")

	tests := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{
			name: "nodejs parent",
			path: filepath.Join(programFiles, "nodejs"),
			want: programFiles,
			ok:   true,
		},
		{
			name: "system32 exact",
			path: systemRoot,
			want: systemRoot,
			ok:   true,
		},
		{
			name: "filesystem root",
			path: string(os.PathSeparator),
			want: "",
			ok:   false,
		},
		{
			name: "relative",
			path: "nodejs",
			want: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		got, ok := runtimeWindowsBaseRoot(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("%s: expected %q ok=%v, got %q ok=%v", tt.name, tt.want, tt.ok, got, ok)
		}
	}
}

func TestRuntimeWindowsPathWithinRoot(t *testing.T) {
	root := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "Program Files")

	if !runtimeWindowsPathWithinRoot(filepath.Join(root, "Python313", "Scripts"), root) {
		t.Fatalf("expected descendant to remain within root %q", root)
	}
	if runtimeWindowsPathWithinRoot(filepath.Join(root, "..", "Other"), root) {
		t.Fatalf("expected sibling path to fall outside root %q", root)
	}
	if runtimeWindowsPathWithinRoot(root, filepath.Join(root, "Python313")) {
		t.Fatalf("expected root not to be contained within descendant")
	}
}

func TestRuntimePlatformNodeCLIInstallationRootWindowsStandardLayout(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	cliPath := writeWindowsNodeJSFixture(t, searchDir, "npm")
	launcherPath := filepath.Join(searchDir, "npm.cmd")

	if got, ok := runtimePlatformNodeCLIInstallationRoot("npm", launcherPath, launcherPath); !ok || got != searchDir {
		t.Fatalf("expected launcher installation root %q, got %q ok=%v", searchDir, got, ok)
	}
	if got, ok := runtimePlatformNodeCLIInstallationRoot("npm", launcherPath, cliPath); !ok || got != searchDir {
		t.Fatalf("expected canonical CLI installation root %q, got %q ok=%v", searchDir, got, ok)
	}
	if got, ok := runtimePlatformNodeCLIInstallationRoot("npm", launcherPath, filepath.Join(searchDir, "npm.ps1")); ok || got != "" {
		t.Fatalf("expected unsupported launcher target rejection, got %q ok=%v", got, ok)
	}
}

func TestRuntimePlatformNodeExecutableInstallationRootWindowsNodejsLayout(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	writeWindowsNodeJSFixture(t, searchDir, "npm")

	if got, ok := runtimePlatformNodeExecutableInstallationRoot(filepath.Join(searchDir, "node.exe")); !ok || got != searchDir {
		t.Fatalf("expected node.exe installation root %q, got %q ok=%v", searchDir, got, ok)
	}
	if got, ok := runtimePlatformNodeExecutableInstallationRoot(filepath.Join(searchDir, "bin", "node.exe")); ok || got != "" {
		t.Fatalf("expected non-standard node.exe layout rejection, got %q ok=%v", got, ok)
	}
}

func TestRuntimeNodeSearchDirsForCLIWindowsIncludesCanonicalRoot(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	launcher := resolvedRuntimeExecutable{
		selectedLauncherRoot:      filepath.Join("C:", "Program Files", "nodejs"),
		canonicalInstallationRoot: filepath.Join("C:", "Program Files", "nodejs"),
	}
	want := []string{launcher.selectedLauncherRoot, launcher.canonicalInstallationRoot}
	if got := runtimeNodeSearchDirsForCLI(launcher); !slices.Equal(got, want) {
		t.Fatalf("expected Windows node CLI search dirs %v, got %v", want, got)
	}
}

func TestRuntimeWindowsNodeCLIInstallationRootRejectsMismatchedLauncherDir(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	writeWindowsNodeJSFixture(t, searchDir, "npm")

	if got, ok := runtimeWindowsNodeCLIInstallationRoot(filepath.Join(searchDir, "npm.cmd"), filepath.Join(filepath.Dir(searchDir), "npm.cmd")); ok || got != "" {
		t.Fatalf("expected mismatched launcher directory rejection, got %q ok=%v", got, ok)
	}
}

func TestRuntimeWindowsCanonicalCLIInstallationRootRejectsMalformedLayouts(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)

	if got, ok := runtimeWindowsCanonicalCLIInstallationRoot("npm", filepath.Join(searchDir, "node_modules", "npm", "npm-cli.js")); ok || got != "" {
		t.Fatalf("expected missing bin directory rejection, got %q ok=%v", got, ok)
	}
	if got, ok := runtimeWindowsCanonicalCLIInstallationRoot("npm", filepath.Join(searchDir, "node_modules", "other", "bin", "npm-cli.js")); ok || got != "" {
		t.Fatalf("expected non-npm package rejection, got %q ok=%v", got, ok)
	}
}

func TestRuntimeWindowsNodeCLIResolvedPathRejectsMissingCanonicalCLI(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	if err := os.MkdirAll(searchDir, 0o755); err != nil {
		t.Fatalf("mkdir nodejs root: %v", err)
	}
	writeRuntimeTestExecutable(t, filepath.Join(searchDir, "npm.cmd"), "@echo off\r\n")

	if path, source, ok, err := runtimeWindowsNodeCLIResolvedPath("npm", filepath.Join(searchDir, "npm.cmd"), filepath.Join(searchDir, "npm.cmd")); err == nil || ok || source != nil || path != "" {
		t.Fatalf("expected missing canonical CLI rejection, path=%q source=%#v ok=%v err=%v", path, source, ok, err)
	}
}

func TestRuntimeWindowsBaseRootsCollapseToProgramFilesAndSystem32(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	programFilesNodejs := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	system32 := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-system32-"), "System32")
	setRuntimeWindowsExecutableRootsTest(t, programFilesNodejs, system32)

	want := []string{filepath.Dir(programFilesNodejs), system32}
	if got := trustedRuntimeWindowsBaseRoots(); !slices.Equal(got, want) {
		t.Fatalf("expected Windows base roots %v, got %v", want, got)
	}
}

func TestResolvePinnedRuntimeExecutableInDirMediatesWindowsCLI(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	cliPath := writeWindowsNodeJSFixture(t, searchDir, "npm")

	resolution, ok := resolvePinnedRuntimeExecutableInDir("npm", searchDir)
	if !ok {
		t.Fatal("expected mediated Windows npm resolution to succeed")
	}
	if resolution.path != cliPath || resolution.canonicalInstallationRoot != searchDir || resolution.selectedLauncherRoot != searchDir {
		t.Fatalf("unexpected mediated resolution: %#v", resolution)
	}
	if err := resolution.closeSource(); err != nil {
		t.Fatalf("close mediated resolution source: %v", err)
	}
}

func TestResolvePinnedRuntimeExecutableInDirRejectsWindowsCLIMissingCanonicalTarget(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	if err := os.MkdirAll(searchDir, 0o755); err != nil {
		t.Fatalf("mkdir nodejs root: %v", err)
	}
	writeRuntimeTestExecutable(t, filepath.Join(searchDir, "npm.cmd"), "@echo off\r\n")

	if resolution, ok := resolvePinnedRuntimeExecutableInDir("npm", searchDir); ok || resolution != (resolvedRuntimeExecutable{}) {
		t.Fatalf("expected missing canonical CLI to reject mediated resolution, got %#v ok=%v", resolution, ok)
	}
}

func TestResolvePinnedRuntimeExecutablePathKeepsOriginalWhenNoWindowsRedirectApplies(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	source := &runtimeExecutableSource{path: filepath.Join(`C:\trusted`, "python.exe")}
	gotSource, gotPath, err := resolvePinnedRuntimeExecutablePath("python", source.path, source)
	if err != nil {
		t.Fatalf("resolve pinned executable path without redirect: %v", err)
	}
	if gotSource != source || gotPath != source.path {
		t.Fatalf("expected original source/path to be retained, got source=%#v path=%q", gotSource, gotPath)
	}
}

func TestResolvePinnedRuntimeExecutablePathClosesRedirectedSourceOnOriginalCloseFailure(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	_ = writeWindowsNodeJSFixture(t, searchDir, "npm")

	redirectedCloses := 0
	originalCloseErr := errors.New("original close failed")
	launcherPath := filepath.Join(searchDir, "npm.cmd")
	gotSource, gotPath, err := resolvePinnedRuntimeExecutablePath("npm", launcherPath, &runtimeExecutableSource{
		path: launcherPath,
		file: &trustedExecutableFileStub{closeErr: originalCloseErr},
		root: &runtimeCloseObserverRoot{onClose: func() {}},
	})
	if gotSource != nil || gotPath != "" || !errors.Is(err, originalCloseErr) {
		t.Fatalf("expected original close failure during redirect, source=%#v path=%q err=%v", gotSource, gotPath, err)
	}
	_ = redirectedCloses
}

func TestResolvePinnedRuntimeExecutablePathJoinsRedirectAndCloseErrors(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	if err := os.MkdirAll(searchDir, 0o755); err != nil {
		t.Fatalf("mkdir nodejs root: %v", err)
	}
	writeRuntimeTestExecutable(t, filepath.Join(searchDir, "npm.cmd"), "@echo off\r\n")

	closeErr := errors.New("source close failed")
	source := &runtimeExecutableSource{
		path: filepath.Join(searchDir, "npm.cmd"),
		file: &trustedExecutableFileStub{closeErr: closeErr},
		root: &stubRoot{},
	}
	_, _, err := resolvePinnedRuntimeExecutablePath("npm", source.path, source)
	if err == nil || !errors.Is(err, closeErr) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected redirect open and close errors, got %v", err)
	}
}

func TestResolvePinnedRuntimeExecutablePathClosesOriginalOnSuccessfulWindowsRedirect(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	searchDir := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, searchDir)
	cliPath := writeWindowsNodeJSFixture(t, searchDir, "npm")

	closeCalls := 0
	launcherPath := filepath.Join(searchDir, "npm.cmd")
	gotSource, gotPath, err := resolvePinnedRuntimeExecutablePath("npm", launcherPath, &runtimeExecutableSource{
		path: launcherPath,
		file: &runtimeCloseObserverFile{onClose: func() { closeCalls++ }},
		root: &runtimeCloseObserverRoot{onClose: func() { closeCalls++ }},
	})
	if err != nil {
		t.Fatalf("resolve redirected pinned executable path: %v", err)
	}
	if gotPath != cliPath || gotSource == nil || gotSource.path != cliPath {
		t.Fatalf("expected redirected CLI path %q, got source=%#v path=%q", cliPath, gotSource, gotPath)
	}
	if closeCalls != 2 {
		t.Fatalf("expected original source handles to close during redirect, got %d closes", closeCalls)
	}
	if err := gotSource.Close(); err != nil {
		t.Fatalf("close redirected source: %v", err)
	}
}

func TestResolvePinnedRuntimeInstallationRootRejectsMalformedNodeCLIAndClosesSource(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	closeErr := errors.New("source close failed")
	source := &runtimeExecutableSource{
		file: &trustedExecutableFileStub{closeErr: closeErr},
		root: &stubRoot{},
	}
	_, ok, err := resolvePinnedRuntimeInstallationRoot("npm", filepath.Join(`C:\trusted\nodejs`, "npm.cmd"), filepath.Join(`C:\trusted\nodejs`, "npm-cli.js"), source)
	if ok || !errors.Is(err, closeErr) {
		t.Fatalf("expected malformed Windows CLI root rejection with close error, ok=%v err=%v", ok, err)
	}
}

func TestResolvePinnedRuntimeInstallationRootSkipsNonCLIExecutables(t *testing.T) {
	root, ok, err := resolvePinnedRuntimeInstallationRoot("python", filepath.Join(`C:\trusted`, "python.exe"), filepath.Join(`C:\trusted`, "python.exe"), nil)
	if err != nil || !ok || root != "" {
		t.Fatalf("expected non-CLI executable to skip installation-root mediation, root=%q ok=%v err=%v", root, ok, err)
	}
}

func TestRuntimeWindowsNodeCLIResolvedPathReturnsNotApplicableForNonLauncher(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	path, source, ok, err := runtimeWindowsNodeCLIResolvedPath("python", "/tmp/python.exe", "/tmp/python.exe")
	if err != nil || ok || source != nil || path != "" {
		t.Fatalf("expected non-launcher to skip Windows CLI mediation, path=%q source=%#v ok=%v err=%v", path, source, ok, err)
	}
}

func TestRuntimeWindowsNodeCLICanonicalPathRejectsUnsupportedExecutable(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	if path, ok := runtimeWindowsNodeCLICanonicalPath("node", "/tmp/node.exe", "/tmp/node.exe"); ok || path != "" {
		t.Fatalf("expected unsupported executable to skip canonical CLI mediation, path=%q ok=%v", path, ok)
	}
}

func TestRuntimeWindowsNodeCLILauncherTargetAcceptsBAT(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	if !runtimeWindowsNodeCLILauncherTarget("npm", filepath.Join("/tmp", "NPM.BAT")) {
		t.Fatal("expected .bat launcher target to be accepted case-insensitively")
	}
}

func TestRuntimePlatformNodeCLIInstallationRootRejectsNonWindows(t *testing.T) {
	setRuntimeOSTest(t, "darwin")

	if root, ok := runtimePlatformNodeCLIInstallationRoot("npm", "/tmp/npm", "/tmp/npm"); ok || root != "" {
		t.Fatalf("expected non-Windows platform helper to reject Windows CLI mediation, root=%q ok=%v", root, ok)
	}
}

func TestRuntimeNodeSearchDirsForCLINonWindowsUsesBinDir(t *testing.T) {
	setRuntimeOSTest(t, "darwin")

	launcher := resolvedRuntimeExecutable{
		selectedLauncherRoot:      "/usr/local/bin",
		canonicalInstallationRoot: "/usr/local/Cellar/node/24.0.0",
	}
	want := []string{launcher.selectedLauncherRoot, filepath.Join(launcher.canonicalInstallationRoot, "bin")}
	if got := runtimeNodeSearchDirsForCLI(launcher); !slices.Equal(got, want) {
		t.Fatalf("expected non-Windows node CLI search dirs %v, got %v", want, got)
	}
}

func TestTrustedRuntimeWindowsHelpersReturnNilOutsideWindows(t *testing.T) {
	setRuntimeOSTest(t, "darwin")
	t.Setenv(runtimeBinDirsEnvKey, filepath.Join(t.TempDir(), "Scripts"))

	if roots := trustedRuntimeWindowsBaseRoots(); len(roots) != 0 {
		t.Fatalf("expected no Windows base roots outside Windows, got %v", roots)
	}
	if roots := trustedRuntimeWindowsConfiguredRoots(); len(roots) != 0 {
		t.Fatalf("expected no configured Windows roots outside Windows, got %v", roots)
	}
}

func TestRuntimePlatformNodeExecutableInstallationRootNonWindowsRejectsWindowsLayout(t *testing.T) {
	setRuntimeOSTest(t, "darwin")

	if got, ok := runtimePlatformNodeExecutableInstallationRoot(filepath.Join("/tmp", "node.exe")); ok || got != "" {
		t.Fatalf("expected non-Windows node.exe layout rejection, got %q ok=%v", got, ok)
	}
}

func TestRuntimeWindowsPathWithinRootAllowsExactRoot(t *testing.T) {
	root := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-program-files-"), "Program Files")
	if !runtimeWindowsPathWithinRoot(root, root) {
		t.Fatalf("expected exact root %q to be contained", root)
	}
}

func TestWindowsRuntimeTrustFailsClosedWithoutOSRoots(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	setRuntimeWindowsExecutableRootsTest(t)

	poisonedProgramFiles := testutil.SecureHomeTempDir(t, "runtime-poisoned-program-files-")
	poisonedNodejs := filepath.Join(poisonedProgramFiles, "nodejs")
	if err := os.MkdirAll(poisonedNodejs, 0o777); err != nil {
		t.Fatalf("mkdir poisoned Program Files nodejs root: %v", err)
	}
	t.Setenv("ProgramFiles", poisonedProgramFiles)

	if got := defaultTrustedRuntimeBinDirEntries(); len(got) != 0 {
		t.Fatalf("expected no Windows defaults without OS roots, got %v", got)
	}
	if got := trustedSearchDirs(poisonedNodejs); len(got) != 0 {
		t.Fatalf("expected Windows trust to fail closed, got %v", got)
	}
}

func TestTrustedRuntimeWindowsExecutableRootsSanitizesOSResults(t *testing.T) {
	validRoot := filepath.Join(testutil.SecureHomeTempDir(t, "runtime-windows-root-"), "nodejs")
	setRuntimeWindowsExecutableRootsTest(t, "", "relative", validRoot, strings.ToUpper(validRoot))

	got := trustedRuntimeWindowsExecutableRoots()
	want := []string{validRoot}
	if !slices.Equal(got, want) {
		t.Fatalf("expected sanitized Windows roots %v, got %v", want, got)
	}
}

func TestRuntimeSearchDirsDefaultOnWindowsKeepsProgramFilesNodejsDir(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	t.Setenv(runtimeBinDirsEnvKey, "")

	programFiles := testutil.SecureHomeTempDir(t, "runtime-program-files-")
	programFilesNodejs := filepath.Join(programFiles, "nodejs")
	if err := os.MkdirAll(programFilesNodejs, 0o777); err != nil {
		t.Fatalf("mkdir ProgramFiles nodejs: %v", err)
	}
	if err := os.Chmod(programFilesNodejs, 0o777); err != nil {
		t.Fatalf("chmod ProgramFiles nodejs: %v", err)
	}
	setRuntimeWindowsExecutableRootsTest(t, programFilesNodejs)

	got := runtimeSearchDirs()
	if !slices.Contains(got, programFilesNodejs) {
		t.Fatalf("expected ProgramFiles nodejs directory in runtime search dirs, got %v", got)
	}
}

func setRuntimeOSTest(t *testing.T, osName string) {
	t.Helper()

	originalOS := runtimeOS
	runtimeOS = osName
	t.Cleanup(func() {
		runtimeOS = originalOS
	})
}

func setRuntimeWindowsExecutableRootsTest(t *testing.T, roots ...string) {
	t.Helper()

	originalRoots := runtimeWindowsExecutableRoots
	copiedRoots := append([]string(nil), roots...)
	runtimeWindowsExecutableRoots = func() []string {
		return append([]string(nil), copiedRoots...)
	}
	t.Cleanup(func() {
		runtimeWindowsExecutableRoots = originalRoots
	})
}

func writeWindowsNodeJSFixture(t *testing.T, searchDir string, executable string) string {
	t.Helper()

	if err := os.MkdirAll(searchDir, 0o755); err != nil {
		t.Fatalf("mkdir nodejs root: %v", err)
	}
	writeRuntimeTestExecutable(t, filepath.Join(searchDir, executable+".cmd"), "@echo off\r\n")
	writeRuntimeTestExecutable(t, filepath.Join(searchDir, "node.exe"), "@echo off\r\n")
	cliPath := filepath.Join(searchDir, "node_modules", "npm", "bin", executable+"-cli.js")
	writeRuntimeTestExecutable(t, cliPath, "#!/usr/bin/env node\n")
	return cliPath
}

func runtimeToolPathForTest(t *testing.T, dir string, name string) string {
	t.Helper()
	if isWindowsRuntime() {
		return filepath.Join(dir, name+".exe")
	}
	return filepath.Join(dir, name)
}

func writeRuntimeTestExecutable(t *testing.T, path string, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir executable parent %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(script), 0o500); err != nil {
		t.Fatalf("write runtime executable %q: %v", path, err)
	}
}

func assertRuntimeCommandUsesPrivateImage(t *testing.T, cmd *runtimeCommand, sourcePath string) {
	t.Helper()
	if cmd.Args[0] != sourcePath {
		t.Fatalf("expected canonical argv[0] %q, got %q", sourcePath, cmd.Args[0])
	}
	if cmd.Path == sourcePath {
		t.Fatalf("expected private launch image instead of source path %q", sourcePath)
	}
	if filepath.Base(cmd.Path) != filepath.Base(sourcePath) {
		t.Fatalf("expected private image basename %q, got %q", filepath.Base(sourcePath), cmd.Path)
	}
}

func registerRuntimeCommandCleanup(t *testing.T, cmd *runtimeCommand) {
	t.Helper()
	t.Cleanup(func() {
		if err := cmd.finish(nil); err != nil {
			t.Errorf("cleanup unstarted runtime command: %v", err)
		}
	})
}

func runtimeCommandWithCleanupFailure(t *testing.T, script string) (*runtimeCommand, *int, error) {
	t.Helper()

	searchDir := testutil.SecureHomeTempDir(t, "runtime-cleanup-error-")
	executablePath := filepath.Join(searchDir, "node")
	writeRuntimeTestExecutable(t, executablePath, script)
	t.Setenv(runtimeBinDirsEnvKey, searchDir)
	cmd, err := buildRuntimeCommand(context.Background(), "node")
	if err != nil {
		t.Fatalf("build trusted runtime command: %v", err)
	}

	cleanupFailure := errors.New("injected staged executable cleanup failure")
	cleanupCalls := 0
	cleanup := cmd.cleanupFn
	cmd.cleanupFn = func() error {
		cleanupCalls++
		return errors.Join(cleanup(), cleanupFailure)
	}
	return cmd, &cleanupCalls, cleanupFailure
}

type runtimeCloseObserverFile struct {
	trustedExecutableFileStub
	onClose func()
}

func (f *runtimeCloseObserverFile) Close() error {
	if f.onClose != nil {
		f.onClose()
	}
	return f.trustedExecutableFileStub.Close()
}

type runtimeCloseObserverRoot struct {
	stubRoot
	onClose func()
}

func (r *runtimeCloseObserverRoot) Close() error {
	if r.onClose != nil {
		r.onClose()
	}
	return r.stubRoot.Close()
}

func unsetRuntimeBinDirsTest(t *testing.T) {
	t.Helper()

	original, present := os.LookupEnv(runtimeBinDirsEnvKey)
	if err := os.Unsetenv(runtimeBinDirsEnvKey); err != nil {
		t.Fatalf("unset runtime bin dirs: %v", err)
	}
	t.Cleanup(func() {
		if present {
			if err := os.Setenv(runtimeBinDirsEnvKey, original); err != nil {
				t.Errorf("restore runtime bin dirs: %v", err)
			}
			return
		}
		if err := os.Unsetenv(runtimeBinDirsEnvKey); err != nil {
			t.Errorf("restore absent runtime bin dirs: %v", err)
		}
	})
	if _, present := os.LookupEnv(runtimeBinDirsEnvKey); present {
		t.Fatal("expected runtime bin dirs override to be unset")
	}
}

func createHomebrewStyleNodeToolFixture(t *testing.T, requestedName string, canonicalName string) (string, string) {
	t.Helper()
	rootDir := testutil.SecureHomeTempDir(t, "runtime-homebrew-node-")
	homebrewPrefix := filepath.Join(rootDir, "opt", "homebrew")
	trustedDir := filepath.Join(homebrewPrefix, "bin")
	versionRoot := filepath.Join(homebrewPrefix, "Cellar", "node", "24.0.0")
	versionBinDir := filepath.Join(versionRoot, "bin")
	canonicalDir := filepath.Join(homebrewPrefix, "lib", "node_modules", "npm", "bin")
	if err := os.MkdirAll(trustedDir, 0o755); err != nil {
		t.Fatalf("mkdir trusted dir: %v", err)
	}
	if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical dir: %v", err)
	}
	if err := os.MkdirAll(versionBinDir, 0o755); err != nil {
		t.Fatalf("mkdir version bin dir: %v", err)
	}

	canonicalPath := filepath.Join(canonicalDir, canonicalName)
	writeRuntimeTestExecutable(t, canonicalPath, "#!/usr/bin/env node\nprocess.stdout.write('"+canonicalName+"\\n');\n")
	nodePath := filepath.Join(versionBinDir, "node")
	writeRuntimeTestExecutable(t, nodePath, "#!/bin/sh\nprintf 'trusted-node\\n'\n")
	versionLink := filepath.Join(versionBinDir, requestedName)
	if err := os.Symlink(canonicalPath, versionLink); err != nil {
		t.Fatalf("symlink version entrypoint: %v", err)
	}
	trustedLink := filepath.Join(trustedDir, requestedName)
	if err := os.Symlink(versionLink, trustedLink); err != nil {
		t.Fatalf("symlink trusted entrypoint: %v", err)
	}
	trustedNodeLink := filepath.Join(trustedDir, "node")
	if err := os.Symlink(filepath.Join("..", "Cellar", "node", "24.0.0", "bin", "node"), trustedNodeLink); err != nil {
		t.Fatalf("symlink trusted node interpreter: %v", err)
	}
	return trustedDir, canonicalPath
}

func createHomebrewStylePythonFixture(t *testing.T) (string, string) {
	t.Helper()
	rootDir := testutil.SecureHomeTempDir(t, "runtime-homebrew-python-")
	trustedDir := filepath.Join(rootDir, "opt", "homebrew", "bin")
	versionBinDir := filepath.Join(rootDir, "opt", "homebrew", "Cellar", "python@3.12", "3.12.4", "bin")
	if err := os.MkdirAll(trustedDir, 0o755); err != nil {
		t.Fatalf("mkdir trusted dir: %v", err)
	}
	if err := os.MkdirAll(versionBinDir, 0o755); err != nil {
		t.Fatalf("mkdir version bin dir: %v", err)
	}

	canonicalPath := filepath.Join(versionBinDir, "python3.12")
	writeRuntimeTestExecutable(t, canonicalPath, "#!/bin/sh\nprintf 'python3.12\\n'\n")
	versionLink := filepath.Join(versionBinDir, "python3")
	if err := os.Symlink("python3.12", versionLink); err != nil {
		t.Fatalf("symlink versioned python entrypoint: %v", err)
	}
	trustedLink := filepath.Join(trustedDir, "python3")
	if err := os.Symlink(filepath.Join("..", "Cellar", "python@3.12", "3.12.4", "bin", "python3"), trustedLink); err != nil {
		t.Fatalf("symlink trusted python entrypoint: %v", err)
	}
	return trustedDir, canonicalPath
}

func createNVMCurrentToolFixture(t *testing.T, requestedName string, canonicalName string) (string, string) {
	t.Helper()
	rootDir := testutil.SecureHomeTempDir(t, "runtime-nvm-")
	versionRoot := filepath.Join(rootDir, ".nvm", "versions", "node", "v24.0.0")
	versionBinDir := filepath.Join(versionRoot, "bin")
	currentDir := filepath.Join(rootDir, ".nvm", "current", "bin")
	if err := os.MkdirAll(versionBinDir, 0o755); err != nil {
		t.Fatalf("mkdir version bin dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, ".nvm"), 0o755); err != nil {
		t.Fatalf("mkdir nvm root: %v", err)
	}

	var canonicalPath string
	if requestedName == canonicalName {
		canonicalPath = filepath.Join(versionBinDir, canonicalName)
		writeRuntimeTestExecutable(t, canonicalPath, "#!/bin/sh\nprintf '"+canonicalName+"\\n'\n")
	} else {
		writeRuntimeTestExecutable(t, filepath.Join(versionBinDir, "node"), "#!/bin/sh\nprintf 'trusted-node\\n'\n")
		canonicalDir := filepath.Join(versionRoot, "lib", "node_modules", "npm", "bin")
		if err := os.MkdirAll(canonicalDir, 0o755); err != nil {
			t.Fatalf("mkdir canonical dir: %v", err)
		}
		canonicalPath = filepath.Join(canonicalDir, canonicalName)
		writeRuntimeTestExecutable(t, canonicalPath, "#!/usr/bin/env node\nprocess.stdout.write('"+canonicalName+"\\n');\n")
		entryLink := filepath.Join(versionBinDir, requestedName)
		if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "npm", "bin", canonicalName), entryLink); err != nil {
			t.Fatalf("symlink requested entrypoint: %v", err)
		}
	}

	if err := os.Symlink(filepath.Join("versions", "node", "v24.0.0"), filepath.Join(rootDir, ".nvm", "current")); err != nil {
		t.Fatalf("symlink current node version: %v", err)
	}
	return currentDir, canonicalPath
}
