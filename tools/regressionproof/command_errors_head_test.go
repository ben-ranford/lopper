package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProofRejectsInvalidAllocationDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{"relative", filepath.Join(root, "missing"), file} {
		r := &runner{}
		if err := r.ownProofAllocation(root, parent, filepath.Join(parent, "base"), proofWorktree); err == nil || len(r.allocations) != 0 {
			t.Fatalf("invalid directory granted an allocation: parent=%q err=%v", parent, err)
		}
	}
	originalMkdir := mkdirTemp
	mkdirTemp = func(string, string) (string, error) { return file, nil }
	t.Cleanup(func() { mkdirTemp = originalMkdir })
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		t.Fatal("invalid allocation reached Git")
		return nil, nil
	}}
	if _, _, err := r.createBaseWorktree(context.Background(), root, strings.Repeat("a", 40)); err == nil {
		t.Fatal("worktree accepted a non-directory allocation")
	}
}

func TestProofCompileRejectsInvalidPackageAndTempRoot(t *testing.T) {
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		t.Fatal("invalid compile request reached Go")
		return nil, nil
	}}
	if err := r.compilePackage(context.Background(), t.TempDir(), "./pkg/../outside"); err == nil || len(r.allocations) != 0 {
		t.Fatalf("invalid package must release its compile allocation: err=%v allocations=%v", err, r.allocations)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, missing)
	}
	if err := r.compilePackage(context.Background(), ".", "./pkg"); err == nil || !strings.Contains(err.Error(), "create compile output directory") {
		t.Fatalf("unavailable temporary root was not reported: %v", err)
	}
}

func TestProofCompileReportsCleanupFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("uses POSIX directory write permissions")
	}
	var outputDir string
	r := &runner{execCommand: func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		outputDir = filepath.Dir(args[6])
		if err := os.WriteFile(args[6], []byte("fixture output"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(outputDir, 0o500); err != nil {
			t.Fatal(err)
		}
		return nil, nil
	}}
	t.Cleanup(func() {
		if outputDir != "" {
			if err := os.Chmod(outputDir, 0o700); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
			if err := os.RemoveAll(outputDir); err != nil {
				t.Error(err)
			}
		}
	})
	err := r.compilePackage(context.Background(), t.TempDir(), "./pkg")
	if err == nil || !strings.Contains(err.Error(), "remove compile output directory") || len(r.allocations) != 0 {
		t.Fatalf("cleanup failure was lost or capability retained: err=%v allocations=%v", err, r.allocations)
	}
}

func TestProofRejectsDeletedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit deleting the current directory")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	cwd := filepath.Join(parent, "cwd")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("platform still resolves a removed working directory")
	}
	r := &runner{}
	if err := r.ownProofAllocation(".", parent, filepath.Join(parent, "base"), proofWorktree); err == nil {
		t.Fatal("deleted repo directory granted an allocation")
	}
	if _, err := r.runGo(context.Background(), ".", nil); err == nil {
		t.Fatal("Go accepted a deleted repo directory")
	}
	if _, err := r.runGit(context.Background(), ".", "merge-base", "--", "HEAD~1", "HEAD"); err == nil {
		t.Fatal("Git accepted a deleted repo directory")
	}
	if err := r.compilePackage(context.Background(), ".", "./pkg"); err == nil || len(r.allocations) != 0 {
		t.Fatalf("compile accepted a deleted repo directory or leaked ownership: err=%v", err)
	}
}

func TestProofRejectsRepositoryResolutionFailure(t *testing.T) {
	// Use the shared resolver seam for hosts whose getcwd retains a deleted
	// directory name; the real removed-directory scenario is tested above too.
	parent := t.TempDir()
	original := absPath
	failure := errors.New("repository no longer resolves")
	absPath = func(string) (string, error) { return "", failure }
	t.Cleanup(func() { absPath = original })
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		t.Fatal("unresolved repository reached execution")
		return nil, nil
	}}
	if err := r.ownProofAllocation(".", parent, filepath.Join(parent, "base"), proofWorktree); !errors.Is(err, failure) {
		t.Fatalf("allocation lost repository resolution failure: %v", err)
	}
	if _, err := r.runGo(context.Background(), ".", nil); !errors.Is(err, failure) {
		t.Fatalf("Go lost repository resolution failure: %v", err)
	}
	if _, err := r.runGit(context.Background(), ".", "merge-base", "--", "HEAD~1", "HEAD"); !errors.Is(err, failure) {
		t.Fatalf("Git lost repository resolution failure: %v", err)
	}
	if err := r.compilePackage(context.Background(), ".", "./pkg"); !errors.Is(err, failure) || len(r.allocations) != 0 {
		t.Fatalf("compile lost resolution failure or retained ownership: err=%v", err)
	}
}

func TestProofRejectsCorruptFullLengthCommit(t *testing.T) {
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		t.Fatal("invalid full-length OID reached Git")
		return nil, nil
	}}
	if _, err := r.changedFiles(context.Background(), t.TempDir(), strings.Repeat("a", 39)+"z"); err == nil {
		t.Fatal("nonhex full-length OID accepted")
	}
}

func TestProofGitResolverFailureStopsExecution(t *testing.T) {
	original := resolveGitBinaryPath
	failure := errors.New("trusted Git unavailable")
	resolveGitBinaryPath = func() (string, error) { return "", failure }
	t.Cleanup(func() { resolveGitBinaryPath = original })
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		t.Fatal("missing trusted Git reached execution")
		return nil, nil
	}}
	if _, err := r.runGit(context.Background(), t.TempDir(), "merge-base", "--", "HEAD~1", "HEAD"); !errors.Is(err, failure) {
		t.Fatalf("trusted executable failure was lost: %v", err)
	}
}
