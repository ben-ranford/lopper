package jvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestJVMAdditionalBranches(t *testing.T) {
	if got := sourceLayoutModuleRoot(filepath.FromSlash("src/main/java/Main.java")); got != "" {
		t.Fatalf("expected repo-level source layout to keep empty module root, got %q", got)
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail on invalid repo path")
	}
	if _, err := scanRepo(context.Background(), "", map[string]string{}, map[string]string{}); err == nil {
		t.Fatalf("expected scanRepo to reject empty repo path")
	}
	if _, ok := buildImportRecord([]string{"", "", "", ""}, "", "dep"); ok {
		t.Fatalf("expected import record build to fail for empty module")
	}
	if dependency := resolveDependency("com.example.deep.Type", map[string]string{"com.example": "short", "com.example.deep": "long"}, nil); dependency != "long" {
		t.Fatalf("expected longest prefix dependency, got %q", dependency)
	}
	if !shouldIgnoreImport("pkg.same.Type", "pkg.same") {
		t.Fatalf("expected same-package import to be ignored")
	}
}

func TestJVMAdditionalReachableBranches(t *testing.T) {
	t.Run("missing detection path and skipped dir helper", func(t *testing.T) {
		if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatalf("expected missing repo to fail detection walk")
		}

		repo := t.TempDir()
		skipDir := filepath.Join(repo, ".gradle")
		if err := os.MkdirAll(skipDir, 0o755); err != nil {
			t.Fatalf("mkdir skip dir: %v", err)
		}
		entries, err := os.ReadDir(repo)
		if err != nil {
			t.Fatalf("read repo dir: %v", err)
		}
		var dirEntry os.DirEntry
		for _, entry := range entries {
			if entry.Name() == ".gradle" {
				dirEntry = entry
				break
			}
		}
		if dirEntry == nil {
			t.Fatalf("expected .gradle entry")
		}
		if err := walkJVMDetectionEntry(skipDir, dirEntry, map[string]struct{}{}, &language.Detection{}, new(int), 8); !errors.Is(err, filepath.SkipDir) {
			t.Fatalf("expected detection walker to skip .gradle, got %v", err)
		}
		if _, err := scanRepo(context.Background(), repo, nil, nil); err != nil {
			t.Fatalf("scan empty repo: %v", err)
		}
	})

	t.Run("rootless source layout and custom weights", func(t *testing.T) {
		if got := sourceLayoutModuleRoot(filepath.FromSlash("/src/main/java/Main.java")); got != "" {
			t.Fatalf("expected absolute rootless source layout to stay empty, got %q", got)
		}
		custom := &report.RemovalCandidateWeights{Usage: 1, Impact: 2, Confidence: 3}
		got := resolveRemovalCandidateWeights(custom)
		if got == report.DefaultRemovalCandidateWeights() {
			t.Fatalf("expected non-nil removal weights to normalize instead of using defaults")
		}
	})
}
