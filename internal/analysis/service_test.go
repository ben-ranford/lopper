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

const (
	programFileName           = "Program.cs"
	newtonsoftDependencyID    = "newtonsoft.json"
	expectedOneDependencyText = "expected one dependency report, got %d"
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
	writeFile(t, filepath.Join(repo, "composer.json"), "{\n  \"require\": {\n    \"monolog/monolog\": \"^3.0\"\n  }\n}\n")
	writeFile(t, filepath.Join(repo, "composer.lock"), "{\n  \"packages\": [\n    {\n      \"name\": \"monolog/monolog\",\n      \"autoload\": {\n        \"psr-4\": {\n          \"Monolog\\\\\": \"src/Monolog\"\n        }\n      }\n    }\n  ]\n}\n")
	writeFile(t, filepath.Join(repo, "index.php"), "<?php\nuse Monolog\\Logger;\n$logger = new Logger(\"app\");\n")
	writeFile(t, filepath.Join(repo, "Cargo.toml"), "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n\n[dependencies]\nanyhow = \"1.0\"\n")
	writeFile(t, filepath.Join(repo, "src", "lib.rs"), "use anyhow::Result;\npub fn run() -> Result<()> { Ok(()) }\n")
	writeFile(t, filepath.Join(repo, "App.csproj"), "<Project Sdk=\"Microsoft.NET.Sdk\"><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
	writeFile(t, filepath.Join(repo, programFileName), "using JsonConvert = Newtonsoft.Json.JsonConvert;\npublic class Program { public static void Main() { _ = JsonConvert.SerializeObject(new { V = 1 }); } }\n")

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
	if !slices.Contains(languages, "js-ts") || !slices.Contains(languages, "python") || !slices.Contains(languages, "jvm") || !slices.Contains(languages, "go") || !slices.Contains(languages, "php") || !slices.Contains(languages, "rust") || !slices.Contains(languages, "dotnet") {
		t.Fatalf("expected js-ts, python, jvm, go, php, rust, and dotnet dependencies, got %#v", languages)
	}
	if len(reportData.LanguageBreakdown) < 7 {
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

func TestServiceAnalyseCSharpAlias(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Api.csproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`)
	writeFile(t, filepath.Join(repo, programFileName), `
using JsonConvert = Newtonsoft.Json.JsonConvert;

public class Program {
  public static void Main() {
    _ = JsonConvert.SerializeObject(new { Name = "demo" });
  }
}
`)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: newtonsoftDependencyID,
		Language:   "csharp",
	})
	if err != nil {
		t.Fatalf("analyse csharp alias: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "dotnet" {
		t.Fatalf("expected language dotnet, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
}

func TestServiceForwardsMinUsageThresholdToDotNet(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "App.csproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`)
	writeFile(t, filepath.Join(repo, programFileName), `
using Newtonsoft.Json;
public class Program { public static void Main() {} }
`)

	service := NewService()
	withDefault, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: newtonsoftDependencyID,
		Language:   "dotnet",
	})
	if err != nil {
		t.Fatalf("analyse with default threshold: %v", err)
	}
	if len(withDefault.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(withDefault.Dependencies))
	}
	if !hasRecommendationCode(withDefault.Dependencies[0], "reduce-low-usage-package-surface") {
		t.Fatalf("expected low-usage recommendation with default threshold")
	}

	zero := 0
	withZero, err := service.Analyse(context.Background(), Request{
		RepoPath:                          repo,
		Dependency:                        newtonsoftDependencyID,
		Language:                          "dotnet",
		MinUsagePercentForRecommendations: &zero,
	})
	if err != nil {
		t.Fatalf("analyse with zero threshold: %v", err)
	}
	if len(withZero.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(withZero.Dependencies))
	}
	if hasRecommendationCode(withZero.Dependencies[0], "reduce-low-usage-package-surface") {
		t.Fatalf("did not expect low-usage recommendation when threshold is 0")
	}
}

func hasRecommendationCode(dep report.DependencyReport, code string) bool {
	for _, rec := range dep.Recommendations {
		if rec.Code == code {
			return true
		}
	}
	return false
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
