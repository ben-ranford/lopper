package swift

import (
	"context"
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
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), strings.Join([]string{
		"import PackageDescription",
		"let package = Package(",
		"  name: \"Demo\",",
		"  dependencies: [",
		"    .package(url: \"https://github.com/Alamofire/Alamofire.git\", from: \"5.8.0\"),",
		"    .package(name: \"swift-nio\", url: \"https://github.com/apple/swift-nio.git\", from: \"2.60.0\"),",
		"  ],",
		"  targets: [",
		"    .target(",
		"      name: \"Demo\",",
		"      dependencies: [",
		"        .product(name: \"Alamofire\", package: \"alamofire\"),",
		"        .product(name: \"NIO\", package: \"swift-nio\")",
		"      ]",
		"    ),",
		"  ]",
		")",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), strings.Join([]string{
		"{",
		"  \"pins\": [",
		"    {",
		"      \"identity\": \"alamofire\",",
		"      \"location\": \"https://github.com/Alamofire/Alamofire.git\",",
		"      \"state\": {\"revision\": \"abc\", \"version\": \"5.8.0\"}",
		"    },",
		"    {",
		"      \"identity\": \"swift-nio\",",
		"      \"location\": \"https://github.com/apple/swift-nio.git\",",
		"      \"state\": {\"revision\": \"def\", \"version\": \"2.60.0\"}",
		"    }",
		"  ],",
		"  \"version\": 2",
		"}",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", "main.swift"), strings.Join([]string{
		"import Alamofire",
		"import struct NIO.ByteBuffer",
		"func run() {",
		"  _ = Session.default",
		"  _ = ByteBufferAllocator().buffer(capacity: 8)",
		"}",
	}, "\n"))

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

func TestSwiftAdapterCountsUnqualifiedSingleDependencyUsage(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), strings.Join([]string{
		"import PackageDescription",
		"let package = Package(",
		"  name: \"Demo\",",
		"  dependencies: [",
		"    .package(url: \"https://github.com/Alamofire/Alamofire.git\", from: \"5.8.0\")",
		"  ],",
		"  targets: [",
		"    .target(name: \"Demo\", dependencies: [.product(name: \"Alamofire\", package: \"alamofire\")])",
		"  ]",
		")",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), strings.Join([]string{
		"{",
		"  \"pins\": [",
		"    {",
		"      \"identity\": \"alamofire\",",
		"      \"location\": \"https://github.com/Alamofire/Alamofire.git\",",
		"      \"state\": {\"revision\": \"abc\", \"version\": \"5.8.0\"}",
		"    }",
		"  ],",
		"  \"version\": 2",
		"}",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", "main.swift"), strings.Join([]string{
		"import Alamofire",
		"func run() {",
		"  _ = Session.default",
		"}",
	}, "\n"))

	depReport, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "alamofire"})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
	}
	if depReport.Dependencies[0].UsedExportsCount == 0 {
		t.Fatalf("expected unqualified usage to count as used, got %#v", depReport.Dependencies[0])
	}
}

func TestSwiftAdapterDoesNotAttributeAmbiguousUnqualifiedUsage(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), strings.Join([]string{
		"import PackageDescription",
		"let package = Package(",
		"  name: \"Demo\",",
		"  dependencies: [",
		"    .package(url: \"https://github.com/Alamofire/Alamofire.git\", from: \"5.8.0\"),",
		"    .package(url: \"https://github.com/onevcat/Kingfisher.git\", from: \"7.9.0\")",
		"  ],",
		"  targets: [",
		"    .target(",
		"      name: \"Demo\",",
		"      dependencies: [",
		"        .product(name: \"Alamofire\", package: \"alamofire\"),",
		"        .product(name: \"Kingfisher\", package: \"kingfisher\")",
		"      ]",
		"    )",
		"  ]",
		")",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), strings.Join([]string{
		"{",
		"  \"pins\": [",
		"    {",
		"      \"identity\": \"alamofire\",",
		"      \"location\": \"https://github.com/Alamofire/Alamofire.git\",",
		"      \"state\": {\"revision\": \"abc\", \"version\": \"5.8.0\"}",
		"    },",
		"    {",
		"      \"identity\": \"kingfisher\",",
		"      \"location\": \"https://github.com/onevcat/Kingfisher.git\",",
		"      \"state\": {\"revision\": \"def\", \"version\": \"7.9.0\"}",
		"    }",
		"  ],",
		"  \"version\": 2",
		"}",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", "main.swift"), strings.Join([]string{
		"import Alamofire",
		"import Kingfisher",
		"func run() {",
		"  _ = Session.default",
		"}",
	}, "\n"))

	depReport, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "alamofire"})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
	}
	if depReport.Dependencies[0].UsedExportsCount != 0 {
		t.Fatalf("expected ambiguous unqualified usage to remain unattributed, got %#v", depReport.Dependencies[0])
	}
}

func TestSwiftAdapterParsesResolvedVariants(t *testing.T) {
	t.Run("v1_object_pins", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), strings.Join([]string{
			"{",
			"  \"object\": {",
			"    \"pins\": [",
			"      {",
			"        \"package\": \"Kingfisher\",",
			"        \"repositoryURL\": \"https://github.com/onevcat/Kingfisher.git\",",
			"        \"state\": {\"revision\": \"abc\", \"version\": \"7.9.0\"}",
			"      }",
			"    ]",
			"  },",
			"  \"version\": 1",
			"}",
		}, "\n"))
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
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), strings.Join([]string{
			"{",
			"  \"pins\": [",
			"    {",
			"      \"identity\": \"swift-collections\",",
			"      \"location\": \"https://github.com/apple/swift-collections.git\",",
			"      \"state\": {\"revision\": \"abc\", \"version\": \"1.1.0\"}",
			"    }",
			"  ],",
			"  \"version\": 2",
			"}",
		}, "\n"))
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
	imports := parseSwiftImports([]byte(strings.Join([]string{
		"import Alamofire",
		"@testable import NIO",
		"import struct Foundation.Date",
		"import class MyLib.Client",
	}, "\n")), "main.swift")
	modules := make([]string, 0, len(imports))
	for _, imported := range imports {
		modules = append(modules, imported.Module)
	}
	if !slices.Equal(modules, []string{"Alamofire", "NIO", "Foundation", "MyLib"}) {
		t.Fatalf("unexpected parsed modules: %#v", modules)
	}
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
