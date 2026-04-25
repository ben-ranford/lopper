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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := buildRuntimeCommand(context.Background(), tc.command)
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
		{
			name:    "shell operator",
			command: `npm test && echo bad`,
			wantErr: "indirect command execution operators",
		},
		{
			name:    "eval flag",
			command: `node -e 'console.log("hi")'`,
			wantErr: "unsafe executable flag",
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

	t.Setenv("PATHEXT", ".CMD;.EXE")
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
	if _, err := newAllowlistedRuntimeCommand(context.Background(), "python"); err == nil {
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
