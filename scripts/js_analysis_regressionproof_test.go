package scripts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestJSAnalysisRejectsSymlinkedSourceOutsideRepo(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outside := t.TempDir()
	externalSource := filepath.Join(outside, "external.js")
	writeFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"fixture\",\n  \"dependencies\": {\n    \"lodash\": \"1.0.0\"\n  }\n}\n")
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", "package.json"), "{\n  \"name\": \"lodash\",\n  \"main\": \"index.js\"\n}\n")
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", "index.js"), "export function map() {}\n")
	writeFile(t, externalSource, "import { map } from \"lodash\"\nexport const used = map([1], (value) => value)\n")

	linkPath := filepath.Join(repo, "src", "app.js")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.Symlink(externalSource, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	reportData, err := analysis.NewService().Analyse(context.Background(), analysis.Request{
		RepoPath:   repo,
		Dependency: "lodash",
		Language:   "js-ts",
	})
	if err != nil {
		t.Fatalf("analyse repo with escaping source symlink: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}

	dependency := reportData.Dependencies[0]
	if dependency.Name != "lodash" {
		t.Fatalf("dependency name = %q, want lodash", dependency.Name)
	}
	if dependency.UsedExportsCount != 0 {
		t.Fatalf("expected no used exports from external symlink content, got %d with imports %#v", dependency.UsedExportsCount, dependency.UsedImports)
	}
	if len(dependency.UsedImports) != 0 {
		t.Fatalf("expected no used imports from external symlink content, got %#v", dependency.UsedImports)
	}

	assertReportLocationsStayWithinRepoRoot(t, repo, reportData)

	joinedWarnings := strings.Join(reportData.Warnings, "\n")
	for _, forbidden := range []string{outside, externalSource} {
		if strings.Contains(joinedWarnings, forbidden) {
			t.Fatalf("expected warnings to avoid external symlink paths, got %#v", reportData.Warnings)
		}
	}
}

func assertReportLocationsStayWithinRepoRoot(t *testing.T, repo string, reportData report.Report) {
	t.Helper()

	for _, dependency := range reportData.Dependencies {
		assertImportLocationsStayWithinRepoRoot(t, repo, dependency.UsedImports)
		assertImportLocationsStayWithinRepoRoot(t, repo, dependency.UnusedImports)
	}
	if reportData.UsageUncertainty != nil {
		for _, sample := range reportData.UsageUncertainty.Samples {
			assertLocationStaysWithinRepoRoot(t, repo, sample)
		}
	}
}

func assertImportLocationsStayWithinRepoRoot(t *testing.T, repo string, imports []report.ImportUse) {
	t.Helper()

	for _, imp := range imports {
		for _, location := range imp.Locations {
			assertLocationStaysWithinRepoRoot(t, repo, location)
		}
	}
}

func assertLocationStaysWithinRepoRoot(t *testing.T, repo string, location report.Location) {
	t.Helper()

	if strings.TrimSpace(location.File) == "" {
		return
	}
	if filepath.IsAbs(location.File) {
		t.Fatalf("expected report location to stay relative to repo root, got absolute path %q", location.File)
	}

	normalized := filepath.Clean(filepath.FromSlash(location.File))
	if normalized == ".." || strings.HasPrefix(normalized, ".."+string(os.PathSeparator)) {
		t.Fatalf("expected report location to stay under repo root, got %q", location.File)
	}

	joined := filepath.Join(repo, normalized)
	rel, err := filepath.Rel(repo, joined)
	if err != nil {
		t.Fatalf("rel location %q: %v", location.File, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("expected report location to stay under repo root, got %q", location.File)
	}
}
