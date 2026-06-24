package shared

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

type stubGradleCatalogDirEntry struct {
	name string
}

const (
	gradleCatalogName             = "libs"
	gradleTestCatalogName         = "testlibs"
	gradleToolsCatalogName        = "tools"
	gradleDefaultCatalogFileName  = "libs.versions.toml"
	gradleOverrideCatalogFileName = "override.versions.toml"
	gradleSettingsFileName        = "settings.gradle.kts"
	gradleDefaultCatalogPath      = "gradle/" + gradleDefaultCatalogFileName
	gradleToolsCatalogPath        = "gradle/tools.versions.toml"
	testRepoRoot                  = "/repo"
	testAppRoot                   = testRepoRoot + "/app"
	testBuildFile                 = testAppRoot + "/build.gradle.kts"
	testOtherBuildFile            = testRepoRoot + "/other/build.gradle.kts"
	testRepoCatalogPath           = testRepoRoot + "/gradle/" + gradleDefaultCatalogFileName
	okhttpGroup                   = "com.squareup.okhttp3"
	okhttpVersion                 = "4.12.0"
)

func (e *stubGradleCatalogDirEntry) Name() string               { return e.name }
func (e *stubGradleCatalogDirEntry) IsDir() bool                { return true }
func (e *stubGradleCatalogDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e *stubGradleCatalogDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestGradleCatalogParserHandlesMultilineEntriesAndWarnings(t *testing.T) {
	content := `
[versions]
agp = "8.5.0"
shared = "1.2.3"

[libraries]
compose-bom = "androidx.compose:compose-bom:2024.05.00"
okhttp = { module = "com.squareup.okhttp3:okhttp", version.ref = "shared" }
broken = "not-a-coordinate"
bad-module = { module = "missing-artifact", version = "1.0.0" }
missing-fields = { version = "1.0.0" }

[libraries.retrofit]
module = "com.squareup.retrofit2:retrofit"
version = "2.11.0"

[libraries.multiline]
group = "org.sample"
name = "artifact"
version = { ref = "AGP" }

[libraries.pending-lib]
group = "org.pending"
name = "pending-lib"
version = "1.0.0"

[bundles]
networking = ["retrofit", "okhttp", "retrofit", "missing"]
unsupported = []
`
	parsed, warnings := parseGradleCatalogFile(content, gradleCatalogName, gradleDefaultCatalogPath)

	if got := parsed.libraries["compose.bom"]; got.Artifact != "compose-bom" || got.Version != "2024.05.00" {
		t.Fatalf("expected string dependency to parse, got %#v", got)
	}
	if got := parsed.libraries["multiline"]; got.Version != "8.5.0" || got.Group != "org.sample" || got.Artifact != "artifact" {
		t.Fatalf("expected multiline dependency to resolve nested version ref, got %#v", got)
	}
	if got := parsed.libraries["pending.lib"]; got.Artifact != "pending-lib" || got.Version != "1.0.0" {
		t.Fatalf("expected pending multiline dependency to parse, got %#v", got)
	}
	bundle := parsed.bundles["networking"]
	if len(bundle) != 2 {
		t.Fatalf("expected duplicate bundle entries to be deduped and missing ones skipped, got %#v", bundle)
	}

	expectedWarnings := []string{
		`unsupported Gradle version catalog library "broken" in ` + gradleDefaultCatalogPath,
		`unsupported Gradle version catalog module "bad-module" in ` + gradleDefaultCatalogPath,
		`unsupported Gradle version catalog library "missing-fields" in ` + gradleDefaultCatalogPath,
		`unsupported Gradle version catalog bundle "unsupported" in ` + gradleDefaultCatalogPath,
		`unable to resolve Gradle version catalog bundle member "missing" in ` + gradleDefaultCatalogPath,
	}
	assertGradleCatalogWarningsContain(t, warnings, expectedWarnings...)
}

func TestGradleCatalogParserReportsInvalidTOML(t *testing.T) {
	parsed, warnings := parseGradleCatalogFile("[libraries]\nbroken = { group = \"org.tail\"\n", gradleCatalogName, gradleDefaultCatalogPath)
	if len(parsed.libraries) != 0 || len(parsed.bundles) != 0 {
		t.Fatalf("expected invalid TOML to leave catalog empty, got %#v", parsed)
	}
	assertGradleCatalogWarningsContain(t, warnings, "unable to parse Gradle version catalog "+gradleDefaultCatalogPath)
}

func TestGradleCatalogResolverCollectsAllReferenceForms(t *testing.T) {
	okhttp := GradleCatalogLibrary{Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: okhttpVersion}
	retrofit := GradleCatalogLibrary{Catalog: gradleCatalogName, Alias: "retrofit", Group: "com.squareup.retrofit2", Artifact: "retrofit", Version: "2.11.0"}
	junit := GradleCatalogLibrary{Catalog: gradleTestCatalogName, Alias: "junit", Group: "org.junit.jupiter", Artifact: "junit-jupiter-api", Version: "5.10.0"}

	resolver := GradleCatalogResolver{
		knownCatalogs: map[string]struct{}{
			gradleCatalogName:     {},
			gradleTestCatalogName: {},
		},
		scopes: []gradleCatalogScope{
			{
				root: filepath.Clean(testAppRoot),
				libraries: map[string]GradleCatalogLibrary{
					buildGradleCatalogScopeKey(gradleCatalogName, "okhttp"):    okhttp,
					buildGradleCatalogScopeKey(gradleCatalogName, "retrofit"):  retrofit,
					buildGradleCatalogScopeKey(gradleTestCatalogName, "junit"): junit,
				},
				bundles: map[string][]GradleCatalogLibrary{
					buildGradleCatalogScopeKey(gradleCatalogName, "networking"): {okhttp, retrofit, okhttp},
					buildGradleCatalogScopeKey(gradleTestCatalogName, "qa"):     {junit},
				},
			},
		},
	}

	dependencies, warnings := resolver.ParseDependencyReferences(testBuildFile, `
dependencies {
  implementation(libs["okhttp"])
  implementation(libs.bundles.networking)
  implementation(testLibs.findBundle("qa").get())
  implementation(testLibs.findLibrary("junit").get())
  implementation(libs.findLibrary("missing").get())
  implementation(testLibs.findBundle("missing").get())
  implementation(libs.plugins.android)
  implementation(libs.versions.kotlin)
  implementation(unknownCatalog.foo)
  implementation(libs.findLibrary("retrofit").get())
  implementation(libs.retrofit)
}
`)
	if len(dependencies) != 3 {
		t.Fatalf("expected unique dependencies from property, bracket, bundle, and finder references, got %#v", dependencies)
	}
	expectedWarnings := []string{
		"unable to resolve Gradle version catalog alias libs.missing in " + testBuildFile,
		"unable to resolve Gradle version catalog bundle testlibs.bundles.missing in " + testBuildFile,
		"unsupported Gradle version catalog reference libs.plugins.android in " + testBuildFile,
		"unsupported Gradle version catalog reference libs.versions.kotlin in " + testBuildFile,
	}
	assertGradleCatalogWarningsContain(t, warnings, expectedWarnings...)

	if resolver.scopeForBuildFile(testOtherBuildFile) != nil {
		t.Fatalf("expected unmatched build file to have no catalog scope")
	}
	if dependency, warning := resolver.resolveLibraryReference(testOtherBuildFile, gradleCatalogName, "okhttp"); dependency != (GradleCatalogLibrary{}) || warning == "" {
		t.Fatalf("expected unmatched scope to emit unresolved alias warning, got %#v %q", dependency, warning)
	}
	if bundle, warning := resolver.resolveBundleReference(testOtherBuildFile, gradleCatalogName, "networking"); len(bundle) != 0 || warning == "" {
		t.Fatalf("expected unmatched scope to emit unresolved bundle warning, got %#v %q", bundle, warning)
	}
	if dependency, warning := resolver.resolveLibraryReference(testBuildFile, gradleCatalogName, ""); dependency != (GradleCatalogLibrary{}) || warning != "" {
		t.Fatalf("expected empty alias lookup to be ignored, got %#v %q", dependency, warning)
	}
	if bundle, warning := resolver.resolveBundleReference(testBuildFile, gradleCatalogName, ""); len(bundle) != 0 || warning != "" {
		t.Fatalf("expected empty bundle lookup to be ignored, got %#v %q", bundle, warning)
	}

	var nilResolver *GradleCatalogResolver
	if dependencies, warnings := nilResolver.ParseDependencyReferences(testBuildFile, "implementation(libs.okhttp)"); len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("expected nil resolver to return nil results, got %#v %#v", dependencies, warnings)
	}
}

func TestGradleCatalogRegistryHelpersAndWarnings(t *testing.T) {
	repo := t.TempDir()
	paths := newGradleCatalogRegistryTestPaths(repo)
	registry := newGradleCatalogRegistry(repo)
	assertGradleCatalogRegistryMissingInputWarnings(t, registry, repo, paths)
	assertGradleCatalogRegistryDirectoryFiltering(t)
	assertGradleCatalogRegistrySourcePathResolution(t, repo, paths.defaultCatalogPath)
	assertGradleCatalogRegistryKnownCatalogNormalization(t, registry, repo)
	assertGradleCatalogRegistryMergeWarnings(t, registry, repo, paths)
	assertGradleCatalogRegistrySettingsParsing(t)
	assertGradleCatalogRegistrySettingsLoading(t, repo, paths.settingsPath)
	assertGradleCatalogRegistryResolverLoading(t, repo)
	assertGradleCatalogRegistryBuildResolverSortsScopes(t, repo)
	assertGradleCatalogLookupIndexResolveCases(t)
}

type gradleCatalogRegistryTestPaths struct {
	missingSettingsPath string
	missingCatalogPath  string
	vendorCatalogPath   string
	defaultCatalogPath  string
	overrideCatalogPath string
	otherCatalogPath    string
	settingsPath        string
}

func newGradleCatalogRegistryTestPaths(repo string) gradleCatalogRegistryTestPaths {
	return gradleCatalogRegistryTestPaths{
		missingSettingsPath: filepath.Join(repo, "missing", gradleSettingsFileName),
		missingCatalogPath:  filepath.Join(repo, "gradle", "missing.versions.toml"),
		vendorCatalogPath:   filepath.Join(repo, "vendor", gradleDefaultCatalogFileName),
		defaultCatalogPath:  filepath.Join(repo, "gradle", gradleDefaultCatalogFileName),
		overrideCatalogPath: filepath.Join(repo, "gradle", gradleOverrideCatalogFileName),
		otherCatalogPath:    filepath.Join(repo, "gradle", "other.versions.toml"),
		settingsPath:        filepath.Join(repo, gradleSettingsFileName),
	}
}

func assertGradleCatalogRegistryMissingInputWarnings(t *testing.T, registry *gradleCatalogRegistry, repo string, paths gradleCatalogRegistryTestPaths) {
	t.Helper()

	registry.loadSettingsFile(paths.missingSettingsPath)
	registry.loadSource(gradleCatalogSource{
		root: repo,
		name: gradleCatalogName,
		path: paths.missingCatalogPath,
	})
	assertGradleCatalogWarningsContain(t, registry.warnings, "unable to read missing/"+gradleSettingsFileName+":", "unable to read gradle/missing.versions.toml:")
}

func assertGradleCatalogRegistryDirectoryFiltering(t *testing.T) {
	t.Helper()

	if err := maybeSkipGradleCatalogDirectory(&stubGradleCatalogDirEntry{name: "build"}); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected build directory to be skipped, got %v", err)
	}
	if err := maybeSkipGradleCatalogDirectory(&stubGradleCatalogDirEntry{name: "src"}); err != nil {
		t.Fatalf("expected normal directory to be scanned, got %v", err)
	}
}

func assertGradleCatalogRegistrySourcePathResolution(t *testing.T, repo string, defaultCatalogPath string) {
	t.Helper()

	relativePath := resolveGradleCatalogSourcePath(repo, "gradle/test-libs.versions.toml")
	if want := filepath.Join(repo, "gradle", "test-libs.versions.toml"); relativePath != want {
		t.Fatalf("expected relative catalog path to resolve under repo, got %q want %q", relativePath, want)
	}
	absolutePath := resolveGradleCatalogSourcePath(repo, defaultCatalogPath)
	if want := defaultCatalogPath; absolutePath != want {
		t.Fatalf("expected absolute catalog path to stay absolute, got %q want %q", absolutePath, want)
	}
}

func assertGradleCatalogRegistryKnownCatalogNormalization(t *testing.T, registry *gradleCatalogRegistry, repo string) {
	t.Helper()

	registry.trackKnownCatalog(" TestLibs ")
	if _, ok := registry.knownCatalogs["testlibs"]; !ok {
		t.Fatalf("expected known catalogs to be normalized, got %#v", registry.knownCatalogs)
	}
	registry.trackKnownCatalog("   ")
	registry.registerSource(repo, "", "")
}

func assertGradleCatalogRegistryMergeWarnings(t *testing.T, registry *gradleCatalogRegistry, repo string, paths gradleCatalogRegistryTestPaths) {
	t.Helper()

	registry.registerDefaultCatalog(paths.vendorCatalogPath)
	registry.registerDefaultCatalog(paths.defaultCatalogPath)
	registry.registerSource(repo, gradleCatalogName, paths.overrideCatalogPath)
	registry.registerSource(repo, gradleCatalogName, paths.otherCatalogPath)

	scope := registry.ensureScope(repo)
	registry.mergeLibraries(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: paths.defaultCatalogPath}, map[string]GradleCatalogLibrary{
		"okhttp": {Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: okhttpVersion},
	})
	registry.mergeLibraries(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: paths.overrideCatalogPath}, map[string]GradleCatalogLibrary{
		"okhttp": {Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: "4.13.0"},
	})
	registry.mergeBundles(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: paths.defaultCatalogPath}, map[string][]GradleCatalogLibrary{
		"networking": {
			{Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: okhttpVersion},
		},
	})
	registry.mergeBundles(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: paths.overrideCatalogPath}, map[string][]GradleCatalogLibrary{
		"networking": {
			{Catalog: gradleCatalogName, Alias: "retrofit", Group: "com.squareup.retrofit2", Artifact: "retrofit", Version: "2.11.0"},
		},
	})
	assertGradleCatalogWarningsContain(t, registry.warnings, fmt.Sprintf("multiple Gradle version catalog sources configured for libs under %s; using %s", filepath.Clean(repo), paths.defaultCatalogPath), fmt.Sprintf("Gradle version catalog alias libs.%s resolves to multiple coordinates under %s; keeping %s:%s", "okhttp", filepath.Clean(repo), okhttpGroup, "okhttp"), fmt.Sprintf("Gradle version catalog bundle libs.%s resolves to multiple dependency sets under %s; keeping the first definition", "networking", filepath.Clean(repo)))
}

func assertGradleCatalogRegistrySettingsParsing(t *testing.T) {
	t.Helper()

	settingsContent := `
dependencyResolutionManagement {
  versionCatalogs {
    create("   ") {
      from(files("gradle/ignored.versions.toml"))
    }
    create("tools") {
      from(files("gradle/tools.versions.toml", "gradle/tools-extra.versions.toml"))
    }
    create("dynamic") {
      from(dynamicCatalogProvider())
    }
  }
}
`
	refs, warnings := parseGradleSettingsCatalogRefs(settingsContent, gradleSettingsFileName)
	if len(refs) != 2 || refs[0].Path != gradleToolsCatalogPath || refs[1].Path != "" {
		t.Fatalf("expected settings parser to keep first file and unsupported source, got %#v", refs)
	}
	assertGradleCatalogWarningsContain(t, warnings, "multiple Gradle version catalog files declared for tools in "+gradleSettingsFileName+"; using "+gradleToolsCatalogPath, "unsupported Gradle version catalog source for dynamic in "+gradleSettingsFileName)
}

func assertGradleCatalogRegistrySettingsLoading(t *testing.T, repo string, settingsPath string) {
	t.Helper()

	writeGradleCatalogTestFile(t, settingsPath, `
dependencyResolutionManagement {
  versionCatalogs {
    create("dynamic") {
      from(dynamicCatalogProvider())
    }
  }
}
`)
	registry := newGradleCatalogRegistry(repo)
	registry.loadSettingsFile(settingsPath)
	if _, ok := registry.knownCatalogs["dynamic"]; !ok {
		t.Fatalf("expected settings loader to track catalogs without file-backed sources, got %#v", registry.knownCatalogs)
	}
	if len(registry.sources) != 0 {
		t.Fatalf("expected unsupported settings source to avoid source registration, got %#v", registry.sources)
	}
}

func assertGradleCatalogRegistryResolverLoading(t *testing.T, repo string) {
	t.Helper()

	missingRepoResolver, warnings := LoadGradleCatalogResolver(filepath.Join(repo, "does-not-exist"))
	if len(missingRepoResolver.scopes) != 0 {
		t.Fatalf("expected missing repo to produce an empty resolver, got %#v", missingRepoResolver)
	}
	assertGradleCatalogWarningsContain(t, warnings, "unable to scan Gradle version catalogs:")

	emptyResolver, warnings := LoadGradleCatalogResolver("")
	if len(warnings) != 0 || len(emptyResolver.knownCatalogs) != 0 || len(emptyResolver.scopes) != 0 {
		t.Fatalf("expected empty repo path to return an empty resolver, got %#v %#v", emptyResolver, warnings)
	}
}

func assertGradleCatalogRegistryBuildResolverSortsScopes(t *testing.T, repo string) {
	t.Helper()

	registry := newGradleCatalogRegistry(repo)
	registry.scopesByRoot[filepath.Join(repo, "zz")] = &gradleCatalogScope{root: filepath.Join(repo, "zz")}
	registry.scopesByRoot[filepath.Join(repo, "aa")] = &gradleCatalogScope{root: filepath.Join(repo, "aa")}
	registry.scopesByRoot[filepath.Join(repo, "module", "nested")] = &gradleCatalogScope{root: filepath.Join(repo, "module", "nested")}

	resolver := registry.buildResolver()
	if len(resolver.scopes) != 3 {
		t.Fatalf("expected resolver to include all scopes, got %#v", resolver.scopes)
	}
	if resolver.scopes[0].root != filepath.Join(repo, "module", "nested") || resolver.scopes[1].root != filepath.Join(repo, "aa") || resolver.scopes[2].root != filepath.Join(repo, "zz") {
		t.Fatalf("expected resolver scopes sorted by depth then lexical order, got %#v", resolver.scopes)
	}
}

func assertGradleCatalogLookupIndexResolveCases(t *testing.T) {
	t.Helper()

	var nilIndex *gradleCatalogLookupIndex
	if nilIndex.resolve(testBuildFile) != nil {
		t.Fatalf("expected nil catalog lookup index to miss")
	}
	emptyIndex := gradleCatalogLookupIndex{}
	if emptyIndex.resolve(testBuildFile) != nil {
		t.Fatalf("expected empty catalog lookup index to miss")
	}

	index := newGradleCatalogLookupIndex([]gradleCatalogScope{{root: testAppRoot}})
	firstMatch := index.resolve(testBuildFile)
	secondMatch := index.resolve(testBuildFile)
	if firstMatch == nil || secondMatch == nil {
		t.Fatalf("expected catalog lookup index to cache matched scopes")
	}
	firstMiss := index.resolve(testOtherBuildFile)
	secondMiss := index.resolve(testOtherBuildFile)
	if firstMiss != nil || secondMiss != nil {
		t.Fatalf("expected catalog lookup index to cache unmatched scopes")
	}
}

func TestGradleCatalogLibraryEntryHelpers(t *testing.T) {
	library, warnings := parseGradleCatalogLibraryValue(gradleToolsCatalogName, "cli", "dev.example:cli:1.0.0", nil, gradleToolsCatalogPath)
	if len(warnings) != 0 || library.Group != "dev.example" || library.Artifact != "cli" || library.Version != "1.0.0" {
		t.Fatalf("expected string catalog dependency to parse, got %#v %#v", library, warnings)
	}
	pluginVersions := map[string]string{"plugin": "2.0.0"}
	pluginValue := map[string]any{
		"module":  "dev.example:plugin",
		"version": map[string]any{"ref": "PLUGIN"},
	}
	library, warnings = parseGradleCatalogLibraryValue(gradleToolsCatalogName, "plugin", pluginValue, pluginVersions, gradleToolsCatalogPath)
	if len(warnings) != 0 || library.Version != "2.0.0" {
		t.Fatalf("expected nested version ref to resolve case-insensitively, got %#v %#v", library, warnings)
	}
	namedValue := map[string]any{
		"group":   "dev.example",
		"name":    "named",
		"version": "1.2.3",
	}
	library, warnings = parseGradleCatalogLibraryValue(gradleToolsCatalogName, "named", namedValue, nil, gradleToolsCatalogPath)
	if len(warnings) != 0 || library.Group != "dev.example" || library.Artifact != "named" || library.Version != "1.2.3" {
		t.Fatalf("expected group/name catalog dependency to parse, got %#v %#v", library, warnings)
	}
	if _, warnings = parseGradleCatalogLibraryValue(gradleToolsCatalogName, "broken", "not-a-coordinate", nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected invalid string library to emit a warning")
	}
	if _, warnings = parseGradleCatalogLibraryValue(gradleToolsCatalogName, "bad-module", map[string]any{"module": "bad-module"}, nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected invalid module field to emit a warning")
	}
	if _, warnings = parseGradleCatalogLibraryValue(gradleToolsCatalogName, "missing-fields", map[string]any{"version": "1.0.0"}, nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected missing coordinates to emit a warning")
	}
	if _, warnings = parseGradleCatalogLibraryValue(gradleToolsCatalogName, "invalid", 42, nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected unsupported library format to emit a warning")
	}
}

func TestGradleCatalogVersionAndCoordinateHelpers(t *testing.T) {
	if version := resolveGradleCatalogVersionFields(map[string]any{"version": "1.0.0"}, nil); version != "1.0.0" {
		t.Fatalf("expected explicit version field to win, got %q", version)
	}
	if version := resolveGradleCatalogVersionFields(map[string]any{"version.ref": "shared"}, map[string]string{"shared": "2.0.0"}); version != "2.0.0" {
		t.Fatalf("expected version ref lookup to resolve, got %q", version)
	}
	if version := resolveGradleCatalogVersionFields(map[string]any{"version": map[string]any{"ref": "SHARED"}}, map[string]string{"shared": "3.0.0"}); version != "3.0.0" {
		t.Fatalf("expected nested version ref to resolve, got %q", version)
	}
	if version := resolveGradleCatalogVersionFields(nil, nil); version != "" {
		t.Fatalf("expected missing version to stay empty, got %q", version)
	}

	if _, _, _, ok := parseGradleCatalogCoordinates("group:artifact"); !ok {
		t.Fatalf("expected two-part coordinates to parse")
	}
	if _, _, _, ok := parseGradleCatalogCoordinates("group:artifact:1.0.0"); !ok {
		t.Fatalf("expected three-part coordinates to parse")
	}
	if _, _, _, ok := parseGradleCatalogCoordinates("too:many:parts:here"); ok {
		t.Fatalf("expected invalid coordinates to be rejected")
	}
	if _, _, _, ok := parseGradleCatalogCoordinates(":artifact:1.0.0"); ok {
		t.Fatalf("expected coordinates missing a group to be rejected")
	}
}

func TestGradleCatalogDecodedParserDefensiveBranches(t *testing.T) {
	parser := newGradleCatalogFileParser(gradleCatalogName, gradleDefaultCatalogPath)
	parser.consumeDocument(map[string]any{
		"versions":  "not-a-table",
		"libraries": "not-a-table",
		"bundles":   "not-a-table",
	})
	parsed, warnings := parser.finalize()
	if len(parsed.libraries) != 0 || len(parsed.bundles) != 0 || len(warnings) != 0 {
		t.Fatalf("expected non-table decoded sections to be ignored, got %#v %#v", parsed, warnings)
	}

	parser.consumeVersionTable(map[string]any{"empty": "", "bad": 12})
	if len(parser.versions) != 0 {
		t.Fatalf("expected invalid decoded versions to be ignored, got %#v", parser.versions)
	}
	if got := parseGradleCatalogBundleValue([]any{"okhttp", "", 12}); len(got) != 1 || got[0] != "okhttp" {
		t.Fatalf("expected decoded bundle values to keep only strings, got %#v", got)
	}
	if got := parseGradleCatalogBundleValue("bad"); len(got) != 0 {
		t.Fatalf("expected non-array bundle value to be ignored, got %#v", got)
	}
	if version := resolveGradleCatalogVersionFields(map[string]any{"version": 12, "version.ref": 13}, nil); version != "" {
		t.Fatalf("expected non-string version fields to be ignored, got %q", version)
	}
}

func TestGradleCatalogPathAndWarningHelpers(t *testing.T) {
	if got := normalizeGradleCatalogAccessor(" libs-network . core "); got != "libs.network.core" {
		t.Fatalf("expected accessor to normalize separators and spaces, got %q", got)
	}
	if got := normalizeGradleCatalogAccessor("   "); got != "" {
		t.Fatalf("expected empty accessor to stay empty, got %q", got)
	}
	if got := normalizeGradleCatalogExpression(" libs . bundles . networking "); got != "libs.bundles.networking" {
		t.Fatalf("expected expression to normalize, got %q", got)
	}
	if !isGradleCatalogSubPath(testAppRoot, testAppRoot) {
		t.Fatalf("expected exact root match to count as a subpath")
	}
	if !isGradleCatalogSubPath(testAppRoot, testBuildFile) || isGradleCatalogSubPath(testAppRoot, testOtherBuildFile) {
		t.Fatalf("expected subpath detection to distinguish matching roots")
	}
	if got := relativeGradleCatalogPath(testRepoRoot, testRepoCatalogPath); got != gradleDefaultCatalogPath {
		t.Fatalf("expected relative catalog path to be repo-relative, got %q", got)
	}
	if got := relativeGradleCatalogPath(testRepoRoot, gradleDefaultCatalogPath); got != gradleDefaultCatalogPath {
		t.Fatalf("expected mixed absolute/relative paths to fall back to the original path, got %q", got)
	}
	if got := relativeGradleCatalogPathFromFile(filepath.Join(testRepoRoot, "app", "..", "gradle", gradleDefaultCatalogFileName)); got != testRepoCatalogPath {
		t.Fatalf("expected cleaned file path, got %q", got)
	}
	if got := formatGradleCatalogReadWarning(testRepoRoot, testRepoCatalogPath, fs.ErrPermission); got != fmt.Sprintf("unable to read %s: %v", gradleDefaultCatalogPath, fs.ErrPermission) {
		t.Fatalf("expected read warning to be repo-relative, got %q", got)
	}

	if warnings := DedupeWarnings(nil); len(warnings) != 0 {
		t.Fatalf("expected nil warning slice to stay nil, got %#v", warnings)
	}
	if got := DedupeWarnings([]string{" second ", "", "first", "second"}); strings.Join(got, ",") != "first,second" {
		t.Fatalf("expected warnings to be trimmed, deduped, and sorted, got %#v", got)
	}
}

func TestGradleCatalogLookupIndexCachesScopeMatches(t *testing.T) {
	resolver := GradleCatalogResolver{
		scopes: []gradleCatalogScope{
			{root: filepath.Clean(testAppRoot)},
		},
	}
	if resolver.lookup.scopesByRoot != nil {
		t.Fatalf("expected zero-value resolver lookup index to be uninitialized")
	}

	scope := resolver.scopeForBuildFile(testBuildFile)
	if scope == nil || scope.root != filepath.Clean(testAppRoot) {
		t.Fatalf("expected build file to resolve against app scope, got %#v", scope)
	}
	if resolver.lookup.scopesByRoot == nil || resolver.lookup.cachedScopes[filepath.Clean(testBuildFile)] == nil {
		t.Fatalf("expected lookup index and cache to be initialized, got %#v", resolver.lookup)
	}

	if unresolved := resolver.scopeForBuildFile(testOtherBuildFile); unresolved != nil {
		t.Fatalf("expected non-matching build file to stay unresolved, got %#v", unresolved)
	}
	if _, ok := resolver.lookup.cachedUnmatched[filepath.Clean(testOtherBuildFile)]; !ok {
		t.Fatalf("expected unmatched lookup to be cached, got %#v", resolver.lookup.cachedUnmatched)
	}
}

func TestGradleCatalogReferenceCollectorIgnoresUnknownInputs(t *testing.T) {
	resolver := GradleCatalogResolver{knownCatalogs: map[string]struct{}{gradleCatalogName: {}}}
	collector := newGradleCatalogReferenceCollector(&resolver, testBuildFile)
	collector.collectReferences(`
dependencies {
  implementation(unknownCatalog.findLibrary("missing").get())
  implementation(unknownCatalog["missing"])
  implementation(libs)
}
`)
	if len(collector.dependencies) != 0 || len(collector.warnings) != 0 {
		t.Fatalf("expected unmatched collector inputs to be ignored, got %#v %#v", collector.dependencies, collector.warnings)
	}
}

func assertGradleCatalogWarningsContain(t *testing.T, warnings []string, wants ...string) {
	t.Helper()
	joined := strings.Join(warnings, "\n")
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected warning %q in %q", want, joined)
		}
	}
}
