package php

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const helpersComposerJSON = "composer.json"
const helpersComposerLock = "composer.lock"
const helpersMonologDependency = "monolog/monolog"
const helpersVendorLibDependency = "vendor/lib"
const helpersVendorPkgDependency = "vendor/pkg"
const helpersABLines = "a\nb\n"
const helpersMonologLogger = "Monolog\\Logger"
const helpersScanRepoErr = "scanRepo: %v"
const helpersUnexpectedUnresolvedFmt = "unexpected unresolved: %d"
const helpersPHPHeader = "<?php\n"

func TestAdapterIdentityAndDetectWrapper(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "php" {
		t.Fatalf("unexpected id: %q", adapter.ID())
	}
	if !slices.Equal(adapter.Aliases(), []string{"php8", "php7"}) {
		t.Fatalf("unexpected aliases: %#v", adapter.Aliases())
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^3.0"}}`, helpersMonologDependency))
	matched, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !matched {
		t.Fatalf("expected match")
	}
}

func TestResolveByNamespaceHeuristicAndNormalizePackagePart(t *testing.T) {
	resolver := composerResolver{declared: map[string]struct{}{"vendor/my-lib": {}}}
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

	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"name":"acme/app","require":{%q:"^3.0"}}`, helpersMonologDependency))
	manifest, ok, err = readComposerManifest(repo)
	if err != nil || !ok {
		t.Fatalf("expected manifest parse success, ok=%v err=%v", ok, err)
	}
	if manifest.Name != "acme/app" {
		t.Fatalf("unexpected manifest name: %q", manifest.Name)
	}

	writeFile(t, filepath.Join(repo, helpersComposerJSON), `{not-json`)
	_, _, err = readComposerManifest(repo)
	if err == nil || !strings.Contains(err.Error(), "parse composer.json") {
		t.Fatalf("expected parse error branch, got %v", err)
	}
}

func TestReadComposerInputsRejectOversizedFiles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
		limit    int64
		read     func(string) error
	}{
		{
			name:     "composer manifest",
			filename: helpersComposerJSON,
			limit:    maxComposerManifestBytes,
			read: func(repo string) error {
				_, _, err := readComposerManifest(repo)
				return err
			},
		},
		{
			name:     "composer lock",
			filename: helpersComposerLock,
			limit:    maxComposerLockBytes,
			read: func(repo string) error {
				data := composerData{NamespaceToDep: map[string]string{}}
				return loadComposerLockMappings(repo, &data)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWritePaddedFile(t, filepath.Join(repo, tc.filename), "{}", tc.limit+1)

			if err := tc.read(repo); !errors.Is(err, safeio.ErrFileTooLarge) {
				t.Fatalf("expected oversized %s to fail with ErrFileTooLarge, got %v", tc.filename, err)
			}
		})
	}
}

func TestPureOversizedFileErrorPreservesJoinedOperationalErrors(t *testing.T) {
	if !isPureOversizedFileError(safeio.ErrFileTooLarge) {
		t.Fatalf("expected direct oversized sentinel to be classified as skippable")
	}
	if !isPureOversizedFileError(fmt.Errorf("wrapped: %w", safeio.ErrFileTooLarge)) {
		t.Fatalf("expected wrapped oversized sentinel to be classified as skippable")
	}
	closeErr := errors.New("close failed")
	if isPureOversizedFileError(errors.Join(safeio.ErrFileTooLarge, closeErr)) {
		t.Fatalf("expected joined operational error to be preserved")
	}
}

func TestReadComposerInputsAcceptExactLimitFiles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
		limit    int64
	}{
		{name: "composer manifest", filename: helpersComposerJSON, limit: maxComposerManifestBytes},
		{name: "composer lock", filename: helpersComposerLock, limit: maxComposerLockBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWritePaddedFile(t, filepath.Join(repo, tc.filename), "{}", tc.limit)

			bytes, found, err := readOptionalRepoFile(repo, tc.filename)
			if err != nil {
				t.Fatalf("read exact-limit %s: %v", tc.filename, err)
			}
			if !found {
				t.Fatalf("expected exact-limit %s to be found", tc.filename)
			}
			if int64(len(bytes)) != tc.limit {
				t.Fatalf("expected exact-limit %s read to return %d bytes, got %d", tc.filename, tc.limit, len(bytes))
			}
		})
	}
}

func TestLoadComposerLockMappingsBranches(t *testing.T) {
	repo := t.TempDir()
	data := composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(repo, &data); err != nil {
		t.Fatalf("expected missing lock branch without error, got %v", err)
	}

	writeFile(t, filepath.Join(repo, helpersComposerLock), `{bad-json`)
	if err := loadComposerLockMappings(repo, &data); err == nil || !strings.Contains(err.Error(), "parse composer.lock") {
		t.Fatalf("expected lock parse error, got %v", err)
	}

	lockTemplate := `{
  "packages": [
    {"name":%q,"autoload":{"psr-4":{"Monolog\\":"src/Monolog"}}}
  ],
  "packages-dev": [
    {"name":"phpunit/phpunit","autoload":{"psr-4":{"PHPUnit\\Framework\\":"src"}}}
  ]
}`
	lockContent := fmt.Sprintf(lockTemplate, helpersMonologDependency)
	writeFile(t, filepath.Join(repo, helpersComposerLock), lockContent)
	data = composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(repo, &data); err != nil {
		t.Fatalf("load mappings: %v", err)
	}
	if data.NamespaceToDep["Monolog"] != helpersMonologDependency {
		t.Fatalf("expected Monolog mapping, got %#v", data.NamespaceToDep)
	}
	if data.NamespaceToDep["PHPUnit\\Framework"] != "phpunit/phpunit" {
		t.Fatalf("expected PHPUnit mapping, got %#v", data.NamespaceToDep)
	}
}

func TestLoadComposerDataAndLocalNamespaces(t *testing.T) {
	repo := t.TempDir()
	composerTemplate := `{
  "require":{"php":"^8.2","ext-json":"*",%q:"^1.0"},
  "require-dev":{"vendor/dev-tool":"^1.0"},
  "autoload":{"psr-4":{"App\\":"src/"}},
  "autoload-dev":{"psr-4":{"Tests\\":"tests/"}}
}`
	composerContent := fmt.Sprintf(composerTemplate, helpersVendorLibDependency)
	writeFile(t, filepath.Join(repo, helpersComposerJSON), composerContent)
	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("load data: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if _, ok := data.DeclaredDependencies[helpersVendorLibDependency]; !ok {
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

func TestLoadComposerDataWarnsAndContinuesWhenLockIsOversized(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	testutil.MustWritePaddedFile(t, filepath.Join(repo, helpersComposerLock), "{}", maxComposerLockBytes+1)

	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("expected oversized optional composer.lock to warn and continue, got %v", err)
	}
	if _, ok := data.DeclaredDependencies[helpersVendorLibDependency]; !ok {
		t.Fatalf("expected manifest dependency to be retained, got %#v", data.DeclaredDependencies)
	}
	if !data.UsageIncomplete {
		t.Fatal("expected oversized optional composer.lock to mark dependency coverage incomplete")
	}
	if !containsWarning(warnings, "skipped composer.lock because it exceeds") {
		t.Fatalf("expected oversized lock warning, got %#v", warnings)
	}
}

func TestNamespaceAndUseHelpers(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Monolog": helpersMonologDependency},
		declared:       map[string]struct{}{helpersMonologDependency: {}},
	}
	imports, _, unresolved := parseImports([]byte(helpersPHPHeader+"use Monolog\\Logger as Log;\n$logger = new \\Monolog\\Logger('x');\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf("unexpected unresolved count: %d", unresolved)
	}
	if len(imports) == 0 {
		t.Fatalf("expected imports from use+namespace refs")
	}

	line := lineTextAt(helpersABLines, 2)
	if line != "b" {
		t.Fatalf("unexpected lineTextAt result: %q", line)
	}
	if got := lineTextAt("a", 9); got != "" {
		t.Fatalf("expected out-of-range lineTextAt to be empty, got %q", got)
	}

	module, local := splitAlias(helpersMonologLogger + " as Log")
	if module != helpersMonologLogger || local != "Log" {
		t.Fatalf("unexpected splitAlias result: module=%q local=%q", module, local)
	}
	if got := lastNamespaceSegment(helpersMonologLogger); got != "Logger" {
		t.Fatalf("unexpected last segment: %q", got)
	}
}

func TestParsePHPImportsStructuredResult(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Monolog": helpersMonologDependency},
		declared:       map[string]struct{}{helpersMonologDependency: {}},
	}
	content := []byte(helpersPHPHeader + "use Monolog\\{Logger, Handler\\StreamHandler};\n$logger = new \\Monolog\\Logger('x');\n")

	parsed := parsePHPImports(content, "x.php", resolver)

	if parsed.unresolvedCount != 0 {
		t.Fatalf("unexpected unresolved count: %d", parsed.unresolvedCount)
	}
	if parsed.groupedByDep[helpersMonologDependency] != 1 {
		t.Fatalf("expected grouped import count for dependency, got %#v", parsed.groupedByDep)
	}
	if len(parsed.imports) != 3 {
		t.Fatalf("expected grouped imports plus namespace reference, got %#v", parsed.imports)
	}
}

func TestParsePHPImportsBoundsAdversarialUseStatements(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < maxPHPUseStatementsPerFile+17; i++ {
		fmt.Fprintf(&content, "use Vendor\\Lib\\Thing%d;\n", i)
	}

	parsed := parsePHPImports([]byte(content.String()), "adversarial-use.php", resolver)

	if !parsed.useStatementLimitHit {
		t.Fatalf("expected use statement limit to be reported")
	}
	if len(parsed.imports) != maxPHPUseStatementsPerFile {
		t.Fatalf("expected exactly %d bounded use imports, got %d", maxPHPUseStatementsPerFile, len(parsed.imports))
	}
	if parsed.imports[len(parsed.imports)-1].Location.Line != maxPHPUseStatementsPerFile+1 {
		t.Fatalf("expected last bounded use import on line %d, got %#v", maxPHPUseStatementsPerFile+1, parsed.imports[len(parsed.imports)-1])
	}
	for _, imp := range parsed.imports {
		if imp.Wildcard {
			t.Fatalf("did not expect namespace-reference import while parsing use-statement adversary, got %#v", imp)
		}
	}
}

func TestParsePHPImportsBoundsGroupedUseBindings(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	content.WriteString("use Vendor\\Lib\\{")
	for i := 0; i < maxPHPUseStatementsPerFile+17; i++ {
		if i > 0 {
			content.WriteString(", ")
		}
		fmt.Fprintf(&content, "Thing%d", i)
	}
	content.WriteString("};\n")

	parsed := parsePHPImports([]byte(content.String()), "adversarial-grouped-use.php", resolver)

	if !parsed.useBindingLimitHit {
		t.Fatalf("expected grouped use binding limit to be reported")
	}
	if len(parsed.imports) != maxPHPUseStatementsPerFile {
		t.Fatalf("expected exactly %d bounded grouped use imports, got %d", maxPHPUseStatementsPerFile, len(parsed.imports))
	}
	if parsed.groupedByDep[helpersVendorLibDependency] != 1 {
		t.Fatalf("expected grouped dependency attribution, got %#v", parsed.groupedByDep)
	}
}

func TestParsePHPImportsBoundsUnresolvedGroupedUseParts(t *testing.T) {
	resolver := composerResolver{declared: map[string]struct{}{}}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for stmt := 0; stmt < 2; stmt++ {
		content.WriteString("use Vendor\\Lib\\{")
		for i := 0; i < maxPHPUseStatementsPerFile; i++ {
			if i > 0 {
				content.WriteString(", ")
			}
			fmt.Fprintf(&content, "Thing%d", i)
		}
		content.WriteString("};\n")
	}

	parsed := parsePHPImports([]byte(content.String()), "adversarial-unresolved-grouped-use.php", resolver)

	if !parsed.useBindingLimitHit {
		t.Fatal("expected unresolved grouped use parts to consume the binding budget")
	}
	if len(parsed.imports) != 0 {
		t.Fatalf("expected unresolved grouped use parts to emit no imports, got %d", len(parsed.imports))
	}
	if parsed.unresolvedCount != maxPHPUseStatementsPerFile {
		t.Fatalf("expected unresolved count to stop at %d, got %d", maxPHPUseStatementsPerFile, parsed.unresolvedCount)
	}
}

func TestParsePHPImportsBoundsAdversarialNamespaceReferences(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < maxPHPNamespaceReferencesPerFile+23; i++ {
		content.WriteString("$client = new \\Vendor\\Lib\\Client();\n")
	}

	parsed := parsePHPImports([]byte(content.String()), "adversarial-namespace.php", resolver)

	if parsed.useStatementLimitHit {
		t.Fatalf("did not expect use statement limit for namespace-only adversary")
	}
	if !parsed.namespaceReferenceLimitHit {
		t.Fatalf("expected namespace reference limit to be reported")
	}
	if len(parsed.imports) != maxPHPNamespaceReferencesPerFile {
		t.Fatalf("expected exactly %d bounded namespace imports, got %d", maxPHPNamespaceReferencesPerFile, len(parsed.imports))
	}
	if parsed.imports[0].Location.Line != 2 {
		t.Fatalf("expected first namespace import on line 2, got %#v", parsed.imports[0])
	}
	if parsed.imports[len(parsed.imports)-1].Location.Line != maxPHPNamespaceReferencesPerFile+1 {
		t.Fatalf("expected last bounded namespace import on line %d, got %#v", maxPHPNamespaceReferencesPerFile+1, parsed.imports[len(parsed.imports)-1])
	}
}

func TestScanRepoWarnsWhenPHPImportScansAreBounded(t *testing.T) {
	repo := t.TempDir()
	var useContent strings.Builder
	useContent.WriteString(helpersPHPHeader)
	for i := 0; i < maxPHPUseStatementsPerFile+1; i++ {
		fmt.Fprintf(&useContent, "use Vendor\\Lib\\UseThing%d;\n", i)
	}
	writeFile(t, filepath.Join(repo, "src", "use-adversary.php"), useContent.String())

	var namespaceContent strings.Builder
	namespaceContent.WriteString(helpersPHPHeader)
	for i := 0; i < maxPHPNamespaceReferencesPerFile+1; i++ {
		namespaceContent.WriteString("$client = new \\Vendor\\Lib\\Client();\n")
	}
	writeFile(t, filepath.Join(repo, "src", "namespace-adversary.php"), namespaceContent.String())

	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		NamespaceToDep:       map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if !containsWarning(scan.Warnings, "stopped PHP use import scan") {
		t.Fatalf("expected bounded use import warning, got %#v", scan.Warnings)
	}
	if !containsWarning(scan.Warnings, "stopped PHP namespace reference scan") {
		t.Fatalf("expected bounded namespace reference warning, got %#v", scan.Warnings)
	}
}

func TestReadPHPFileAndScanNoPHP(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	_, rel, err := readPHPFile(repo, filepath.Join(repo, helpersComposerJSON))
	if err != nil {
		t.Fatalf("readPHPFile: %v", err)
	}
	if rel != helpersComposerJSON {
		t.Fatalf("unexpected rel path: %q", rel)
	}

	scan, err := scanRepo(context.Background(), repo, composerData{DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}}})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if len(scan.Files) != 0 {
		t.Fatalf("expected no php files, got %d", len(scan.Files))
	}
	if !containsWarning(scan.Warnings, "no PHP source files") {
		t.Fatalf("expected no-PHP warning, got %#v", scan.Warnings)
	}
}

func TestReadPHPFileRejectsOversizedSource(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", "oversized.php")
	testutil.MustWritePaddedFile(t, sourcePath, helpersPHPHeader, maxScannablePHPFile+1)

	if _, _, err := readPHPFile(repo, sourcePath); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized PHP source to fail with ErrFileTooLarge, got %v", err)
	}
}

func TestReadPHPFileAcceptsExactLimitSource(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", "exact.php")
	testutil.MustWritePaddedFile(t, sourcePath, helpersPHPHeader, maxScannablePHPFile)

	content, relPath, err := readPHPFile(repo, sourcePath)
	if err != nil {
		t.Fatalf("read exact-limit PHP source: %v", err)
	}
	if relPath != filepath.Join("src", "exact.php") {
		t.Fatalf("unexpected rel path: %q", relPath)
	}
	if int64(len(content)) != maxScannablePHPFile {
		t.Fatalf("expected exact-limit PHP source read to return %d bytes, got %d", maxScannablePHPFile, len(content))
	}
}

func TestScanRepoSkipsOversizedPHPSourceWithWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWritePaddedFile(t, filepath.Join(repo, "src", "oversized.php"), helpersPHPHeader, maxScannablePHPFile+1)

	scan, err := scanRepo(context.Background(), repo, composerData{DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}}})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if len(scan.Files) != 0 {
		t.Fatalf("expected oversized PHP source to be skipped, got %#v", scan.Files)
	}
	if !containsWarning(scan.Warnings, "skipped 1 large PHP file") || !containsWarning(scan.Warnings, fmt.Sprintf("%d bytes", maxScannablePHPFile)) {
		t.Fatalf("expected oversized PHP warning with byte limit, got %#v", scan.Warnings)
	}
	if !scan.UsageIncomplete {
		t.Fatal("expected oversized PHP source to mark scan usage incomplete")
	}
}

func TestScanRepoMarksUsageIncompleteWhenUseStatementLimitHit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < maxPHPUseStatementsPerFile+1; i++ {
		fmt.Fprintf(&content, "use Vendor\\Lib\\Thing%d;\n", i)
	}
	writeFile(t, filepath.Join(repo, "src", "adversarial-use.php"), content.String())

	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		NamespaceToDep:       map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if !scan.UsageIncomplete {
		t.Fatal("expected use statement cap to mark scan usage incomplete")
	}
	if !containsWarning(scan.Warnings, "stopped PHP use import scan") {
		t.Fatalf("expected use statement cap warning, got %#v", scan.Warnings)
	}

	dep, _ := buildDependencyReport(helpersVendorLibDependency, scan, 100)
	if !dep.UsageIncomplete {
		t.Fatal("expected dependency report to be marked usage incomplete")
	}
	for _, rec := range dep.Recommendations {
		if rec.Code == "remove-unused-dependency" || rec.Code == "low-usage-dependency" {
			t.Fatalf("did not expect definitive usage recommendation with incomplete scan: %#v", dep.Recommendations)
		}
	}
}

func TestBuildDependencyReportSuppressesRemovalAdviceWhenUsageIncomplete(t *testing.T) {
	scan := scanResult{
		UsageIncomplete:      true,
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		Files: []fileScan{{
			Path: "src/small.php",
			Imports: []importBinding{{
				Dependency: helpersVendorLibDependency,
				Module:     "Vendor\\Lib\\Thing",
				Name:       "Thing",
				Local:      "Thing",
			}},
			Usage: map[string]int{"Thing": 0},
		}},
	}

	dep, _ := buildDependencyReport(helpersVendorLibDependency, scan, 40)
	if !dep.UsageIncomplete {
		t.Fatal("expected dependency report to be marked usage incomplete")
	}
	if len(dep.UnusedImports) != 0 {
		t.Fatalf("expected unused imports to be suppressed, got %#v", dep.UnusedImports)
	}
	if len(dep.SuppressedUnusedImports) != 1 {
		t.Fatalf("expected one suppressed unused import, got %#v", dep.SuppressedUnusedImports)
	}
	for _, rec := range dep.Recommendations {
		if rec.Code == "remove-unused-dependency" || rec.Code == "low-usage-dependency" {
			t.Fatalf("did not expect removal recommendation with incomplete usage: %#v", dep.Recommendations)
		}
	}
}

func TestShouldSkipDirAndDependencyHelpers(t *testing.T) {
	if !shouldSkipDir("vendor") {
		t.Fatalf("expected vendor to be skipped")
	}
	if shouldSkipDir("src") {
		t.Fatalf("did not expect src to be skipped")
	}
	for _, platformPackage := range []string{
		"php", "php-64bit", "hhvm", "composer", "composer-plugin-api", "composer-runtime-api", "ext-json", "lib-icu",
	} {
		if dep, ok := normalizeComposerDependency(platformPackage); ok || dep != "" {
			t.Fatalf("Composer platform package %q should be ignored, dep=%q ok=%v", platformPackage, dep, ok)
		}
	}
	if dep, ok := normalizeComposerDependency(helpersVendorLibDependency); !ok || dep != helpersVendorLibDependency {
		t.Fatalf("vendor/lib should be accepted, dep=%q ok=%v", dep, ok)
	}
	if dep, ok := normalizeComposerDependency("php-http/discovery"); !ok || dep != "php-http/discovery" {
		t.Fatalf("qualified package with a platform-like vendor should be accepted, dep=%q ok=%v", dep, ok)
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
	repoFile := filepath.Join(repo, helpersComposerJSON)
	writeFile(t, repoFile, fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected walk error when repoPath is a file")
	}
}

func TestDependenciesInFileAndAllDependencies(t *testing.T) {
	scan := scanResult{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		Files: []fileScan{{
			Imports: []importBinding{{Dependency: "vendor/tool"}},
		}},
	}
	deps := allDependencies(scan)
	if !slices.Equal(deps, []string{helpersVendorLibDependency, "vendor/tool"}) {
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
	writeFile(t, filepath.Join(d, helpersComposerJSON), "{}")
	if !hasComposerManifest(d) {
		t.Fatalf("expected manifest")
	}
}

func TestScanRepoContextCanceled(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, "src", "x.php"), helpersPHPHeader)
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
	resolver := composerResolver{namespaceToDep: map[string]string{
		"Symfony":              "symfony/symfony",
		"Symfony\\Component\\": "symfony/component",
	}}
	if got := resolver.resolveWithPSR4("Symfony\\Component\\Yaml\\Yaml"); got != "symfony/component" {
		t.Fatalf("expected longest prefix match, got %q", got)
	}
}

func TestLineNumberAtBoundaries(t *testing.T) {
	if got := lineNumberAt(helpersABLines, 0); got != 1 {
		t.Fatalf("expected line 1 at offset 0, got %d", got)
	}
	if got := lineNumberAt(helpersABLines, 3); got != 2 {
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
	if got := normalizeNamespace(`\Monolog\Logger\`); got != helpersMonologLogger {
		t.Fatalf("unexpected normalizeNamespace: %q", got)
	}
}

func TestParseUseStatementFunctionAndConstImports(t *testing.T) {
	resolver := composerResolver{declared: map[string]struct{}{helpersVendorLibDependency: {}}}
	resolver.namespaceToDep = map[string]string{"Vendor\\Lib": helpersVendorLibDependency}
	imports, _, unresolved := parseUseStatement("function Vendor\\Lib\\helper, const Vendor\\Lib\\VERSION", "x.php", 1, resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(imports))
	}
}

func assertGroupedUseStatementImports(t *testing.T, resolver composerResolver, statement string, expectedModules []string) {
	t.Helper()

	imports, groupedDeps, unresolved := parseUseStatement(statement, "x.php", 1, resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(groupedDeps) != 1 {
		t.Fatalf("expected grouped dependency attribution, got %#v", groupedDeps)
	}
	if _, ok := groupedDeps[helpersVendorLibDependency]; !ok {
		t.Fatalf("expected grouped dependency %q, got %#v", helpersVendorLibDependency, groupedDeps)
	}
	if len(imports) != len(expectedModules) {
		t.Fatalf("expected %d imports, got %d", len(expectedModules), len(imports))
	}
	for i, imp := range imports {
		if imp.Dependency != helpersVendorLibDependency {
			t.Fatalf("expected dependency %q, got %#v", helpersVendorLibDependency, imp)
		}
		if imp.Module != expectedModules[i] {
			t.Fatalf("expected module %q, got %#v", expectedModules[i], imp)
		}
	}
}

func TestParseGroupedUseStatementFunctionAndConstImports(t *testing.T) {
	resolver := composerResolver{declared: map[string]struct{}{helpersVendorLibDependency: {}}}
	resolver.namespaceToDep = map[string]string{"Vendor\\Lib": helpersVendorLibDependency}

	tests := []struct {
		statement       string
		expectedModules []string
	}{
		{
			statement:       "function Vendor\\Lib\\{helper, util as utilAlias}",
			expectedModules: []string{"Vendor\\Lib\\helper", "Vendor\\Lib\\util"},
		},
		{
			statement:       "const Vendor\\Lib\\{VERSION, BUILD as B}",
			expectedModules: []string{"Vendor\\Lib\\VERSION", "Vendor\\Lib\\BUILD"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.statement, func(t *testing.T) {
			assertGroupedUseStatementImports(t, resolver, tc.statement, tc.expectedModules)
		})
	}
}

func TestParseNamespaceReferencesSkipsUseLine(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Monolog": helpersMonologDependency}}
	imports, unresolved := parseNamespaceReferences([]byte(helpersPHPHeader+"use Monolog\\Logger;\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 0 {
		t.Fatalf("expected no namespace imports from use-line, got %#v", imports)
	}
}

func TestParseNamespaceReferencesDoesNotLetUseLinesExhaustReferenceLimit(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Monolog": helpersMonologDependency}}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	content.WriteString("<?php use Monolog\\InlineLogger;\n")
	for i := 0; i < maxPHPUseStatementsPerFile*2; i++ {
		content.WriteString("use Monolog\\Logger;\n")
	}
	content.WriteString("$logger = new \\Monolog\\Logger(\"app\");\n")

	imports, unresolved := parseNamespaceReferences([]byte(content.String()), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 1 {
		t.Fatalf("expected namespace reference after bounded use lines to be preserved, got %#v", imports)
	}
	if imports[0].Module != helpersMonologLogger {
		t.Fatalf("expected module %q, got %#v", helpersMonologLogger, imports[0])
	}
	expectedLine := maxPHPUseStatementsPerFile*2 + 3
	if imports[0].Location.Line != expectedLine {
		t.Fatalf("expected namespace reference after use block on line %d, got %#v", expectedLine, imports[0])
	}
}

func TestParseNamespaceReferencesIgnoresCommentAndStringMentions(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Monolog": helpersMonologDependency}}
	content := helpersPHPHeader +
		"$class = \"\\\\Monolog\\\\Logger\";\n" +
		"// \\Monolog\\Logger\n" +
		"# \\Monolog\\Logger\n" +
		"/* \\Monolog\\Logger */\n" +
		"$logger = new \\Monolog\\Logger(\"app\");\n"
	imports, unresolved := parseNamespaceReferences([]byte(content), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 1 {
		t.Fatalf("expected exactly one code namespace import, got %#v", imports)
	}
	if imports[0].Module != helpersMonologLogger {
		t.Fatalf("expected module %q, got %#v", helpersMonologLogger, imports[0])
	}
	if imports[0].Location.Line != 6 {
		t.Fatalf("expected code namespace import on line 6, got %d", imports[0].Location.Line)
	}
}

func TestDependencyFromModuleBranches(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Monolog": helpersMonologDependency},
		localNamespace: map[string]struct{}{"App": {}},
		declared:       map[string]struct{}{helpersVendorPkgDependency: {}},
	}
	if dep, resolved := resolver.dependencyFromModule(""); dep != "" || resolved {
		t.Fatalf("expected empty module branch, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`App\Thing`); dep != "" || resolved {
		t.Fatalf("expected local namespace to be excluded, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`Monolog\Logger`); dep != helpersMonologDependency || !resolved {
		t.Fatalf("expected psr-4 dependency, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`Vendor\Pkg\Client`); dep != helpersVendorPkgDependency || !resolved {
		t.Fatalf("expected heuristic dependency, got dep=%q resolved=%v", dep, resolved)
	}
	if dep, resolved := resolver.dependencyFromModule(`Unknown\Pkg\Client`); dep != "" || !resolved {
		t.Fatalf("expected unresolved namespace branch, got dep=%q resolved=%v", dep, resolved)
	}
}

func TestParseNamespaceReferencesUnresolvedBranch(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{},
		declared:       map[string]struct{}{},
	}
	imports, unresolved := parseNamespaceReferences([]byte(helpersPHPHeader+"$foo = new \\Unknown\\Pkg\\Thing();\n"), "x.php", resolver)
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
	writeFile(t, filepath.Join(repoBadManifest, helpersComposerJSON), `{bad-json`)
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repoBadManifest, TopN: 1}); err == nil {
		t.Fatalf("expected parse error from composer.json")
	}

	repoBadLock := t.TempDir()
	writeFile(t, filepath.Join(repoBadLock, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repoBadLock, helpersComposerLock), `{bad-json`)
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repoBadLock, TopN: 1}); err == nil {
		t.Fatalf("expected parse error from composer.lock")
	}

	repoCanceled := t.TempDir()
	writeFile(t, filepath.Join(repoCanceled, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repoCanceled, "src", "x.php"), helpersPHPHeader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Analyse(ctx, language.Request{RepoPath: repoCanceled, TopN: 1}); err == nil {
		t.Fatalf("expected canceled analysis")
	}
}

func TestDetectWithConfidenceCanceledContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, "src", "x.php"), helpersPHPHeader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(ctx, repo); err == nil {
		t.Fatalf("expected canceled detection")
	}
}

func TestParseUseStatementAndPartEdgeBranches(t *testing.T) {
	resolver := composerResolver{declared: map[string]struct{}{}, namespaceToDep: map[string]string{}}

	imports, grouped, unresolved := parseUseStatement("", "x.php", 1, resolver)
	if len(imports) != 0 || len(grouped) != 0 || unresolved != 0 {
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
	writeFile(t, filepath.Join(repo, "src", "x.php"), helpersPHPHeader+"$foo = new \\Unknown\\Pkg\\Thing();\n")
	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{},
		NamespaceToDep:       map[string]string{},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if !containsWarning(scan.Warnings, "no Composer dependencies discovered") {
		t.Fatalf("expected no-composer-dependency warning, got %#v", scan.Warnings)
	}
	if !containsWarning(scan.Warnings, "unable to map") {
		t.Fatalf("expected unresolved namespace warning, got %#v", scan.Warnings)
	}
}

func TestScanRepoSkipsNestedComposerPackagesAndTracksDynamicUsage(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, "src", "root.php"), helpersPHPHeader+
		"use Vendor\\Lib\\{Client};\n"+
		"class_exists(Client::class);\n"+
		"$client = new Client();\n")
	writeFile(t, filepath.Join(repo, "packages", "nested", helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorPkgDependency))
	writeFile(t, filepath.Join(repo, "packages", "nested", "src", "nested.php"), helpersPHPHeader+
		"use Vendor\\Pkg\\Nested;\n"+
		"$nested = new Nested();\n")

	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		NamespaceToDep:       map[string]string{"Vendor\\Lib": helpersVendorLibDependency, "Vendor\\Pkg": helpersVendorPkgDependency},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if len(scan.Files) != 1 || scan.Files[0].Path != filepath.Join("src", "root.php") {
		t.Fatalf("expected only root PHP file to be scanned, got %#v", scan.Files)
	}
	if scan.DynamicUsageByDependency[helpersVendorLibDependency] != 1 {
		t.Fatalf("expected dynamic usage count for %q, got %#v", helpersVendorLibDependency, scan.DynamicUsageByDependency)
	}
	if scan.GroupedImportsByDependency[helpersVendorLibDependency] != 1 {
		t.Fatalf("expected grouped import count for %q, got %#v", helpersVendorLibDependency, scan.GroupedImportsByDependency)
	}
	if !containsWarning(scan.Warnings, "skipped 1 nested composer package directory") {
		t.Fatalf("expected nested package skip warning, got %#v", scan.Warnings)
	}
	if !containsWarning(scan.Warnings, "dynamic loading/reflection patterns detected") {
		t.Fatalf("expected dynamic usage warning, got %#v", scan.Warnings)
	}
}

func TestReadComposerManifestAndLockMappingsErrorFromFileRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root-file")
	writeFile(t, root, "x")
	if _, _, err := readComposerManifest(root); err == nil {
		t.Fatalf("expected readComposerManifest non-not-exist error for file root")
	}
	if loadComposerLockMappings(root, &composerData{NamespaceToDep: map[string]string{}}) == nil {
		t.Fatalf("expected loadComposerLockMappings non-not-exist error for file root")
	}
}

func TestResolveByNamespaceHeuristicTooShort(t *testing.T) {
	resolver := composerResolver{declared: map[string]struct{}{helpersVendorPkgDependency: {}}}
	if got := resolver.resolveByNamespaceHeuristic("Vendor"); got != "" {
		t.Fatalf("expected empty heuristic for short namespace, got %q", got)
	}
}

func TestAdditionalBranchCoverageNormalizeAndTopNBranches(t *testing.T) {
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

	deps, warnings := buildTopPHPDependencies(10, scanResult{}, 40, report.DefaultRemovalCandidateWeights())
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
	top, _ := buildTopPHPDependencies(1, scan, 40, report.DefaultRemovalCandidateWeights())
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

}

func TestAdditionalBranchCoverageResolverAndUseBranches(t *testing.T) {
	resolver := composerResolver{
		localNamespace: map[string]struct{}{"": {}, "App": {}},
		namespaceToDep: map[string]string{"": "empty/dep", "Monolog": helpersMonologDependency},
	}
	if !resolver.isLocalNamespace(`App\Svc`) {
		t.Fatalf("expected local namespace match")
	}
	if got := resolver.resolveWithPSR4(`Monolog\Logger`); got != helpersMonologDependency {
		t.Fatalf("expected psr4 match, got %q", got)
	}
	if got := resolver.resolveByNamespaceHeuristic(`\Thing`); got != "" {
		t.Fatalf("expected empty heuristic for blank vendor, got %q", got)
	}

	unknownResolver := composerResolver{declared: map[string]struct{}{}, namespaceToDep: map[string]string{}}
	imports, _, unresolved := parseUseStatement(`Unknown\Pkg\Thing`, "x.php", 1, unknownResolver)
	if len(imports) != 0 || unresolved == 0 {
		t.Fatalf("expected unresolved non-grouped use statement branch, imports=%#v unresolved=%d", imports, unresolved)
	}
	imports, _, unresolved = parseUseStatement(`Unknown\Pkg\{Thing}`, "x.php", 1, unknownResolver)
	if len(imports) != 0 || unresolved == 0 {
		t.Fatalf("expected unresolved grouped use statement branch, imports=%#v unresolved=%d", imports, unresolved)
	}

	knownResolver := composerResolver{namespaceToDep: map[string]string{"Foo\\Bar": "foo/bar"}}
	imports, unresolved = parseNamespaceReferences([]byte(helpersPHPHeader+"\\Foo\\Bar; \\Foo\\Bar;\n"), "x.php", knownResolver)
	if unresolved != 0 || len(imports) != 1 {
		t.Fatalf("expected duplicate namespace refs to de-dup, imports=%#v unresolved=%d", imports, unresolved)
	}
}

func TestAdditionalBranchCoverageRecommendationsAndErrors(t *testing.T) {
	dep := report.DependencyReport{
		Name:          "x/pkg",
		UsedImports:   nil,
		UnusedImports: []report.ImportUse{{Name: "Thing", Module: "X\\Thing"}},
	}
	recs := buildRecommendations(dep, 40)
	if len(recs) == 0 {
		t.Fatalf("expected remove-unused recommendation")
	}

	adapter := NewAdapter()
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: string([]byte{'b', 'a', 'd', 0x00}), TopN: 1}); err == nil {
		t.Fatalf("expected invalid repo path error")
	}
}

func TestScanRepoMaxFilesAndSkipDirBranches(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, "vendor", "x.php"), helpersPHPHeader)
	for i := 0; i < maxScanFiles+1; i++ {
		writeFile(t, filepath.Join(repo, "src", fmt.Sprintf("f-%04d.txt", i)), "x")
	}
	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		NamespaceToDep:       map[string]string{},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if !containsWarning(scan.Warnings, "scan stopped after") {
		t.Fatalf("expected bounded scan warning, got %#v", scan.Warnings)
	}
}

func TestLoadComposerLockMappingsSkipsInvalidEntries(t *testing.T) {
	repo := t.TempDir()
	lockTemplate := `{
  "packages": [
    {"name":"", "autoload":{"psr-4":{"\\\\":"src"}}},
	    {"name":%q, "autoload":{"psr-4":{"\\\\":"src","Vendor\\\\Pkg\\\\":"src"}}}
  ]
}`
	lockContent := fmt.Sprintf(lockTemplate, helpersVendorPkgDependency)
	writeFile(t, filepath.Join(repo, helpersComposerLock), lockContent)
	data := composerData{NamespaceToDep: map[string]string{}}
	if err := loadComposerLockMappings(repo, &data); err != nil {
		t.Fatalf("loadComposerLockMappings: %v", err)
	}
	if _, ok := data.NamespaceToDep[""]; ok {
		t.Fatalf("did not expect empty namespace key in mappings")
	}
	if !hasNamespaceDependencyMapping(data.NamespaceToDep, "Vendor", helpersVendorPkgDependency) {
		t.Fatalf("expected valid namespace mapping, got %#v", data.NamespaceToDep)
	}
}

func hasNamespaceDependencyMapping(namespaceToDep map[string]string, namespaceFragment string, dependency string) bool {
	for namespace, current := range namespaceToDep {
		if strings.Contains(namespace, namespaceFragment) && current == dependency {
			return true
		}
	}
	return false
}
