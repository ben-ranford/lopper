package php

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestAdapterIdentityAndDetectWrapper(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "php" {
		t.Fatalf("unexpected id: %q", adapter.ID())
	}
	if !slices.Equal(adapter.Aliases(), []string{"php8", "php7"}) {
		t.Fatalf("unexpected aliases: %#v", adapter.Aliases())
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{"require":{"monolog/monolog":"^3.0"}}`)
	matched, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !matched {
		t.Fatalf("expected match")
	}
}

func TestResolveByNamespaceHeuristicAndNormalizePackagePart(t *testing.T) {
	resolver := dependencyResolver{declared: map[string]struct{}{"vendor/my-lib": {}}}
	if got := resolver.resolveByNamespaceHeuristic(`Vendor\MyLib\Client`); got != "vendor/my-lib" {
		t.Fatalf("unexpected heuristic dependency: %q", got)
	}
	if got := resolver.resolveByNamespaceHeuristic(`Vendor\Unknown\Thing`); got != "" {
		t.Fatalf("expected no match, got %q", got)
	}
	if got := normalizePackagePart("MyJSON_Lib"); got != "my-j-s-o-n-lib" {
		t.Fatalf("unexpected normalizePackagePart: %q", got)
	}
}

func TestReadComposerManifestBranches(t *testing.T) {
	repo := t.TempDir()
	manifest, ok, err := readComposerManifest(repo)
	if err != nil || ok || manifest.Name != "" {
		t.Fatalf("expected missing manifest branch, got ok=%v err=%v", ok, err)
	}

	writeFile(t, filepath.Join(repo, "composer.json"), `{"name":"acme/app","require":{"monolog/monolog":"^3.0"}}`)
	manifest, ok, err = readComposerManifest(repo)
	if err != nil || !ok {
		t.Fatalf("expected manifest parse success, ok=%v err=%v", ok, err)
	}
	if manifest.Name != "acme/app" {
		t.Fatalf("unexpected manifest name: %q", manifest.Name)
	}

	writeFile(t, filepath.Join(repo, "composer.json"), `{not-json`)
	_, _, err = readComposerManifest(repo)
	if err == nil || !strings.Contains(err.Error(), "parse composer.json") {
		t.Fatalf("expected parse error branch, got %v", err)
	}
}

func TestLoadComposerLockMappingsBranches(t *testing.T) {
	repo := t.TempDir()
	data := composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(repo, &data); err != nil {
		t.Fatalf("expected missing lock branch without error, got %v", err)
	}

	writeFile(t, filepath.Join(repo, "composer.lock"), `{bad-json`)
	if err := loadComposerLockMappings(repo, &data); err == nil || !strings.Contains(err.Error(), "parse composer.lock") {
		t.Fatalf("expected lock parse error, got %v", err)
	}

	writeFile(t, filepath.Join(repo, "composer.lock"), `{
  "packages": [
    {"name":"monolog/monolog","autoload":{"psr-4":{"Monolog\\":"src/Monolog"}}}
  ],
  "packages-dev": [
    {"name":"phpunit/phpunit","autoload":{"psr-4":{"PHPUnit\\Framework\\":"src"}}}
  ]
}`)
	data = composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(repo, &data); err != nil {
		t.Fatalf("load mappings: %v", err)
	}
	if data.NamespaceToDep["Monolog"] != "monolog/monolog" {
		t.Fatalf("expected Monolog mapping, got %#v", data.NamespaceToDep)
	}
	if data.NamespaceToDep["PHPUnit\\Framework"] != "phpunit/phpunit" {
		t.Fatalf("expected PHPUnit mapping, got %#v", data.NamespaceToDep)
	}
}

func TestLoadComposerDataAndLocalNamespaces(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{
  "require":{"php":"^8.2","ext-json":"*","vendor/lib":"^1.0"},
  "require-dev":{"vendor/dev-tool":"^1.0"},
  "autoload":{"psr-4":{"App\\":"src/"}},
  "autoload-dev":{"psr-4":{"Tests\\":"tests/"}}
}`)
	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("load data: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if _, ok := data.DeclaredDependencies["vendor/lib"]; !ok {
		t.Fatalf("missing vendor/lib in declared deps")
	}
	if _, ok := data.DeclaredDependencies["vendor/dev-tool"]; !ok {
		t.Fatalf("missing vendor/dev-tool in declared deps")
	}
	if _, ok := data.DeclaredDependencies["php"]; ok {
		t.Fatalf("did not expect php pseudo dependency")
	}
	if _, ok := data.LocalNamespaces["App"]; !ok {
		t.Fatalf("missing App local namespace")
	}
	if _, ok := data.LocalNamespaces["Tests"]; !ok {
		t.Fatalf("missing Tests local namespace")
	}
}

func TestNamespaceAndUseHelpers(t *testing.T) {
	resolver := dependencyResolver{
		namespaceToDep: map[string]string{"Monolog": "monolog/monolog"},
		declared:       map[string]struct{}{"monolog/monolog": {}},
	}
	imports, _, unresolved := parseImports([]byte("<?php\nuse Monolog\\Logger as Log;\n$logger = new \\Monolog\\Logger('x');\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf("unexpected unresolved count: %d", unresolved)
	}
	if len(imports) == 0 {
		t.Fatalf("expected imports from use+namespace refs")
	}

	line := lineTextAt("a\nb\n", 2)
	if line != "b" {
		t.Fatalf("unexpected lineTextAt result: %q", line)
	}
	if got := lineTextAt("a", 9); got != "" {
		t.Fatalf("expected out-of-range lineTextAt to be empty, got %q", got)
	}

	module, local := splitAlias("Monolog\\Logger as Log")
	if module != "Monolog\\Logger" || local != "Log" {
		t.Fatalf("unexpected splitAlias result: module=%q local=%q", module, local)
	}
	if got := lastNamespaceSegment("Monolog\\Logger"); got != "Logger" {
		t.Fatalf("unexpected last segment: %q", got)
	}
}

func TestReadPHPFileAndScanNoPHP(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{"require":{"vendor/lib":"^1.0"}}`)
	_, rel, err := readPHPFile(repo, filepath.Join(repo, "composer.json"))
	if err != nil {
		t.Fatalf("readPHPFile: %v", err)
	}
	if rel != "composer.json" {
		t.Fatalf("unexpected rel path: %q", rel)
	}

	scan, err := scanRepo(context.Background(), repo, composerData{DeclaredDependencies: map[string]struct{}{"vendor/lib": {}}})
	if err != nil {
		t.Fatalf("scanRepo: %v", err)
	}
	if len(scan.Files) != 0 {
		t.Fatalf("expected no php files, got %d", len(scan.Files))
	}
	if !containsWarning(scan.Warnings, "no PHP source files") {
		t.Fatalf("expected no-PHP warning, got %#v", scan.Warnings)
	}
}

func TestShouldSkipDirAndDependencyHelpers(t *testing.T) {
	if !shouldSkipDir("vendor") {
		t.Fatalf("expected vendor to be skipped")
	}
	if shouldSkipDir("src") {
		t.Fatalf("did not expect src to be skipped")
	}
	if dep, ok := normalizeComposerDependency("ext-json"); ok || dep != "" {
		t.Fatalf("ext-json should be ignored")
	}
	if dep, ok := normalizeComposerDependency("vendor/lib"); !ok || dep != "vendor/lib" {
		t.Fatalf("vendor/lib should be accepted, dep=%q ok=%v", dep, ok)
	}
}

func TestDetectWithConfidenceEmptyRepoPathAndFileError(t *testing.T) {
	adapter := NewAdapter()
	detection, err := adapter.DetectWithConfidence(context.Background(), "")
	if err != nil {
		t.Fatalf("detect empty repo path: %v", err)
	}
	if detection.Confidence < 0 {
		t.Fatalf("unexpected confidence: %d", detection.Confidence)
	}

	repo := t.TempDir()
	repoFile := filepath.Join(repo, "composer.json")
	writeFile(t, repoFile, `{"require":{"vendor/lib":"^1.0"}}`)
	if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected walk error when repoPath is a file")
	}
}

func TestDependenciesInFileAndAllDependencies(t *testing.T) {
	scan := scanResult{
		DeclaredDependencies: map[string]struct{}{"vendor/lib": {}},
		Files: []fileScan{{
			Imports: []importBinding{{Dependency: "vendor/tool"}},
		}},
	}
	deps := allDependencies(scan)
	if !slices.Equal(deps, []string{"vendor/lib", "vendor/tool"}) {
		t.Fatalf("unexpected deps: %#v", deps)
	}
	inFile := dependenciesInFile([]importBinding{{Dependency: "A/B"}, {Dependency: "a/b"}, {Dependency: ""}})
	if len(inFile) != 1 {
		t.Fatalf("expected deduped deps in file, got %#v", inFile)
	}
}

func TestHasComposerManifest(t *testing.T) {
	d := t.TempDir()
	if hasComposerManifest(d) {
		t.Fatalf("did not expect manifest")
	}
	writeFile(t, filepath.Join(d, "composer.json"), "{}")
	if !hasComposerManifest(d) {
		t.Fatalf("expected manifest")
	}
}

func TestScanRepoContextCanceled(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{"require":{"vendor/lib":"^1.0"}}`)
	writeFile(t, filepath.Join(repo, "src", "x.php"), "<?php\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanRepo(ctx, repo, composerData{DeclaredDependencies: map[string]struct{}{}}); err == nil {
		t.Fatalf("expected canceled scan error")
	}
}

func TestReadPHPFileMissingReturnsError(t *testing.T) {
	repo := t.TempDir()
	if _, _, err := readPHPFile(repo, filepath.Join(repo, "missing.php")); err == nil {
		t.Fatalf("expected missing file error")
	}
}

func TestResolveWithPSR4LongestPrefix(t *testing.T) {
	resolver := dependencyResolver{namespaceToDep: map[string]string{
		"Symfony":              "symfony/symfony",
		"Symfony\\Component\\": "symfony/component",
	}}
	if got := resolver.resolveWithPSR4("Symfony\\Component\\Yaml\\Yaml"); got != "symfony/component" {
		t.Fatalf("expected longest prefix match, got %q", got)
	}
}

func TestLineNumberAtBoundaries(t *testing.T) {
	if got := lineNumberAt("a\nb\n", 0); got != 1 {
		t.Fatalf("expected line 1 at offset 0, got %d", got)
	}
	if got := lineNumberAt("a\nb\n", 3); got != 2 {
		t.Fatalf("expected line 2 at offset 3, got %d", got)
	}
}

func TestLoadComposerDataMissingManifestWarning(t *testing.T) {
	repo := t.TempDir()
	_, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("loadComposerData: %v", err)
	}
	if !containsWarning(warnings, "composer.json not found") {
		t.Fatalf("expected missing manifest warning, got %#v", warnings)
	}
}

func TestNormalizeNamespace(t *testing.T) {
	if got := normalizeNamespace(`\Monolog\Logger\`); got != "Monolog\\Logger" {
		t.Fatalf("unexpected normalizeNamespace: %q", got)
	}
}

func TestParseUseStatementFunctionAndConstImports(t *testing.T) {
	resolver := dependencyResolver{declared: map[string]struct{}{"vendor/lib": {}}}
	resolver.namespaceToDep = map[string]string{"Vendor\\Lib": "vendor/lib"}
	imports, _, unresolved := parseUseStatement("function Vendor\\Lib\\helper, const Vendor\\Lib\\VERSION", "x.php", 1, resolver)
	if unresolved != 0 {
		t.Fatalf("unexpected unresolved: %d", unresolved)
	}
	if len(imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(imports))
	}
}

func TestParseNamespaceReferencesSkipsUseLine(t *testing.T) {
	resolver := dependencyResolver{namespaceToDep: map[string]string{"Monolog": "monolog/monolog"}}
	imports, unresolved := parseNamespaceReferences([]byte("<?php\nuse Monolog\\Logger;\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf("unexpected unresolved: %d", unresolved)
	}
	if len(imports) != 0 {
		t.Fatalf("expected no namespace imports from use-line, got %#v", imports)
	}
}

func TestDependencyFromModuleBranches(t *testing.T) {
	resolver := dependencyResolver{
		namespaceToDep: map[string]string{"Monolog": "monolog/monolog"},
		localNamespace: map[string]struct{}{"App": {}},
		declared:       map[string]struct{}{"vendor/pkg": {}},
	}
	if dep, resolved := resolver.dependencyFromModule(""); dep != "" || resolved {
		t.Fatalf("expected empty module branch, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`App\Thing`); dep != "" || resolved {
		t.Fatalf("expected local namespace to be excluded, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`Monolog\Logger`); dep != "monolog/monolog" || !resolved {
		t.Fatalf("expected psr-4 dependency, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`Vendor\Pkg\Client`); dep != "vendor/pkg" || !resolved {
		t.Fatalf("expected heuristic dependency, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`Unknown\Pkg\Client`); dep != "" || !resolved {
		t.Fatalf("expected unresolved namespace branch, got dep=%q resolved=%v", dep, resolved)
	}
}

func TestParseNamespaceReferencesUnresolvedBranch(t *testing.T) {
	resolver := dependencyResolver{
		namespaceToDep: map[string]string{},
		declared:       map[string]struct{}{},
	}
	imports, unresolved := parseNamespaceReferences([]byte("<?php\n$foo = new \\Unknown\\Pkg\\Thing();\n"), "x.php", resolver)
	if len(imports) != 0 {
		t.Fatalf("expected no imports, got %#v", imports)
	}
	if unresolved == 0 {
		t.Fatalf("expected unresolved namespace count > 0")
	}
}

func TestBuildRequestedPHPDependenciesDefaultBranch(t *testing.T) {
	deps, warnings := buildRequestedPHPDependencies(language.Request{}, scanResult{})
	if len(deps) != 0 {
		t.Fatalf("expected no deps, got %#v", deps)
	}
	if !containsWarning(warnings, "no dependency or top-N target provided") {
		t.Fatalf("expected missing-target warning, got %#v", warnings)
	}
}

func TestResolveMinUsageRecommendationThreshold(t *testing.T) {
	if got := resolveMinUsageRecommendationThreshold(nil); got <= 0 {
		t.Fatalf("expected default positive threshold, got %d", got)
	}
	value := 7
	if got := resolveMinUsageRecommendationThreshold(&value); got != 7 {
		t.Fatalf("expected explicit threshold, got %d", got)
	}
}

func TestAnalyseErrorBranches(t *testing.T) {
	adapter := NewAdapter()

	repoBadManifest := t.TempDir()
	writeFile(t, filepath.Join(repoBadManifest, "composer.json"), `{bad-json`)
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repoBadManifest, TopN: 1}); err == nil {
		t.Fatalf("expected parse error from composer.json")
	}

	repoBadLock := t.TempDir()
	writeFile(t, filepath.Join(repoBadLock, "composer.json"), `{"require":{"vendor/lib":"^1.0"}}`)
	writeFile(t, filepath.Join(repoBadLock, "composer.lock"), `{bad-json`)
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repoBadLock, TopN: 1}); err == nil {
		t.Fatalf("expected parse error from composer.lock")
	}

	repoCanceled := t.TempDir()
	writeFile(t, filepath.Join(repoCanceled, "composer.json"), `{"require":{"vendor/lib":"^1.0"}}`)
	writeFile(t, filepath.Join(repoCanceled, "src", "x.php"), "<?php\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Analyse(ctx, language.Request{RepoPath: repoCanceled, TopN: 1}); err == nil {
		t.Fatalf("expected canceled analysis")
	}
}

func TestDetectWithConfidenceCanceledContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{"require":{"vendor/lib":"^1.0"}}`)
	writeFile(t, filepath.Join(repo, "src", "x.php"), "<?php\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(ctx, repo); err == nil {
		t.Fatalf("expected canceled detection")
	}
}

func TestParseUseStatementAndPartEdgeBranches(t *testing.T) {
	resolver := dependencyResolver{declared: map[string]struct{}{}, namespaceToDep: map[string]string{}}

	imports, grouped, unresolved := parseUseStatement("", "x.php", 1, resolver)
	if imports != nil || grouped != nil || unresolved != 0 {
		t.Fatalf("expected empty statement branch, got imports=%#v grouped=%#v unresolved=%d", imports, grouped, unresolved)
	}

	imp, dep, ok, unresolvedImport := parseUsePart("", "", "x.php", 1, resolver)
	if ok || unresolvedImport || dep != "" || imp.Dependency != "" {
		t.Fatalf("expected empty use part to be ignored")
	}

	imp, dep, ok, unresolvedImport = parseUsePart(`Unknown\Pkg\Thing`, "", "x.php", 1, resolver)
	if ok || dep != "" || !unresolvedImport || imp.Dependency != "" {
		t.Fatalf("expected unresolved import branch, got ok=%v dep=%q unresolved=%v", ok, dep, unresolvedImport)
	}
}

func TestLineTextAtNonPositive(t *testing.T) {
	if got := lineTextAt("abc", 0); got != "" {
		t.Fatalf("expected empty for non-positive target line, got %q", got)
	}
}

func TestScanRepoNoDeclaredDependencyWarningAndUnresolvedWarning(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", "x.php"), "<?php\n$foo = new \\Unknown\\Pkg\\Thing();\n")
	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{},
		NamespaceToDep:       map[string]string{},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf("scanRepo: %v", err)
	}
	if !containsWarning(scan.Warnings, "no Composer dependencies discovered") {
		t.Fatalf("expected no-composer-dependency warning, got %#v", scan.Warnings)
	}
	if !containsWarning(scan.Warnings, "unable to map") {
		t.Fatalf("expected unresolved namespace warning, got %#v", scan.Warnings)
	}
}

func TestReadComposerManifestAndLockMappingsErrorFromFileRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root-file")
	writeFile(t, root, "x")
	if _, _, err := readComposerManifest(root); err == nil {
		t.Fatalf("expected readComposerManifest non-not-exist error for file root")
	}
	data := composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(root, &data); err == nil {
		t.Fatalf("expected loadComposerLockMappings non-not-exist error for file root")
	}
}

func TestResolveByNamespaceHeuristicTooShort(t *testing.T) {
	resolver := dependencyResolver{declared: map[string]struct{}{"vendor/pkg": {}}}
	if got := resolver.resolveByNamespaceHeuristic("Vendor"); got != "" {
		t.Fatalf("expected empty heuristic for short namespace, got %q", got)
	}
}

func TestAdditionalBranchCoverageHelpers(t *testing.T) {
	if got := normalizePackagePart(""); got != "" {
		t.Fatalf("expected empty normalizePackagePart for empty input, got %q", got)
	}
	if got := lastNamespaceSegment(`\`); got != "" {
		t.Fatalf("expected empty last namespace segment for root slash, got %q", got)
	}

	adapter := NewAdapter()
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: string([]byte{'b', 'a', 'd', 0x00}), TopN: 1}); err == nil {
		t.Fatalf("expected invalid repo path error")
	}

	deps, warnings := buildTopPHPDependencies(10, scanResult{}, 40)
	if len(deps) != 0 || !containsWarning(warnings, "no dependency data available for top-N ranking") {
		t.Fatalf("expected empty top-n warning, deps=%#v warnings=%#v", deps, warnings)
	}

	scan := scanResult{
		DeclaredDependencies: map[string]struct{}{"a/pkg": {}, "b/pkg": {}},
		Files: []fileScan{
			{Imports: []importBinding{{Dependency: "a/pkg", Name: "A", Local: "A", Module: "A"}}},
			{Imports: []importBinding{{Dependency: "b/pkg", Name: "B", Local: "B", Module: "B"}}},
		},
	}
	top, _ := buildTopPHPDependencies(1, scan, 40)
	if len(top) != 1 {
		t.Fatalf("expected top-n truncation to one dependency, got %d", len(top))
	}

	dep := report.DependencyReport{
		Name:          "x/pkg",
		UsedImports:   nil,
		UnusedImports: []report.ImportUse{{Name: "Thing", Module: "X\\Thing"}},
	}
	recs := buildRecommendations(dep, 40)
	if len(recs) == 0 {
		t.Fatalf("expected remove-unused recommendation")
	}

	resolver := dependencyResolver{
		localNamespace: map[string]struct{}{"": {}, "App": {}},
		namespaceToDep: map[string]string{"": "empty/dep", "Monolog": "monolog/monolog"},
	}
	if !resolver.isLocalNamespace(`App\Svc`) {
		t.Fatalf("expected local namespace match")
	}
	if got := resolver.resolveWithPSR4(`Monolog\Logger`); got != "monolog/monolog" {
		t.Fatalf("expected psr4 match, got %q", got)
	}
	if got := resolver.resolveByNamespaceHeuristic(`\Thing`); got != "" {
		t.Fatalf("expected empty heuristic for blank vendor, got %q", got)
	}

	unknownResolver := dependencyResolver{declared: map[string]struct{}{}, namespaceToDep: map[string]string{}}
	imports, _, unresolved := parseUseStatement(`Unknown\Pkg\Thing`, "x.php", 1, unknownResolver)
	if len(imports) != 0 || unresolved == 0 {
		t.Fatalf("expected unresolved non-grouped use statement branch, imports=%#v unresolved=%d", imports, unresolved)
	}
	imports, _, unresolved = parseUseStatement(`Unknown\Pkg\{Thing}`, "x.php", 1, unknownResolver)
	if len(imports) != 0 || unresolved == 0 {
		t.Fatalf("expected unresolved grouped use statement branch, imports=%#v unresolved=%d", imports, unresolved)
	}

	knownResolver := dependencyResolver{namespaceToDep: map[string]string{"Foo\\Bar": "foo/bar"}}
	imports, unresolved = parseNamespaceReferences([]byte("<?php\n\\Foo\\Bar; \\Foo\\Bar;\n"), "x.php", knownResolver)
	if unresolved != 0 || len(imports) != 1 {
		t.Fatalf("expected duplicate namespace refs to de-dup, imports=%#v unresolved=%d", imports, unresolved)
	}
}

func TestScanRepoMaxFilesAndSkipDirBranches(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{"require":{"vendor/lib":"^1.0"}}`)
	writeFile(t, filepath.Join(repo, "vendor", "x.php"), "<?php\n")
	for i := 0; i < maxScanFiles+1; i++ {
		writeFile(t, filepath.Join(repo, "src", fmt.Sprintf("f-%04d.txt", i)), "x")
	}
	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{"vendor/lib": {}},
		NamespaceToDep:       map[string]string{},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf("scanRepo: %v", err)
	}
	if !containsWarning(scan.Warnings, "scan stopped after") {
		t.Fatalf("expected bounded scan warning, got %#v", scan.Warnings)
	}
}

func TestLoadComposerLockMappingsSkipsInvalidEntries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.lock"), `{
  "packages": [
    {"name":"", "autoload":{"psr-4":{"\\\\":"src"}}},
    {"name":"vendor/pkg", "autoload":{"psr-4":{"\\\\":"src","Vendor\\\\Pkg\\\\":"src"}}}
  ]
}`)
	data := composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(repo, &data); err != nil {
		t.Fatalf("loadComposerLockMappings: %v", err)
	}
	if _, ok := data.NamespaceToDep[""]; ok {
		t.Fatalf("did not expect empty namespace key in mappings")
	}
	found := false
	for namespace, dep := range data.NamespaceToDep {
		if strings.Contains(namespace, "Vendor") && dep == "vendor/pkg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected valid namespace mapping, got %#v", data.NamespaceToDep)
	}
}
