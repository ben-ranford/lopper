package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

	firstDir := t.TempDir()
	secondDir := t.TempDir()

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

	firstDir := t.TempDir()
	secondDir := t.TempDir()
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

func TestRuntimeSearchDirsPrefersTrustedPATHSelection(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("Unix PATH trust checks are covered here")
	}

	selectedDir := t.TempDir()
	fallbackDir := t.TempDir()
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

	selectedDir := t.TempDir()
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
	if cmd.Path != selectedPython {
		t.Fatalf("expected PATH-selected Python %q, got %q", selectedPython, cmd.Path)
	}
}

func TestWindowsExecutableExtensions(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "defaults when empty",
			in:   "",
			want: []string{".com", ".exe", ".bat", ".cmd"},
		},
		{
			name: "normalizes case and dot",
			in:   "EXE;.cmd;.BAT;bat",
			want: []string{".exe", ".cmd", ".bat"},
		},
		{
			name: "ignores blanks",
			in:   " ; ;.EXE;; ",
			want: []string{".exe"},
		},
		{
			name: "falls back when all entries are empty",
			in:   ";;;",
			want: []string{".com", ".exe", ".bat", ".cmd"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := windowsExecutableExtensions(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("expected windows executable extensions %v, got %v", tc.want, got)
			}
		})
	}
}

func TestRuntimeExecutableCandidatesWindowsIncludesPathextEntries(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	t.Setenv("PATHEXT", ".CMD;.EXE;.EXE")
	dir := t.TempDir()
	got := runtimeExecutableCandidates("npm", dir)
	want := []string{
		filepath.Join(dir, "npm"),
		filepath.Join(dir, "npm") + ".cmd",
		filepath.Join(dir, "npm") + ".exe",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("expected candidates %v, got %v", want, got)
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

func TestDefaultTrustedRuntimeBinDirEntriesOnWindowsIncludesProgramFiles(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	t.Setenv("ProgramFiles", `D:\Apps`)
	t.Setenv("ProgramFiles(x86)", `E:\Apps86`)

	got := defaultTrustedRuntimeBinDirEntries()
	if !slices.Contains(got, filepath.Join(`D:\Apps`, "nodejs")) {
		t.Fatalf("expected ProgramFiles nodejs entry in %v", got)
	}
	if !slices.Contains(got, filepath.Join(`E:\Apps86`, "nodejs")) {
		t.Fatalf("expected ProgramFiles(x86) nodejs entry in %v", got)
	}
	if !slices.Contains(got, `C:\Windows\System32`) {
		t.Fatalf("expected system32 entry in %v", got)
	}
}

func TestTrustedSearchDirsOnWindowsDoesNotApplyUnixPermissionFilter(t *testing.T) {
	setRuntimeOSTest(t, "windows")

	trustedDir := t.TempDir()
	if err := os.Chmod(trustedDir, 0o777); err != nil {
		t.Fatalf("chmod trusted dir: %v", err)
	}

	got := trustedSearchDirs(trustedDir)
	want := []string{trustedDir}
	if !slices.Equal(got, want) {
		t.Fatalf("expected trusted dirs %v, got %v", want, got)
	}
}

func TestRuntimeSearchDirsDefaultOnWindowsKeepsProgramFilesNodejsDir(t *testing.T) {
	setRuntimeOSTest(t, "windows")
	t.Setenv(runtimeBinDirsEnvKey, "")

	programFiles := t.TempDir()
	programFilesNodejs := filepath.Join(programFiles, "nodejs")
	if err := os.MkdirAll(programFilesNodejs, 0o777); err != nil {
		t.Fatalf("mkdir ProgramFiles nodejs: %v", err)
	}
	if err := os.Chmod(programFilesNodejs, 0o777); err != nil {
		t.Fatalf("chmod ProgramFiles nodejs: %v", err)
	}
	t.Setenv("ProgramFiles", programFiles)
	t.Setenv("ProgramFiles(x86)", "")

	got := runtimeSearchDirs()
	if !slices.Contains(got, programFilesNodejs) {
		t.Fatalf("expected ProgramFiles nodejs directory in runtime search dirs, got %v", got)
	}
}

func TestNewAllowlistedRuntimeCommandRejectsUnsupportedExecutable(t *testing.T) {
	if _, err := newAllowlistedRuntimeCommand(context.Background(), "ruby"); err == nil {
		t.Fatalf("expected unsupported executable error")
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
