package elixir

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestDetectWithConfidenceUmbrellaRoots(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "testdata", "elixir", "umbrella")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect elixir: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected detection to match")
	}
	apiRoot := filepath.Join(repo, "apps", "api")
	if !slices.Contains(detection.Roots, apiRoot) {
		t.Fatalf("expected umbrella app root in detection roots, got %#v", detection.Roots)
	}
	if slices.Contains(detection.Roots, repo) {
		t.Fatalf("did not expect umbrella root in detection roots: %#v", detection.Roots)
	}
}

func TestAnalyseDependencyFixture(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "testdata", "elixir", "mix")
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "jason",
	})
	if err != nil {
		t.Fatalf("analyse fixture: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "elixir" {
		t.Fatalf("expected language elixir, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used imports > 0")
	}
}

func TestAnalyseTopNFixture(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "testdata", "elixir", "mix")
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse top fixture: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in top report")
	}
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "jason") {
		t.Fatalf("expected jason in dependencies, got %#v", names)
	}
}
