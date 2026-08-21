package jvm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	testFileAppJava        = "App.java"
	testFilePomXML         = "pom.xml"
	testFileBuildGradleKTS = "build.gradle.kts"
	testFileMainKT         = "Main.kt"
	testKotlinEscapedWhen  = "`when`"
	errDetectFmt           = "detect: %v"
	errAnalyseFmt          = "analyse: %v"
	errSymlinkFmt          = "symlink not supported: %v"
)

func TestAdapterDetectWithGradleAndJava(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", testFileAppJava), "import org.junit.jupiter.api.Test;\nclass App {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if !detection.Matched {
		t.Fatalf("expected jvm detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %d", detection.Confidence)
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testFilePomXML), `
<project>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter-api</artifactId>
      <version>5.10.0</version>
    </dependency>
  </dependencies>
</project>
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "test", "java", "ExampleTest.java"), `
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.Assertions;

class ExampleTest {
  @Test
  void runs() {
    Assertions.assertTrue(true);
  }
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "junit-jupiter-api",
	})
	if err != nil {
		t.Fatalf(errAnalyseFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "jvm" {
		t.Fatalf("expected language jvm, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
}

func TestAdapterAnalyseDependencyFromMavenDependencyManagement(t *testing.T) {
	repo := t.TempDir()
	properties := `
    <junit.version>5.10.2</junit.version>
`
	pomContent := managedDependencyManagementPOM(properties, "${junit.version}", "3.4.5")
	writeJVMPomFile(t, repo, pomContent)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "test", "java", "ManagedExampleTest.java"), `
import org.junit.jupiter.api.Test;

	class ManagedExampleTest {
	  @Test
	  void runs() {}
	}
	`)

	req := language.Request{RepoPath: repo, Dependency: "junit-jupiter-api"}
	unexpectedWarning := "unable to resolve managed Maven version"
	usageFailure := "expected managed Maven dependency usage to be recorded"
	assertSingleUsedDependencyReportWithoutWarning(t, req, unexpectedWarning, usageFailure)
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testFileBuildGradleKTS), `
dependencies {
  implementation("com.squareup.okhttp3:okhttp:4.12.0")
  implementation("org.junit.jupiter:junit-jupiter-api:5.10.0")
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", testFileMainKT), `
import okhttp3.OkHttpClient
import org.junit.jupiter.api.Assertions

fun run() {
  OkHttpClient()
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf(errAnalyseFmt, err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in top-N report")
	}
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "okhttp") {
		t.Fatalf("expected okhttp dependency in %#v", names)
	}
}

func TestAdapterAnalyseDependencyWithGradleVersionCatalogAlias(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "gradle", "libs.versions.toml"), `
[libraries]
okhttp = { module = "com.squareup.okhttp3:okhttp", version = "4.12.0" }
`)
	testutil.MustWriteFile(t, filepath.Join(repo, testFileBuildGradleKTS), `
dependencies {
  implementation(libs.okhttp)
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", testFileMainKT), `
import okhttp3.OkHttpClient

	fun run() {
	  OkHttpClient()
	}
	`)

	req := language.Request{RepoPath: repo, Dependency: "okhttp"}
	unexpectedWarning := "unable to resolve Gradle version catalog"
	usageFailure := "expected catalog-backed dependency usage to be recorded"
	assertSingleUsedDependencyReportWithoutWarning(t, req, unexpectedWarning, usageFailure)
}

func TestAdapterAnalyseDependencyNormalizesEscapedKotlinPackageLookup(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testFileBuildGradleKTS), `
dependencies {
  implementation("com.example.when:widgets:1.0.0")
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", testFileMainKT), `
package app.consumer

import com.example.`+testKotlinEscapedWhen+`.Widget

fun run() {
  Widget()
}
`)

	reportData := analyseJVMReport(t, language.Request{RepoPath: repo, Dependency: "widgets"})
	assertSingleDependencyUsage(t, reportData.Dependencies, "widgets", "expected escaped Kotlin package segment to resolve to declared dependency")
	dependency := reportData.Dependencies[0]
	if len(dependency.UsedImports) != 1 {
		t.Fatalf("expected one used import, got %#v", dependency.UsedImports)
	}
	if dependency.UsedImports[0].Module != "com.example.`when`.Widget" {
		t.Fatalf("expected report module to preserve source spelling, got %#v", dependency.UsedImports[0])
	}
}

func TestAdapterTopNNormalizesEscapedKotlinFallbackDependency(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", testFileMainKT), `
package app.consumer

import custom.`+testKotlinEscapedWhen+`.Widget

fun run() {
  Widget()
}
`)

	reportData := analyseJVMReport(t, language.Request{RepoPath: repo, TopN: 5})
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one fallback dependency report, got %#v", reportData.Dependencies)
	}
	dependency := reportData.Dependencies[0]
	if dependency.Name != "custom.when" || dependency.UsedExportsCount == 0 {
		t.Fatalf("expected normalized fallback dependency usage, got %#v", dependency)
	}
	if len(dependency.UsedImports) != 1 || dependency.UsedImports[0].Module != "custom.`when`.Widget" {
		t.Fatalf("expected fallback import report to preserve source spelling, got %#v", dependency.UsedImports)
	}
}

func TestAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "jvm" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	if !slices.Contains(aliases, "java") || !slices.Contains(aliases, "kotlin") {
		t.Fatalf("unexpected adapter aliases: %#v", aliases)
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testFilePomXML), "<project/>")
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if !ok {
		t.Fatalf("expected detect=true when pom.xml exists")
	}
}

func TestAdapterDetectWithMixedGradleMavenKotlinModules(t *testing.T) {
	repo := t.TempDir()
	gradleModule := filepath.Join(repo, "modules", "gradle-app")
	mavenModule := filepath.Join(repo, "modules", "maven-app")
	testutil.MustWriteFile(t, filepath.Join(gradleModule, testFileBuildGradleKTS), "plugins { kotlin(\"jvm\") }\n")
	testutil.MustWriteFile(t, filepath.Join(gradleModule, "src", "main", "kotlin", testFileMainKT), "class Main\n")
	testutil.MustWriteFile(t, filepath.Join(mavenModule, testFilePomXML), "<project/>")
	testutil.MustWriteFile(t, filepath.Join(mavenModule, "src", "main", "java", testFileAppJava), "class App {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if !detection.Matched {
		t.Fatalf("expected jvm detection to match")
	}
	if !slices.Contains(detection.Roots, gradleModule) || !slices.Contains(detection.Roots, mavenModule) {
		t.Fatalf("expected module roots in detection output, got %#v", detection.Roots)
	}
}

func TestAdapterDetectRejectsEscapingSymlinkSignals(t *testing.T) {
	t.Run("root gradle symlink", func(t *testing.T) {
		repo := t.TempDir()
		outside := filepath.Join(t.TempDir(), buildGradleName)
		testutil.MustWriteFile(t, outside, "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
		if err := os.Symlink(outside, filepath.Join(repo, buildGradleName)); err != nil {
			t.Skipf(errSymlinkFmt, err)
		}

		detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
		if err != nil {
			t.Fatalf(errDetectFmt, err)
		}
		if detection.Matched {
			t.Fatalf("expected escaping build.gradle symlink to be ignored, got %#v", detection)
		}
	})

	t.Run("source symlink", func(t *testing.T) {
		repo := t.TempDir()
		outside := filepath.Join(t.TempDir(), testFileAppJava)
		testutil.MustWriteFile(t, outside, "class App {}\n")
		sourceLink := filepath.Join(repo, "src", "main", "java", testFileAppJava)
		if err := os.MkdirAll(filepath.Dir(sourceLink), 0o755); err != nil {
			t.Fatalf("mkdir source dir: %v", err)
		}
		if err := os.Symlink(outside, sourceLink); err != nil {
			t.Skipf(errSymlinkFmt, err)
		}

		detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
		if err != nil {
			t.Fatalf(errDetectFmt, err)
		}
		if detection.Matched {
			t.Fatalf("expected escaping source symlink to be ignored, got %#v", detection)
		}
	})
}

func TestAdapterAnalyseSkipsOversizedGradleManifest(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), strings.Repeat("a", maxScannableJVMBuildFile+1))
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", testFileAppJava), "class App {}\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     1,
	})
	if err != nil {
		t.Fatalf(errAnalyseFmt, err)
	}
	warnings := strings.Join(reportData.Warnings, "\n")
	if !strings.Contains(warnings, "unable to read build.gradle: "+safeReadTooLargeMessage()) {
		t.Fatalf("expected oversized build warning, got %#v", reportData.Warnings)
	}
	if !strings.Contains(warnings, "no JVM dependencies discovered") {
		t.Fatalf("expected dependency-discovery warning after oversized build skip, got %#v", reportData.Warnings)
	}
}

func TestAdapterAnalyseSkipsOversizedGradleCatalogInputs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		oversized   string
		warningText string
	}{
		{
			name:        "oversized settings.gradle.kts",
			oversized:   "settings.gradle.kts",
			warningText: "unable to read settings.gradle.kts: file exceeds size limit",
		},
		{
			name:        "oversized gradle/libs.versions.toml",
			oversized:   filepath.Join("gradle", "libs.versions.toml"),
			warningText: "unable to read gradle/libs.versions.toml: file exceeds size limit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repo, buildGradleKTSName), `
dependencies {
  implementation("com.squareup.okhttp3:okhttp:4.12.0")
}
`)
			testutil.MustWriteFile(t, filepath.Join(repo, tc.oversized), strings.Repeat("a", shared.GradleManifestByteLimit+1))
			testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", testFileMainKT), `
import okhttp3.OkHttpClient
fun runClient() { OkHttpClient() }
`)

			reportData := analyseJVMReport(t, language.Request{RepoPath: repo, TopN: 5})
			if !strings.Contains(strings.Join(reportData.Warnings, "\n"), tc.warningText) {
				t.Fatalf("expected oversized manifest warning, got %#v", reportData.Warnings)
			}
			assertSingleDependencyUsage(t, reportData.Dependencies, "okhttp", "expected direct Gradle dependency analysis to continue")
		})
	}
}

func safeReadTooLargeMessage() string {
	return "file exceeds size limit"
}

func TestJVMSourceScanSkipsOversizedFiles(t *testing.T) {
	repo := t.TempDir()
	writeJVMPomFile(t, repo, `
<project>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter-api</artifactId>
      <version>5.10.0</version>
    </dependency>
  </dependencies>
</project>
`)
	largeSource := filepath.Join(repo, "src", "main", "java", testFileAppJava)
	testutil.MustWriteFile(t, largeSource, strings.Repeat("a", maxScannableJVMSourceFile+1))

	result, err := scanRepo(context.Background(), repo, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected oversized source to be skipped, got %#v", result.Files)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "skipped 1 large JVM file(s) above") {
		t.Fatalf("expected oversized-source warning, got %#v", result.Warnings)
	}
}

func TestJVMGuardedReadsRejectEscapingSymlinks(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()

	outsideGradle := filepath.Join(outsideDir, buildGradleName)
	testutil.MustWriteFile(t, outsideGradle, "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
	if err := os.Symlink(outsideGradle, filepath.Join(repo, buildGradleName)); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}
	_, warnings := parseGradleDependenciesWithWarnings(repo)
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, "\n"), buildGradleName) {
		t.Fatalf("expected escaping build.gradle symlink warning, got %#v", warnings)
	}
}

func TestAdapterAnalyseSkipsEscapingSourceSymlinkWithoutConsumingExternalContent(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleKTSName), `
dependencies {
  implementation("org.junit.jupiter:junit-jupiter-api:5.10.0")
  implementation("com.outside:secret:1.0.0")
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", testFileAppJava), `
import org.junit.jupiter.api.Assertions;
class App { void run() { Assertions.assertTrue(true); } }
`)
	outsideSource := filepath.Join(outsideDir, "Outside.java")
	testutil.MustWriteFile(t, outsideSource, `
import com.outside.SecretApi;
class Outside { void use() { SecretApi.call(); } }
`)
	sourceLink := filepath.Join(repo, "src", "main", "java", "Outside.java")
	if err := os.Symlink(outsideSource, sourceLink); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}

	adapter := NewAdapter()
	first := analyseJVMReportTwiceAndRequireStableDependencies(t, adapter, language.Request{RepoPath: repo, TopN: 10})

	warnings := strings.Join(first.Warnings, "\n")
	if !strings.Contains(warnings, "skipped JVM source symlink src/main/java/Outside.java") {
		t.Fatalf("expected source symlink skip warning, got %#v", first.Warnings)
	}
	if !strings.Contains(warnings, "skipped 1 unreadable or untrusted JVM source symlink(s)") {
		t.Fatalf("expected source symlink skip counter, got %#v", first.Warnings)
	}

	usageByDep := map[string]int{}
	for _, dependency := range first.Dependencies {
		usageByDep[dependency.Name] = dependency.UsedExportsCount
	}
	if usageByDep["junit-jupiter-api"] == 0 {
		t.Fatalf("expected in-repo dependency usage to be preserved, got %#v", first.Dependencies)
	}
	if usageByDep["secret"] != 0 {
		t.Fatalf("expected escaping source symlink content to be ignored, got %#v", first.Dependencies)
	}
}

func TestJVMSourceReadEmptyRepoPathStillBoundsLargeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), testFileAppJava)
	testutil.MustWriteFile(t, path, strings.Repeat("a", maxScannableJVMSourceFile+1))

	result := &scanResult{}
	err := scanJVMSourceFile("", path, nil, nil, result)
	if err != nil {
		t.Fatalf("expected bounded empty-root source scan to skip, got %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected no scanned files from oversized empty-root source")
	}
	if result.SkippedLargeFiles != 1 {
		t.Fatalf("expected one skipped large file, got %#v", result)
	}
}

func TestAdapterAnalyseMixedJavaKotlinStableReporting(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testFileBuildGradleKTS), `
dependencies {
  implementation("com.squareup.okhttp3:okhttp:4.12.0")
  implementation("org.junit.jupiter:junit-jupiter-api:5.10.0")
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", testFileAppJava), `
import org.junit.jupiter.api.Assertions;
class App { void run() { Assertions.assertTrue(true); } }
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", testFileMainKT), `
import okhttp3.OkHttpClient as Client
fun runClient() { Client() }
`)

	adapter := NewAdapter()
	first := analyseJVMReportTwiceAndRequireStableDependencies(t, adapter, language.Request{RepoPath: repo, TopN: 10})
	names := make([]string, 0, len(first.Dependencies))
	okhttpUsed := false
	for _, dependency := range first.Dependencies {
		names = append(names, dependency.Name)
		if dependency.Name == "okhttp" && dependency.UsedExportsCount > 0 {
			okhttpUsed = true
		}
	}
	if !slices.Contains(names, "okhttp") || !slices.Contains(names, "junit-jupiter-api") {
		t.Fatalf("expected mixed java/kotlin dependencies, got %#v", names)
	}
	if !okhttpUsed {
		t.Fatalf("expected aliased kotlin import to be counted as used")
	}
}

func TestNormalizeDependencyID(t *testing.T) {
	if got := normalizeDependencyID(" Org.Example.Lib "); got != "org.example.lib" {
		t.Fatalf("unexpected normalized dependency ID: %q", got)
	}
}

func TestSourceLayoutModuleRootUsesInnermostSourceLayout(t *testing.T) {
	path := filepath.Join(string(filepath.Separator), "tmp", "src", "workspace", "repo", "module", "src", "main", "kotlin", "Main.kt")
	want := filepath.Join(string(filepath.Separator), "tmp", "src", "workspace", "repo", "module")
	if got := sourceLayoutModuleRoot(path); got != want {
		t.Fatalf("unexpected source layout root: got %q want %q", got, want)
	}
}

func TestSourceLayoutModuleRootStaysWithinRepository(t *testing.T) {
	repo := t.TempDir()
	moduleRoot := filepath.Join(repo, "module")
	testutil.MustWriteFile(t, filepath.Join(moduleRoot, "src", "main", "java", testFileAppJava), "class App {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if !detection.Matched || len(detection.Roots) != 1 || detection.Roots[0] != moduleRoot {
		t.Fatalf("expected source-layout module root %q, got %#v", moduleRoot, detection)
	}
}

func TestAdapterDetectConfinesAbsoluteSourceLayoutAncestor(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "src", "main", "java", "repository")
	testutil.MustWriteFile(t, filepath.Join(repo, "module", testFileAppJava), "class App {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if !detection.Matched || len(detection.Roots) != 1 || detection.Roots[0] != repo {
		t.Fatalf("expected fallback root to remain confined to repository, got %#v", detection)
	}
}

func analyseJVMReport(t *testing.T, req language.Request) report.Result {
	t.Helper()

	reportData, err := NewAdapter().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf(errAnalyseFmt, err)
	}
	return reportData
}

func analyseJVMReportTwiceAndRequireStableDependencies(t *testing.T, adapter *Adapter, req language.Request) report.Result {
	t.Helper()

	first, err := adapter.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse first pass: %v", err)
	}
	second, err := adapter.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse second pass: %v", err)
	}
	if !reflect.DeepEqual(first.Dependencies, second.Dependencies) {
		t.Fatalf("expected stable dependency reporting across runs")
	}
	return first
}

func assertSingleUsedDependencyReportWithoutWarning(t *testing.T, req language.Request, unexpectedWarning, usageFailure string) {
	t.Helper()

	reportData := analyseJVMReport(t, req)
	assertSingleDependencyUsage(t, reportData.Dependencies, req.Dependency, usageFailure)
	if strings.Contains(strings.Join(reportData.Warnings, "\n"), unexpectedWarning) {
		t.Fatalf("did not expect %q warning, got %#v", unexpectedWarning, reportData.Warnings)
	}
}

func assertSingleDependencyUsage(t *testing.T, dependencies []report.DependencyReport, wantName, failureMessage string) {
	t.Helper()

	if len(dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(dependencies))
	}
	if dependencies[0].Name != wantName || dependencies[0].UsedExportsCount == 0 {
		t.Fatalf("%s, got %#v", failureMessage, dependencies)
	}
}
