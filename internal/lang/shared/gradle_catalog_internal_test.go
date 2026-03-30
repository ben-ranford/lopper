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
retrofit = { module = "com.squareup.retrofit2:retrofit",
  version = "2.11.0" }
okhttp = { module = "com.squareup.okhttp3:okhttp", version.ref = "shared" }
multiline = { group = "org.sample",
  name = "artifact",
  version = { ref = "AGP" } }
pending-lib = { group = "org.pending",
  name = "pending-lib",
  version = "1.0.0" }
broken = "not-a-coordinate"
bad-module = { module = "missing-artifact", version = "1.0.0" }
missing-fields = { version = "1.0.0" }

[bundles]
networking = ["retrofit", "okhttp", "retrofit", "missing"]
unsupported = []
broken-tail = { group = "org.tail"
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
		`unterminated Gradle version catalog entry "broken-tail" in ` + gradleDefaultCatalogPath,
	}
	assertGradleCatalogWarningsContain(t, warnings, expectedWarnings...)
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
	registry := newGradleCatalogRegistry(repo)
	missingSettingsPath := filepath.Join(repo, "missing", gradleSettingsFileName)
	missingCatalogPath := filepath.Join(repo, "gradle", "missing.versions.toml")
	vendorCatalogPath := filepath.Join(repo, "vendor", gradleDefaultCatalogFileName)
	defaultCatalogPath := filepath.Join(repo, "gradle", gradleDefaultCatalogFileName)
	overrideCatalogPath := filepath.Join(repo, "gradle", gradleOverrideCatalogFileName)
	otherCatalogPath := filepath.Join(repo, "gradle", "other.versions.toml")
	settingsPath := filepath.Join(repo, gradleSettingsFileName)

	registry.loadSettingsFile(missingSettingsPath)
	registry.loadSource(gradleCatalogSource{
		root: repo,
		name: gradleCatalogName,
		path: missingCatalogPath,
	})
	initialWarnings := []string{
		"unable to read missing/" + gradleSettingsFileName + ":",
		"unable to read gradle/missing.versions.toml:",
	}
	assertGradleCatalogWarningsContain(t, registry.warnings, initialWarnings...)

	if err := maybeSkipGradleCatalogDirectory(&stubGradleCatalogDirEntry{name: "build"}); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected build directory to be skipped, got %v", err)
	}
	if err := maybeSkipGradleCatalogDirectory(&stubGradleCatalogDirEntry{name: "src"}); err != nil {
		t.Fatalf("expected normal directory to be scanned, got %v", err)
	}

	relativePath := resolveGradleCatalogSourcePath(repo, "gradle/test-libs.versions.toml")
	if want := filepath.Join(repo, "gradle", "test-libs.versions.toml"); relativePath != want {
		t.Fatalf("expected relative catalog path to resolve under repo, got %q want %q", relativePath, want)
	}
	absolutePath := resolveGradleCatalogSourcePath(repo, defaultCatalogPath)
	if want := defaultCatalogPath; absolutePath != want {
		t.Fatalf("expected absolute catalog path to stay absolute, got %q want %q", absolutePath, want)
	}

	registry.trackKnownCatalog(" TestLibs ")
	if _, ok := registry.knownCatalogs["testlibs"]; !ok {
		t.Fatalf("expected known catalogs to be normalized, got %#v", registry.knownCatalogs)
	}
	registry.trackKnownCatalog("   ")
	registry.registerSource(repo, "", "")

	registry.registerDefaultCatalog(vendorCatalogPath)
	registry.registerDefaultCatalog(defaultCatalogPath)
	registry.registerSource(repo, gradleCatalogName, overrideCatalogPath)
	registry.registerSource(repo, gradleCatalogName, otherCatalogPath)

	scope := registry.ensureScope(repo)
	registry.mergeLibraries(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: defaultCatalogPath}, map[string]GradleCatalogLibrary{
		"okhttp": {Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: okhttpVersion},
	})
	registry.mergeLibraries(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: overrideCatalogPath}, map[string]GradleCatalogLibrary{
		"okhttp": {Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: "4.13.0"},
	})
	registry.mergeBundles(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: defaultCatalogPath}, map[string][]GradleCatalogLibrary{
		"networking": {
			{Catalog: gradleCatalogName, Alias: "okhttp", Group: okhttpGroup, Artifact: "okhttp", Version: okhttpVersion},
		},
	})
	registry.mergeBundles(scope, gradleCatalogSource{root: repo, name: gradleCatalogName, path: overrideCatalogPath}, map[string][]GradleCatalogLibrary{
		"networking": {
			{Catalog: gradleCatalogName, Alias: "retrofit", Group: "com.squareup.retrofit2", Artifact: "retrofit", Version: "2.11.0"},
		},
	})
	registryWarnings := []string{
		fmt.Sprintf("multiple Gradle version catalog sources configured for libs under %s; using %s", filepath.Clean(repo), defaultCatalogPath),
		fmt.Sprintf("Gradle version catalog alias libs.%s resolves to multiple coordinates under %s; keeping %s:%s", "okhttp", filepath.Clean(repo), okhttpGroup, "okhttp"),
		fmt.Sprintf("Gradle version catalog bundle libs.%s resolves to multiple dependency sets under %s; keeping the first definition", "networking", filepath.Clean(repo)),
	}
	assertGradleCatalogWarningsContain(t, registry.warnings, registryWarnings...)

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
	settingsWarnings := []string{
		"multiple Gradle version catalog files declared for tools in " + gradleSettingsFileName + "; using " + gradleToolsCatalogPath,
		"unsupported Gradle version catalog source for dynamic in " + gradleSettingsFileName,
	}
	assertGradleCatalogWarningsContain(t, warnings, settingsWarnings...)

	writeGradleCatalogTestFile(t, settingsPath, `
dependencyResolutionManagement {
  versionCatalogs {
    create("dynamic") {
      from(dynamicCatalogProvider())
    }
  }
}
`)
	registry = newGradleCatalogRegistry(repo)
	registry.loadSettingsFile(settingsPath)
	if _, ok := registry.knownCatalogs["dynamic"]; !ok {
		t.Fatalf("expected settings loader to track catalogs without file-backed sources, got %#v", registry.knownCatalogs)
	}
	if len(registry.sources) != 0 {
		t.Fatalf("expected unsupported settings source to avoid source registration, got %#v", registry.sources)
	}

	missingRepoResolver, warnings := LoadGradleCatalogResolver(filepath.Join(repo, "does-not-exist"))
	if len(missingRepoResolver.scopes) != 0 {
		t.Fatalf("expected missing repo to produce an empty resolver, got %#v", missingRepoResolver)
	}
	assertGradleCatalogWarningsContain(t, warnings, "unable to scan Gradle version catalogs:")

	emptyResolver, warnings := LoadGradleCatalogResolver("")
	if len(warnings) != 0 || len(emptyResolver.knownCatalogs) != 0 || len(emptyResolver.scopes) != 0 {
		t.Fatalf("expected empty repo path to return an empty resolver, got %#v %#v", emptyResolver, warnings)
	}

	registry = newGradleCatalogRegistry(repo)
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

func TestGradleCatalogStringAndAssignmentHelpers(t *testing.T) {
	if got, ok := parseGradleCatalogStringValue(`'1.2.3'`); !ok || got != "1.2.3" {
		t.Fatalf("expected single-quoted value to parse, got %q %t", got, ok)
	}
	if got, ok := parseGradleCatalogStringValue("oops"); ok || got != "" {
		t.Fatalf("expected unquoted value to be rejected, got %q %t", got, ok)
	}
	if got, ok := parseGradleCatalogSection("[Libraries]"); !ok || got != "libraries" {
		t.Fatalf("expected section header to normalize, got %q %t", got, ok)
	}
	if _, ok := parseGradleCatalogSection("libraries"); ok {
		t.Fatalf("expected invalid section to be rejected")
	}
	if key, value, ok := parseGradleCatalogAssignment(` "demo-lib" = "org.example:demo:1.0.0" `); !ok || key != "demo-lib" || value != `"org.example:demo:1.0.0"` {
		t.Fatalf("expected assignment to parse, got %q %q %t", key, value, ok)
	}
	if _, _, ok := parseGradleCatalogAssignment("missing-equals"); ok {
		t.Fatalf("expected malformed assignment to be rejected")
	}
	if _, _, ok := parseGradleCatalogAssignment("name = "); ok {
		t.Fatalf("expected assignment missing a value to be rejected")
	}
}

func TestGradleCatalogCommentAndDelimiterHelpers(t *testing.T) {
	if got := stripGradleCatalogComment(`group = "org.example#demo" # comment`); got != `group = "org.example#demo" ` {
		t.Fatalf("expected inline comment to be stripped without touching quoted hash, got %q", got)
	}
	if got := stripGradleCatalogComment(`group = 'org.example#demo' # comment`); got != `group = 'org.example#demo' ` {
		t.Fatalf("expected single-quoted hash to be preserved, got %q", got)
	}
	if got := extractGradleCatalogQuotedStrings(`["one", 'two', "three"]`); strings.Join(got, ",") != "one,two,three" {
		t.Fatalf("expected quoted members to be extracted, got %#v", got)
	}

	if !gradleCatalogValueBalanced(`{ module = "org.example:demo", version = { ref = "v" } }`) {
		t.Fatalf("expected balanced inline table to be treated as complete")
	}
	if gradleCatalogValueBalanced(`{ module = "org.example:demo"`) {
		t.Fatalf("expected unbalanced inline table to be treated as incomplete")
	}
	state := gradleCatalogDelimiterState{}
	state.consume('"')
	state.consume('{')
	state.consume('"')
	state.consume('{')
	state.consume('}')
	if !state.balanced() {
		t.Fatalf("expected delimiter state to ignore braces inside quotes and balance real braces")
	}
	if delta, ok := gradleCatalogBraceDelta('{'); !ok || delta != 1 {
		t.Fatalf("expected opening brace to increment depth, got %d %t", delta, ok)
	}
	if delta, ok := gradleCatalogBracketDelta(']'); !ok || delta != -1 {
		t.Fatalf("expected closing bracket to decrement depth, got %d %t", delta, ok)
	}
	quoteState := gradleCatalogDelimiterState{inSingle: true}
	if quoteState.toggleQuote('"') {
		t.Fatalf("expected double quote inside single-quoted string to be ignored")
	}
	if !quoteState.toggleQuote('\'') || quoteState.inQuoted() {
		t.Fatalf("expected matching single quote to toggle single-quoted state off")
	}
	quoteState = gradleCatalogDelimiterState{inDouble: true}
	if quoteState.toggleQuote('\'') {
		t.Fatalf("expected single quote inside double-quoted string to be ignored")
	}
}

func TestGradleCatalogLibraryEntryHelpers(t *testing.T) {
	library, warnings := parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "cli", `"dev.example:cli:1.0.0"`, nil, gradleToolsCatalogPath)
	if len(warnings) != 0 || library.Group != "dev.example" || library.Artifact != "cli" || library.Version != "1.0.0" {
		t.Fatalf("expected string catalog dependency to parse, got %#v %#v", library, warnings)
	}
	pluginVersions := map[string]string{"plugin": "2.0.0"}
	library, warnings = parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "plugin", `{ module = "dev.example:plugin", version = { ref = "PLUGIN" } }`, pluginVersions, gradleToolsCatalogPath)
	if len(warnings) != 0 || library.Version != "2.0.0" {
		t.Fatalf("expected nested version ref to resolve case-insensitively, got %#v %#v", library, warnings)
	}
	if _, warnings = parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "broken", `"not-a-coordinate"`, nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected invalid string library to emit a warning")
	}
	if _, warnings = parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "bad-module", `{ module = "bad-module", version = "1.0.0" }`, nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected invalid module field to emit a warning")
	}
	if _, warnings = parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "missing-fields", `{ version = "1.0.0" }`, nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected missing coordinates to emit a warning")
	}
	if _, warnings = parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "invalid", "provider(\"coords\")", nil, gradleToolsCatalogPath); len(warnings) == 0 {
		t.Fatalf("expected unsupported library format to emit a warning")
	}
	emptyLibrary, warnings := parseGradleCatalogLibraryEntry(gradleToolsCatalogName, "empty", "   ", nil, gradleToolsCatalogPath)
	if len(warnings) != 0 || emptyLibrary.Group != "" || emptyLibrary.Artifact != "" || emptyLibrary.Alias != "empty" {
		t.Fatalf("expected blank library value to be ignored, got %#v %#v", emptyLibrary, warnings)
	}
}

func TestGradleCatalogVersionAndCoordinateHelpers(t *testing.T) {
	if version := resolveGradleCatalogVersion(map[string]string{"version": "1.0.0"}, "", nil); version != "1.0.0" {
		t.Fatalf("expected explicit version field to win, got %q", version)
	}
	if version := resolveGradleCatalogVersion(map[string]string{"version.ref": "shared"}, "", map[string]string{"shared": "2.0.0"}); version != "2.0.0" {
		t.Fatalf("expected version ref lookup to resolve, got %q", version)
	}
	if version := resolveGradleCatalogVersion(nil, `{ version = { ref = "SHARED" } }`, map[string]string{"shared": "3.0.0"}); version != "3.0.0" {
		t.Fatalf("expected nested version ref to resolve, got %q", version)
	}
	if version := resolveGradleCatalogVersion(nil, "", nil); version != "" {
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
	if got := parseGradleCatalogNestedVersionRef(`{ version = { ref = "shared" } }`); got != "shared" {
		t.Fatalf("expected nested version ref to parse, got %q", got)
	}
	if got := parseGradleCatalogNestedVersionRef(`{ version = "1.0.0" }`); got != "" {
		t.Fatalf("expected missing nested version ref to return empty, got %q", got)
	}
}

func TestGradleCatalogParserInputHelpers(t *testing.T) {
	parser := newGradleCatalogFileParser(gradleCatalogName, gradleDefaultCatalogPath)
	parser.consumeLine("not an assignment")
	parser.consumeVersionEntry("bad", "nope")
	if len(parser.versions) != 0 {
		t.Fatalf("expected invalid parser inputs to leave version table empty, got %#v", parser.versions)
	}
	if got, ok := parseGradleCatalogStringValue(`"`); ok || got != "" {
		t.Fatalf("expected short quoted value to be rejected, got %q %t", got, ok)
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

func TestGradleCatalogReferenceCollectorIgnoresUnknownInputs(t *testing.T) {
	resolver := GradleCatalogResolver{knownCatalogs: map[string]struct{}{gradleCatalogName: {}}}
	collector := newGradleCatalogReferenceCollector(&resolver, testBuildFile)
	collector.collectFinderMatches(`implementation(unknownCatalog.findLibrary("missing").get())`)
	collector.collectBracketMatches(`implementation(unknownCatalog["missing"])`)
	collector.handlePropertyExpression(gradleCatalogName)
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
