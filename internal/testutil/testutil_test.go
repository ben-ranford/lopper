package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCanceledContextIsDone(t *testing.T) {
	ctx := CanceledContext()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected canceled context")
	}
}

func TestWriteHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.txt")

	MustWriteFile(t, path, "hello")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf("unexpected content: %q", got)
	}

	WriteNumberedTextFiles(t, dir, 3)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f-%d.txt", i))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
	}

	tempPath := WriteTempFile(t, "temp.txt", "x")
	if info, err := os.Stat(tempPath); err != nil {
		t.Fatalf("stat temp file: %v", err)
	} else if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected 0644, got %o", got)
	}
}

func TestChdirAndMustFirstFileEntry(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	dir := t.TempDir()
	MustWriteFile(t, filepath.Join(dir, "a.txt"), "a")

	t.Run("chdir and first file", func(t *testing.T) {
		Chdir(t, dir)
		cleanDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks for dir: %v", err)
		}
		if cwd, err := os.Getwd(); err != nil {
			t.Fatalf("getwd after chdir: %v", err)
		} else if cwd != dir && cwd != cleanDir {
			t.Fatalf("expected cwd %s (or %s), got %s", dir, cleanDir, cwd)
		}

		entry := MustFirstFileEntry(t, dir)
		if entry.IsDir() {
			t.Fatal("expected file entry")
		}
	})

	if cwd, err := os.Getwd(); err != nil {
		t.Fatalf("getwd after cleanup: %v", err)
	} else if cwd != originalWD {
		t.Fatalf("expected cwd restored to %s, got %s", originalWD, cwd)
	}
}

func TestFatalPathsViaHelperProcess(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{
		"mkdir-failure",
		"write-failure",
		"chdir-failure",
		"first-file-none",
	} {
		t.Run(tc, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperFatalPath", "--", tc)
			cmd.Env = append(os.Environ(), "TESTUTIL_FATAL_HELPER=1")
			err := cmd.Run()
			if err == nil {
				t.Fatalf("expected helper to fail for scenario %s", tc)
			}
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("expected ExitError, got %T: %v", err, err)
			}
		})
	}
}

func TestHelperFatalPath(t *testing.T) {
	if os.Getenv("TESTUTIL_FATAL_HELPER") != "1" {
		return
	}
	if len(os.Args) < 2 {
		t.Fatal("missing helper scenario")
	}
	scenario := os.Args[len(os.Args)-1]

	switch scenario {
	case "mkdir-failure":
		dir := t.TempDir()
		parentFile := filepath.Join(dir, "parent")
		if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
			t.Fatalf("setup parent file: %v", err)
		}
		MustWriteFileMode(t, filepath.Join(parentFile, "child.txt"), "x", 0o600)
	case "write-failure":
		dir := t.TempDir()
		MustWriteFileMode(t, dir, "x", 0o600)
	case "chdir-failure":
		Chdir(t, filepath.Join(t.TempDir(), "missing"))
	case "first-file-none":
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
			t.Fatalf("setup subdir: %v", err)
		}
		_ = MustFirstFileEntry(t, dir)
	default:
		t.Fatalf("unknown helper scenario %q", scenario)
	}
}
