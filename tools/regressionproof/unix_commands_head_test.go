//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProofGoExecutableRejectsMissingNonExecutableAndRedirect(t *testing.T) {
	root, foreignRoot := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "bin", "go")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := validateProofGoExecutable(path); err == nil {
		t.Fatal("missing toolchain executable accepted")
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateProofGoExecutable(path); err == nil {
		t.Fatal("non-executable toolchain file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(foreignRoot, "go")
	if err := os.WriteFile(foreign, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, path); err != nil {
		t.Fatal(err)
	}
	if _, err := validateProofGoExecutable(path); err == nil {
		t.Fatal("executable redirect outside compiler root accepted")
	}
}

func TestProofGitConstructorAcceptsOnlyFixedSystemPaths(t *testing.T) {
	original := resolveGitBinaryPath
	t.Cleanup(func() { resolveGitBinaryPath = original })
	for _, path := range []string{"/usr/bin/git", "/bin/git", filepath.Join(t.TempDir(), "git")} {
		resolveGitBinaryPath = func() (string, error) { return path, nil }
		cmd, err := newProofGitCommand(context.Background())
		if path == "/usr/bin/git" || path == "/bin/git" {
			if err != nil || cmd.Path != path {
				t.Fatalf("supported system fallback changed: path=%q command=%v err=%v", path, cmd, err)
			}
		} else if err == nil || cmd != nil {
			t.Fatalf("foreign Git executable accepted: command=%v err=%v", cmd, err)
		}
	}
}
