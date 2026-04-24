package swift

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftCarthageLoadersHandleMissingMalformedAndEmptyFiles(t *testing.T) {
	catalog := newSwiftCoverageCatalog()
	repo := t.TempDir()
	assertMissingCarthageLoader(t, repo, &catalog, loadCarthageManifestData)
	assertCarthageLoaderRejectsDirectory(t, repo, carthageManifestName, &catalog, loadCarthageManifestData)

	manifestPath := filepath.Join(repo, carthageManifestName)
	testutil.MustWriteFile(t, manifestPath, "\n# comment only\n")
	found, warnings, err := loadCarthageManifestData(repo, &catalog)
	if err != nil || !found || len(warnings) == 0 || !strings.Contains(warnings[0], "no Carthage declarations") {
		t.Fatalf("expected empty Cartfile warning, found=%v warnings=%#v err=%v", found, warnings, err)
	}

	assertMissingCarthageLoader(t, repo, &catalog, loadCarthageResolvedData)
	assertCarthageLoaderRejectsDirectory(t, repo, carthageResolvedName, &catalog, loadCarthageResolvedData)
	resolvedPath := filepath.Join(repo, carthageResolvedName)
	testutil.MustWriteFile(t, resolvedPath, "github \"ReactiveX/RxSwift\"\n")
	found, warnings, err = loadCarthageResolvedData(repo, &catalog)
	if err != nil || !found || len(warnings) == 0 || !strings.Contains(warnings[0], "no Carthage entries") {
		t.Fatalf("expected unresolved Cartfile.resolved warning, found=%v warnings=%#v err=%v", found, warnings, err)
	}
}

func TestSwiftCarthageParserRejectsMalformedLines(t *testing.T) {
	if _, _, ok := splitCarthageLinePrefix("github"); ok {
		t.Fatalf("expected splitCarthageLinePrefix to reject lines without quoted source")
	}
	if _, _, ok := splitCarthageLinePrefix("  "); ok {
		t.Fatalf("expected splitCarthageLinePrefix to reject blank lines")
	}
	if _, _, ok := readQuotedValue(`plain`); ok {
		t.Fatalf("expected unquoted value parse to fail")
	}
	if _, _, ok := readQuotedValue(`"unterminated`); ok {
		t.Fatalf("expected unterminated quote parse to fail")
	}
	if _, ok := parseCarthageLine(`unknown "owner/repo" "1.0.0"`, false); ok {
		t.Fatalf("expected unsupported Carthage kind to be ignored")
	}
	if _, ok := parseCarthageLine(`github owner/repo "1.0.0"`, false); ok {
		t.Fatalf("expected unquoted source to be ignored")
	}
	if _, ok := parseCarthageLine(`github "owner/repo"`, true); ok {
		t.Fatalf("expected missing required reference to be ignored")
	}
}

func TestSwiftCarthageParserHandlesEscapesAndDependencies(t *testing.T) {
	if value, _, ok := readQuotedValue(`"value\"with\"escapes" trailing`); !ok || value != `value"with"escapes` {
		t.Fatalf("expected quoted value with escapes to parse, got value=%q ok=%v", value, ok)
	}
	if entry, ok := parseCarthageLine(`binary "https://example.com/FancyKit.json" "1.2.3"`, true); !ok || entry.Dependency != "fancykit" {
		t.Fatalf("expected binary Carthage entry to parse with normalized dependency, got %#v ok=%v", entry, ok)
	}
	if got := deriveCarthageDependencyID("github", ""); got != "" {
		t.Fatalf("expected empty github source to produce empty dependency id, got %q", got)
	}
	if got := deriveCarthageDependencyID("github", "owner/repo.git"); got != "repo" {
		t.Fatalf("expected github source dependency id from repo name, got %q", got)
	}
	if got := deriveCarthageDependencyID("binary", "https://example.com/FancyKit.json"); got != "fancykit" {
		t.Fatalf("expected binary source dependency id without extension, got %q", got)
	}
}

func TestSwiftCarthageDedupeCandidatesAndReferences(t *testing.T) {
	dependencies := parseCarthageDependencies([]byte(strings.Repeat(`github "owner/repo" "1.0.0"`+"\n", maxCarthageDeclarations+5)), false)
	if len(dependencies) != 1 {
		t.Fatalf("expected dedupe + max declaration cap to return one dependency, got %#v", dependencies)
	}
	if got := dedupeCarthageDependencies(nil); len(got) != 0 {
		t.Fatalf("expected nil entries to dedupe as empty result, got %#v", got)
	}
	deduped := dedupeCarthageDependencies([]carthageDependency{{Dependency: ""}, {Dependency: "Repo"}, {Dependency: "repo"}})
	if len(deduped) != 1 || deduped[0].Dependency != "repo" {
		t.Fatalf("expected dedupe to skip empties and normalize duplicates, got %#v", deduped)
	}
	if aliases := carthageAliasCandidates(carthageDependency{Kind: "binary", Source: "https://example.com/FancyKit.json"}, "fancykit"); len(aliases) == 0 {
		t.Fatalf("expected binary alias candidates")
	}
	if classifyVersion, classifyRevision := classifyCarthageReference(""); classifyVersion != "" || classifyRevision != "" {
		t.Fatalf("expected empty reference to classify as empty, got version=%q revision=%q", classifyVersion, classifyRevision)
	}
	if isLikelyCarthageVersion("abcdef") {
		t.Fatalf("expected non-numeric reference not to classify as version")
	}
	if !isLikelyCarthageVersion("1.2.3-1+1") {
		t.Fatalf("expected semver-like reference to classify as version")
	}
}

func TestSwiftCarthageCatalogOptionsAndAnalyseErrorBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "let package = Package(name: \"Demo\")\n")
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), "{\"pins\":[]}\n")
	assertCarthageSourceCount(t, dependencyCatalogOptions{}, 4)
	assertCarthageSourceCount(t, dependencyCatalogOptions{EnableCarthage: true}, 6)

	catalog := newSwiftCoverageCatalog()
	ensureDeclaredDependencyForManager(&catalog, "", carthageManager)
	ensureResolvedDependencyForManager(&catalog, "", "1.0.0", "", "source", carthageManager)

	_, analyseErr := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: string([]byte{0}),
		TopN:     1,
		Features: mustResolveCarthagePreviewFeatures(t),
	})
	if analyseErr == nil {
		t.Fatalf("expected analyse to fail on invalid repo path")
	}
}

type carthageLoader func(string, *dependencyCatalog) (bool, []string, error)

func assertMissingCarthageLoader(t *testing.T, repo string, catalog *dependencyCatalog, loader carthageLoader) {
	t.Helper()
	found, warnings, err := loader(repo, catalog)
	if err != nil || found || len(warnings) != 0 {
		t.Fatalf("expected missing Carthage file to be ignored, found=%v warnings=%#v err=%v", found, warnings, err)
	}
}

func assertCarthageLoaderRejectsDirectory(t *testing.T, repo, name string, catalog *dependencyCatalog, loader carthageLoader) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir %s path: %v", name, err)
	}
	if _, _, err := loader(repo, catalog); err == nil {
		t.Fatalf("expected %s read error for directory path", name)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s dir: %v", name, err)
	}
}

func assertCarthageSourceCount(t *testing.T, options dependencyCatalogOptions, expected int) {
	t.Helper()
	if sources := discoveredSwiftCatalogSources(options); len(sources) != expected {
		t.Fatalf("expected source list length %d, got %#v", expected, sources)
	}
}

func mustResolveCarthagePreviewFeatures(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-9998",
		Name:      swiftCarthagePreviewFlagName,
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{swiftCarthagePreviewFlagName},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}

func newSwiftCoverageCatalog() dependencyCatalog {
	return dependencyCatalog{
		Dependencies:       make(map[string]dependencyMeta),
		AliasToDependency:  make(map[string]string),
		ModuleToDependency: make(map[string]string),
		LocalModules:       make(map[string]struct{}),
	}
}
