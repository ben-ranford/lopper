package swift

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const missingSwiftCatalogSuffix = " not found"

func TestSwiftAdapterDetectWithNestedPackageRoots(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "// root package\n")
	writeSwiftAppSourceFile(t, repo, swiftImportFoundationSource)
	testutil.MustWriteFile(t, filepath.Join(repo, "Packages", "Feature", packageManifestName), "// nested package\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected swift detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %d", detection.Confidence)
	}
	if !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected repo root in detection roots, got %#v", detection.Roots)
	}
	nested := filepath.Join(repo, "Packages", "Feature")
	if !slices.Contains(detection.Roots, nested) {
		t.Fatalf("expected nested package root in detection roots, got %#v", detection.Roots)
	}
}

func TestSwiftAdapterAnalyseDependencyAndTopN(t *testing.T) {
	repo := t.TempDir()
	dependencies := []swiftFixtureDependency{
		alamofireFixtureDependency(),
		swiftNIOFixtureDependency(),
	}
	mainContent := `import Alamofire
import struct NIO.ByteBuffer
func run() {
  _ = Session.default
  _ = ByteBufferAllocator().buffer(capacity: 8)
}`
	writeSwiftDemoPackage(t, repo, dependencies, mainContent)

	adapter := NewAdapter()
	depReport := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
	if depReport.Language != swiftAdapterID {
		t.Fatalf("expected swift language, got %q", depReport.Language)
	}
	if depReport.TotalExportsCount == 0 {
		t.Fatalf("expected mapped import signals for alamofire, got %#v", depReport)
	}

	topReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	names := make([]string, 0, len(topReport.Dependencies))
	for _, dep := range topReport.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "alamofire") {
		t.Fatalf("expected alamofire in topN output, got %#v", names)
	}
	if !slices.Contains(names, swiftNIOID) {
		t.Fatalf("expected %s in topN output, got %#v", swiftNIOID, names)
	}
}

func TestSwiftAdapterCountsUnqualifiedSingleDependencyUsage(t *testing.T) {
	dependencies := []swiftFixtureDependency{alamofireFixtureDependency()}
	reportData := analyseSwiftDependencyUsage(t, dependencies, swiftAlamofireSessionUsageSource)
	if reportData.UsedExportsCount < 1 {
		t.Fatalf("expected unqualified usage to count as used, got %#v", reportData)
	}
}

func TestSwiftAdapterDoesNotAttributeAmbiguousUnqualifiedUsage(t *testing.T) {
	dependencies := []swiftFixtureDependency{
		alamofireFixtureDependency(),
		kingfisherFixtureDependency(),
	}
	mainContent := `import Alamofire
import Kingfisher
func run() {
  _ = Session.default
}`
	reportData := analyseSwiftDependencyUsage(t, dependencies, mainContent)
	if reportData.UsedExportsCount != 0 {
		t.Fatalf("expected ambiguous unqualified usage to remain unattributed, got %#v", reportData)
	}
}

func TestSwiftAdapterDoesNotCountLocalTypeUsageAsDependencyUsage(t *testing.T) {
	dependencies := []swiftFixtureDependency{alamofireFixtureDependency()}
	reportData := analyseSwiftDependencyUsage(t, dependencies, swiftAlamofireLocalThingUsageSource)
	if reportData.UsedExportsCount != 0 {
		t.Fatalf("expected local-only symbols to not count as dependency usage, got %#v", reportData)
	}
}

func TestSwiftAdapterParsesResolvedVariants(t *testing.T) {
	t.Run("v1_object_pins", func(t *testing.T) {
		repo := t.TempDir()
		resolvedContent := `{
  "object": {
    "pins": [
      {
        "package": "Kingfisher",
        "repositoryURL": "` + kingfisherRepositoryURL + `",
        "state": {"revision": "` + swiftResolvedRevision + `", "version": "` + kingfisherVersion + `"}
      }
    ]
  },
  "version": 1
}`
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), resolvedContent)
		writeSwiftAppSourceFile(t, repo, swiftKingfisherSharedUsageSource)

		reportData := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: kingfisherFixtureName})
		if reportData.TotalExportsCount == 0 {
			t.Fatalf("expected import mapped from v1 pins, got %#v", reportData)
		}
	})

	t.Run("v2_top_level_pins", func(t *testing.T) {
		repo := t.TempDir()
		resolvedContent := `{
  "pins": [
    {
      "identity": "swift-collections",
      "location": "` + swiftCollectionsRepositoryURL + `",
      "state": {"revision": "` + swiftResolvedRevision + `", "version": "` + swiftCollectionsVersion + `"}
    }
  ],
  "version": 2
}`
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), resolvedContent)
		writeSwiftAppSourceFile(t, repo, swiftImportSwiftCollectionsSource)

		reportData := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "swift-collections"})
		if len(reportData.UnusedImports) == 0 && len(reportData.UsedImports) == 0 {
			t.Fatalf("expected import mapped from v2 pins, got %#v", reportData)
		}
	})
}

func TestSwiftAdapterWarningsForMissingManifestAndResolved(t *testing.T) {
	repo := t.TempDir()
	writeSwiftAppSourceFile(t, repo, swiftImportFoundationSource)

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, TopN: 5})
	assertWarningContains(t, reportData.Warnings, packageManifestName+missingSwiftCatalogSuffix)
	assertWarningContains(t, reportData.Warnings, packageResolvedName+missingSwiftCatalogSuffix)
	assertWarningContains(t, reportData.Warnings, "no Swift dependencies were discovered")
}

func TestSwiftAdapterSwiftPMMissingResolvedRiskCue(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), buildSwiftManifestContent([]swiftFixtureDependency{alamofireFixtureDependency()}))
	writeSwiftDemoSourceFile(t, repo, swiftAlamofireSessionValueSource)

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if !hasRiskCueCode(dep, "missing-lock-resolution") {
		t.Fatalf("expected Package.resolved risk cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendationCode(dep, "refresh-package-resolved") {
		t.Fatalf("expected Package.resolved recommendation, got %#v", dep.Recommendations)
	}
	assertWarningContains(t, reportData.Warnings, packageResolvedName+missingSwiftCatalogSuffix)
}

func TestParseSwiftImportsPatterns(t *testing.T) {
	importsContent := []byte(`import Alamofire
@testable import NIO
import struct Foundation.Date
import class MyLib.Client`)
	imports := parseSwiftImports(importsContent, swiftMainFileName)
	modules := make([]string, 0, len(imports))
	for _, imported := range imports {
		modules = append(modules, imported.Module)
	}
	if !slices.Equal(modules, []string{"Alamofire", "NIO", "Foundation", "MyLib"}) {
		t.Fatalf("unexpected parsed modules: %#v", modules)
	}
}

type swiftFixtureDependency struct {
	identity     string
	manifestName string
	url          string
	version      string
	productName  string
}

func analyseSwiftDependencyUsage(t *testing.T, dependencies []swiftFixtureDependency, mainContent string) report.DependencyReport {
	t.Helper()

	repo := t.TempDir()
	writeSwiftDemoPackage(t, repo, dependencies, mainContent)
	return mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
}

func buildSwiftManifestContent(dependencies []swiftFixtureDependency) string {
	lines := []string{
		"import PackageDescription",
		"let package = Package(",
		`  name: "` + swiftDemoPackageName + `",`,
		"  dependencies: [",
	}
	for index, dependency := range dependencies {
		suffix := ","
		if index == len(dependencies)-1 {
			suffix = ""
		}
		if dependency.manifestName != "" {
			lines = append(lines, fmt.Sprintf("    .package(name: %q, url: %q, from: %q)%s", dependency.manifestName, dependency.url, dependency.version, suffix))
			continue
		}
		lines = append(lines, fmt.Sprintf("    .package(url: %q, from: %q)%s", dependency.url, dependency.version, suffix))
	}
	lines = append(lines, "  ],", "  targets: [")
	if len(dependencies) == 1 {
		lines = append(lines, fmt.Sprintf("    .target(name: %q, dependencies: [.product(name: %q, package: %q)])", "Demo", dependencies[0].productName, dependencies[0].identity))
	} else {
		lines = append(lines, `    .target(`, `      name: "Demo",`, "      dependencies: [")
		for index, dependency := range dependencies {
			suffix := ","
			if index == len(dependencies)-1 {
				suffix = ""
			}
			lines = append(lines, fmt.Sprintf("        .product(name: %q, package: %q)%s", dependency.productName, dependency.identity, suffix))
		}
		lines = append(lines, "      ]", "    )")
	}
	lines = append(lines, "  ]", ")")
	return strings.Join(lines, "\n")
}

func buildSwiftResolvedContent(dependencies []swiftFixtureDependency) string {
	lines := []string{"{", `  "pins": [`}
	for index, dependency := range dependencies {
		suffix := ","
		if index == len(dependencies)-1 {
			suffix = ""
		}
		entryLines := []string{
			"    {",
			fmt.Sprintf(`      "identity": %q,`, dependency.identity),
			fmt.Sprintf(`      "location": %q,`, dependency.url),
			fmt.Sprintf(`      "state": {"revision": %q, "version": %q}`, swiftResolvedRevision, dependency.version),
			"    }" + suffix,
		}
		lines = append(lines, entryLines...)
	}
	lines = append(lines, `  ],`, `  "version": 2`, "}")
	return strings.Join(lines, "\n")
}

func assertWarningContains(t *testing.T, warnings []string, substring string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return
		}
	}
	t.Fatalf("expected warning containing %q, got %#v", substring, warnings)
}
