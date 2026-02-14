package testutil

import (
	"context"
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

func TestCanceledContext(t *testing.T) {
	ctx := CanceledContext()
	if ctx.Err() == nil {
		t.Fatalf("expected canceled context")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("unexpected context error: %v", ctx.Err())
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

func TestFileHelpers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b.txt")
	MustWriteFile(t, p, "hello")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("unexpected content: %q", content)
	}

	MustWriteFileMode(t, filepath.Join(dir, "mode.txt"), "x", 0o644)
	info, err := os.Stat(filepath.Join(dir, "mode.txt"))
	if err != nil {
		t.Fatalf("stat mode file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected mode: %o", info.Mode().Perm())
	}
}

func TestWriteNumberedTextFilesAndFirstEntry(t *testing.T) {
	dir := t.TempDir()
	WriteNumberedTextFiles(t, dir, 3)
	entry := MustFirstFileEntry(t, dir)
	if entry == nil {
		t.Fatalf("expected first file entry")
	}
	if entry.Name() != "f-0.txt" {
		t.Fatalf("expected first file entry to be %q, got %q", "f-0.txt", entry.Name())
	}
}

func TestMustFirstFileEntrySkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	MustWriteFile(t, filepath.Join(dir, "z.txt"), "x")
	entry := MustFirstFileEntry(t, dir)
	if entry == nil {
		t.Fatalf("expected file entry after directory entries")
	}
	if entry.Name() != "z.txt" {
		t.Fatalf("expected first file entry to be %q, got %q", "z.txt", entry.Name())
	}
}

func TestWriteTempFile(t *testing.T) {
	path := WriteTempFile(t, "tmp.txt", "abc")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if string(content) != "abc" {
		t.Fatalf("unexpected temp file content: %q", content)
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

func TestChdir(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	Chdir(t, dir)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}
	wdResolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("eval wd symlink: %v", err)
	}
	dirResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval dir symlink: %v", err)
	}
	if wdResolved != dirResolved {
		t.Fatalf("expected wd %q, got %q", dirResolved, wdResolved)
	}
	t.Cleanup(func() {
		wd2, err := os.Getwd()
		if err == nil && wd2 != original {
			_ = os.Chdir(original)
		}
	})
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
