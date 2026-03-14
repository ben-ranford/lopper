package python

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestPythonAdditionalHelperBranches(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	if err := applyPythonRootSignals(repoFile, &language.Detection{}, map[string]struct{}{}); err == nil {
		t.Fatalf("expected root signal stat error for non-directory repo path")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail on invalid repo path")
	}
	if _, err := scanRepo(context.Background(), ""); err == nil {
		t.Fatalf("expected scanRepo to reject empty repo path")
	}

	if module, local := parseImportPart("   "); module != "" || local != "" {
		t.Fatalf("expected blank import part to stay empty, got module=%q local=%q", module, local)
	}
	if dependency := dependencyFromModule(t.TempDir(), ""); dependency != "" {
		t.Fatalf("expected blank dependency module to resolve empty, got %q", dependency)
	}

	localRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localRepo, "localpkg"), 0o755); err != nil {
		t.Fatalf("mkdir local package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localRepo, "localpkg", "__init__.py"), []byte(""), 0o600); err != nil {
		t.Fatalf("write __init__.py: %v", err)
	}
	if dependency := dependencyFromModule(localRepo, "localpkg.module"); dependency != "" {
		t.Fatalf("expected local package import to resolve empty, got %q", dependency)
	}
}

func TestPythonRequestedDependencyAndWildcardRecommendationBranches(t *testing.T) {
	scan := scanResult{
		Files: []fileScan{{
			Imports: []importBinding{{
				Dependency: "dep",
				Module:     "dep",
				Name:       "*",
				Local:      "*",
				Wildcard:   true,
			}},
			Usage: map[string]int{"*": 1},
		}},
	}

	dependencies, warnings := buildRequestedPythonDependencies(language.Request{Dependency: "dep"}, scan)
	if len(dependencies) != 1 || len(warnings) != 0 {
		t.Fatalf("unexpected dependency report selection: deps=%#v warnings=%#v", dependencies, warnings)
	}
	if len(dependencies[0].RiskCues) == 0 || len(dependencies[0].Recommendations) == 0 {
		t.Fatalf("expected wildcard risk cues and recommendations, got %#v", dependencies[0])
	}
}

func TestPythonAdditionalDetectionAndParserBranches(t *testing.T) {
	t.Run("detect with missing repo returns walk error", func(t *testing.T) {
		_, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing"))
		if err == nil {
			t.Fatalf("expected missing repo to fail detection walk")
		}
	})

	t.Run("analyse empty repo path fails when cwd is gone", func(t *testing.T) {
		originalWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chdir(originalWD); err != nil {
				t.Fatalf("restore wd %s: %v", originalWD, err)
			}
		})

		deadDir := filepath.Join(t.TempDir(), "dead")
		if err := os.MkdirAll(deadDir, 0o755); err != nil {
			t.Fatalf("mkdir dead dir: %v", err)
		}
		if err := os.Chdir(deadDir); err != nil {
			t.Fatalf("chdir dead dir: %v", err)
		}
		if err := os.RemoveAll(deadDir); err != nil {
			t.Fatalf("remove dead dir: %v", err)
		}

		if _, err := NewAdapter().Analyse(context.Background(), language.Request{}); err == nil {
			t.Fatalf("expected analyse to fail when cwd cannot be resolved")
		}
	})

	t.Run("skip dir helpers and default weights", func(t *testing.T) {
		repo := t.TempDir()
		skipDir := filepath.Join(repo, ".venv")
		if err := os.MkdirAll(skipDir, 0o755); err != nil {
			t.Fatalf("mkdir skip dir: %v", err)
		}
		entries, err := os.ReadDir(repo)
		if err != nil {
			t.Fatalf("read repo dir: %v", err)
		}
		var dirEntry os.DirEntry
		for _, entry := range entries {
			if entry.Name() == ".venv" {
				dirEntry = entry
				break
			}
		}
		if dirEntry == nil {
			t.Fatalf("expected .venv entry")
		}

		if err := walkPythonDetectionEntry(skipDir, dirEntry, map[string]struct{}{}, &language.Detection{}, new(int), 8); err != filepath.SkipDir {
			t.Fatalf("expected python detection walker to skip .venv, got %v", err)
		}
		if err := scanPythonRepoEntry(repo, skipDir, dirEntry, &scanResult{}); err != filepath.SkipDir {
			t.Fatalf("expected python scanner to skip .venv, got %v", err)
		}
		if got := resolveRemovalCandidateWeights(nil); got != report.DefaultRemovalCandidateWeights() {
			t.Fatalf("expected default weights, got %#v", got)
		}
		customWeights := &report.RemovalCandidateWeights{Usage: 4, Impact: 2, Confidence: 2}
		if got := resolveRemovalCandidateWeights(customWeights); got != report.NormalizeRemovalCandidateWeights(*customWeights) {
			t.Fatalf("expected custom weights to be normalized, got %#v", got)
		}
	})

	t.Run("parser helper skip branches", func(t *testing.T) {
		repo := t.TempDir()
		imports := parseImportLine("requests, numpy as np", "main.py", repo, 0, "import requests, numpy as np")
		if len(imports) != 2 || imports[0].Local != "requests" || imports[1].Local != "np" {
			t.Fatalf("expected import parsing to default locals and preserve aliases, got %#v", imports)
		}

		fromImports := parseFromImportLine("requests", "get", "main.py", repo, 0, "from requests import get")
		if len(fromImports) != 1 || fromImports[0].Name != "get" || fromImports[0].Local != "get" {
			t.Fatalf("expected from-import local to default to symbol, got %#v", fromImports)
		}

		if dependency := dependencyFromModule(repo, "."); dependency != "" {
			t.Fatalf("expected empty root module to resolve empty, got %q", dependency)
		}
	})
}
