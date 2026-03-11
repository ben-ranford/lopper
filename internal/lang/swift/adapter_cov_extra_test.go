package swift

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != swiftAdapterID {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if !slices.Equal(adapter.Aliases(), []string{"swiftpm"}) {
		t.Fatalf("unexpected aliases: %#v", adapter.Aliases())
	}

	repo := t.TempDir()
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if ok {
		t.Fatalf("expected detect=false for empty repo")
	}

	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "// manifest\n")
	ok, err = adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect manifest repo: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true when Package.swift exists")
	}

	if _, err := adapter.DetectWithConfidence(testutil.CanceledContext(), repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled detect to fail with context.Canceled, got %v", err)
	}
}

func TestSwiftHelperBranches(t *testing.T) {
	pins, err := parseResolvedPins([]byte(`{"pins":[{"identity":"alamofire","location":"https://github.com/Alamofire/Alamofire.git","state":{"version":"5.8.0"}}],"object":{"pins":[{"package":"Kingfisher","repositoryURL":"https://github.com/onevcat/Kingfisher.git","state":{"revision":"abc"}}]}}`))
	if err != nil {
		t.Fatalf("parse resolved pins: %v", err)
	}
	if len(pins) != 2 {
		t.Fatalf("expected both top-level and object pins, got %#v", pins)
	}
	if _, err := parseResolvedPins([]byte("{")); err == nil {
		t.Fatalf("expected invalid resolved json to fail")
	}

	catalog := dependencyCatalog{LocalModules: make(map[string]struct{})}
	collectLocalModules(`.target(name: "Demo").library(name: "SupportKit")`, &catalog)
	if _, ok := catalog.LocalModules[lookupKey("Demo")]; !ok {
		t.Fatalf("expected Demo local module, got %#v", catalog.LocalModules)
	}
	if _, ok := catalog.LocalModules[lookupKey("SupportKit")]; !ok {
		t.Fatalf("expected SupportKit local module, got %#v", catalog.LocalModules)
	}

	depID, aliases := parsePackageDeclaration(`name: "swift-nio", url: "https://github.com/apple/swift-nio.git"`)
	if depID != "swift-nio" || !slices.Contains(aliases, "swift-nio") {
		t.Fatalf("expected url-based declaration parsing, got %q %#v", depID, aliases)
	}
	depID, aliases = parsePackageDeclaration(`path: "../LocalPackage"`)
	if depID != "localpackage" || !slices.Contains(aliases, "LocalPackage") {
		t.Fatalf("expected path-based declaration parsing, got %q %#v", depID, aliases)
	}

	fields := parseStringFields(`name: "Demo", path: "Sources\/Demo", note: "escaped \"quote\""`)
	if fields["path"] != `Sources\/Demo` || fields["note"] != `escaped "quote"` {
		t.Fatalf("unexpected parsed fields: %#v", fields)
	}

	argsList := extractDotCallArguments(`.target(name: "Demo", dependencies: [.product(name: "Alamofire", package: "alamofire")]) .plugin(name: "CodeGen")`, "target", 2)
	if len(argsList) != 1 || !strings.Contains(argsList[0], `name: "Demo"`) {
		t.Fatalf("expected one target call, got %#v", argsList)
	}
	limitedArgs := extractDotCallArguments(`.target(name: "One") .target name: "ignored" .target(name: "Two")`, "target", 1)
	if len(limitedArgs) != 1 || !strings.Contains(limitedArgs[0], `"One"`) {
		t.Fatalf("expected max-items extraction to stop after first call, got %#v", limitedArgs)
	}
	if _, _, ok := captureParenthesized("target", 0); ok {
		t.Fatalf("expected captureParenthesized to reject non-paren input")
	}
	if _, _, ok := captureParenthesized(`("unterminated"`, 0); ok {
		t.Fatalf("expected unterminated parens to fail capture")
	}

	if got := derivePackageIdentity("git@github.com:apple/swift-nio.git"); got != "swift-nio" {
		t.Fatalf("unexpected git identity: %q", got)
	}
	if got := derivePackageIdentity(" ../LocalPackage "); got != "LocalPackage" {
		t.Fatalf("unexpected path identity: %q", got)
	}

	lookup := map[string]string{}
	setLookup(lookup, lookupKey("NIO"), "swift-nio")
	setLookup(lookup, lookupKey("NIO"), "other")
	if _, ok := resolveLookup(lookup, lookupKey("NIO")); ok {
		t.Fatalf("expected ambiguous lookup to fail resolution")
	}

	catalog = dependencyCatalog{
		Dependencies:       map[string]dependencyMeta{"swift-nio": {}},
		AliasToDependency:  map[string]string{lookupKey("swift-nio"): "swift-nio"},
		ModuleToDependency: map[string]string{lookupKey("NIO"): "swift-nio"},
		LocalModules:       map[string]struct{}{lookupKey("Demo"): {}},
	}
	if got := resolveDependencyReference(catalog, "NIO"); got != "swift-nio" {
		t.Fatalf("expected module lookup to resolve dependency, got %q", got)
	}
	if got := resolveDependencyReference(catalog, "swift-nio"); got != "swift-nio" {
		t.Fatalf("expected normalized fallback lookup, got %q", got)
	}
	if !shouldTrackUnresolvedImport("MysteryKit", catalog) {
		t.Fatalf("expected unknown third-party import to be tracked")
	}
	if shouldTrackUnresolvedImport("Foundation", catalog) {
		t.Fatalf("expected stdlib import to be ignored")
	}
	if shouldTrackUnresolvedImport("Demo", catalog) {
		t.Fatalf("expected local module import to be ignored")
	}
	if shouldTrackUnresolvedImport("MysteryKit", dependencyCatalog{}) {
		t.Fatalf("expected unresolved tracking to disable when no dependencies are known")
	}

	warning := unresolvedImportWarning(map[string]int{"Gamma": 1, "Alpha": 3, "Beta": 3, "Delta": 1, "Epsilon": 1, "Zeta": 1})
	if !strings.Contains(warning, "Alpha (3), Beta (3)") || !strings.Contains(warning, "+1 more") {
		t.Fatalf("unexpected unresolved import warning: %q", warning)
	}

	minUsage := 42
	if got := resolveMinUsageRecommendationThreshold(&minUsage); got != minUsage {
		t.Fatalf("expected explicit min usage threshold, got %d", got)
	}
	if got := resolveMinUsageRecommendationThreshold(nil); got <= 0 {
		t.Fatalf("expected default min usage threshold to be positive, got %d", got)
	}

	defaultWeights := report.DefaultRemovalCandidateWeights()
	if got := resolveRemovalCandidateWeights(nil); got != defaultWeights {
		t.Fatalf("expected default weights, got %#v", got)
	}
	customWeights := report.RemovalCandidateWeights{Usage: 0.2, Impact: 0.3, Confidence: 0.5}
	if got := resolveRemovalCandidateWeights(&customWeights); got != customWeights {
		t.Fatalf("expected provided weights to remain unchanged, got %#v", got)
	}

	if got := normalizeDependencyID("__Swift_NIO__"); got != "swift-nio" {
		t.Fatalf("unexpected normalized dependency id: %q", got)
	}
	if !shouldSkipDir(".build") || shouldSkipDir("Sources") {
		t.Fatalf("unexpected skip-dir behavior")
	}

	if got := dedupeWarnings([]string{" repeated ", "", "repeated", "unique"}); !slices.Equal(got, []string{"repeated", "unique"}) {
		t.Fatalf("unexpected deduped warnings: %#v", got)
	}
	if got := dedupeStrings([]string{" Demo ", "demo", "", "NIO"}); !slices.Equal(got, []string{"Demo", "NIO"}) {
		t.Fatalf("unexpected deduped strings: %#v", got)
	}
	lookupSet := toLookupSet([]string{" Swift NIO ", "", "Foundation"})
	if _, ok := lookupSet[lookupKey("Swift NIO")]; !ok {
		t.Fatalf("expected normalized lookup key to exist, got %#v", lookupSet)
	}
}

func TestSwiftScanAndRecommendationBranches(t *testing.T) {
	repo := t.TempDir()
	dependencies := []swiftFixtureDependency{
		{identity: "alamofire", url: "https://github.com/Alamofire/Alamofire.git", version: "5.8.0", productName: "Alamofire"},
	}
	mainContent := `import Alamofire
import MysteryKit
func run() {
  _ = Session.default
}`
	writeSwiftDemoPackage(t, repo, dependencies, mainContent)
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", "Big.swift"), strings.Repeat("x", maxScannableSwiftFile+1))
	testutil.MustWriteFile(t, filepath.Join(repo, ".build", "ignored.swift"), "import Alamofire\n")

	catalog, warnings, err := buildDependencyCatalog(repo)
	if err != nil {
		t.Fatalf("build dependency catalog: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no catalog warnings, got %#v", warnings)
	}

	scan, err := scanRepo(context.Background(), repo, catalog)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if _, ok := scan.ImportedDependencies["alamofire"]; !ok {
		t.Fatalf("expected imported dependency to be tracked, got %#v", scan.ImportedDependencies)
	}
	if len(scan.Files) != 2 {
		t.Fatalf("expected Package.swift and the source file to be scanned, got %#v", scan.Files)
	}
	assertWarningContains(t, scan.Warnings, "skipped 1 Swift file(s)")
	assertWarningContains(t, scan.Warnings, "could not map some Swift imports")

	if _, err := scanRepo(testutil.CanceledContext(), repo, catalog); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled scan to fail with context.Canceled, got %v", err)
	}

	meta := dependencyMeta{Declared: true, Source: packageResolvedName}
	catalog.Dependencies["alamofire"] = meta
	file := fileScan{
		Path: "Sources/Demo/main.swift",
		Imports: []importBinding{{
			Module:     "Alamofire",
			Name:       "Alamofire",
			Local:      "Alamofire",
			Dependency: "alamofire",
		}},
		Usage: map[string]int{},
	}
	reportScan := scanResult{
		Files:                []fileScan{file},
		KnownDependencies:    map[string]struct{}{"alamofire": {}},
		ImportedDependencies: map[string]struct{}{"alamofire": {}},
	}
	reportData, reportWarnings := buildDependencyReport("alamofire", reportScan, catalog, 50)
	if len(reportWarnings) != 0 {
		t.Fatalf("expected import-backed report to avoid warnings, got %#v", reportWarnings)
	}
	if len(buildDependencyRiskCues(meta)) != 1 {
		t.Fatalf("expected unresolved dependency risk cue")
	}
	if len(reportData.Recommendations) != 3 {
		t.Fatalf("expected all recommendation branches to trigger, got %#v", reportData.Recommendations)
	}
	if reportData.Provenance == nil || reportData.Provenance.Signals[0] != packageResolvedName {
		t.Fatalf("expected provenance from lockfile source, got %#v", reportData.Provenance)
	}

	topReports, topWarnings := buildTopSwiftDependencies(scanResult{}, dependencyCatalog{}, 50)(5, scanResult{}, report.DefaultRemovalCandidateWeights())
	if len(topReports) != 0 || len(topWarnings) != 1 {
		t.Fatalf("expected empty top dependency ranking warning, got %#v %#v", topReports, topWarnings)
	}
}

func TestSwiftCatalogWarningAndFallbackBranches(t *testing.T) {
	t.Run("manifest without packages and empty resolved pins", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), `import PackageDescription
let package = Package(
  name: "Demo",
  targets: [
    .target(name: "Demo")
  ]
)`)
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), `{"pins":[],"version":2}`)

		catalog, warnings, err := buildDependencyCatalog(repo)
		if err != nil {
			t.Fatalf("build dependency catalog: %v", err)
		}
		if len(catalog.Dependencies) != 0 {
			t.Fatalf("expected no discovered dependencies, got %#v", catalog.Dependencies)
		}
		if _, ok := catalog.LocalModules[lookupKey("Demo")]; !ok {
			t.Fatalf("expected local module to be captured, got %#v", catalog.LocalModules)
		}
		assertWarningContains(t, warnings, "no .package(...) declarations found in Package.swift")
		assertWarningContains(t, warnings, "no pins found in Package.resolved")
		assertWarningContains(t, warnings, "no Swift package dependencies were discovered")

		reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 3})
		if err != nil {
			t.Fatalf("analyse topN local-only repo: %v", err)
		}
		assertWarningContains(t, reportData.Warnings, "no dependency data available for top-N ranking")
	})

	t.Run("resolved package and repository url fallbacks", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), `{"object":{"pins":[{"package":"Kingfisher","repositoryURL":"https://github.com/onevcat/Kingfisher.git","state":{"revision":"abc","version":"7.9.0"}}]}}`)

		catalog := dependencyCatalog{
			Dependencies:       make(map[string]dependencyMeta),
			AliasToDependency:  make(map[string]string),
			ModuleToDependency: make(map[string]string),
			LocalModules:       make(map[string]struct{}),
		}
		found, warnings, err := loadResolvedData(repo, &catalog)
		if err != nil {
			t.Fatalf("load resolved data: %v", err)
		}
		if !found || len(warnings) != 0 {
			t.Fatalf("expected resolved data to load without warnings, got found=%v warnings=%#v", found, warnings)
		}
		meta, ok := catalog.Dependencies["kingfisher"]
		if !ok || !meta.Resolved || meta.Source != "https://github.com/onevcat/Kingfisher.git" || meta.Version != "7.9.0" {
			t.Fatalf("expected repositoryURL fallback dependency metadata, got %#v", catalog.Dependencies)
		}
		if depID, ok := resolveLookup(catalog.ModuleToDependency, lookupKey("Kingfisher")); !ok || depID != "kingfisher" {
			t.Fatalf("expected package fallback module mapping, got %#v", catalog.ModuleToDependency)
		}
	})
}

func TestSwiftUsageHeuristicBranches(t *testing.T) {
	if got := applyUnqualifiedUsageHeuristic(nil, nil, map[string]int{"Session": 2}); got["Session"] != 2 {
		t.Fatalf("expected empty import heuristic to preserve usage, got %#v", got)
	}

	multipleDeps := []importBinding{
		{Dependency: "alamofire", Module: "Alamofire", Local: "Session"},
		{Dependency: "kingfisher", Module: "Kingfisher", Local: "KingfisherManager"},
	}
	if got := applyUnqualifiedUsageHeuristic([]byte("let value = Session.default"), multipleDeps, map[string]int{}); len(got) != 0 {
		t.Fatalf("expected multiple dependency heuristic to avoid attribution, got %#v", got)
	}

	singleDep := []importBinding{{Dependency: "alamofire", Module: "Alamofire", Local: "Session"}}
	if got := applyUnqualifiedUsageHeuristic([]byte("import Alamofire\n_ = Session.default"), singleDep, map[string]int{"Session": 3}); got["Session"] != 3 {
		t.Fatalf("expected qualified usage to remain unchanged, got %#v", got)
	}
	if got := applyUnqualifiedUsageHeuristic([]byte("import Alamofire\nstruct Session {}\nlet value = Session()"), singleDep, map[string]int{}); len(got) != 0 {
		t.Fatalf("expected local declarations to avoid inferred usage, got %#v", got)
	}
	if got := applyUnqualifiedUsageHeuristic([]byte("import Alamofire\nlet value = NetworkSession()"), singleDep, map[string]int{}); got["Session"] != 1 {
		t.Fatalf("expected inferred unqualified usage, got %#v", got)
	}

	if hasPotentialUnqualifiedSymbolUsage([]byte("import Alamofire\nlet value: URL? = nil"), singleDep) {
		t.Fatalf("expected standard Swift symbols to be ignored")
	}
	if !hasPotentialUnqualifiedSymbolUsage([]byte("import Alamofire\nlet value = NetworkSession()"), singleDep) {
		t.Fatalf("expected non-standard symbol usage to be detected")
	}

	symbols := collectLocalDeclaredSymbols([]byte("struct Demo {}\nprotocol Runner {}\nimport Alamofire\n"))
	if _, ok := symbols[lookupKey("Demo")]; !ok {
		t.Fatalf("expected Demo to be collected, got %#v", symbols)
	}
	if _, ok := symbols[lookupKey("Runner")]; !ok {
		t.Fatalf("expected Runner to be collected, got %#v", symbols)
	}
}

func TestSwiftLoadFileErrorBranches(t *testing.T) {
	repo := t.TempDir()

	manifestPath := filepath.Join(repo, packageManifestName)
	if err := os.MkdirAll(manifestPath, 0o750); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	catalog := dependencyCatalog{
		Dependencies:       make(map[string]dependencyMeta),
		AliasToDependency:  make(map[string]string),
		ModuleToDependency: make(map[string]string),
		LocalModules:       make(map[string]struct{}),
	}
	if found, warnings, err := loadManifestData(repo, &catalog); err == nil || found || len(warnings) != 0 {
		t.Fatalf("expected manifest directory to fail loading, got found=%v warnings=%#v err=%v", found, warnings, err)
	}

	if err := os.RemoveAll(manifestPath); err != nil {
		t.Fatalf("remove manifest dir: %v", err)
	}
	resolvedPath := filepath.Join(repo, packageResolvedName)
	if err := os.MkdirAll(resolvedPath, 0o750); err != nil {
		t.Fatalf("mkdir resolved dir: %v", err)
	}
	if found, warnings, err := loadResolvedData(repo, &catalog); err == nil || found || len(warnings) != 0 {
		t.Fatalf("expected resolved directory to fail loading, got found=%v warnings=%#v err=%v", found, warnings, err)
	}
}

func TestSwiftLoadManifestBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), `import PackageDescription
let package = Package(
  name: "Demo",
  dependencies: [
    .package(id: "swift-collections", from: "1.1.0"),
    .package(path: "../LocalDep"),
    .package(name: "LegacyKit", url: "https://example.com/legacy.git", from: "1.0.0")
  ],
  targets: [
    .target(
      name: "Demo",
      dependencies: [
        .product(name: "Collections", package: "swift-collections"),
        .product(name: "LegacyAPI", package: "LegacyKit"),
        .product(name: "Mystery", package: "mystery-kit")
      ]
    ),
    .binaryTarget(name: "BinaryOnly", path: "BinaryOnly.xcframework"),
    .macro(name: "CodeGen", dependencies: [])
  ]
)`)

	catalog := dependencyCatalog{
		Dependencies:       make(map[string]dependencyMeta),
		AliasToDependency:  make(map[string]string),
		ModuleToDependency: make(map[string]string),
		LocalModules:       make(map[string]struct{}),
	}
	found, warnings, err := loadManifestData(repo, &catalog)
	if err != nil {
		t.Fatalf("load manifest data: %v", err)
	}
	if !found || len(warnings) != 0 {
		t.Fatalf("expected manifest to load without warnings, got found=%v warnings=%#v", found, warnings)
	}

	for _, depID := range []string{"swift-collections", "localdep", "legacy", "mystery-kit"} {
		if _, ok := catalog.Dependencies[depID]; !ok {
			t.Fatalf("expected dependency %q in catalog, got %#v", depID, catalog.Dependencies)
		}
	}
	if depID, ok := resolveLookup(catalog.ModuleToDependency, lookupKey("Collections")); !ok || depID != "swift-collections" {
		t.Fatalf("expected product module mapping for Collections, got %#v", catalog.ModuleToDependency)
	}
	if depID, ok := resolveLookup(catalog.ModuleToDependency, lookupKey("LegacyAPI")); !ok || depID != "legacy" {
		t.Fatalf("expected product module mapping for LegacyAPI, got %#v", catalog.ModuleToDependency)
	}
	for _, moduleName := range []string{"BinaryOnly", "CodeGen"} {
		if _, ok := catalog.LocalModules[lookupKey(moduleName)]; !ok {
			t.Fatalf("expected local module %q, got %#v", moduleName, catalog.LocalModules)
		}
	}
}

func TestSwiftRemainingBranchCoverage(t *testing.T) {
	if !shouldSkipDir(".git") {
		t.Fatalf("expected common git dir to be skipped")
	}

	aliasOnlyCatalog := dependencyCatalog{
		Dependencies:       map[string]dependencyMeta{"legacy": {}},
		AliasToDependency:  map[string]string{lookupKey("LegacyKit"): "legacy"},
		ModuleToDependency: map[string]string{},
		LocalModules:       map[string]struct{}{},
	}
	if got := resolveDependencyReference(aliasOnlyCatalog, "LegacyKit"); got != "legacy" {
		t.Fatalf("expected alias-based dependency resolution, got %q", got)
	}

	if args := extractDotCallArguments(".target name: \"ignored\"", "target", 5); len(args) != 0 {
		t.Fatalf("expected malformed target call to be ignored, got %#v", args)
	}
	if args := extractDotCallArguments("let value = 1", "target", 5); len(args) != 0 {
		t.Fatalf("expected missing token extraction to return no args, got %#v", args)
	}
	inner, next, ok := captureParenthesized(`("quoted \"value\"")`, 0)
	if !ok || next <= 0 || !strings.Contains(inner, `quoted`) {
		t.Fatalf("expected escaped string paren capture, got inner=%q next=%d ok=%v", inner, next, ok)
	}

	rawFields := parseStringFields(`note: "\q"`)
	if rawFields["note"] != `\q` {
		t.Fatalf("expected raw field preservation on unquote failure, got %#v", rawFields)
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import Foundation\n")
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect swift-only repo: %v", err)
	}
	if !detection.Matched || !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected swift-only repo detection with repo root fallback, got %#v", detection)
	}

	scan, err := scanRepo(context.Background(), t.TempDir(), dependencyCatalog{
		Dependencies:       map[string]dependencyMeta{"alamofire": {}},
		AliasToDependency:  map[string]string{},
		ModuleToDependency: map[string]string{},
		LocalModules:       map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf("scan empty repo: %v", err)
	}
	assertWarningContains(t, scan.Warnings, "no Swift files found for analysis")

	if _, err := NewAdapter().Analyse(testutil.CanceledContext(), language.Request{RepoPath: repo, TopN: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled analyse to fail with context.Canceled, got %v", err)
	}
}

func TestSwiftFinalHelperBranches(t *testing.T) {
	lookup := map[string]string{}
	setLookup(lookup, "", "alamofire")
	setLookup(lookup, lookupKey("Alamofire"), "")
	setLookup(lookup, lookupKey("Alamofire"), "alamofire")
	setLookup(lookup, lookupKey("Alamofire"), "alamofire")
	if depID, ok := resolveLookup(lookup, lookupKey("Alamofire")); !ok || depID != "alamofire" {
		t.Fatalf("expected stable lookup to keep original dependency, got %#v", lookup)
	}

	catalog := dependencyCatalog{
		Dependencies:       map[string]dependencyMeta{"alamofire": {}},
		AliasToDependency:  map[string]string{},
		ModuleToDependency: map[string]string{},
		LocalModules:       map[string]struct{}{},
	}
	if got := resolveDependencyReference(catalog, " "); got != "" {
		t.Fatalf("expected empty dependency reference to stay unresolved, got %q", got)
	}
	if got := resolveDependencyReference(catalog, "missing"); got != "" {
		t.Fatalf("expected missing dependency reference to stay unresolved, got %q", got)
	}
	if shouldTrackUnresolvedImport("", catalog) {
		t.Fatalf("expected empty unresolved import to be ignored")
	}

	imports := parseSwiftImports([]byte("// comment only\nimport \n@testable import Alamofire // trailing comment\n"), "main.swift")
	if len(imports) != 1 || imports[0].Module != "Alamofire" {
		t.Fatalf("expected only valid Swift imports to parse, got %#v", imports)
	}

	symbols := collectLocalDeclaredSymbols([]byte("// struct Ignored {}\ntypealias Service = Int\n"))
	if _, ok := symbols[lookupKey("Service")]; !ok {
		t.Fatalf("expected uncommented typealias to be collected, got %#v", symbols)
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, ".build", "generated.swift"), "import Alamofire\n")
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect skipped-dir repo: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected Swift files in skipped dirs to be ignored, got %#v", detection)
	}
}

func TestSwiftMissingFileAndNoMatchBranches(t *testing.T) {
	repo := t.TempDir()
	catalog := dependencyCatalog{
		Dependencies:       make(map[string]dependencyMeta),
		AliasToDependency:  make(map[string]string),
		ModuleToDependency: make(map[string]string),
		LocalModules:       make(map[string]struct{}),
	}
	if found, warnings, err := loadManifestData(repo, &catalog); found || err != nil || len(warnings) != 0 {
		t.Fatalf("expected missing manifest to return not-found without warnings, got found=%v warnings=%#v err=%v", found, warnings, err)
	}
	if found, warnings, err := loadResolvedData(repo, &catalog); found || err != nil || len(warnings) != 0 {
		t.Fatalf("expected missing resolved file to return not-found without warnings, got found=%v warnings=%#v err=%v", found, warnings, err)
	}

	if fields := parseStringFields("dependencies: []"); len(fields) != 0 {
		t.Fatalf("expected no string fields to be parsed, got %#v", fields)
	}
	if imports := parseSwiftImports([]byte("let value = 1\nimport \n"), "main.swift"); len(imports) != 0 {
		t.Fatalf("expected invalid import lines to be ignored, got %#v", imports)
	}

	symbols := collectLocalDeclaredSymbols([]byte("// actor Ignored {}\nclass Visible {}\nenum Mode {}\n"))
	for _, symbol := range []string{"Visible", "Mode"} {
		if _, ok := symbols[lookupKey(symbol)]; !ok {
			t.Fatalf("expected %s to be collected, got %#v", symbol, symbols)
		}
	}
	if _, ok := symbols[lookupKey("Ignored")]; ok {
		t.Fatalf("expected commented declarations to be ignored, got %#v", symbols)
	}
}

func TestSwiftErrorPropagationBranches(t *testing.T) {
	t.Run("manifest read error bubbles through catalog and analyse", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, packageManifestName), 0o750); err != nil {
			t.Fatalf("mkdir manifest dir: %v", err)
		}
		if _, _, err := buildDependencyCatalog(repo); err == nil {
			t.Fatalf("expected manifest directory to fail catalog build")
		}
		if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 1}); err == nil {
			t.Fatalf("expected analyse to fail when manifest cannot be read")
		}
	})

	t.Run("resolved parse error bubbles through loader and catalog", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "import PackageDescription\n")
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), "{")

		catalog := dependencyCatalog{
			Dependencies:       make(map[string]dependencyMeta),
			AliasToDependency:  make(map[string]string),
			ModuleToDependency: make(map[string]string),
			LocalModules:       make(map[string]struct{}),
		}
		if _, _, err := loadResolvedData(repo, &catalog); err == nil {
			t.Fatalf("expected invalid resolved file to fail parsing")
		}
		if _, _, err := buildDependencyCatalog(repo); err == nil {
			t.Fatalf("expected catalog build to fail for invalid resolved file")
		}
	})
}
