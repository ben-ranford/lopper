package swift

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftAdapterDetectWithCarthageRoots(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), buildCartfileContent([]swiftFixtureCarthageDependency{rxSwiftCarthageFixtureDependency()}))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", swiftMainFileName), "import RxSwift\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Packages", "Feature", carthageResolvedName), buildCartfileResolvedContent([]swiftFixtureCarthageDependency{{kind: "github", source: "SnapKit/SnapKit", reference: "5.7.0"}}))

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected swift detection to match")
	}
	if !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected repo root in detection roots, got %#v", detection.Roots)
	}
	nested := filepath.Join(repo, "Packages", "Feature")
	if !slices.Contains(detection.Roots, nested) {
		t.Fatalf("expected nested Carthage root in detection roots, got %#v", detection.Roots)
	}
}

func TestSwiftBuildDependencyCatalogGatesCarthageByFeatureFlag(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), buildCartfileContent([]swiftFixtureCarthageDependency{rxSwiftCarthageFixtureDependency()}))
	testutil.MustWriteFile(t, filepath.Join(repo, carthageResolvedName), buildCartfileResolvedContent([]swiftFixtureCarthageDependency{rxSwiftCarthageFixtureDependency()}))

	catalog, _, err := buildDependencyCatalog(repo)
	if err != nil {
		t.Fatalf("build dependency catalog (default): %v", err)
	}
	if catalog.HasCarthage || len(catalog.Dependencies) != 0 {
		t.Fatalf("expected default catalog to ignore Carthage, got %#v", catalog)
	}

	catalog, warnings, err := buildDependencyCatalogWithOptions(repo, dependencyCatalogOptions{EnableCarthage: true})
	if err != nil {
		t.Fatalf("build dependency catalog (carthage enabled): %v", err)
	}
	if !catalog.HasCarthage {
		t.Fatalf("expected Carthage catalog state to be active")
	}
	if _, ok := catalog.Dependencies["rxswift"]; !ok {
		t.Fatalf("expected rxswift dependency in Carthage-enabled catalog, got %#v", catalog.Dependencies)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for complete Carthage catalog, got %#v", warnings)
	}
}

func TestSwiftAdapterCarthageFlagControlsDependencyAttribution(t *testing.T) {
	repo := t.TempDir()
	writeSwiftDemoCarthageProject(t, repo, []swiftFixtureCarthageDependency{rxSwiftCarthageFixtureDependency()}, `import RxSwift
let value = DisposeBag()`)

	disabled := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "rxswift"})
	if disabled.TotalExportsCount != 0 {
		t.Fatalf("expected no Carthage attribution when preview flag is disabled, got %#v", disabled)
	}

	enabled := mustSingleSwiftDependencyReport(t, language.Request{
		RepoPath:   repo,
		Dependency: "rxswift",
		Features:   mustResolveSwiftCarthageFeatureSet(t, true),
	})
	if enabled.TotalExportsCount == 0 {
		t.Fatalf("expected Carthage attribution when preview flag is enabled, got %#v", enabled)
	}
	if enabled.Provenance == nil || len(enabled.Provenance.Signals) == 0 || enabled.Provenance.Signals[0] != "ReactiveX/RxSwift" {
		t.Fatalf("expected Carthage provenance signal, got %#v", enabled.Provenance)
	}
}

func TestSwiftAdapterCarthageMissingResolvedRiskCue(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), buildCartfileContent([]swiftFixtureCarthageDependency{rxSwiftCarthageFixtureDependency()}))
	writeSwiftDemoSourceFile(t, repo, `import RxSwift
let value = DisposeBag()`)

	reportData := mustAnalyseSwiftRequest(t, language.Request{
		RepoPath:   repo,
		Dependency: "rxswift",
		Features:   mustResolveSwiftCarthageFeatureSet(t, true),
	})
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if !hasRiskCueCode(dep, "missing-carthage-lock-resolution") {
		t.Fatalf("expected Cartfile.resolved risk cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendationCode(dep, "refresh-cartfile-resolved") {
		t.Fatalf("expected Cartfile.resolved recommendation, got %#v", dep.Recommendations)
	}
	assertWarningContains(t, reportData.Warnings, carthageResolvedName+" not found")
}

func TestSwiftAdapterWarnsOnAmbiguousCarthageModuleMappings(t *testing.T) {
	repo := t.TempDir()
	dependencies := []swiftFixtureCarthageDependency{
		{kind: "github", source: "Acme/Firebase-Analytics", reference: "1.0.0"},
		{kind: "github", source: "Acme/FirebaseAnalytics", reference: "1.0.0"},
	}
	writeSwiftDemoCarthageProject(t, repo, dependencies, `import FirebaseAnalytics
let value = Analytics.self`)

	reportData := mustAnalyseSwiftRequest(t, language.Request{
		RepoPath: repo,
		TopN:     5,
		Features: mustResolveSwiftCarthageFeatureSet(t, true),
	})
	assertWarningContains(t, reportData.Warnings, "ambiguous Carthage module mapping")
}

func TestSwiftParseCarthageDependencies(t *testing.T) {
	manifest := parseCarthageManifestDependencies([]byte(`# comment

github "ReactiveX/RxSwift" ~> 6.0
git "https://github.com/Quick/Quick.git" "7.0.0"
binary "https://example.com/FancyKit.json" == 1.2.0
`))
	if len(manifest) != 3 {
		t.Fatalf("expected 3 Cartfile dependencies, got %#v", manifest)
	}
	if manifest[0].Dependency != "fancykit" || manifest[1].Dependency != "quick" || manifest[2].Dependency != "rxswift" {
		t.Fatalf("unexpected normalized Cartfile dependencies: %#v", manifest)
	}

	resolved := parseCarthageResolvedDependencies([]byte(`github "ReactiveX/RxSwift" "6.8.0"
git "https://github.com/Quick/Quick.git" "7.0.0"
git "https://example.com/internal-kit.git" "abcdef123456"
`))
	if len(resolved) != 3 {
		t.Fatalf("expected 3 Cartfile.resolved dependencies, got %#v", resolved)
	}
	version, revision := classifyCarthageReference("6.8.0")
	if version != "6.8.0" || revision != "" {
		t.Fatalf("expected semver reference classification, got version=%q revision=%q", version, revision)
	}
	version, revision = classifyCarthageReference("abcdef123456")
	if version != "" || revision == "" {
		t.Fatalf("expected revision reference classification, got version=%q revision=%q", version, revision)
	}
}

func mustResolveSwiftCarthageFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	opts := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if enabled {
		opts.Enable = []string{swiftCarthagePreviewFlagName}
	}
	resolved, err := featureflags.DefaultRegistry().Resolve(opts)
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return resolved
}
