package jvm

import (
	"context"
	"errors"
	"fmt"
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
	content := []byte("import com.acme.`when`.Widget as `when`\nfun call(value: Boolean) { if (value) when { else -> `when`() } }\n")
	imports := parseImports(content, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if len(imports) != 1 || imports[0].Module != "com.acme.`when`.Widget" || imports[0].Name != "Widget" || imports[0].Local != "when" || imports[0].Dependency != acmeLibName {
		t.Fatalf("expected Kotlin backtick alias import, got %#v", imports)
	}
	if usage := countUsage(content, imports); usage["when"] != 1 {
		t.Fatalf("expected Kotlin backtick alias usage 1 without bare keyword hits, got %d", usage["when"])
	}
	if usage := countUsage([]byte("import com.acme.`when`.Widget as `when`\nfun call(value: Boolean) { if (value) when { else -> Unit } }\n"), imports); usage["when"] != 0 {
		t.Fatalf("expected bare Kotlin keyword to leave escaped alias unused, got %d", usage["when"])
	}
	directContent := []byte("import com.acme.`when`\nfun call(value: Boolean) { if (value) when { else -> Unit } }\n")
	directImports := parseImports(directContent, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if len(directImports) != 1 || directImports[0].Local != "when" {
		t.Fatalf("expected escaped terminal import, got %#v", directImports)
	}
	if usage := countUsage(directContent, directImports); usage["when"] != 0 {
		t.Fatalf("expected bare Kotlin keyword to leave escaped terminal import unused, got %d", usage["when"])
	}
	if usage := countUsage([]byte("import com.acme.`when`\n`when`()\n"), directImports); usage["when"] != 1 {
		t.Fatalf("expected escaped terminal import usage 1, got %d", usage["when"])
	}
	packageContent := []byte("package com.example.`when`\nimport com.acme.Widget as `when`\nfun call(value: Boolean) { if (value) when { else -> Unit } }\n")
	packageImports := parseImports(packageContent, "App.kt", "com.example.`when`", nil, map[string]string{"com.acme": acmeLibName})
	if usage := countUsage(packageContent, packageImports); usage["when"] != 0 {
		t.Fatalf("expected escaped package declaration to leave alias unused, got %d", usage["when"])
	}
	legalAliasContent := []byte("import com.Alias.Widget as `Alias`\nAlias()\n")
	legalAliasImports := parseImports(legalAliasContent, "App.kt", "com.example.app", nil, map[string]string{"com.Alias": acmeLibName})
	if len(legalAliasImports) != 1 {
		t.Fatalf("expected legal bare alias import, got %#v", legalAliasImports)
	}
	if usage := countUsage(legalAliasContent, legalAliasImports); usage["Alias"] != 1 {
		t.Fatalf("expected legal bare alias usage 1, got %d", usage["Alias"])
	}
	if usage := countUsage([]byte("import com.Alias.Widget as `Alias`\n"), legalAliasImports); usage["Alias"] != 0 {
		t.Fatalf("expected declaration-only legal alias to remain unused, got %d", usage["Alias"])
	}
	tabDirective := []byte("import\tcom.acme.Widget as `when`\nfun call(value: Boolean) { if (value) when { else -> Unit } }\n")
	tabImports := parseImports(tabDirective, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName})
	if usage := countUsage(tabDirective, tabImports); usage["when"] != 0 {
		t.Fatalf("expected tab-separated import directive to leave escaped alias unused, got %d", usage["when"])
	}

	unsupportedAlias := []byte("import com.acme.Widget as `my type`\n`my type`()\n")
	if imports := parseImports(unsupportedAlias, "App.kt", "com.example.app", nil, map[string]string{"com.acme": acmeLibName}); len(imports) != 0 {
		t.Fatalf("expected unsupported escaped alias to be ignored, got %#v", imports)
	}
}

func TestJVMParsePackageSupportsKotlinEscapedSegment(t *testing.T) {
	pkg := parsePackage([]byte("package com.example.`when`\n"))
	if pkg != "com.example.`when`" {
		t.Fatalf("expected Kotlin escaped package, got %q", pkg)
	}
	imports := parseImports([]byte("import com.example.`when`.Widget\n"), "App.kt", pkg, nil, nil)
	if len(imports) != 0 {
		t.Fatalf("expected escaped same-package import to be ignored, got %#v", imports)
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
