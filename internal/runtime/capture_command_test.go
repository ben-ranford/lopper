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
			command: `node -e "console.log('hello world')"`,
			want:    []string{"node", "-e", "console.log('hello world')"},
		},
		{
			name:    "single quoted args",
			command: `node -e 'console.log("hello")'`,
			want:    []string{"node", "-e", `console.log("hello")`},
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

func TestBuildRuntimeCommandRejectsMalformedInput(t *testing.T) {
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

func TestResolveRuntimeExecutablePathSkipsNonExecutableCandidate(t *testing.T) {
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

func TestNewAllowlistedRuntimeCommandRejectsUnsupportedExecutable(t *testing.T) {
	if _, err := newAllowlistedRuntimeCommand(context.Background(), "python"); err == nil {
		t.Fatalf("expected unsupported executable error")
	}
}
