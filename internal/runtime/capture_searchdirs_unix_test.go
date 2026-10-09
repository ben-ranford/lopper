//go:build darwin || linux

package runtime

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTrustedSearchDirsPreservesUsableDirectoryModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o100, 0o111} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := setupRuntimeSearchTool(t, mode)
			t.Setenv(runtimeBinDirsEnvKey, dir)
			if got := runtimeSearchDirs(); !slices.Equal(got, []string{dir}) {
				t.Fatalf("usable directory excluded: %v", got)
			}
			assertRuntimeSearchCommand(t, dir)
		})
	}
}

func TestTrustedSearchDirsReflectActualSearchAccess(t *testing.T) {
	dir := setupRuntimeSearchTool(t, 0)
	fallback := setupRuntimeSearchTool(t, 0o700)
	// Determine actual access by executing a known child, including under users
	// whose privileges permit searching a directory despite its permission bits.
	probe := exec.Command(filepath.Join(dir, "node"), "--version")
	err := probe.Run()
	if err != nil && !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unexpected access-control probe failure: %v", err)
	}
	searchable := err == nil
	t.Logf("mode 0000 child searchable: %v", searchable)
	t.Setenv(runtimeBinDirsEnvKey, strings.Join([]string{dir, fallback}, string(os.PathListSeparator)))
	if got := slices.Contains(runtimeSearchDirs(), dir); got != searchable {
		t.Fatalf("directory admission = %v, actual child access = %v", got, searchable)
	}
	if searchable {
		assertRuntimeSearchCommand(t, dir)
		return
	}
	assertRuntimeSearchCommand(t, fallback)
	t.Setenv(runtimeBinDirsEnvKey, dir)
	if _, err := buildRuntimeCommand(context.Background(), "node --version"); err == nil {
		t.Fatal("unsearchable-only directory resolved a command")
	}
}

func TestTrustedSearchDirsPreservesDirectoryAliases(t *testing.T) {
	target := setupRuntimeSearchTool(t, 0o100)
	alias := filepath.Join(t.TempDir(), "cross-parent")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	entries := []string{alias + string(os.PathSeparator), target, alias}
	t.Setenv(runtimeBinDirsEnvKey, strings.Join(entries, string(os.PathListSeparator)))
	if got := runtimeSearchDirs(); !slices.Equal(got, []string{alias, target}) {
		t.Fatalf("directory alias preference or deduplication changed: %v", got)
	}
	assertRuntimeSearchCommand(t, alias)
}

func setupRuntimeSearchTool(t *testing.T, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\nprintf 'fixture-ok\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("restore directory permissions: %v", err)
		}
	})
	return dir
}

func assertRuntimeSearchCommand(t *testing.T, dir string) {
	t.Helper()
	cmd, err := buildRuntimeCommand(context.Background(), "node --version")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "node")
	if cmd.Path != want || !slices.Equal(cmd.Args, []string{want, "--version"}) {
		t.Fatalf("command changed: path %q, args %v", cmd.Path, cmd.Args)
	}
	out, err := cmd.Output()
	if err != nil || string(out) != "fixture-ok\n" {
		t.Fatalf("command execution = %q, %v", out, err)
	}
}
