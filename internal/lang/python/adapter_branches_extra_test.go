package python

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	mustWriteFile(t, filepath.Join(repo, "main.py"), "import requests")
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
	mustWriteFile(t, repoFile, "x")
	if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect error for non-directory repo path")
	}
}

func TestPythonAnalyseErrorBranches(t *testing.T) {
	adapter := NewAdapter()
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	mustWriteFile(t, repoFile, "x")
	rep, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repoFile, TopN: 1})
	if err != nil {
		t.Fatalf("analyse file repo path: %v", err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatalf("expected warnings for repo path without Python files")
	}

	repo := t.TempDir()
	ctx := canceledContext()
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
	mustWriteFile(t, source, "import requests")
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
		mustWriteFile(t, outside, "import requests")
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
	writeNumberedTextFiles(t, manyFilesRepo, 520)
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), manyFilesRepo)
	if err != nil {
		t.Fatalf("detect with many files: %v", err)
	}
	if detection.Matched {
		t.Fatalf("did not expect matched detection from non-python files")
	}

	// scanRepo error propagation branch.
	_, err = scanRepo(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected scanRepo error for missing path")
	}
}
