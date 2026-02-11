package python

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestPythonDetectWithConfidenceEmptyRepoPathAndErrors(t *testing.T) {
	adapter := NewAdapter()

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.py"), []byte("import requests"), 0o600); err != nil {
		t.Fatalf("write main.py: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	detection, err := adapter.DetectWithConfidence(context.Background(), "")
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence != 35 {
		t.Fatalf("expected source-only confidence floor 35, got %#v", detection)
	}
	if len(detection.Roots) == 0 {
		t.Fatalf("expected detection roots")
	}

	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect error for non-directory repo path")
	}
}

func TestPythonAnalyseErrorBranches(t *testing.T) {
	adapter := NewAdapter()
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	rep, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repoFile, TopN: 1})
	if err != nil {
		t.Fatalf("analyse file repo path: %v", err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatalf("expected warnings for repo path without Python files")
	}

	repo := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Analyse(ctx, language.Request{RepoPath: repo, TopN: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
}

func TestPythonImportAndDependencyBranches(t *testing.T) {
	repo := t.TempDir()
	if deps := parseImports([]byte("from requests import "), "a.py", repo); len(deps) != 0 {
		t.Fatalf("expected no bindings for empty from-import symbols, got %#v", deps)
	}
	if deps := parseImportLine("os,requests", "a.py", repo, 0, "import os,requests"); len(deps) != 1 {
		t.Fatalf("expected stdlib import filtered out, got %#v", deps)
	}
	if deps := parseFromImportLine("requests", " , ", "a.py", repo, 0, "from requests import ,"); len(deps) != 0 {
		t.Fatalf("expected empty from-import symbol parts to be skipped, got %#v", deps)
	}
	if dep := dependencyFromModule(repo, ".invalid"); dep != "" {
		t.Fatalf("expected empty dependency for invalid module root, got %q", dep)
	}
	if dep := dependencyFromModule(repo, " "); dep != "" {
		t.Fatalf("expected empty dependency for blank module name, got %q", dep)
	}
}

func TestPythonWalkAndScanEntryBranches(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".venv"), 0o755); err != nil {
		t.Fatalf("mkdir .venv: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == ".venv" {
			visited := 0
			detection := &language.Detection{}
			err := walkPythonDetectionEntry(filepath.Join(repo, ".venv"), entry, map[string]struct{}{}, detection, &visited, 10)
			if !errors.Is(err, filepath.SkipDir) {
				t.Fatalf("expected filepath.SkipDir for skipped dir, got %v", err)
			}
		}
	}

	fileRepo := t.TempDir()
	source := filepath.Join(fileRepo, "mod.py")
	if err := os.WriteFile(source, []byte("import requests"), 0o600); err != nil {
		t.Fatalf("write mod.py: %v", err)
	}
	fileEntries, err := os.ReadDir(fileRepo)
	if err != nil {
		t.Fatalf("readdir fileRepo: %v", err)
	}
	for _, entry := range fileEntries {
		if entry.IsDir() {
			continue
		}
		// Force enforceRepoBoundary failure by scanning a file outside repo.
		outside := filepath.Join(t.TempDir(), "outside.py")
		if err := os.WriteFile(outside, []byte("import requests"), 0o600); err != nil {
			t.Fatalf("write outside file: %v", err)
		}
		err := scanPythonRepoEntry(fileRepo, outside, entry, &scanResult{})
		if err == nil {
			t.Fatalf("expected boundary error for outside path")
		}

		err = scanPythonRepoEntry(fileRepo, filepath.Join(fileRepo, "missing.py"), entry, &scanResult{})
		if err == nil {
			t.Fatalf("expected read error for missing python file path")
		}
	}

	// DetectWithConfidence should ignore fs.SkipAll from max file budget.
	manyFilesRepo := t.TempDir()
	for i := 0; i < 520; i++ {
		path := filepath.Join(manyFilesRepo, "f-"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), manyFilesRepo)
	if err != nil {
		t.Fatalf("detect with many files: %v", err)
	}
	if detection.Matched {
		t.Fatalf("did not expect matched detection from non-python files")
	}

	// scanRepo callback error branch (platform-dependent permissions).
	if err := os.MkdirAll(filepath.Join(manyFilesRepo, "bad"), 0o000); err != nil {
		t.Fatalf("mkdir bad dir: %v", err)
	}
	defer os.Chmod(filepath.Join(manyFilesRepo, "bad"), 0o755)
	_, err = scanRepo(context.Background(), manyFilesRepo)
	if err != nil && !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unexpected scanRepo error: %v", err)
	}
}
