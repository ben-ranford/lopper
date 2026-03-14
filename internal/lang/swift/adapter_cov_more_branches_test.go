package swift

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestSwiftAdditionalBranchCoverage(t *testing.T) {
	t.Run("detection helpers", func(t *testing.T) {
		if err := contextError(context.TODO()); err != nil {
			t.Fatalf("expected nil context error, got %v", err)
		}
		if err := maybeSkipSwiftDir(swiftBuildDirName); !errors.Is(err, filepath.SkipDir) {
			t.Fatalf("expected swift build dir to be skipped, got %v", err)
		}
		if err := maybeSkipSwiftDir("Sources"); err != nil {
			t.Fatalf("did not expect Sources to be skipped, got %v", err)
		}
		if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatalf("expected missing repo to fail detection")
		}
	})

	t.Run("manifest and resolved loaders", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, packageManifestName), 0o755); err != nil {
			t.Fatalf("mkdir package manifest dir: %v", err)
		}
		catalog := dependencyCatalog{
			Dependencies:       map[string]dependencyMeta{},
			AliasToDependency:  map[string]string{},
			ModuleToDependency: map[string]string{},
			LocalModules:       map[string]struct{}{},
		}
		if _, _, err := loadManifestData(repo, &catalog); err == nil {
			t.Fatalf("expected manifest directory to fail load")
		}

		repo = t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, packageResolvedName), []byte(`{"pins":[]}`), 0o644); err != nil {
			t.Fatalf("write empty Package.resolved: %v", err)
		}
		found, warnings, err := loadResolvedData(repo, &catalog)
		if err != nil || !found {
			t.Fatalf("expected resolved loader success, found=%v err=%v", found, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "no pins found") {
			t.Fatalf("expected empty pins warning, got %#v", warnings)
		}
		if got := resolvedPinDependencyID(resolvedPin{}); got != "" {
			t.Fatalf("expected empty resolved pin dependency id, got %q", got)
		}
		if got := resolvedPinSource(resolvedPin{}); got != "" {
			t.Fatalf("expected empty resolved pin source, got %q", got)
		}

		repo = t.TempDir()
		manifest := `import PackageDescription
let package = Package(
  name: "Demo",
  dependencies: [
    .package(url: "", from: "1.0.0"),
    .package(url: "https://github.com/apple/swift-nio.git", from: "2.0.0")
  ],
  targets: [
    .target(name: "Demo", dependencies: [
      .product(name: "", package: "swift-nio"),
      .product(name: "NIO", package: "")
    ])
  ]
)`
		if err := os.WriteFile(filepath.Join(repo, packageManifestName), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write package manifest: %v", err)
		}
		found, warnings, err = loadManifestData(repo, &catalog)
		if err != nil || !found {
			t.Fatalf("expected manifest loader success, found=%v err=%v", found, err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected incomplete manifest declarations to be skipped without warnings, got %#v", warnings)
		}
		if _, ok := catalog.Dependencies[swiftNIOID]; !ok {
			t.Fatalf("expected valid dependency to remain discoverable, got %#v", catalog.Dependencies)
		}

		repo = t.TempDir()
		resolved := `{"pins":[{},{"identity":"swift-nio","location":"https://github.com/apple/swift-nio.git","state":{"version":"2.0.0"}}]}`
		if err := os.WriteFile(filepath.Join(repo, packageResolvedName), []byte(resolved), 0o644); err != nil {
			t.Fatalf("write package resolved: %v", err)
		}
		catalog = dependencyCatalog{
			Dependencies:       map[string]dependencyMeta{},
			AliasToDependency:  map[string]string{},
			ModuleToDependency: map[string]string{},
			LocalModules:       map[string]struct{}{},
		}
		found, warnings, err = loadResolvedData(repo, &catalog)
		if err != nil || !found {
			t.Fatalf("expected resolved loader success with skipped blank pin, found=%v err=%v", found, err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no warnings for mixed pins, got %#v", warnings)
		}
		if _, ok := catalog.Dependencies[swiftNIOID]; !ok {
			t.Fatalf("expected valid resolved pin to be merged, got %#v", catalog.Dependencies)
		}

		catalog = dependencyCatalog{LocalModules: map[string]struct{}{}}
		collectLocalModules(`.target(name: "")`, &catalog)
		if len(catalog.LocalModules) != 0 {
			t.Fatalf("expected blank local module names to be ignored, got %#v", catalog.LocalModules)
		}
	})

	t.Run("catalog and import mapping helpers", func(t *testing.T) {
		catalog := dependencyCatalog{
			Dependencies:       map[string]dependencyMeta{"dep": {}},
			AliasToDependency:  map[string]string{},
			ModuleToDependency: map[string]string{},
			LocalModules:       map[string]struct{}{lookupKey("Demo"): {}},
		}
		ensureDependency(&catalog, "", true, true, "1.0.0", "abc", "git")
		if len(catalog.Dependencies) != 1 {
			t.Fatalf("expected blank dependency id to be ignored, got %#v", catalog.Dependencies)
		}
		ensureDependency(&catalog, "dep", false, true, "1.0.0", "abc", "git")
		ensureDependency(&catalog, "dep", true, false, "2.0.0", "def", "other")
		meta := catalog.Dependencies["dep"]
		if !meta.Declared || !meta.Resolved || meta.Version != "1.0.0" || meta.Revision != "abc" || meta.Source != "git" {
			t.Fatalf("expected first non-empty dependency metadata to persist, got %#v", meta)
		}

		setLookup(catalog.AliasToDependency, "", "dep")
		setLookup(catalog.AliasToDependency, "dep", "")
		if len(catalog.AliasToDependency) != 0 {
			t.Fatalf("expected blank lookup keys/values to be ignored, got %#v", catalog.AliasToDependency)
		}

		scanner := newRepoScanner(t.TempDir(), catalog)
		imports := scanner.resolveImports([]importBinding{
			{Module: "dep", Dependency: "", Name: "", Local: ""},
			{Module: "MysteryKit"},
			{Module: "Demo"},
		})
		if len(imports) != 1 || imports[0].Dependency != "dep" || imports[0].Name != "dep" || imports[0].Local != "dep" {
			t.Fatalf("expected known dependency import to be normalized, got %#v", imports)
		}
		if scanner.unresolvedImports["MysteryKit"] != 1 {
			t.Fatalf("expected unresolved third-party import to be tracked, got %#v", scanner.unresolvedImports)
		}
		if scanner.unresolvedImports["Demo"] != 0 {
			t.Fatalf("expected local module import not to be tracked, got %#v", scanner.unresolvedImports)
		}
	})

	t.Run("unqualified usage helpers", func(t *testing.T) {
		if got := importsByDependency([]importBinding{{Dependency: ""}, {Dependency: "dep", Local: "Dep", Module: "Dep"}}); len(got) != 1 || len(got["dep"]) != 1 {
			t.Fatalf("expected blank dependencies to be skipped, got %#v", got)
		}
		if lineHasPotentialUnqualifiedSymbolUsage("Dep()", map[string]struct{}{lookupKey("Dep"): {}}, map[string]struct{}{}) {
			t.Fatalf("expected imported module symbol to be ignored")
		}
		if lineHasPotentialUnqualifiedSymbolUsage("LocalType()", map[string]struct{}{}, map[string]struct{}{lookupKey("LocalType"): {}}) {
			t.Fatalf("expected local declarations to be ignored")
		}
		if lineHasPotentialUnqualifiedSymbolUsage("String()", nil, nil) {
			t.Fatalf("expected standard Swift symbol to be ignored")
		}
		if !lineHasPotentialUnqualifiedSymbolUsage("CustomBuilder()", nil, nil) {
			t.Fatalf("expected unknown capitalized symbol to be treated as potential usage")
		}

		baseUsage := map[string]int{"existing": 1}
		if got := applyUnqualifiedUsageHeuristic([]byte("CustomBuilder()\n"), nil, baseUsage); got["existing"] != 1 {
			t.Fatalf("expected empty import heuristic to preserve usage, got %#v", got)
		}

		usage := map[string]int{"Dep": 1}
		imports := []importBinding{{Dependency: "dep", Local: "Dep", Module: "Dep"}}
		if got := applyUnqualifiedUsageHeuristic([]byte("Dep.make()\n"), imports, usage); got["Dep"] != 1 {
			t.Fatalf("expected qualified usage heuristic to keep existing usage, got %#v", got)
		}

		unqualified := map[string]int{}
		if got := applyUnqualifiedUsageHeuristic([]byte("CustomBuilder()\n"), imports, unqualified); got["Dep"] != 1 {
			t.Fatalf("expected unqualified usage seeding, got %#v", got)
		}

		multiDepUsage := map[string]int{}
		multiDepImports := []importBinding{
			{Dependency: "dep-a", Local: "DepA", Module: "DepA"},
			{Dependency: "dep-b", Local: "DepB", Module: "DepB"},
		}
		if got := applyUnqualifiedUsageHeuristic([]byte("CustomBuilder()\n"), multiDepImports, multiDepUsage); len(got) != 0 {
			t.Fatalf("expected multi-dependency heuristic to avoid seeding, got %#v", got)
		}
	})

	t.Run("invalid path and helper guard branches", func(t *testing.T) {
		if _, err := NewAdapter().DetectWithConfidence(context.Background(), "\x00"); err == nil {
			t.Fatalf("expected invalid repo path to fail detection")
		}
		if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
			t.Fatalf("expected invalid repo path to fail analysis")
		}

		if got := derivePackageIdentity("."); got != "" {
			t.Fatalf("expected dot path to normalize to empty package identity, got %q", got)
		}
		if got := dedupeStrings(nil); len(got) != 0 {
			t.Fatalf("expected nil string dedupe result, got %#v", got)
		}
		if got := extractDotCallArguments(".package(", "package", 10); len(got) != 0 {
			t.Fatalf("expected malformed dot-call input to stop without arguments, got %#v", got)
		}
		if _, _, ok := captureParenthesized("x", 0); ok {
			t.Fatalf("expected non-parenthesized input to fail capture")
		}
		depth := 0
		if closed, valid := advanceParenthesisDepth(')', &depth); closed || valid {
			t.Fatalf("expected negative depth to report invalid state, closed=%v valid=%v depth=%d", closed, valid, depth)
		}

		if imports := parseSwiftImports([]byte("import \n"), swiftMainFileName); len(imports) != 0 {
			t.Fatalf("expected blank Swift import to be ignored, got %#v", imports)
		}

		var builder strings.Builder
		state := swiftStringScanState{inString: true}
		if next := consumeSwiftStringContent([]byte("\\n"), 0, &builder, &state); next != 1 || !state.escaped {
			t.Fatalf("expected escaped string branch to mark escape state, next=%d state=%#v", next, state)
		}
		if matchesSwiftStringDelimiter([]byte(`"#x`), 0, 2, false) {
			t.Fatalf("expected mismatched raw string delimiter to fail")
		}

		symbols := collectLocalDeclaredSymbols([]byte("struct _ {}\n"))
		if len(symbols) != 0 {
			t.Fatalf("expected non-alphanumeric local declaration names to be ignored, got %#v", symbols)
		}

		depReport, warnings := buildDependencyReport("dep", scanResult{}, dependencyCatalog{}, 50)
		if depReport.Name != "dep" || len(warnings) != 1 || !strings.Contains(warnings[0], "no imports found") {
			t.Fatalf("expected missing-import dependency report warning, report=%#v warnings=%#v", depReport, warnings)
		}

		topReports, topWarnings := buildTopSwiftDependencies(scanResult{KnownDependencies: map[string]struct{}{"alpha": {}, "beta": {}}, ImportedDependencies: map[string]struct{}{}}, dependencyCatalog{}, 50)(1, scanResult{}, report.DefaultRemovalCandidateWeights())
		if len(topReports) != 1 || len(topWarnings) != 2 {
			t.Fatalf("expected top-N slicing with per-dependency warnings, reports=%#v warnings=%#v", topReports, topWarnings)
		}
	})

	t.Run("scanner walk branches", func(t *testing.T) {
		repo := t.TempDir()
		scanner := newRepoScanner(repo, dependencyCatalog{})
		if err := scanner.walk(context.Background(), filepath.Join(repo, "missing.swift"), nil, fs.ErrNotExist); !os.IsNotExist(err) {
			t.Fatalf("expected walk error passthrough, got %v", err)
		}

		limitedPath := filepath.Join(repo, "Limited.swift")
		if err := os.WriteFile(limitedPath, []byte("import Foundation\n"), 0o644); err != nil {
			t.Fatalf("write limited swift file: %v", err)
		}
		entries, err := os.ReadDir(repo)
		if err != nil {
			t.Fatalf("read repo dir: %v", err)
		}
		var limitedEntry fs.DirEntry
		for _, entry := range entries {
			if entry.Name() == "Limited.swift" {
				limitedEntry = entry
				break
			}
		}
		if limitedEntry == nil {
			t.Fatalf("expected Limited.swift dir entry")
		}
		scanner.visited = maxScanFiles
		if err := scanner.scanSwiftFile(limitedPath, limitedEntry); !errors.Is(err, fs.SkipAll) {
			t.Fatalf("expected scan file bound skip, got %v", err)
		}

		missingPath := filepath.Join(repo, "Missing.swift")
		if err := os.WriteFile(missingPath, []byte("import Foundation\n"), 0o644); err != nil {
			t.Fatalf("write missing swift file: %v", err)
		}
		entries, err = os.ReadDir(repo)
		if err != nil {
			t.Fatalf("re-read repo dir: %v", err)
		}
		var missingEntry fs.DirEntry
		for _, entry := range entries {
			if entry.Name() == "Missing.swift" {
				missingEntry = entry
				break
			}
		}
		if missingEntry == nil {
			t.Fatalf("expected Missing.swift dir entry")
		}
		if err := os.Remove(missingPath); err != nil {
			t.Fatalf("remove missing swift file: %v", err)
		}
		if err := newRepoScanner(repo, dependencyCatalog{}).scanSwiftFile(missingPath, missingEntry); err == nil {
			t.Fatalf("expected removed Swift source file to fail scan")
		}
	})
}
