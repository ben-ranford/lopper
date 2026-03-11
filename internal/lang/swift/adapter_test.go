package swift

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftAdapterDetectWithNestedPackageRoots(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "// root package\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import Foundation\n")
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
		{identity: "alamofire", url: "https://github.com/Alamofire/Alamofire.git", version: "5.8.0", productName: "Alamofire"},
		{identity: "swift-nio", manifestName: "swift-nio", url: "https://github.com/apple/swift-nio.git", version: "2.60.0", productName: "NIO"},
	}
	mainContent := `import Alamofire
import struct NIO.ByteBuffer
func run() {
  _ = Session.default
  _ = ByteBufferAllocator().buffer(capacity: 8)
}`
	writeSwiftDemoPackage(t, repo, dependencies, mainContent)

	adapter := NewAdapter()
	depReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "alamofire"})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
	}
	if depReport.Dependencies[0].Language != swiftAdapterID {
		t.Fatalf("expected swift language, got %q", depReport.Dependencies[0].Language)
	}
	if depReport.Dependencies[0].TotalExportsCount == 0 {
		t.Fatalf("expected mapped import signals for alamofire, got %#v", depReport.Dependencies[0])
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
	if !slices.Contains(names, "swift-nio") {
		t.Fatalf("expected swift-nio in topN output, got %#v", names)
	}
}

func TestSwiftAdapterUsageAttribution(t *testing.T) {
	testCases := []struct {
		name               string
		dependencies       []swiftFixtureDependency
		mainContent        string
		wantUsedExports    int
		wantUsedExportsMin bool
		failureDescription string
	}{
		{
			name: "counts unqualified single dependency usage",
			dependencies: []swiftFixtureDependency{
				{identity: "alamofire", url: "https://github.com/Alamofire/Alamofire.git", version: "5.8.0", productName: "Alamofire"},
			},
			mainContent: `import Alamofire
func run() {
  _ = Session.default
}`,
			wantUsedExports:    1,
			wantUsedExportsMin: true,
			failureDescription: "expected unqualified usage to count as used",
		},
		{
			name: "does not attribute ambiguous unqualified usage",
			dependencies: []swiftFixtureDependency{
				{identity: "alamofire", url: "https://github.com/Alamofire/Alamofire.git", version: "5.8.0", productName: "Alamofire"},
				{identity: "kingfisher", url: "https://github.com/onevcat/Kingfisher.git", version: "7.9.0", productName: "Kingfisher"},
			},
			mainContent: `import Alamofire
import Kingfisher
func run() {
  _ = Session.default
}`,
			wantUsedExports:    0,
			failureDescription: "expected ambiguous unqualified usage to remain unattributed",
		},
		{
			name: "does not count local type usage as dependency usage",
			dependencies: []swiftFixtureDependency{
				{identity: "alamofire", url: "https://github.com/Alamofire/Alamofire.git", version: "5.8.0", productName: "Alamofire"},
			},
			mainContent: `import Alamofire
struct LocalThing {
  let id: String
}
func run() {
  let thing = LocalThing(id: "1")
  _ = thing.id
}`,
			wantUsedExports:    0,
			failureDescription: "expected local-only symbols to not count as dependency usage",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := t.TempDir()
			writeSwiftDemoPackage(t, repo, testCase.dependencies, testCase.mainContent)

			depReport, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "alamofire"})
			if err != nil {
				t.Fatalf("analyse dependency: %v", err)
			}
			if len(depReport.Dependencies) != 1 {
				t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
			}

			gotUsedExports := depReport.Dependencies[0].UsedExportsCount
			if testCase.wantUsedExportsMin {
				if gotUsedExports < testCase.wantUsedExports {
					t.Fatalf("%s, got %#v", testCase.failureDescription, depReport.Dependencies[0])
				}
				return
			}
			if gotUsedExports != testCase.wantUsedExports {
				t.Fatalf("%s, got %#v", testCase.failureDescription, depReport.Dependencies[0])
			}
		})
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
        "repositoryURL": "https://github.com/onevcat/Kingfisher.git",
        "state": {"revision": "abc", "version": "7.9.0"}
      }
    ]
  },
  "version": 1
}`
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), resolvedContent)
		testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import Kingfisher\n_ = KingfisherManager.shared\n")

		reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "kingfisher"})
		if err != nil {
			t.Fatalf("analyse: %v", err)
		}
		if len(reportData.Dependencies) != 1 {
			t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
		}
		if reportData.Dependencies[0].TotalExportsCount == 0 {
			t.Fatalf("expected import mapped from v1 pins, got %#v", reportData.Dependencies[0])
		}
	})

	t.Run("v2_top_level_pins", func(t *testing.T) {
		repo := t.TempDir()
		resolvedContent := `{
  "pins": [
    {
      "identity": "swift-collections",
      "location": "https://github.com/apple/swift-collections.git",
      "state": {"revision": "abc", "version": "1.1.0"}
    }
  ],
  "version": 2
}`
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), resolvedContent)
		testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import SwiftCollections\n")

		reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "swift-collections"})
		if err != nil {
			t.Fatalf("analyse: %v", err)
		}
		if len(reportData.Dependencies) != 1 {
			t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
		}
		if len(reportData.Dependencies[0].UnusedImports) == 0 && len(reportData.Dependencies[0].UsedImports) == 0 {
			t.Fatalf("expected import mapped from v2 pins, got %#v", reportData.Dependencies[0])
		}
	})
}

func TestSwiftAdapterWarningsForMissingManifestAndResolved(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import Foundation\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	assertWarningContains(t, reportData.Warnings, packageManifestName+" not found")
	assertWarningContains(t, reportData.Warnings, packageResolvedName+" not found")
	assertWarningContains(t, reportData.Warnings, "no Swift package dependencies were discovered")
}

func TestParseSwiftImportsPatterns(t *testing.T) {
	importsContent := []byte(`import Alamofire
@testable import NIO
import struct Foundation.Date
import class MyLib.Client`)
	imports := parseSwiftImports(importsContent, "main.swift")
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

func writeSwiftDemoPackage(t *testing.T, repo string, dependencies []swiftFixtureDependency, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), buildSwiftManifestContent(dependencies))
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), buildSwiftResolvedContent(dependencies))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", "main.swift"), mainContent)
}

func buildSwiftManifestContent(dependencies []swiftFixtureDependency) string {
	lines := []string{
		"import PackageDescription",
		"let package = Package(",
		`  name: "Demo",`,
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
			fmt.Sprintf(`      "state": {"revision": %q, "version": %q}`, "abc", dependency.version),
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
