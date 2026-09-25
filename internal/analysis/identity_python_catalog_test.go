package analysis

import (
	"context"
	"encoding/json"
	"errors"
	pythonlang "github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonAdapterCatalogEnrichesWithoutReopeningManifests(t *testing.T) {
	repo := t.TempDir()
	files := map[string]string{
		"pyproject.toml": "[project]\ndependencies=['runtime-only>=1']\n[project.optional-dependencies]\ndocs=['requests==2.32.3','optional-only==1.0']\n",
		"main.py":        "import requests\nprint(requests)\n",
		"poetry.lock":    "[[package]]\nname='runtime-only'\nversion='4.0'\n",
	}
	for name, content := range files {
		testutil.MustWriteFile(t, filepath.Join(repo, name), content)
	}
	result, err := pythonlang.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10, Features: mustResolveDependencyIdentityPreviewFeatureSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PythonManifestCatalog || len(result.PythonManifests) != 2 {
		t.Fatalf("missing catalog: %#v", result.PythonManifests)
	}
	// Cache transport must retain internal artifacts without adding public JSON fields.
	public, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(public)), "pythonmanifest") {
		t.Fatal("internal catalog leaked into report JSON")
	}
	cache, entry := cacheWithPayloadForLookupTest(t, newCachedPayload(result), "python-catalog")
	cached, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("cache lookup: hit=%v err=%v", hit, err)
	}
	for name := range files {
		if err := os.Remove(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	result = mergeReports(repo, []report.Report{cached})
	annotateDependencyIdentities(repo, &result)
	for _, dep := range result.Dependencies {
		if dep.Name == "optional-only" {
			t.Fatal("optional evidence added inventory")
		}
	}
	assertIdentity(t, findIdentityDependency(t, result, "python", "requests"), report.DependencyIdentity{Ecosystem: "pypi", Name: "requests", Version: "2.32.3", VersionStatus: identityStatusDeclared, PURL: "pkg:pypi/requests@2.32.3", PURLStatus: identityStatusResolved, Source: "pyproject.toml", Confidence: "high"})
	assertIdentity(t, findIdentityDependency(t, result, "python", "runtime-only"), report.DependencyIdentity{Ecosystem: "pypi", Name: "runtime-only", Version: "4.0", VersionStatus: identityStatusResolved, PURL: "pkg:pypi/runtime-only@4.0", PURLStatus: identityStatusResolved, Source: "poetry.lock", Confidence: "high"})
}

func TestPythonCatalogPathsUseAnalysisRoot(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	input := report.Report{RepoPath: filepath.Join(root, "nested"), PythonManifestCatalog: true,
		PythonManifests: []report.PythonManifestDocument{{Path: "pyproject.toml", Document: map[string]any{
			"project": map[string]any{"dependencies": []any{"requests==2.32.3"}},
		}}}, Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}
	merged := mergeReportsWithIdentityRoot(repo, root, []report.Report{input})
	annotateDependencyIdentities(root, &merged)
	dep := findIdentityDependency(t, merged, "python", "requests")
	if dep.Identity.Source != "nested/pyproject.toml" || input.PythonManifests[0].Path != "pyproject.toml" {
		t.Fatalf("wrong catalog remapping: %#v", dep.Identity)
	}
}

func TestIdentityDiscoveryLimitAndCancellation(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"a", "b", "ignored/c"} {
		testutil.MustWriteFile(t, filepath.Join(repo, name), "content")
	}
	warnings := newIdentityWarningCollector(repo)
	visited := 0
	visit := func(string, fs.DirEntry, error) error { visited++; return nil }
	if err := walkIdentityFilesWithinLimit(context.Background(), repo, warnings, 1, nil, visit); err != nil {
		t.Fatal(err)
	}
	if visited != 1 || len(warnings.list()) != 1 || !strings.Contains(warnings.list()[0], "exceeds 1 files") {
		t.Fatalf("limit evidence: visited=%d warnings=%v", visited, warnings.list())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := walkIdentityFilesWithinLimit(ctx, repo, warnings, 10, nil, visit); !errors.Is(err, context.Canceled) || visited != 1 {
		t.Fatalf("cancellation: visited=%d err=%v", visited, err)
	}
	if _, err := finalizeReportWithContext(ctx, Request{}, repo, repo, nil, report.Report{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("finalization ignored cancellation: %v", err)
	}
}

func TestPipfileCatalogSectionValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		packages map[string]any
		valid    bool
	}{
		{"nil package", map[string]any{"requests": nil}, true},
		{"nil version", map[string]any{"requests": map[string]any{"version": nil}}, true},
		{"string version", map[string]any{"requests": map[string]any{"version": "==1.0"}}, true},
		{"invalid package", map[string]any{"requests": "1.0"}, false},
		{"invalid version", map[string]any{"requests": map[string]any{"version": 1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPipfileIdentitySection(tc.packages); got != tc.valid {
				t.Fatalf("valid=%v, want %v", got, tc.valid)
			}
		})
	}
}

func TestPythonCatalogPreservesIdentityEvidenceAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, content, version, warning string
	}{
		{"Pipfile", "[packages]\nrequests='==2.32.3'", "2.32.3", ""},
		{"Pipfile.lock", `{"default":{"requests":{"version":"==2.32.3"}}}`, "2.32.3", ""},
		{"requirements.txt", "requests==2.32.3\n", "2.32.3", ""},
		{"uv.lock", "[[package]]\nname='requests'\nversion='2.32.3'", "2.32.3", ""},
		{"pyproject.toml", "[invalid", "", "invalid TOML"},
		{"Pipfile.lock", `{"default":"invalid"}`, "", "default section"},
		{"Pipfile.lock", `{"default":{"requests":{"version":42}}}`, "", "default section"},
	} {
		t.Run(tc.name+tc.content, func(t *testing.T) {
			assertPythonCatalogIdentityEvidence(t, tc.name, tc.content, tc.version, tc.warning)
		})
	}
	for _, kind := range []string{"permission", "missing", "large", "other"} {
		t.Run(kind, func(t *testing.T) {
			repo := t.TempDir()
			warnings := newIdentityWarningCollector(repo)
			collectPythonCatalogEvidence(repo, make(identityIndex), []report.PythonManifestDocument{{Path: "requirements.txt", Failure: "failed", FailureKind: kind, FailureStage: "read"}}, warnings)
			if len(warnings.list()) != 1 {
				t.Fatalf("warnings: %v", warnings.list())
			}
		})
	}
}

func assertPythonCatalogIdentityEvidence(t *testing.T, name, content, version, warning string) {
	t.Helper()
	repo := t.TempDir()
	path := filepath.Join(repo, name)
	testutil.MustWriteFile(t, path, content)
	document, readErr := pythonlang.ReadPackagingDocument(repo, path)
	if readErr != nil && warning == "" {
		t.Fatal(readErr)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	result := report.Report{PythonManifestCatalog: true, PythonManifests: []report.PythonManifestDocument{document}, Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}
	annotateDependencyIdentities(repo, &result)
	dep := findIdentityDependency(t, result, "python", "requests")
	if dep.Identity.Version != version {
		t.Fatalf("identity: %#v", dep.Identity)
	}
	if warning != "" && !strings.Contains(strings.Join(result.Warnings, "\n"), warning) {
		t.Fatalf("warnings: %v", result.Warnings)
	}
}

func TestIdentityEvidenceRequestCancellationStopsDiscovery(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "requirements.txt"), "requests==2.32.3\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "node_modules", "example", "package.json"), `{"name":"example","version":"1.0.0"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	index, warnings := collectIdentityEvidenceWithContext(ctx, repo, identityEvidenceLanguages{python: true, dotnet: true, pub: true, ruby: true, elixir: true})
	if len(index) != 0 || len(warnings) == 0 {
		t.Fatalf("cancelled discovery read evidence: index=%v warnings=%v", index, warnings)
	}
}

func TestPythonCatalogScopedCacheHitKeepsIdentitySource(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pkg", "pyproject.toml"), "[project]\ndependencies=['requests==2.32.3']\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "pkg", "main.py"), "import requests\nrequests.get('https://example.test')\n")
	request := Request{RepoPath: repo, Language: "python", TopN: 10, IncludePatterns: []string{"pkg/**"}, Features: mustResolveDependencyIdentityPreviewFeatureSet(t), Cache: &CacheOptions{Enabled: true, Path: t.TempDir()}}
	service := NewService()
	first, err := service.Analyse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Analyse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Cache == nil || second.Cache.Hits != 1 {
		t.Fatalf("expected cache hit: %+v", second.Cache)
	}
	before := findIdentityDependency(t, first, "python", "requests").Identity
	after := findIdentityDependency(t, second, "python", "requests").Identity
	if before.Source != "pkg/pyproject.toml" || after.Source != before.Source || after.Version != before.Version {
		t.Fatalf("scoped cache changed identity: before=%+v after=%+v", before, after)
	}
}

func TestPythonCatalogDeferredIdentityEvidence(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pyproject.toml"), "[project]\ndependencies=['requests==2.32.3']\n")
	result := report.Report{PythonManifestCatalog: true, PythonManifests: []report.PythonManifestDocument{
		{Path: "pyproject.toml", Deferred: true},
		{Path: "missing/requirements.txt", Deferred: true},
	}, Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}
	payload, err := json.Marshal(result.PythonManifests)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &result.PythonManifests); err != nil {
		t.Fatal(err)
	}
	annotateDependencyIdentities(repo, &result)
	if result.Dependencies[0].Identity == nil || result.Dependencies[0].Identity.Version != "2.32.3" {
		t.Fatalf("deferred evidence lost: %+v", result.Dependencies)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected missing deferred document warning")
	}
}

func TestIdentityDiscoveryContinuesAfterUnreadableDirectory(t *testing.T) {
	repo := t.TempDir()
	blocked := filepath.Join(repo, "a-blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-readable", "go.mod"), "module example.com/app\n")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blocked, 0o700); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.ReadDir(blocked); err == nil {
		t.Skip("filesystem does not enforce directory permissions")
	}
	warnings := newIdentityWarningCollector(repo)
	snapshot := discoverIdentityManifestSnapshotWithContext(context.Background(), repo, warnings)
	if len(snapshot.goModFiles) != 1 {
		t.Fatalf("sibling discovery stopped: %+v", snapshot)
	}
	if messages := warnings.list(); len(messages) != 1 || !strings.Contains(messages[0], "a-blocked") {
		t.Fatalf("expected path-specific discovery warning: %v", messages)
	}
}
