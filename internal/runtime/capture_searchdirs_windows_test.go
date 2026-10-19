package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTrustedSearchDirsWindowsExtendedPaths(t *testing.T) {
	dir := setupWindowsRuntimeSearchExecutable(t, t.TempDir())
	assertWindowsRuntimeSearchPaths(t, dir)
}

func assertWindowsRuntimeSearchPaths(t *testing.T, dir string) {
	t.Helper()
	extended := windowsRuntimeSearchExtendedPath(dir)
	assertWindowsRuntimeSearchRoots(t, dir)
	t.Setenv("PATHEXT", ".EXE")
	for _, entry := range []string{dir, dir + `\`, extended, extended + `\`} {
		t.Run(entry, func(t *testing.T) {
			for _, key := range []string{runtimeBinDirsEnvKey, "PATH"} {
				t.Run(key, func(t *testing.T) {
					assertWindowsRuntimeSearchEntry(t, entry, dir, key)
				})
			}
		})
	}
}

func assertWindowsRuntimeSearchEntry(t *testing.T, entry, dir, key string) {
	t.Helper()
	t.Setenv(runtimeBinDirsEnvKey, "")
	t.Setenv(key, strings.Join([]string{entry, dir, entry}, ";"))
	want := []string{filepath.Clean(entry)}
	if want[0] != dir {
		want = append(want, dir)
	}
	if got := runtimeSearchDirs(); len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("valid directory dropped or preference changed: got %v, want prefix %v", got, want)
	}
	assertWindowsRuntimeSearchCommand(t, filepath.Clean(entry))
}

func TestTrustedSearchDirsWindowsRootsAndUnusableEntries(t *testing.T) {
	dir := setupWindowsRuntimeSearchExecutable(t, t.TempDir())
	assertWindowsRuntimeSearchRoots(t, dir)
	extended := windowsRuntimeSearchExtendedPath(dir)
	entries := []string{"", ".", filepath.Join(extended, "missing"), filepath.Join(extended, "node.exe"), extended}
	if got := trustedSearchDirs(strings.Join(entries, ";")); !slices.Equal(got, []string{extended}) {
		t.Fatalf("unusable entry handling changed: %v", got)
	}
	t.Setenv(runtimeBinDirsEnvKey, filepath.Join(extended, "missing"))
	if _, err := buildRuntimeCommand(context.Background(), "node --version"); err == nil {
		t.Fatal("missing-only directory resolved a command")
	}
}

func assertWindowsRuntimeSearchRoots(t *testing.T, dir string) {
	t.Helper()
	root := filepath.VolumeName(dir) + `\`
	for _, entry := range []string{root, windowsRuntimeSearchExtendedPath(root)} {
		if got := trustedSearchDirs(entry); !slices.Equal(got, []string{filepath.Clean(entry)}) {
			t.Fatalf("valid root dropped: entry %q, got %v", entry, got)
		}
	}
}

func setupWindowsRuntimeSearchExecutable(t *testing.T, dir string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.exe"), contents, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertWindowsRuntimeSearchCommand(t *testing.T, dir string) {
	t.Helper()
	const command = "node -test.run=TestRuntimeSearchWindowsExecutableHelper"
	cmd, err := buildRuntimeCommand(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "node.exe")
	if cmd.Path != want || !slices.Equal(cmd.Args, []string{want, "-test.run=TestRuntimeSearchWindowsExecutableHelper"}) {
		t.Fatalf("command changed: path %q, args %v", cmd.Path, cmd.Args)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start selected executable: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup selected executable: %v", err)
		}
	}()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait selected executable: %v", err)
	}
	if strings.TrimSpace(output.String()) != "PASS" {
		t.Fatalf("actual executable failed: output %q", output.String())
	}
}

func TestRuntimeSearchWindowsExecutableHelper(t *testing.T) {
	// The copied test executable runs only this test, providing a real executable
	// without requiring an installed runtime or invoking a command interpreter.
	t.Log("runtime search executable reached")
}

func windowsRuntimeSearchExtendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\\?\` + path
}
