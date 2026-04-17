package jvm

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

func managedDependencyManagementPOM(properties, junitVersion, springVersion string) string {
	propertiesBlock := ""
	if strings.TrimSpace(properties) != "" {
		propertiesBlock = fmt.Sprintf("\n  <properties>\n%s\n  </properties>", properties)
	}

	springVersionBlock := ""
	if strings.TrimSpace(springVersion) != "" {
		springVersionBlock = fmt.Sprintf("\n        <version>%s</version>", springVersion)
	}

	return fmt.Sprintf(`
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
`, propertiesBlock, junitVersion, springVersionBlock)
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

func TestJVMParsePomDependenciesIncludesManagedAndBOMEntries(t *testing.T) {
	repo := t.TempDir()
	writeJVMPomFile(t, repo, managedDependencyManagementPOM(`
    <junit.version>5.10.2</junit.version>
    <spring.boot.version>3.4.5</spring.boot.version>
`, "${junit.version}", "${spring.boot.version}"))
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

func TestJVMPomAndBuildHelperGuardBranches(t *testing.T) {
	descriptors, warnings := parsePomDependencyContent("pom.xml", "<project>")
	if len(descriptors) != 0 {
		t.Fatalf("expected invalid pom content to produce no descriptors, got %#v", descriptors)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to parse Maven pom.xml pom.xml") {
		t.Fatalf("expected invalid pom warning, got %#v", warnings)
	}

	descriptor, warning := parsePomDependency(pomDependencyModel{
		GroupID:    "${missing.group}",
		ArtifactID: "demo-artifact",
		Version:    "1.0.0",
	}, map[string]string{}, pomDependencyManaged, "pom.xml")
	if descriptor != (dependencyDescriptor{}) || warning != "" {
		t.Fatalf("expected unresolved managed coordinates to be dropped without warnings, got descriptor=%#v warning=%q", descriptor, warning)
	}

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
	setPomPropertyValue(propertyMap, "", "ignored")
	setPomPropertyValue(propertyMap, "ignored", "")
	if _, ok := propertyMap["ignored"]; ok {
		t.Fatalf("expected blank setter inputs to be ignored, got %#v", propertyMap)
	}

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

	t.Run("max file walk budget", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "class Main {}")
		entry := testutil.MustFirstFileEntry(t, repo)
		visited := 1
		roots := map[string]struct{}{}
		detect := &language.Detection{}
		err := walkJVMDetectionEntry(filepath.Join(repo, entry.Name()), entry, roots, detect, &visited, 1)
		if !errors.Is(err, fs.SkipAll) {
			t.Fatalf("expected fs.SkipAll when maxFiles exceeded, got %v", err)
		}
	})
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

	pattern := regexp.MustCompile(`does-not-match`)
	if got := parseGradleMatches("", pattern); len(got) != 0 {
		t.Fatalf("expected no gradle matches for nil pattern, got %#v", got)
	}

	if got := fallbackDependency(""); got != "" {
		t.Fatalf("expected empty fallback dependency for empty module, got %q", got)
	}
	if got := lastModuleSegment(""); got != "" {
		t.Fatalf("expected empty last module segment for empty module, got %q", got)
	}
}
