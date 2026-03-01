package elixir

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func fixturePath(parts ...string) string {
	base := []string{"..", "..", "..", "testdata", "elixir"}
	return filepath.Join(append(base, parts...)...)
}

func TestDetectWithConfidenceFixtures(t *testing.T) {
	tests := []struct {
		name         string
		repo         string
		wantRootPart string
		noRootPart   string
	}{
		{name: "mix", repo: fixturePath("mix"), wantRootPart: filepath.Join("testdata", "elixir", "mix")},
		{
			name:         "umbrella",
			repo:         fixturePath("umbrella"),
			wantRootPart: filepath.Join("umbrella", "apps", "api"),
			noRootPart:   filepath.Join("testdata", "elixir", "umbrella"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detection, err := NewAdapter().DetectWithConfidence(context.Background(), tc.repo)
			if err != nil {
				t.Fatalf("detect: %v", err)
			}
			if !detection.Matched || detection.Confidence <= 0 {
				t.Fatalf("expected detection match with confidence, got %#v", detection)
			}
			if len(detection.Roots) == 0 || !containsSuffix(detection.Roots, tc.wantRootPart) {
				t.Fatalf("expected root containing %q, got %#v", tc.wantRootPart, detection.Roots)
			}
			if tc.noRootPart != "" && containsSuffix(detection.Roots, tc.noRootPart) {
				t.Fatalf("did not expect root containing %q, got %#v", tc.noRootPart, detection.Roots)
			}
		})
	}
}

func TestAnalyseFixtureDependencyAndTopN(t *testing.T) {
	repo := fixturePath("mix")
	adapter := NewAdapter()
	byDependency, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "jason",
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(byDependency.Dependencies) != 1 {
		t.Fatalf("expected one dependency, got %d", len(byDependency.Dependencies))
	}
	if byDependency.Dependencies[0].Language != "elixir" || byDependency.Dependencies[0].UsedExportsCount == 0 {
		t.Fatalf("expected used elixir dependency row, got %#v", byDependency.Dependencies[0])
	}

	top, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("analyse top: %v", err)
	}
	names := make([]string, 0, len(top.Dependencies))
	for _, dep := range top.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "jason") || !slices.Contains(names, "nimble-csv") {
		t.Fatalf("expected jason and nimble-csv in top dependencies, got %#v", names)
	}
}

func TestAdapterIdentityAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "elixir" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if got := adapter.Aliases(); !slices.Equal(got, []string{"ex", "mix"}) {
		t.Fatalf("unexpected aliases: %#v", got)
	}
	matched, err := adapter.Detect(context.Background(), fixturePath("mix"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !matched {
		t.Fatalf("expected fixture to match elixir adapter")
	}
}

func TestLoadDeclaredDependenciesAndHelpers(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "mix.exs"), "defp deps, do: [{:ecto_sql, \"~> 3.0\"}]")
	declared, err := loadDeclaredDependencies(repo)
	if err != nil {
		t.Fatalf("load deps: %v", err)
	}
	if _, ok := declared["ecto-sql"]; !ok {
		t.Fatalf("expected ecto-sql in declared deps, got %#v", declared)
	}
	if got := camelToSnake("PhoenixHTML"); got != "phoenix_html" {
		t.Fatalf("unexpected snake case value: %q", got)
	}
	if dep := dependencyFromModule("Ecto.Changeset", declared); dep != "ecto-sql" && dep != "" {
		t.Fatalf("unexpected dependency resolution: %q", dep)
	}
	if !shouldSkipDir("_build") || shouldSkipDir("lib") {
		t.Fatalf("unexpected skip-dir behavior")
	}
}

func TestResolveWeights(t *testing.T) {
	defaults := resolveWeights(nil)
	if defaults != report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected default weights, got %#v", defaults)
	}
	custom := report.RemovalCandidateWeights{Usage: 0.6, Impact: 0.2, Confidence: 0.2}
	got := resolveWeights(&custom)
	if got != report.NormalizeRemovalCandidateWeights(custom) {
		t.Fatalf("expected normalized custom weights, got %#v", got)
	}
}

func TestDetectReturnsErrorOnMissingRepo(t *testing.T) {
	adapter := NewAdapter()
	matched, err := adapter.Detect(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected detect error for missing repo path")
	}
	if matched {
		t.Fatalf("expected missing repo not to match")
	}
}

func TestAnalyseReturnsErrorOnMissingRepo(t *testing.T) {
	adapter := NewAdapter()
	_, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath: filepath.Join(t.TempDir(), "missing"),
		TopN:     1,
	})
	if err == nil {
		t.Fatalf("expected analyse error for missing repo path")
	}
}

func containsSuffix(values []string, suffix string) bool {
	for _, value := range values {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}
