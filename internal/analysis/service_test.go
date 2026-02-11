package analysis

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
	if !slices.Contains(languages, "js-ts") || !slices.Contains(languages, "python") || !slices.Contains(languages, "jvm") {
		t.Fatalf("expected js-ts, python, and jvm dependencies, got %#v", languages)
	}
	if len(reportData.LanguageBreakdown) < 3 {
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
