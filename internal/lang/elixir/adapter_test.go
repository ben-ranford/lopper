package elixir

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	elixirFooBarDependency = "foo-bar"
	elixirAliasFooBar      = "alias Foo.Bar"
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
			assertDetectionFixture(t, tc.repo, tc.wantRootPart, tc.noRootPart)
		})
	}
}

func assertDetectionFixture(t *testing.T, repoPath string, wantRootPart string, noRootPart string) {
	t.Helper()
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repoPath)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched || detection.Confidence <= 0 {
		t.Fatalf("expected detection match with confidence, got %#v", detection)
	}
	if len(detection.Roots) == 0 || !containsSuffix(detection.Roots, wantRootPart) {
		t.Fatalf("expected root containing %q, got %#v", wantRootPart, detection.Roots)
	}
	if noRootPart != "" && containsSuffix(detection.Roots, noRootPart) {
		t.Fatalf("did not expect root containing %q, got %#v", noRootPart, detection.Roots)
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
	testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defp deps, do: [{:ecto_sql, \"~> 3.0\"}]")
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

func TestDetectWithConfidenceUmbrellaCustomAppsPath(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defmodule Demo.MixProject do\n  use Mix.Project\n  def project, do: [apps_path: \"services\"]\nend\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "services", "api", mixExsName), "defmodule Api.MixProject do\n  use Mix.Project\nend\n")
	assertDetectionFixture(t, repo, filepath.Join("services", "api"), filepath.Base(repo))
}

func TestDetectWithConfidenceIgnoresCommentedAppsPath(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defmodule Demo.MixProject do\n  use Mix.Project\n  # apps_path: \"services\"\n  def project, do: []\nend\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "services", "api", mixExsName), "defmodule Api.MixProject do\n  use Mix.Project\nend\n")
	assertDetectionFixture(t, repo, filepath.Base(repo), "")
}

func TestParseImportsAliasAsSetsLocalName(t *testing.T) {
	content := []byte("defmodule Demo do\n  alias Foo.Bar, as: Baz\n  Baz.run()\nend\n")
	declared := map[string]struct{}{"foo": {}}
	imports := parseImports(content, "lib/demo.ex", declared)
	if len(imports) != 1 {
		t.Fatalf("expected one import, got %#v", imports)
	}
	if imports[0].Local != "Baz" {
		t.Fatalf("expected alias local name Baz, got %q", imports[0].Local)
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

func TestDetectUmbrellaAppsPathBranches(t *testing.T) {
	if umbrella, appsPath := detectUmbrellaAppsPath([]byte("def project, do: []\n")); umbrella || appsPath != "" {
		t.Fatalf("expected non-umbrella config without apps_path, got umbrella=%v appsPath=%q", umbrella, appsPath)
	}
	if umbrella, appsPath := detectUmbrellaAppsPath([]byte("# apps_path: \"services\"\ndef project, do: []\n")); umbrella || appsPath != "" {
		t.Fatalf("expected commented apps_path to be ignored, got umbrella=%v appsPath=%q", umbrella, appsPath)
	}
	if umbrella, appsPath := detectUmbrellaAppsPath([]byte("def project, do: [apps_path: \"   \"]\n")); !umbrella || appsPath != "apps" {
		t.Fatalf("expected blank apps_path to fall back to apps, got umbrella=%v appsPath=%q", umbrella, appsPath)
	}
}

func TestAddUmbrellaRootsAndDependencyFallbacks(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "apps", "api", mixExsName), "defmodule Api.MixProject do\nend\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "apps", "README.md"), "not a mix app\n")

	roots := map[string]struct{}{}
	if err := addUmbrellaRoots(repo, "apps", roots); err != nil {
		t.Fatalf("add umbrella roots: %v", err)
	}
	if _, ok := roots[filepath.Join(repo, "apps", "api")]; !ok {
		t.Fatalf("expected api umbrella root, got %#v", roots)
	}
	if dep := dependencyFromModule("", map[string]struct{}{elixirFooBarDependency: {}}); dep != "" {
		t.Fatalf("expected empty dependency for empty module, got %q", dep)
	}
	if dep := dependencyFromModule("FooBar.Client", map[string]struct{}{elixirFooBarDependency: {}}); dep != elixirFooBarDependency {
		t.Fatalf("expected hyphenated dependency fallback, got %q", dep)
	}
}

func TestAddUmbrellaRootsRejectsInvalidGlobPattern(t *testing.T) {
	if addUmbrellaRoots(t.TempDir(), "[", map[string]struct{}{}) == nil {
		t.Fatalf("expected invalid glob pattern error")
	}
}

func TestDetectFromRootFilesReturnsStatErrorForInvalidRepoPath(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "not-a-dir")
	testutil.MustWriteFile(t, repoFile, "x")

	_, err := detectFromRootFiles(repoFile, &language.Detection{}, map[string]struct{}{})
	if err == nil {
		t.Fatalf("expected detectFromRootFiles to fail for non-directory repo path")
	}
}

func TestLineBytesAndParseAliasLocalEdgeCases(t *testing.T) {
	if got := lineBytes([]byte(elixirAliasFooBar+"\n"), -1); len(got) != 0 {
		t.Fatalf("expected nil line bytes for negative start, got %q", string(got))
	}
	if got := lineBytes([]byte(elixirAliasFooBar), 100); len(got) != 0 {
		t.Fatalf("expected nil line bytes for out-of-range start, got %q", string(got))
	}
	if got := parseAliasLocal([]byte(elixirAliasFooBar)); got != "" {
		t.Fatalf("expected no alias local without as:, got %q", got)
	}
}

func TestLoadDeclaredDependenciesReturnsReadErrors(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, mixLockName), 0o755); err != nil {
		t.Fatalf("mkdir mix.lock: %v", err)
	}

	if _, err := loadDeclaredDependencies(repo); err == nil {
		t.Fatalf("expected loadDeclaredDependencies to fail when mix.lock is a directory")
	}
}

func TestBuildRequestedDependenciesWarnsWhenDependencyHasNoImports(t *testing.T) {
	dependencies, warnings := buildRequestedDependencies(language.Request{Dependency: "ecto"}, scanResult{
		declared: map[string]struct{}{"ecto": {}},
	})
	if len(dependencies) != 1 || dependencies[0].Name != "ecto" {
		t.Fatalf("expected one dependency report, got %#v", dependencies)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no imports found") {
		t.Fatalf("expected no-import warning, got %#v", warnings)
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

func TestElixirAdditionalReachableBranches(t *testing.T) {
	t.Run("detect root file read and walk errors", testElixirDetectRootFileReadAndWalkErrors)
	t.Run("scan repo unreadable source and helper edges", testElixirScanRepoUnreadableSourceAndHelperEdges)
	t.Run("declared dependency read branches", testElixirDeclaredDependencyReadBranches)
}

func testElixirDetectRootFileReadAndWalkErrors(t *testing.T) {
	repo := t.TempDir()
	mixExs := filepath.Join(repo, mixExsName)
	if err := os.WriteFile(mixExs, []byte("defmodule Demo.MixProject do end\n"), 0o000); err != nil {
		t.Fatalf("write unreadable mix.exs: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(mixExs, 0o600); err != nil {
			t.Fatalf("restore mix.exs perms: %v", err)
		}
	})
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), repo); err == nil {
		t.Fatalf("expected unreadable root mix.exs to fail detection")
	}
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection walk")
	}
}

func testElixirScanRepoUnreadableSourceAndHelperEdges(t *testing.T) {
	repo := t.TempDir()
	unreadable := filepath.Join(repo, "lib", "demo.ex")
	testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defmodule Demo.MixProject do end\n")
	if err := os.MkdirAll(filepath.Dir(unreadable), 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	if err := os.WriteFile(unreadable, []byte(elixirAliasFooBar+"\n"), 0o000); err != nil {
		t.Fatalf("write unreadable source: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadable, 0o600); err != nil {
			t.Fatalf("restore unreadable source perms: %v", err)
		}
	})

	if _, err := scanElixirRepo(context.Background(), repo, map[string]struct{}{"foo": {}}); err == nil {
		t.Fatalf("expected unreadable source file to fail scan")
	}
	if got := string(lineBytes([]byte(elixirAliasFooBar), 0)); got != elixirAliasFooBar {
		t.Fatalf("expected lineBytes without newline to return remaining content, got %q", got)
	}
}

func testElixirDeclaredDependencyReadBranches(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, mixExsName), 0o755); err != nil {
		t.Fatalf("mkdir mix.exs: %v", err)
	}
	if _, err := loadDeclaredDependencies(repo); err == nil {
		t.Fatalf("expected loadDeclaredDependencies to fail when mix.exs is a directory")
	}
	if dep := dependencyFromModule("FooBar.Client", map[string]struct{}{elixirFooBarDependency: {}}); dep != elixirFooBarDependency {
		t.Fatalf("expected direct normalized dependency match, got %q", dep)
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
