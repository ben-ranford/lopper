package jvm

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	junitJupiterAPIName = "junit-jupiter-api"
	junitJupiterGroup   = "org.junit.jupiter"
	acmeLibName         = "acme-lib"
	jvmGradleDirName    = ".gradle"
)

func writeJVMPomFile(t *testing.T, repo, content string) {
	t.Helper()

	testutil.MustWriteFile(t, filepath.Join(repo, "pom.xml"), content)
}

func canonicalRepoPath(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	resolved, err := filepath.EvalSymlinks(repo)
	if err == nil && resolved != "" {
		return resolved
	}
	return repo
}

func managedDependencyManagementPOM(properties, junitVersion, springVersion string) string {
	propertiesBlock := ""
	if strings.TrimSpace(properties) != "" {
		propertiesBlock = fmt.Sprintf("\n  <properties>\n%s\n  </properties>", properties)
	}

	springVersionBlock := ""
	if strings.TrimSpace(springVersion) != "" {
		springVersionBlock = fmt.Sprintf("\n        <version>%s</version>", springVersion)
	}

	template := `
<project>%s
  <dependencyManagement>
    <dependencies>
      <dependency>
        <groupId>org.junit.jupiter</groupId>
        <artifactId>junit-jupiter-api</artifactId>
        <version>%s</version>
      </dependency>
      <dependency>
        <groupId>org.springframework.boot</groupId>
        <artifactId>spring-boot-dependencies</artifactId>%s
        <type>pom</type>
        <scope>import</scope>
      </dependency>
    </dependencies>
  </dependencyManagement>
</project>
`

	return fmt.Sprintf(template, propertiesBlock, junitVersion, springVersionBlock)
}

func TestJVMParsePackageAndImports(t *testing.T) {
	content := []byte("package com.example.app;\nimport java.util.List;\nimport org.junit.jupiter.api.Test;\nimport com.acme.lib.Widget;\n")
	pkg := parsePackage(content)
	if pkg != "com.example.app" {
		t.Fatalf("unexpected parsed package: %q", pkg)
	}

	prefixes := map[string]string{junitJupiterGroup: junitJupiterAPIName}
	aliases := map[string]string{"com.acme": acmeLibName}
	imports := parseImports(content, "App.java", pkg, prefixes, aliases)
	if len(imports) != 2 {
		t.Fatalf("expected two non-stdlib imports, got %#v", imports)
	}
	if imports[0].Dependency == "" || imports[1].Dependency == "" {
		t.Fatalf("expected dependencies to be resolved: %#v", imports)
	}
}

func TestJVMParseImportsSupportsKotlinBacktickAlias(t *testing.T) {
	content := []byte("import com.acme.`when`.Widget as `foo-bar`\nfun use() { `foo-bar`() }\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if len(imports) != 1 || imports[0].Module != "com.acme.`when`.Widget" || imports[0].Local != "foo-bar" || imports[0].Dependency != acmeLibName {
		t.Fatalf("expected Kotlin backtick alias import, got %#v", imports)
	}
	if usage := countUsage(content, imports); usage["foo-bar"] != 1 {
		t.Fatalf("expected escaped Kotlin alias usage to count once, got %#v", usage)
	}
}

func TestJVMCountUsageKeepsUnescapedKotlinAliasUse(t *testing.T) {
	content := []byte("import com.acme.Widget as `WidgetAlias`\nimport com.acme.Getter as `get`\nimport com.acme.Setter as `set`\nimport com.acme.Delegate as `by`\nimport com.acme.Record as `data`\nimport com.acme.Access as `open`\nimport com.acme.Value as `value`\nfun use() { WidgetAlias(); get(); set(); by(); data(); open(); value() }\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if usage := countUsage(content, imports); usage["WidgetAlias"] != 1 || usage["get"] != 1 || usage["set"] != 1 || usage["by"] != 1 || usage["data"] != 1 || usage["open"] != 1 || usage["value"] != 1 {
		t.Fatalf("expected unescaped Kotlin aliases, including soft keywords, to count once, got %#v", usage)
	}
}

func TestJVMParseImportsKeepsDottedEscapedFinalSegment(t *testing.T) {
	content := []byte("import com.acme.`foo.bar`\nfun use() { `foo.bar`() }\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if len(imports) != 1 || imports[0].Name != "`foo.bar`" || imports[0].Local != "foo.bar" || countUsage(content, imports)["foo.bar"] != 1 {
		t.Fatalf("expected dotted escaped final segment to be preserved, got %#v", imports)
	}
}

func TestJVMCountUsageKeepsUnescapedUnicodeKotlinAliasUse(t *testing.T) {
	content := []byte("import com.acme.Widget as `π`\nfun use() { π() }\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if usage := countUsage(content, imports); usage["π"] != 1 {
		t.Fatalf("expected unescaped Unicode Kotlin alias usage to count once, got %#v", usage)
	}
}

func TestJVMCountUsageSupportsEscapedNonBareKotlinAliases(t *testing.T) {
	content := []byte("import com.acme.Widget as /* note */\t`foo bar` // note\nimport com.acme.Gadget as   `foo.bar` /* note */\nimport com.acme.Quoted as `foo\"bar`\nfun use() { `foo bar`(); `foo.bar`(); `foo\"bar`() }\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	usage := countUsage(content, imports)
	if usage["foo bar"] != 1 || usage["foo.bar"] != 1 || usage["foo\"bar"] != 1 {
		t.Fatalf("expected escaped non-bare Kotlin aliases to count once, got %#v", usage)
	}
}

func TestJVMCountUsageExcludesEscapedMarkersOnDeclarationLine(t *testing.T) {
	content := []byte("package\tcom.example.`when`\nimport\tcom.acme.Widget as `when`\nimport\tcom.acme.`when`.Other\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if usage := countUsage(content, imports); usage["when"] != 0 {
		t.Fatalf("expected package and import markers not to count as usage, got %#v", usage)
	}
}

func TestJVMParseImportsNormalizesEscapedModuleSegmentsForLookup(t *testing.T) {
	content := []byte("import com.acme.`when`.Widget\n")
	imports := parseImports(content, "App.kt", "com.example.app", map[string]string{"com.acme.when": acmeLibName}, nil)
	if len(imports) != 1 || imports[0].Module != "com.acme.`when`.Widget" || imports[0].Dependency != acmeLibName {
		t.Fatalf("expected escaped module segment to resolve through normalized prefix, got %#v", imports)
	}
}

func TestJVMParsePackageSupportsKotlinBacktickSegments(t *testing.T) {
	content := []byte("package com.example.`when`\nimport com.example.`when`.util.Helper\n")
	pkg := parsePackage(content)
	if pkg != "com.example.when" {
		t.Fatalf("unexpected parsed escaped package: %q", pkg)
	}

	imports := parseImports(content, "App.kt", pkg, nil, map[string]string{"com.example": acmeLibName})
	if len(imports) != 0 {
		t.Fatalf("expected escaped package-local import to be ignored, got %#v", imports)
	}
}

func TestJVMParseImportsHandlesBlockComments(t *testing.T) {
	content := []byte(`package com.example.app;
import java.util.List;
import org.junit.jupiter.api.Test; /* trailing block comment */
/*
import com.acme.lib.Commented;
*/
import com.acme.lib.Widget;
`)

	pkg := parsePackage(content)
	prefixes := map[string]string{junitJupiterGroup: junitJupiterAPIName}
	aliases := map[string]string{"com.acme": acmeLibName}

	imports := parseImports(content, "App.java", pkg, prefixes, aliases)
	if len(imports) != 2 {
		t.Fatalf("expected two imports outside block comments, got %#v", imports)
	}
	if imports[0].Module != "org.junit.jupiter.api.Test" {
		t.Fatalf("expected trailing block-comment import to parse, got %#v", imports[0])
	}
	if imports[1].Module != "com.acme.lib.Widget" {
		t.Fatalf("expected non-commented import to parse, got %#v", imports[1])
	}
}

func TestJVMScanRepoInfersFallbackDependenciesForUnmappedImports(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", "App.kt")
	testutil.MustWriteFile(t, sourcePath, `package com.example.app
import com.example.app.LocalType
import custom.deep.feature.Type
import vendor.tools.Helper as AliasHelper
import single

fun use(input: Type, helper: AliasHelper) {
    println(input)
    println(helper)
}
`)

	result, err := scanRepo(context.Background(), repo, nil, nil)
	if err != nil {
		t.Fatalf("scan repo with fallback imports: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings for fallback import scan, got %#v", result.Warnings)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected one scanned file, got %#v", result.Files)
	}

	imports := result.Files[0].Imports
	if len(imports) != 3 {
		t.Fatalf("expected same-package import to be ignored while fallback imports remain, got %#v", imports)
	}
	if imports[0].Dependency != "custom.deep" || imports[0].Name != "Type" || imports[0].Local != "Type" {
		t.Fatalf("expected dotted fallback dependency to resolve first two segments, got %#v", imports[0])
	}
	if imports[1].Dependency != "vendor.tools" || imports[1].Local != "AliasHelper" {
		t.Fatalf("expected aliased fallback dependency to keep alias-local name, got %#v", imports[1])
	}
	if imports[2].Dependency != "single" || imports[2].Name != "single" {
		t.Fatalf("expected single-segment fallback dependency to survive scan, got %#v", imports[2])
	}
}

func TestJVMIgnoreAndResolveDependencyHelpers(t *testing.T) {
	pkg := "com.example.app"
	prefixes := map[string]string{junitJupiterGroup: junitJupiterAPIName}
	aliases := map[string]string{"com.acme": acmeLibName}
	ignoreCases := []struct {
		module string
		want   bool
	}{
		{module: "java.util.List", want: true},
		{module: "com.example.app.internal.Type", want: true},
		{module: "com.other.lib.Type", want: false},
	}
	for _, tc := range ignoreCases {
		if got := shouldIgnoreImport(tc.module, pkg); got != tc.want {
			t.Fatalf("shouldIgnoreImport(%q): expected %v, got %v", tc.module, tc.want, got)
		}
	}

	resolveCases := []struct {
		module   string
		prefixes map[string]string
		aliases  map[string]string
		want     string
	}{
		{module: "org.junit.jupiter.api.Test", prefixes: prefixes, aliases: aliases, want: junitJupiterAPIName},
		{module: "com.acme.lib.Widget", prefixes: map[string]string{}, aliases: aliases, want: acmeLibName},
	}
	for _, tc := range resolveCases {
		if got := resolveDependency(tc.module, tc.prefixes, tc.aliases); got != tc.want {
			t.Fatalf("resolveDependency(%q): expected %q, got %q", tc.module, tc.want, got)
		}
	}

	fallbackCases := []struct {
		module string
		want   string
	}{
		{module: "single", want: "single"},
		{module: "a.b.c", want: "a.b"},
	}
	for _, tc := range fallbackCases {
		if got := fallbackDependency(tc.module); got != tc.want {
			t.Fatalf("fallbackDependency(%q): expected %q, got %q", tc.module, tc.want, got)
		}
	}
}

func TestJVMParsingFormattingHelpers(t *testing.T) {
	if got := lastModuleSegment("a.b.C"); got != "C" {
		t.Fatalf("unexpected last module segment: %q", got)
	}
	if firstContentColumn("\t import x") <= 1 {
		t.Fatalf("expected firstContentColumn to detect indentation")
	}
	if got := stripLineComment("import a // trailing"); got != "import a " {
		t.Fatalf("unexpected stripLineComment result: %q", got)
	}
}

func TestJVMDescriptorAndBuildFileHelpers(t *testing.T) {
	descriptors := []dependencyDescriptor{
		{Name: "okhttp", Group: "com.squareup", Artifact: "okhttp"},
		{Name: "okhttp", Group: "com.squareup", Artifact: "okhttp"},
		{Name: "junit", Group: "org.junit", Artifact: "junit"},
	}
	deduped := dedupeAndSortDescriptors(descriptors)
	if len(deduped) != 2 {
		t.Fatalf("expected deduped descriptors, got %#v", deduped)
	}

	prefixes, aliases := buildDescriptorLookups(deduped)
	if prefixes["com.squareup.okhttp"] == "" {
		t.Fatalf("expected artifact prefix lookup")
	}
	if aliases["junit"] == "" {
		t.Fatalf("expected alias lookup for artifact")
	}

	if !matchesBuildFile(buildGradleName, []string{buildGradleName}) || matchesBuildFile("foo.txt", []string{buildGradleName}) {
		t.Fatalf("unexpected build file matching")
	}
	if !shouldSkipDir(".git") || !shouldSkipDir(jvmGradleDirName) || shouldSkipDir("src") {
		t.Fatalf("unexpected shouldSkipDir behavior")
	}

	repo := t.TempDir()
	writeJVMPomFile(t, repo, `
<project>
  <dependencies>
    <dependency>
      <groupId>org.junit</groupId>
      <artifactId>junit</artifactId>
    </dependency>
  </dependencies>
</project>
`)
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `implementation 'com.squareup.okhttp3:okhttp:4.12.0'`)
	poms := parsePomDependencies(repo)
	gradle := parseGradleDependencies(repo)
	if len(poms) == 0 || len(gradle) == 0 {
		t.Fatalf("expected pom and gradle dependencies, got pom=%#v gradle=%#v", poms, gradle)
	}
	all, _, _, _ := collectDeclaredDependencies(repo)
	names := make([]string, 0, len(all))
	for _, dep := range all {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "junit") || !slices.Contains(names, "okhttp") {
		t.Fatalf("expected declared dependencies from build files, got %#v", names)
	}
}

func TestJVMParseGradleDependenciesSupportsCommonConfigurations(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `
dependencies {
  annotationProcessor "org.projectlombok:lombok:1.18.32"
  testAnnotationProcessor("org.mapstruct:mapstruct-processor:1.6.0")
  testCompileOnly "org.jetbrains:annotations:24.1.0"
  debugImplementation "com.android.support:appcompat-v7:28.0.0"
  releaseImplementation("com.android.support:multidex:1.0.3")
  kaptTest "com.google.dagger:dagger-compiler:2.52"
  kaptAndroidTest("com.google.dagger:dagger-android-processor:2.52")
  classpath "com.android.tools.build:gradle:8.7.0"
}
`)

	descriptors, warnings := parseGradleDependenciesWithWarnings(repo)
	if len(warnings) != 0 {
		t.Fatalf("expected no gradle warnings, got %#v", warnings)
	}

	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		names = append(names, descriptor.Name)
	}

	expected := []string{
		"lombok",
		"mapstruct-processor",
		"annotations",
		"appcompat-v7",
		"multidex",
		"dagger-compiler",
		"dagger-android-processor",
		"gradle",
	}
	if len(descriptors) != len(expected) {
		t.Fatalf("expected %d gradle descriptors, got %#v", len(expected), descriptors)
	}

	for _, name := range expected {
		if !slices.Contains(names, name) {
			t.Fatalf("expected gradle dependency %q in %#v", name, descriptors)
		}
	}
}

func TestJVMParseGradleDependenciesWarnsOnOversizedBuildFiles(t *testing.T) {
	t.Parallel()

	for _, name := range []string{buildGradleName, buildGradleKTSName} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repo, name), strings.Repeat("a", maxScannableJVMBuildFile+1))

			descriptors, warnings := parseGradleDependenciesWithWarnings(repo)
			if len(descriptors) != 0 {
				t.Fatalf("expected no gradle descriptors from oversized %s, got %#v", name, descriptors)
			}
			warningText := strings.Join(warnings, "\n")
			if !strings.Contains(warningText, "unable to read "+name+": file exceeds size limit") {
				t.Fatalf("expected oversized %s warning, got %#v", name, warnings)
			}
		})
	}
}

func TestJVMCollectBuildDescriptorsResolveCatalogReferencesOutsideRoot(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "settings.gradle.kts"), `
dependencyResolutionManagement {
  versionCatalogs {
    create("testLibs") {
      from(files("gradle/test-libs.versions.toml"))
    }
  }
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "gradle", "test-libs.versions.toml"), `
[libraries]
junit-jupiter = { group = "org.junit.jupiter", name = "junit-jupiter-api", version = "5.10.0" }
`)
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleKTSName), `
dependencies {
  implementation(testLibs.junit.jupiter)
}
`)

	descriptors, warnings := collectBuildDescriptors(repo)
	if len(warnings) != 0 {
		t.Fatalf("expected catalog-backed build descriptor parsing without warnings, got %#v", warnings)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected catalog-backed build descriptor, got %#v", descriptors)
	}
}

func TestJVMParsePomDependenciesIncludesManagedAndBOMEntries(t *testing.T) {
	repo := t.TempDir()
	properties := `
    <junit.version>5.10.2</junit.version>
    <spring.boot.version>3.4.5</spring.boot.version>
`
	pomContent := managedDependencyManagementPOM(properties, "${junit.version}", "${spring.boot.version}")
	writeJVMPomFile(t, repo, pomContent)
	descriptors, warnings := parsePomDependenciesWithWarnings(repo)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for resolvable managed dependencies, got %#v", warnings)
	}

	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		names = append(names, descriptor.Name)
	}
	for _, name := range []string{"junit-jupiter-api", "spring-boot-dependencies"} {
		if !slices.Contains(names, name) {
			t.Fatalf("expected managed Maven dependency %q in %#v", name, descriptors)
		}
	}
}

func TestJVMParsePomDependenciesWarnsForUnresolvedManagedVersions(t *testing.T) {
	repo := t.TempDir()
	writeJVMPomFile(t, repo, managedDependencyManagementPOM("", "${missing.version}", ""))

	descriptors, warnings := parsePomDependenciesWithWarnings(repo)
	if len(descriptors) != 2 {
		t.Fatalf("expected managed dependencies to remain surfaced, got %#v", descriptors)
	}

	joined := strings.Join(warnings, "\n")
	for _, expected := range []string{
		"unable to resolve managed Maven version for org.junit.jupiter:junit-jupiter-api in pom.xml",
		"unable to resolve imported Maven BOM version for org.springframework.boot:spring-boot-dependencies in pom.xml",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected warning %q in %q", expected, joined)
		}
	}
}

func TestParsePomDependencyContentReturnsInvalidXMLWarning(t *testing.T) {
	descriptors, warnings := parsePomDependencyContent("pom.xml", "<project>")
	if len(descriptors) != 0 {
		t.Fatalf("expected invalid pom content to produce no descriptors, got %#v", descriptors)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to parse Maven POM pom.xml") {
		t.Fatalf("expected invalid pom warning, got %#v", warnings)
	}
}

func TestParsePomDependencyDropsUnresolvedManagedCoordinates(t *testing.T) {
	dependency := pomDependencyModel{
		GroupID:    "${missing.group}",
		ArtifactID: "demo-artifact",
		Version:    "1.0.0",
	}
	descriptor, warning := parsePomDependency(dependency, map[string]string{}, pomDependencyManaged, "pom.xml")
	if descriptor != (dependencyDescriptor{}) || warning != "" {
		t.Fatalf("expected unresolved managed coordinates to be dropped without warnings, got descriptor=%#v warning=%q", descriptor, warning)
	}
}

func TestBuildPomPropertyMapUsesParentFallbacksAndIgnoresBlankValues(t *testing.T) {
	propertyMap := buildPomPropertyMap(pomProjectModel{
		ArtifactID: "demo-artifact",
		Parent: pomParentModel{
			GroupID: "com.example.parent",
			Version: "1.2.3",
		},
		Properties: pomPropertiesModel{
			Properties: []pomPropertyModel{
				{XMLName: xml.Name{Local: "ok"}, Value: " value "},
				{XMLName: xml.Name{Local: ""}, Value: "ignored"},
				{XMLName: xml.Name{Local: "blankValue"}, Value: " "},
			},
		},
	})
	if propertyMap["ok"] != "value" {
		t.Fatalf("expected trimmed explicit property, got %#v", propertyMap)
	}
	if propertyMap["project.groupId"] != "com.example.parent" || propertyMap["project.version"] != "1.2.3" {
		t.Fatalf("expected parent fallback properties, got %#v", propertyMap)
	}
	if _, ok := propertyMap["blankValue"]; ok {
		t.Fatalf("expected blank-value property to be ignored, got %#v", propertyMap)
	}
}

func TestSetPomPropertyValueIgnoresBlankInputs(t *testing.T) {
	propertyMap := map[string]string{}
	setPomPropertyValue(propertyMap, "", "ignored")
	setPomPropertyValue(propertyMap, "ignored", "")
	if _, ok := propertyMap["ignored"]; ok {
		t.Fatalf("expected blank setter inputs to be ignored, got %#v", propertyMap)
	}
}

func TestParseBuildFilesSkipsNonBuildEntries(t *testing.T) {
	repo := t.TempDir()
	writeJVMPomFile(t, repo, `<project/>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "README.md"), "no build files here")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	buildDescriptors := parseBuildFiles(repo, pomXMLName, func(string) []dependencyDescriptor {
		return []dependencyDescriptor{{Name: "demo", Group: "org.example", Artifact: "demo"}}
	})
	if len(buildDescriptors) != 1 {
		t.Fatalf("expected build file walk to collect one descriptor, got %#v", buildDescriptors)
	}

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	var readmeEntry fs.DirEntry
	var srcEntry fs.DirEntry
	for _, entry := range entries {
		switch entry.Name() {
		case "README.md":
			readmeEntry = entry
		case "src":
			srcEntry = entry
		}
	}
	if readmeEntry == nil || srcEntry == nil {
		t.Fatalf("expected README and src entries, got %#v", entries)
	}

	collected := []dependencyDescriptor{{Name: "existing", Group: "org.example", Artifact: "existing"}}
	seen := map[string]struct{}{}
	if err := parseBuildFileEntry(repo, filepath.Join(repo, "src"), srcEntry, []string{pomXMLName}, func(string) []dependencyDescriptor { return nil }, seen, &collected); err != nil {
		t.Fatalf("expected non-skipped directory to be ignored, got %v", err)
	}
	if err := parseBuildFileEntry(repo, filepath.Join(repo, "README.md"), readmeEntry, []string{pomXMLName}, func(string) []dependencyDescriptor { return nil }, seen, &collected); err != nil {
		t.Fatalf("expected non-build file to be ignored, got %v", err)
	}
	if len(collected) != 1 || collected[0].Name != "existing" {
		t.Fatalf("expected non-build entries to leave descriptors unchanged, got %#v", collected)
	}
}

func TestJVMShouldSkipDirHasNoPerCallAllocations(t *testing.T) {
	allocs := testing.AllocsPerRun(1000, func() {
		_ = shouldSkipDir(jvmGradleDirName)
		_ = shouldSkipDir("src")
	})
	if allocs != 0 {
		t.Fatalf("expected zero allocations per shouldSkipDir call, got %v", allocs)
	}
	if !shouldSkipDir(".gradle") || shouldSkipDir("src") {
		t.Fatalf("unexpected shouldSkipDir behavior")
	}
}

func TestJVMLookupStrategyBuilders(t *testing.T) {
	prefixes := map[string]string{}
	aliases := map[string]string{}

	addGroupLookups(prefixes, aliases, "dep", junitJupiterGroup)
	addArtifactLookups(prefixes, aliases, "dep", junitJupiterGroup, junitJupiterAPIName)

	if got := prefixes[junitJupiterGroup]; got != "dep" {
		t.Fatalf("expected group prefix lookup, got %q", got)
	}
	if got := prefixes[junitJupiterGroup+".junit.jupiter.api"]; got != "dep" {
		t.Fatalf("expected artifact prefix lookup, got %q", got)
	}
	for _, key := range []string{junitJupiterGroup, "org.junit", "jupiter", "junit.jupiter.api"} {
		if got := aliases[key]; got != "dep" {
			t.Fatalf("expected alias %q to map to dep, got %q", key, got)
		}
	}

	customPrefixes := map[string]string{}
	customAliases := map[string]string{}
	addLookupByStrategy(customPrefixes, customAliases, "custom", "group", "artifact", func(group, artifact string) ([]string, []string) {
		return []string{group + "." + artifact}, []string{artifact}
	})
	if got := customPrefixes["group.artifact"]; got != "custom" {
		t.Fatalf("expected custom strategy prefix mapping, got %q", got)
	}
	if got := customAliases["artifact"]; got != "custom" {
		t.Fatalf("expected custom strategy alias mapping, got %q", got)
	}
}

func TestJVMScanAndRequestedDependencyBranches(t *testing.T) {
	if _, err := scanRepo(context.Background(), "", nil, nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected fs.ErrInvalid for empty repo path, got %v", err)
	}

	repo := t.TempDir()
	result, err := scanRepo(context.Background(), repo, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatalf("scan empty repo: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected warning for repo without source files")
	}

	deps, warnings := buildRequestedJVMDependencies(language.Request{}, scanResult{})
	if len(deps) != 0 {
		t.Fatalf("expected nil dependency list when no target is provided")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for missing dependency/topN target")
	}
}

func TestJVMDetectAndWalkBranches(t *testing.T) {
	adapter := NewAdapter()

	t.Run("confidence cap", func(t *testing.T) {
		repo := t.TempDir()
		for path, content := range map[string]string{
			"pom.xml":          "<project/>",
			buildGradleName:    "",
			"build.gradle.kts": "",
		} {
			testutil.MustWriteFile(t, filepath.Join(repo, path), content)
		}
		detection, err := adapter.DetectWithConfidence(context.Background(), repo)
		if err != nil {
			t.Fatalf("detect with confidence: %v", err)
		}
		if !detection.Matched || detection.Confidence != 95 {
			t.Fatalf("expected matched detection capped at 95, got %#v", detection)
		}
	})

	t.Run("max file walk budget", testJVMMaxTraversalWalkBudget)
	t.Run("escaping symlinks do not consume file budget", func(t *testing.T) {
		testJVMEscapingSymlinkFloodDoesNotConsumeCandidateBudget(t, adapter)
	})
	t.Run("oversized directory fails closed", testJVMOversizedDirectoryFailsClosed)
	t.Run("exact traversal budget completes", testJVMExactTraversalBudgetCompletes)
	t.Run("directory enumeration errors propagate", testJVMDetectionDirectoryErrors)
	t.Run("traversal budget guards queued entries", testJVMDetectionBudgetGuards)
	t.Run("traversal budget stops rejected-entry flood", testJVMTraversalBudgetStopsRejectedEntryFlood)
	t.Run("confined candidate budget still stops ordinary file flood", testJVMConfinedCandidateBudgetStopsOrdinaryFileFlood)
	t.Run("broken and escaping entries count toward traversal only", testJVMBrokenAndEscapingEntriesCountTowardTraversalOnly)
}

func testJVMMaxTraversalWalkBudget(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "class Main {}")
	entry := testutil.MustFirstFileEntry(t, repo)
	budget := &jvmDetectionBudget{maxTraversalEntries: 1, traversalEntriesSeen: 1}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	err := walkJVMDetectionEntry(repo, filepath.Join(repo, entry.Name()), entry, roots, detect, budget)
	if !errors.Is(err, errJVMDetectionTraversalLimit) {
		t.Fatalf("expected traversal-limit error, got %v", err)
	}
}

func testJVMOversizedDirectoryFailsClosed(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "z-seed"), 0o755); err != nil {
		t.Fatalf("mkdir seed directory: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "a-Main.java"), "class Main {}\n")
	rawEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read flood entries: %v", err)
	}
	markerEntry := rawEntries[0]
	fillEntry := rawEntries[1]

	budget := defaultJVMDetectionBudget()
	wantChildren := budget.maxTraversalEntries - 1
	directory := &jvmDetectionTestDirectory{
		fillEntry:     fillEntry,
		repeatEntries: wantChildren,
		overflowEntry: markerEntry,
	}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	walker := newJVMDetectionWalker(repo, roots, detect, budget)
	openCalls := 0
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		openCalls++
		return directory, nil
	}

	err = walker.walk()
	assertJVMOversizedDirectoryOutcome(t, jvmOversizedDirectoryOutcome{
		repo:        repo,
		err:         err,
		markerEntry: markerEntry,
		fillEntry:   fillEntry,
		directory:   directory,
		budget:      budget,
		detect:      detect,
		openCalls:   openCalls,
	})
	assertJVMOversizedDirectoryReads(t, directory, wantChildren)
}

type jvmOversizedDirectoryOutcome struct {
	repo        string
	err         error
	markerEntry fs.DirEntry
	fillEntry   fs.DirEntry
	directory   *jvmDetectionTestDirectory
	budget      *jvmDetectionBudget
	detect      *language.Detection
	openCalls   int
}

func assertJVMOversizedDirectoryOutcome(t *testing.T, outcome jvmOversizedDirectoryOutcome) {
	t.Helper()

	wantChildren := outcome.budget.maxTraversalEntries - 1
	if !errors.Is(outcome.err, errJVMDetectionTraversalLimit) {
		t.Fatalf("expected explicit traversal-limit error, got %v", outcome.err)
	}
	if !strings.Contains(outcome.err.Error(), outcome.repo) {
		t.Fatalf("expected traversal-limit error to contain directory path %q, got %v", outcome.repo, outcome.err)
	}
	if outcome.markerEntry.Name() >= outcome.fillEntry.Name() || !outcome.directory.overflowReturned {
		t.Fatalf("expected lexically early JVM marker to appear only in overflow probe, marker=%q fill=%q", outcome.markerEntry.Name(), outcome.fillEntry.Name())
	}
	if outcome.directory.entriesReturned != wantChildren+1 {
		t.Fatalf("expected %d bounded children plus one overflow probe, got %d", wantChildren, outcome.directory.entriesReturned)
	}
	if outcome.budget.traversalEntriesSeen != 1 || outcome.budget.traversalEntriesQueued != wantChildren {
		t.Fatalf("expected root visited and %d children bounded in queue, got seen=%d queued=%d", wantChildren, outcome.budget.traversalEntriesSeen, outcome.budget.traversalEntriesQueued)
	}
	if outcome.budget.confinedCandidatesSeen != 0 {
		t.Fatalf("expected overflow marker not to consume candidate budget, got %d", outcome.budget.confinedCandidatesSeen)
	}
	if outcome.detect.Matched {
		t.Fatalf("expected overflow marker not to produce a partial detection, got %#v", outcome.detect)
	}
	if outcome.openCalls != 1 || outcome.directory.closeCalls != 1 {
		t.Fatalf("expected one directory open/close, got opens=%d closes=%d", outcome.openCalls, outcome.directory.closeCalls)
	}
}

func assertJVMOversizedDirectoryReads(t *testing.T, directory *jvmDetectionTestDirectory, wantChildren int) {
	t.Helper()

	wantBudgetReads := (wantChildren + jvmDetectionReadBatchSize - 1) / jvmDetectionReadBatchSize
	if len(directory.readSizes) != wantBudgetReads+1 {
		t.Fatalf("expected %d bounded reads plus one overflow probe, got %d", wantBudgetReads, len(directory.readSizes))
	}
	for index, size := range directory.readSizes[:wantBudgetReads] {
		if size <= 0 || size > jvmDetectionReadBatchSize {
			t.Fatalf("read %d requested invalid batch size %d", index, size)
		}
	}
	if got := directory.readSizes[wantBudgetReads-1]; got != wantChildren%jvmDetectionReadBatchSize {
		t.Fatalf("expected final bounded read size %d, got %d", wantChildren%jvmDetectionReadBatchSize, got)
	}
	if got := directory.readSizes[wantBudgetReads]; got != 1 {
		t.Fatalf("expected one-entry overflow probe, got %d", got)
	}
}

func testJVMExactTraversalBudgetCompletes(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatalf("mkdir exact-budget directory %s: %v", name, err)
		}
	}

	budget := &jvmDetectionBudget{maxTraversalEntries: 3, maxConfinedCandidates: 2}
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	if err := walker.walk(); err != nil {
		t.Fatalf("expected complete tree at exact traversal budget to succeed, got %v", err)
	}
	if budget.traversalEntriesSeen != 3 || budget.traversalEntriesQueued != 0 {
		t.Fatalf("expected exact budget to drain all entries, got seen=%d queued=%d", budget.traversalEntriesSeen, budget.traversalEntriesQueued)
	}
}

func testJVMDetectionDirectoryErrors(t *testing.T) {
	t.Run("root open", testJVMDetectionRootOpenError)
	t.Run("open", testJVMDetectionDirectoryOpenError)
	t.Run("read and close", testJVMDetectionDirectoryReadAndCloseErrors)
	t.Run("no progress", testJVMDetectionDirectoryNoProgress)
	t.Run("oversized batch", testJVMDetectionDirectoryOversizedBatch)
	t.Run("limit probe read error", testJVMDetectionLimitProbeReadError)
	t.Run("limit probe entry and read error", testJVMDetectionLimitProbeEntryAndReadError)
	t.Run("limit probe no progress", testJVMDetectionLimitProbeNoProgress)
}

func testJVMDetectionRootOpenError(t *testing.T) {
	openErr := errors.New("open root")
	walker := newJVMDetectionWalker(t.TempDir(), map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return nil, openErr
	}
	if err := walker.walk(); !errors.Is(err, openErr) {
		t.Fatalf("expected root open error, got %v", err)
	}
}

func testJVMDetectionDirectoryOpenError(t *testing.T) {
	openErr := errors.New("open directory")
	repo := t.TempDir()
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return nil, openErr
	}
	if err := walker.walk(); !errors.Is(err, openErr) {
		t.Fatalf("expected directory open error, got %v", err)
	}
}

func testJVMDetectionDirectoryReadAndCloseErrors(t *testing.T) {
	readErr := errors.New("read directory")
	closeErr := errors.New("close directory")
	directory := &jvmDetectionTestDirectory{readErr: readErr, closeErr: closeErr}
	repo := t.TempDir()
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return directory, nil
	}
	err := walker.walk()
	if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined read and close errors, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected failed reader to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionDirectoryNoProgress(t *testing.T) {
	directory := &jvmDetectionTestDirectory{noProgress: true}
	repo := t.TempDir()
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return directory, nil
	}
	if err := walker.walk(); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected no-progress error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected no-progress reader to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionDirectoryOversizedBatch(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "seed"), 0o755); err != nil {
		t.Fatalf("mkdir oversized batch seed: %v", err)
	}
	seedEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read oversized batch seed: %v", err)
	}
	readErr := errors.New("read oversized detection batch")
	directory := &jvmDetectionTestDirectory{
		fillEntry:          seedEntries[0],
		extraEntries:       1,
		readErrWithEntries: errors.Join(io.EOF, readErr),
	}
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return directory, nil
	}
	if err := walker.walk(); !errors.Is(err, errJVMDetectionTraversalLimit) || !errors.Is(err, io.EOF) || !errors.Is(err, readErr) {
		t.Fatalf("expected joined oversized-batch traversal-limit, EOF, and read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected oversized batch reader to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionLimitProbeReadError(t *testing.T) {
	readErr := errors.New("probe directory")
	directory := &jvmDetectionTestDirectory{readErr: readErr}
	budget := &jvmDetectionBudget{maxTraversalEntries: 1, maxConfinedCandidates: 1}
	repo := t.TempDir()
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return directory, nil
	}
	if err := walker.walk(); !errors.Is(err, readErr) {
		t.Fatalf("expected limit probe read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected failed limit probe to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionLimitProbeEntryAndReadError(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "entry"), []byte("entry"), 0o600); err != nil {
		t.Fatalf("write probe entry: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read probe entry: %v", err)
	}
	readErr := errors.New("read overflowing detection probe")
	directory := &jvmDetectionTestDirectory{
		fillEntry:          entries[0],
		overflowEntry:      entries[0],
		readErrWithEntries: readErr,
	}
	budget := &jvmDetectionBudget{maxTraversalEntries: 1, maxConfinedCandidates: 1}
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return directory, nil
	}

	err = walker.walk()
	if !errors.Is(err, errJVMDetectionTraversalLimit) || !errors.Is(err, readErr) {
		t.Fatalf("expected joined probe traversal-limit and read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected overflowing probe directory to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionLimitProbeNoProgress(t *testing.T) {
	directory := &jvmDetectionTestDirectory{noProgress: true}
	budget := &jvmDetectionBudget{maxTraversalEntries: 1, maxConfinedCandidates: 1}
	repo := t.TempDir()
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	walker.openRoot = func(string) (jvmDetectionRoot, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return directory, nil
	}
	if err := walker.walk(); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected limit probe no-progress error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected no-progress limit probe to close once, got %d", directory.closeCalls)
	}
}

type jvmDetectionTestDirectory struct {
	fillEntry          fs.DirEntry
	readErr            error
	readErrWithEntries error
	closeErr           error
	noProgress         bool
	extraEntries       int
	repeatEntries      int
	overflowEntry      fs.DirEntry
	overflowReturned   bool
	readSizes          []int
	entriesReturned    int
	closeCalls         int
}

func (d *jvmDetectionTestDirectory) Stat() (fs.FileInfo, error) {
	if d.fillEntry != nil {
		return d.fillEntry.Info()
	}
	if d.overflowEntry != nil {
		return d.overflowEntry.Info()
	}
	return nil, errors.New("unexpected stat")
}

func (d *jvmDetectionTestDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	d.readSizes = append(d.readSizes, count)
	if d.readErr != nil {
		return nil, d.readErr
	}
	if d.noProgress {
		return nil, nil
	}
	if d.fillEntry == nil {
		return nil, io.EOF
	}

	if d.repeatEntries > 0 {
		entryCount := min(count, d.repeatEntries)
		entries := make([]fs.DirEntry, entryCount)
		for index := range entries {
			entries[index] = d.fillEntry
		}
		d.repeatEntries -= entryCount
		d.entriesReturned += len(entries)
		return entries, d.readErrWithEntries
	}
	if d.overflowEntry != nil && !d.overflowReturned {
		d.overflowReturned = true
		d.entriesReturned++
		return []fs.DirEntry{d.overflowEntry}, d.readErrWithEntries
	}

	entries := make([]fs.DirEntry, count+d.extraEntries)
	for index := range entries {
		entries[index] = d.fillEntry
	}
	d.entriesReturned += len(entries)
	return entries, d.readErrWithEntries
}

func (d *jvmDetectionTestDirectory) Close() error {
	d.closeCalls++
	return d.closeErr
}

type jvmDetectionTestRoot struct {
	info fs.FileInfo
}

func (*jvmDetectionTestRoot) Open(string) (jvmDetectionDirectory, error) {
	return nil, errors.New("unexpected root open")
}

func (*jvmDetectionTestRoot) OpenRoot(string) (jvmDetectionRoot, error) {
	return nil, errors.New("unexpected child root open")
}

func (r *jvmDetectionTestRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == "." && r.info != nil {
		return r.info, nil
	}
	return nil, errors.New("unexpected root lstat")
}

func (*jvmDetectionTestRoot) Close() error {
	return nil
}

func newJVMDetectionTestRoot(t testing.TB, path string) *jvmDetectionTestRoot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat detection test root %s: %v", path, err)
	}
	return &jvmDetectionTestRoot{info: info}
}

func testJVMDetectionBudgetGuards(t *testing.T) {
	unlimited := &jvmDetectionBudget{}
	if got := unlimited.traversalReadSize(); got != jvmDetectionReadBatchSize {
		t.Fatalf("expected unlimited budget batch size %d, got %d", jvmDetectionReadBatchSize, got)
	}
	if !unlimited.queueTraversalEntries(1) || !unlimited.dequeueTraversalEntry() {
		t.Fatal("expected unlimited budget to queue and dequeue an entry")
	}
	if unlimited.dequeueTraversalEntry() {
		t.Fatal("expected empty traversal queue dequeue to fail")
	}

	bounded := &jvmDetectionBudget{maxTraversalEntries: 1}
	if !bounded.queueTraversalEntries(1) {
		t.Fatal("expected bounded budget to queue its final entry")
	}
	if bounded.queueTraversalEntries(1) || bounded.queueTraversalEntries(-1) {
		t.Fatal("expected bounded budget to reject over-limit and negative entries")
	}
}

func testJVMEscapingSymlinkFloodDoesNotConsumeCandidateBudget(t *testing.T, adapter *Adapter) {
	repo := t.TempDir()
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")
	for index := 0; index < 1024; index++ {
		linkPath := filepath.Join(repo, fmt.Sprintf("a-%04d.java", index))
		if err := os.Symlink(outsideSource, linkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-module", "src", "main", "java", "Main.java"), "class Main {}\n")

	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence after escaping symlink flood: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected legitimate later JVM file to remain detectable, got %#v", detection)
	}
}

func testJVMTraversalBudgetStopsRejectedEntryFlood(t *testing.T) {
	repo := t.TempDir()
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")
	for index := 0; index < 4; index++ {
		linkPath := filepath.Join(repo, fmt.Sprintf("escape-%04d.java", index))
		if err := os.Symlink(outsideSource, linkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-module", "src", "main", "java", "Main.java"), "class Main {}\n")

	entries, err := sortedJVMRepoEntries(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}

	budget := &jvmDetectionBudget{maxTraversalEntries: 2, maxConfinedCandidates: 1024}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	stopPath := ""
	for _, entry := range entries {
		path := filepath.Join(repo, entry.Name())
		err := walkJVMDetectionEntry(repo, path, entry, roots, detect, budget)
		if errors.Is(err, errJVMDetectionTraversalLimit) {
			stopPath = path
			break
		}
		if err != nil {
			t.Fatalf("walk detection entry %s: %v", entry.Name(), err)
		}
	}

	if stopPath == "" {
		t.Fatal("expected traversal budget flood to stop the walker")
	}
	if budget.traversalEntriesSeen != 2 {
		t.Fatalf("expected traversal budget to stop after bounded work, got %d", budget.traversalEntriesSeen)
	}
	if budget.confinedCandidatesSeen != 0 {
		t.Fatalf("expected rejected entries to avoid confined candidate budget, got %d", budget.confinedCandidatesSeen)
	}
	if detect.Matched {
		t.Fatalf("expected traversal stop before legitimate file update, got %#v", detect)
	}
}

func testJVMConfinedCandidateBudgetStopsOrdinaryFileFlood(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 4; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("Main%04d.java", index)), "class Main {}\n")
	}

	budget := &jvmDetectionBudget{maxTraversalEntries: 8, maxConfinedCandidates: 2}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	walker := newJVMDetectionWalker(repo, roots, detect, budget)
	if err := walker.walk(); !errors.Is(err, fs.SkipAll) {
		t.Fatalf("expected confined candidate budget flood to stop the walker, got %v", err)
	}
	if budget.traversalEntriesSeen != 4 || budget.traversalEntriesQueued != 1 {
		t.Fatalf("expected root and three visited files with one bounded queued file, got seen=%d queued=%d", budget.traversalEntriesSeen, budget.traversalEntriesQueued)
	}
	if budget.confinedCandidatesSeen != 3 {
		t.Fatalf("expected confined candidate budget to stop after bounded work, got %d", budget.confinedCandidatesSeen)
	}
	if !detect.Matched {
		t.Fatalf("expected ordinary confined files to update detection before the stop, got %#v", detect)
	}
}

func testJVMBrokenAndEscapingEntriesCountTowardTraversalOnly(t *testing.T) {
	repo := t.TempDir()
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")

	escapingLink := filepath.Join(repo, "Escaping.java")
	if err := os.Symlink(outsideSource, escapingLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	brokenLink := filepath.Join(repo, "Broken.java")
	if err := os.Symlink(filepath.Join(repo, "missing", "Broken.java"), brokenLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	entries, err := sortedJVMRepoEntries(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	budget := &jvmDetectionBudget{maxTraversalEntries: 8, maxConfinedCandidates: 8}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	for _, entry := range entries {
		if err := walkJVMDetectionEntry(repo, filepath.Join(repo, entry.Name()), entry, roots, detect, budget); err != nil {
			t.Fatalf("walk detection entry %s: %v", entry.Name(), err)
		}
	}

	if budget.traversalEntriesSeen != 2 {
		t.Fatalf("expected broken and escaping links to count toward traversal budget, got %d", budget.traversalEntriesSeen)
	}
	if budget.confinedCandidatesSeen != 0 {
		t.Fatalf("expected broken and escaping links to avoid confined candidate budget, got %d", budget.confinedCandidatesSeen)
	}
	if detect.Matched {
		t.Fatalf("expected rejected links to keep detection unmatched, got %#v", detect)
	}
}

func sortedJVMRepoEntries(repo string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(repo)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(left, right os.DirEntry) int {
		return strings.Compare(left.Name(), right.Name())
	})
	return entries, nil
}

func TestJVMParseHelpersEdgeBranches(t *testing.T) {
	matches := [][]string{
		{"only-one"},
		{"", "", ""},
		{"full", "org.example", "lib"},
	}
	descriptors := parseDependencyDescriptorsFromMatches(matches)
	if len(descriptors) != 1 || descriptors[0].Name != "lib" {
		t.Fatalf("unexpected descriptor parse result: %#v", descriptors)
	}

	if got := fallbackDependency(""); got != "" {
		t.Fatalf("expected empty fallback dependency for empty module, got %q", got)
	}
	if got := lastModuleSegment(""); got != "" {
		t.Fatalf("expected empty last module segment for empty module, got %q", got)
	}
	if got := fallbackDependency("a.b"); got != "a.b" {
		t.Fatalf("expected two-segment fallback dependency to keep both segments, got %q", got)
	}
	if got := relativeSourceScanPath("", "Main.java"); got != "Main.java" {
		t.Fatalf("expected empty rooted source path to preserve original path, got %q", got)
	}

	token, replacement, ok := pomPropertyReplacement([]string{"${missing}"}, map[string]string{"missing": "ignored"})
	if ok || token != "" || replacement != "" {
		t.Fatalf("expected malformed pom property match to be rejected, got token=%q replacement=%q ok=%v", token, replacement, ok)
	}
	token, replacement, ok = pomPropertyReplacement([]string{"${empty}", "empty"}, map[string]string{"empty": "   "})
	if ok || token != "${empty}" || replacement != "" {
		t.Fatalf("expected empty pom property replacement to be rejected, got token=%q replacement=%q ok=%v", token, replacement, ok)
	}
	descriptors, warnings := parseBuildFilesWithWarnings(filepath.Join(t.TempDir(), "missing"), func(string, string) ([]dependencyDescriptor, []string) { return nil, nil }, buildGradleName)
	if len(descriptors) != 0 || len(warnings) != 1 {
		t.Fatalf("expected missing rooted build walk to return one warning, got descriptors=%#v warnings=%#v", descriptors, warnings)
	}
}
