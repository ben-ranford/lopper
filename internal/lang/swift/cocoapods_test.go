package swift

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
	firebaseAnalyticsPodName = "Firebase/Analytics"
	privatePodGitSource      = "https://example.com/private.git"
	archivePodSource         = "https://example.com/archive.zip"
)

func TestSwiftAdapterDetectWithCocoaPodsRoots(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent([]swiftFixturePodDependency{alamofirePodFixtureDependency()}))
	writeSwiftAppSourceFile(t, repo, swiftImportAlamofireSource)
	testutil.MustWriteFile(t, filepath.Join(repo, "Packages", "Feature", podLockName), buildPodLockContent([]swiftFixturePodDependency{kingfisherPodFixtureDependency()}))

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
		t.Fatalf("expected nested CocoaPods root in detection roots, got %#v", detection.Roots)
	}
}

func TestSwiftAdapterAnalyseCocoaPodsDependency(t *testing.T) {
	repo := t.TempDir()
	writeSwiftDemoCocoaPodsProject(t, repo, []swiftFixturePodDependency{alamofirePodFixtureDependency()}, swiftAlamofireSessionUsageSource)

	depReport := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
	if depReport.Language != swiftAdapterID {
		t.Fatalf("expected swift language, got %q", depReport.Language)
	}
	if depReport.TotalExportsCount == 0 {
		t.Fatalf("expected CocoaPods import evidence for alamofire, got %#v", depReport)
	}
}

func TestSwiftAdapterCocoaPodsModuleMappings(t *testing.T) {
	t.Run("base pod name maps to module import", func(t *testing.T) {
		dependency := swiftFixturePodDependency{name: "GoogleUtilities/Environment", version: "8.0.0"}
		assertCocoaPodsModuleMapping(t, dependency, "GoogleUtilities", "Logger.self", "GoogleUtilities", "googleutilities/environment")
	})

	t.Run("subspec concatenation maps to module import", func(t *testing.T) {
		dependency := swiftFixturePodDependency{name: firebaseAnalyticsPodName, version: "10.20.0"}
		assertCocoaPodsModuleMapping(t, dependency, "FirebaseAnalytics", "Analytics.self", "FirebaseAnalytics", "firebase/analytics")
	})
}

func TestSwiftAdapterMergesSwiftPMAndCocoaPodsCatalogs(t *testing.T) {
	repo := t.TempDir()
	writeSwiftDemoPackage(t, repo, []swiftFixtureDependency{alamofireFixtureDependency()}, `import Alamofire
import Kingfisher
func run() {
  _ = Session.default
  _ = KingfisherManager.shared
}`)
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent([]swiftFixturePodDependency{kingfisherPodFixtureDependency()}))
	testutil.MustWriteFile(t, filepath.Join(repo, podLockName), buildPodLockContent([]swiftFixturePodDependency{kingfisherPodFixtureDependency()}))

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, TopN: 10})
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "alamofire") {
		t.Fatalf("expected swiftpm dependency in merged report, got %#v", names)
	}
	if !slices.Contains(names, "kingfisher") {
		t.Fatalf("expected CocoaPods dependency in merged report, got %#v", names)
	}
}

func TestSwiftAdapterWarnsOnAmbiguousCocoaPodsModuleMappings(t *testing.T) {
	repo := t.TempDir()
	dependencies := []swiftFixturePodDependency{
		{name: "GoogleUtilities/Environment", version: "8.0.0"},
		{name: "GoogleUtilities/AppDelegateSwizzler", version: "8.0.0"},
	}
	writeSwiftDemoCocoaPodsProject(t, repo, dependencies, `import GoogleUtilities
let value = AppDelegateSwizzler.self`)

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, TopN: 5})
	assertWarningContains(t, reportData.Warnings, "ambiguous CocoaPods module mapping")
	assertWarningContains(t, reportData.Warnings, "CocoaPods module mapping may be incomplete")
}

func TestSwiftAdapterCocoaPodsMissingLockfileRiskCue(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent([]swiftFixturePodDependency{alamofirePodFixtureDependency()}))
	writeSwiftDemoSourceFile(t, repo, swiftAlamofireSessionValueSource)

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if !hasRiskCueCode(dep, "missing-pod-lock-resolution") {
		t.Fatalf("expected Podfile.lock risk cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendationCode(dep, "refresh-podfile-lock") {
		t.Fatalf("expected Podfile.lock recommendation, got %#v", dep.Recommendations)
	}
	assertWarningContains(t, reportData.Warnings, podLockName+" not found")
}

func hasRiskCueCode(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func hasRecommendationCode(dep report.DependencyReport, code string) bool {
	for _, recommendation := range dep.Recommendations {
		if recommendation.Code == code {
			return true
		}
	}
	return false
}

func assertCocoaPodsModuleMapping(t *testing.T, dependency swiftFixturePodDependency, importedModule string, usageExpression string, dependencyQuery string, expectedDependency string) {
	t.Helper()

	repo := t.TempDir()
	writeSwiftDemoCocoaPodsProject(t, repo, []swiftFixturePodDependency{dependency}, "import "+importedModule+"\nlet value = "+usageExpression)

	depReport := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: dependencyQuery})
	if depReport.Name != expectedDependency {
		t.Fatalf("expected dependency alias to resolve to %q, got %#v", expectedDependency, depReport)
	}
	if len(depReport.UsedImports) == 0 && len(depReport.UnusedImports) == 0 {
		t.Fatalf("expected mapped import evidence for %q, got %#v", dependency.name, depReport)
	}
}

func TestSwiftCocoaPodsCatalogStateActive(t *testing.T) {
	var nilState *packageManagerCatalogState
	if nilState.Active() {
		t.Fatalf("expected nil state to be inactive")
	}
	state := packageManagerCatalogState{ManifestFound: true}
	if !state.Active() {
		t.Fatalf("expected manifest-present state to be active")
	}
}

func TestSwiftCocoaPodsSourceHelpers(t *testing.T) {
	doc := podLockDocument{
		CheckoutOptions: map[string]map[string]any{
			"PrivatePod": {
				":git": privatePodGitSource,
			},
		},
		ExternalSources: map[string]map[string]any{
			"Firebase": {
				":path": "../Firebase",
			},
			"ArchivePod": {
				":http": archivePodSource,
			},
		},
	}
	if got := extractPodSource(nil); got != "" {
		t.Fatalf("expected empty source for nil options, got %q", got)
	}
	if got := extractPodSource(map[string]any{":http": archivePodSource}); got != archivePodSource {
		t.Fatalf("unexpected extracted http source: %q", got)
	}
	if got := podBaseName(" " + firebaseAnalyticsPodName + " "); got != "Firebase" {
		t.Fatalf("unexpected pod base name: %q", got)
	}
	if got := podSourceCandidates(firebaseAnalyticsPodName); !slices.Equal(got, []string{firebaseAnalyticsPodName, "Firebase"}) {
		t.Fatalf("unexpected pod source candidates: %#v", got)
	}
	if got := podSourceCandidates(" "); len(got) != 0 {
		t.Fatalf("expected blank pod source candidates to be empty, got %#v", got)
	}
	if got := lookupPodSource(doc.ExternalSources, firebaseAnalyticsPodName); got != "../Firebase" {
		t.Fatalf("expected base pod source lookup, got %q", got)
	}
	if got := lookupPodSource(nil, firebaseAnalyticsPodName); got != "" {
		t.Fatalf("expected empty lookup for nil source map, got %q", got)
	}
	if got := podLockSource(doc, "PrivatePod"); got != privatePodGitSource {
		t.Fatalf("expected checkout options source, got %q", got)
	}
	if got := podLockSource(doc, "ArchivePod"); got != archivePodSource {
		t.Fatalf("expected external source fallback, got %q", got)
	}
}

func TestSwiftCocoaPodsSpecHelpers(t *testing.T) {
	if specs := podLockSpecs("Alamofire (5.8.1)"); !slices.Equal(specs, []string{"Alamofire (5.8.1)"}) {
		t.Fatalf("unexpected string pod specs: %#v", specs)
	}
	if specs := podLockSpecs(map[string]any{"PrivatePod (1.0.0)": nil}); !slices.Equal(specs, []string{"PrivatePod (1.0.0)"}) {
		t.Fatalf("unexpected map pod specs: %#v", specs)
	}
	if specs := podLockSpecs(map[any]any{"LegacyKit (1.0.0)": nil, 7: nil}); !slices.Equal(specs, []string{"LegacyKit (1.0.0)"}) {
		t.Fatalf("unexpected legacy pod specs: %#v", specs)
	}
	if specs := podLockSpecs(42); len(specs) != 0 {
		t.Fatalf("expected unsupported pod specs to be empty, got %#v", specs)
	}
	if name, version := parsePodSpec("LocalPod"); name != "LocalPod" || version != "" {
		t.Fatalf("expected bare pod spec parsing, got %q %q", name, version)
	}
	if name, version := parsePodSpec(" "); name != "" || version != "" {
		t.Fatalf("expected blank pod spec parsing to stay empty, got %q %q", name, version)
	}
}

func TestSwiftCocoaPodsEntryAndDedupeHelpers(t *testing.T) {
	doc := podLockDocument{ExternalSources: map[string]map[string]any{"LocalPod": {":path": "../LocalPod"}}}
	entry := podLockEntryFromSpec("LocalPod (1.0.0)", doc)
	if entry.Name != "LocalPod" || entry.Version != "1.0.0" || entry.Source != "../LocalPod" {
		t.Fatalf("unexpected pod lock entry: %#v", entry)
	}
	if blank := podLockEntryFromSpec("", doc); blank.Name != "" {
		t.Fatalf("expected blank spec to produce empty entry, got %#v", blank)
	}
	deduped := dedupePodLockEntries([]podLockEntry{
		{Name: "Alamofire", Version: "5.8.1"},
		{Name: "alamofire", Version: "5.8.1"},
		{Name: "Beta", Version: "1.0.0"},
		{Name: "", Version: "0.0.0"},
	})
	if len(deduped) != 2 || deduped[0].Name != "Alamofire" || deduped[1].Name != "Beta" {
		t.Fatalf("unexpected deduped pod lock entries: %#v", deduped)
	}
	parsed, err := parsePodLockEntries([]byte(`PODS:
  - Alamofire (5.8.1)
  - Alamofire (5.8.1)
  - LegacyKit (1.0.0):
    - Dependency
EXTERNAL SOURCES:
  LegacyKit:
    :http: https://example.com/legacy.zip
`))
	if err != nil {
		t.Fatalf("parse pod lock entries: %v", err)
	}
	if len(parsed) != 2 || parsed[1].Source != "https://example.com/legacy.zip" {
		t.Fatalf("unexpected parsed pod lock entries: %#v", parsed)
	}
}

func TestSwiftCocoaPodsModuleHelpers(t *testing.T) {
	if candidates := podModuleCandidates("Google-Mobile-Ads-SDK"); !slices.Contains(candidates, "GoogleMobileAds") {
		t.Fatalf("expected SDK-trimmed module candidate, got %#v", candidates)
	}
	if parts := podNameParts(" /One//Two/ "); !slices.Equal(parts, []string{"One", "Two"}) {
		t.Fatalf("unexpected pod name parts: %#v", parts)
	}
	if tokens := podModuleTokens(" Google-Mobile-Ads-SDK "); !slices.Equal(tokens, []string{"Google", "Mobile", "Ads", "SDK"}) {
		t.Fatalf("unexpected pod module tokens: %#v", tokens)
	}
}

func TestSwiftLoadPodLockDataCapturesExternalSourceMetadata(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, podLockName), `PODS:
  - PrivatePod (1.0.0)
CHECKOUT OPTIONS:
  PrivatePod:
    :git: `+privatePodGitSource+`
COCOAPODS: 1.13.0
`)
	catalog := newTestSwiftCatalog()
	found, warnings, err := loadPodLockData(repo, &catalog)
	if err != nil || !found {
		t.Fatalf("expected pod lock data to load, found=%v err=%v", found, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for populated pod lock data, got %#v", warnings)
	}
	meta := catalog.Dependencies["privatepod"]
	if !meta.ResolvedViaCocoaPods || meta.Source != privatePodGitSource {
		t.Fatalf("expected CocoaPods resolved metadata with git source, got %#v", meta)
	}
}

func TestSwiftCocoaPodsManifestLoaderBranches(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, podManifestName)
	if err := os.MkdirAll(manifestPath, 0o750); err != nil {
		t.Fatalf("mkdir pod manifest dir: %v", err)
	}
	catalog := newTestSwiftCatalog()
	if found, warnings, err := loadPodManifestData(repo, &catalog); err == nil || found || len(warnings) != 0 {
		t.Fatalf("expected pod manifest directory to fail loading, got found=%v warnings=%#v err=%v", found, warnings, err)
	}

	if err := os.RemoveAll(manifestPath); err != nil {
		t.Fatalf("remove pod manifest dir: %v", err)
	}
	testutil.MustWriteFile(t, manifestPath, "# comment only\n")
	found, warnings, err := loadPodManifestData(repo, &catalog)
	if err != nil || !found {
		t.Fatalf("expected comment-only Podfile to load, found=%v err=%v", found, err)
	}
	assertWarningContains(t, warnings, "no pod declarations found in Podfile")

	testutil.MustWriteFile(t, manifestPath, "pod '---'\n")
	catalog = newTestSwiftCatalog()
	found, warnings, err = loadPodManifestData(repo, &catalog)
	if err != nil || !found || len(warnings) != 0 {
		t.Fatalf("expected invalid pod name to be skipped without warnings, found=%v warnings=%#v err=%v", found, warnings, err)
	}
	if len(catalog.Dependencies) != 0 {
		t.Fatalf("expected invalid pod name to produce no dependencies, got %#v", catalog.Dependencies)
	}
}

func TestSwiftCocoaPodsLockLoaderBranches(t *testing.T) {
	repo := t.TempDir()
	lockPath := filepath.Join(repo, podLockName)
	if err := os.MkdirAll(lockPath, 0o750); err != nil {
		t.Fatalf("mkdir pod lock dir: %v", err)
	}
	catalog := newTestSwiftCatalog()
	if found, warnings, err := loadPodLockData(repo, &catalog); err == nil || found || len(warnings) != 0 {
		t.Fatalf("expected pod lock directory to fail loading, got found=%v warnings=%#v err=%v", found, warnings, err)
	}

	if err := os.RemoveAll(lockPath); err != nil {
		t.Fatalf("remove pod lock dir: %v", err)
	}
	testutil.MustWriteFile(t, lockPath, "PODS: [\n")
	if found, warnings, err := loadPodLockData(repo, &catalog); err == nil || found || len(warnings) != 0 {
		t.Fatalf("expected invalid Podfile.lock to fail parsing, got found=%v warnings=%#v err=%v", found, warnings, err)
	}

	testutil.MustWriteFile(t, lockPath, "PODS: []\n")
	catalog = newTestSwiftCatalog()
	found, warnings, err := loadPodLockData(repo, &catalog)
	if err != nil || !found {
		t.Fatalf("expected empty pod lock to load, found=%v err=%v", found, err)
	}
	assertWarningContains(t, warnings, "no pods found in Podfile.lock")

	testutil.MustWriteFile(t, lockPath, "PODS:\n  - '--- (1.0.0)'\n")
	catalog = newTestSwiftCatalog()
	found, warnings, err = loadPodLockData(repo, &catalog)
	if err != nil || !found {
		t.Fatalf("expected invalid pod lock name to load, found=%v err=%v", found, err)
	}
	if len(catalog.Dependencies) != 0 {
		t.Fatalf("expected invalid pod lock name to produce no dependencies, got %#v", catalog.Dependencies)
	}
	assertWarningContains(t, warnings, "no pods found in Podfile.lock")
}

func TestSwiftCocoaPodsParserAndWarningBranches(t *testing.T) {
	if got := parsePodDeclarations([]byte("\n# comment\npod 'Alamofire'\nnot a pod\n")); !slices.Equal(got, []string{"Alamofire"}) {
		t.Fatalf("unexpected parsed pod declarations: %#v", got)
	}
	if _, err := parsePodLockEntries([]byte("PODS: [\n")); err == nil {
		t.Fatalf("expected invalid pod lock yaml to fail parsing")
	}
	if got := podBaseName(" / / "); got != "" {
		t.Fatalf("expected blank pod base name, got %q", got)
	}
	if got := dedupePodLockEntries(nil); len(got) != 0 {
		t.Fatalf("expected nil pod entries to stay nil, got %#v", got)
	}
	ambiguous := make(map[string]struct{}, maxWarningSamples+2)
	for _, alias := range []string{"Alpha", "Beta", "Delta", "Epsilon", "Eta", "Gamma", "Zeta"} {
		ambiguous[alias] = struct{}{}
	}
	warning := cocoaPodsAmbiguityWarning(ambiguous)
	if !strings.Contains(warning, "Alpha, Beta, Delta, Epsilon, Eta") || !strings.Contains(warning, "+2 more") {
		t.Fatalf("expected truncated ambiguity warning, got %q", warning)
	}
}

func TestSwiftBuildDependencyCatalogSurfacesPodLoaderFailures(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, podManifestName), 0o750); err != nil {
		t.Fatalf("mkdir pod manifest dir: %v", err)
	}
	if _, _, err := buildDependencyCatalog(repo); err == nil {
		t.Fatalf("expected build dependency catalog to surface Podfile loader failure")
	}
}
