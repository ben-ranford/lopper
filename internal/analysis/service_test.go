package analysis

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestServiceAnalyseAllLanguages(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	writeFile(t, filepath.Join(repo, "index.js"), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", "package.json"), "{\n  \"main\": \"index.js\"\n}\n")
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", "index.js"), "export function map() {}\n")
	writeFile(t, filepath.Join(repo, "main.py"), "import requests\nrequests.get('https://example.test')\n")
	writeFile(t, filepath.Join(repo, "build.gradle"), "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
	writeFile(t, filepath.Join(repo, "src", "test", "java", "ExampleTest.java"), "import org.junit.jupiter.api.Test;\nclass ExampleTest {}\n")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/demo\n\nrequire github.com/google/uuid v1.6.0\n")
	writeFile(t, filepath.Join(repo, "main.go"), "package main\n\nimport \"github.com/google/uuid\"\n\nfunc main() { _ = uuid.NewString() }\n")

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath: repo,
		TopN:     10,
		Language: "all",
	})
	if err != nil {
		t.Fatalf("analyse all: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in report")
	}
	languages := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		languages = append(languages, dep.Language)
	}
	if !slices.Contains(languages, "js-ts") || !slices.Contains(languages, "python") || !slices.Contains(languages, "jvm") || !slices.Contains(languages, "go") {
		t.Fatalf("expected js-ts, python, jvm, and go dependencies, got %#v", languages)
	}
	if len(reportData.LanguageBreakdown) < 4 {
		t.Fatalf("expected language breakdown for multiple adapters, got %#v", reportData.LanguageBreakdown)
	}
}

func TestMergeRecommendationsPriorityOrder(t *testing.T) {
	left := []report.Recommendation{
		{Code: "consider-replacement", Priority: "low"},
	}
	right := []report.Recommendation{
		{Code: "prefer-subpath-imports", Priority: "medium"},
		{Code: "remove-unused-dependency", Priority: "high"},
	}

	merged := mergeRecommendations(left, right)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged recommendations, got %d", len(merged))
	}
	got := []string{
		merged[0].Code,
		merged[1].Code,
		merged[2].Code,
	}
	want := []string{
		"remove-unused-dependency",
		"prefer-subpath-imports",
		"consider-replacement",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected recommendation order: got %#v want %#v", got, want)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLowConfidenceWarningThreshold(t *testing.T) {
	candidate := language.Candidate{
		Adapter:   nil,
		Detection: language.Detection{Confidence: 30},
	}
	candidate.Adapter = stubAdapter{id: "js-ts"}

	warnings := lowConfidenceWarning("all", candidate, 40)
	if len(warnings) == 0 {
		t.Fatalf("expected warning for confidence below threshold")
	}

	warnings = lowConfidenceWarning("all", candidate, 20)
	if len(warnings) != 0 {
		t.Fatalf("expected no warning when threshold is lower than confidence")
	}
}

type stubAdapter struct {
	id string
}

func (s stubAdapter) ID() string { return s.id }

func (s stubAdapter) Aliases() []string { return nil }

func (s stubAdapter) Detect(context.Context, string) (bool, error) { return true, nil }

func (s stubAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	return report.Report{}, nil
}
