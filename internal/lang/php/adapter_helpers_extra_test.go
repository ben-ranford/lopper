package php

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
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

const (
	testMaxComposerManifestBytes           int64 = 2 * 1024 * 1024
	testMaxComposerLockBytes               int64 = 8 * 1024 * 1024
	testMaxScannablePHPFile                int64 = 2 * 1024 * 1024
	testMaxPHPUseStatementsPerFile               = 4096
	testMaxPHPNamespaceDeclarationsPerFile       = 4096
	testMaxPHPNamespaceReferencesPerFile         = 4096
	testMaxPHPConfigBytes                        = 64 * 1024
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
			limit:    testMaxComposerManifestBytes,
			read: func(repo string) error {
				_, _, err := readComposerManifest(repo)
				return err
			},
		},
		{
			name:     "composer lock",
			filename: helpersComposerLock,
			limit:    testMaxComposerLockBytes,
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
	if !isPureOversizedFileErrorForTest(safeio.ErrFileTooLarge) {
		t.Fatalf("expected direct oversized sentinel to be classified as skippable")
	}
	if !isPureOversizedFileErrorForTest(fmt.Errorf("wrapped: %w", safeio.ErrFileTooLarge)) {
		t.Fatalf("expected wrapped oversized sentinel to be classified as skippable")
	}
	closeErr := errors.New("close failed")
	if isPureOversizedFileErrorForTest(errors.Join(safeio.ErrFileTooLarge, closeErr)) {
		t.Fatalf("expected joined operational error to be preserved")
	}
}
func TestReadComposerInputsAcceptExactLimitFiles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
		limit    int64
	}{
		{name: "composer manifest", filename: helpersComposerJSON, limit: testMaxComposerManifestBytes},
		{name: "composer lock", filename: helpersComposerLock, limit: testMaxComposerLockBytes},
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
	if data.ShortOpenTags {
		t.Fatalf("did not expect short open tags without explicit PHP configuration")
	}
}

func TestLoadComposerDataDetectsRootShortOpenTagConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, ".user.ini"), "short_open_tag = On\n")
	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("load data with short-open config: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings with short-open config: %#v", warnings)
	}
	if !data.ShortOpenTags {
		t.Fatalf("expected .user.ini to enable short open tags")
	}
	if !parsesShortOpenTagEnabled("php_value memory_limit 128M\nphp_value short_open_tag On # comment\n") {
		t.Fatalf("expected parser to skip unrelated php_value directives before short_open_tag")
	}
}

func TestShortOpenTagConfigUsesFinalAssignment(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "ini enables last", content: "short_open_tag = Off\nshort_open_tag = On\n", want: true},
		{name: "ini disables last", content: "short_open_tag = On\nshort_open_tag = Off\n", want: false},
		{name: "htaccess enables last", content: "php_flag short_open_tag Off\nphp_flag short_open_tag On\n", want: true},
		{name: "htaccess disables last", content: "php_flag short_open_tag On\nphp_flag short_open_tag Off\n", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsesShortOpenTagEnabled(tc.content); got != tc.want {
				t.Fatalf("expected short_open_tag enabled=%v, got %v", tc.want, got)
			}
		})
	}
}

func TestLoadComposerDataMarksInterpolatedShortOpenTagConfigIncomplete(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, ".user.ini"), "short_open_tag = ${LOPPER_SHORT_TAGS}\n")

	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("load data with interpolated short-open config: %v", err)
	}
	if data.ShortOpenTags {
		t.Fatal("did not expect unresolved short-open config to enable short tags")
	}
	if !data.ShortOpenTagPolicy.incompleteForFile(filepath.Join(repo, "src", "index.php")) {
		t.Fatal("expected interpolated root PHP config to mark files under that directory incomplete")
	}
	if !containsWarning(warnings, "could not resolve PHP short_open_tag config .user.ini") {
		t.Fatalf("expected unresolved PHP config warning, got %#v", warnings)
	}
}

func TestLoadComposerDataTracksOversizedShortOpenTagConfigByDirectory(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	testutil.MustWritePaddedFile(t, filepath.Join(repo, ".user.ini"), "short_open_tag = On\n", testMaxPHPConfigBytes+1)

	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("expected oversized PHP config to warn and continue, got %v", err)
	}
	if usageIncompleteForTest(t, data) {
		t.Fatal("did not expect config ingestion alone to mark all dependency coverage incomplete")
	}
	if !data.ShortOpenTagPolicy.incompleteForFile(filepath.Join(repo, "src", "index.php")) {
		t.Fatal("expected oversized root PHP config to mark files under that directory incomplete")
	}
	if !containsWarning(warnings, "skipped PHP short_open_tag config .user.ini because it exceeds") {
		t.Fatalf("expected oversized PHP config warning, got %#v", warnings)
	}
}

func TestLoadComposerDataChildSettingOverridesIncompleteAncestorConfig(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	testutil.MustWritePaddedFile(t, filepath.Join(repo, "php.ini"), "short_open_tag = On\n", testMaxPHPConfigBytes+1)
	writeFile(t, filepath.Join(repo, "src", ".user.ini"), "short_open_tag = Off\n")

	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("expected mixed short-open configs to load: %v", err)
	}
	if usageIncompleteForTest(t, data) {
		t.Fatalf("did not expect readable child setting to inherit ancestor incompleteness: %#v", data)
	}
	childFile := filepath.Join(repo, "src", "index.php")
	if data.ShortOpenTagPolicy.incompleteForFile(childFile) {
		t.Fatalf("expected child setting to stop ancestor incompleteness for %s", childFile)
	}
	if data.ShortOpenTagPolicy.enabledForFile(childFile) {
		t.Fatalf("expected child short_open_tag setting to win for %s", childFile)
	}
	if !containsWarning(warnings, "skipped PHP short_open_tag config php.ini because it exceeds") {
		t.Fatalf("expected oversized ancestor config warning, got %#v", warnings)
	}
}

func TestLoadComposerDataBoundsShortOpenTagConfigDiscovery(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	for i := 0; i < maxPHPConfigWalkEntries; i++ {
		writeFile(t, filepath.Join(repo, "ordinary", fmt.Sprintf("file-%04d.txt", i)), "")
	}

	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("expected config discovery cap to warn and continue, got %v", err)
	}
	if !usageIncompleteForTest(t, data) {
		t.Fatal("expected capped config discovery to mark dependency coverage incomplete")
	}
	if !containsWarning(warnings, "PHP short_open_tag config discovery stopped after") {
		t.Fatalf("expected config discovery cap warning, got %#v", warnings)
	}
}

func TestAdapterExcludesCacheDirectoryFromShortOpenTagDiscovery(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, "a-cache")
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	writeFile(t, filepath.Join(repo, "z-config", "php.ini"), "short_open_tag = On\n")
	for i := 0; i < maxPHPConfigWalkEntries; i++ {
		writeFile(t, filepath.Join(cacheDir, fmt.Sprintf("entry-%04d.txt", i)), "")
	}

	result, err := NewAdapter().Analyse(context.Background(), language.AnalysisOptions{
		RepoPath:      repo,
		ExcludedPaths: []string{cacheDir},
	})
	if err != nil {
		t.Fatalf("analyse with excluded cache directory: %v", err)
	}
	if result.UsageIncomplete {
		t.Fatalf("expected excluded cache directory not to consume config traversal budget, warnings=%#v", result.Warnings)
	}
}

func TestLoadComposerDataHonorsCanceledContextDuringShortOpenTagConfigDiscovery(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))

	_, _, err := loadComposerDataWithContext(testutil.CanceledContext(), repo)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context from config discovery, got %v", err)
	}
}

func TestLoadComposerDataWarnsAndContinuesWhenLockIsOversized(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	testutil.MustWritePaddedFile(t, filepath.Join(repo, helpersComposerLock), "{}", testMaxComposerLockBytes+1)

	data, warnings, err := loadComposerData(repo)
	if err != nil {
		t.Fatalf("expected oversized optional composer.lock to warn and continue, got %v", err)
	}
	if _, ok := data.DeclaredDependencies[helpersVendorLibDependency]; !ok {
		t.Fatalf("expected manifest dependency to be retained, got %#v", data.DeclaredDependencies)
	}
	if !usageIncompleteForTest(t, data) {
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

func TestParsePHPImportsIgnoresFakeHeredocMarkersBeforeImports(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		declared:       map[string]struct{}{"vendor/package": {}},
	}
	content := []byte(helpersPHPHeader +
		"// <<<TXT\n" +
		"$label = \"<<<DOC\";\n" +
		"use Vendor\\Package\\Client;\n" +
		"$client = new Client();\n")

	parsed := parsePHPImports(content, "fake-heredoc.php", resolver)

	if parsed.unresolvedCount != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, parsed.unresolvedCount)
	}
	if len(parsed.imports) != 1 {
		t.Fatalf("expected import after comment/string fake heredoc markers, got %#v", parsed.imports)
	}
	if parsed.imports[0].Module != `Vendor\Package\Client` || parsed.imports[0].Wildcard {
		t.Fatalf("expected normal use import after fake heredoc markers, got %#v", parsed.imports[0])
	}
}

func TestParsePHPImportsParsesMultilineUseAliasDeclaration(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		declared:       map[string]struct{}{"vendor/package": {}},
	}
	content := []byte(helpersPHPHeader +
		"use Vendor\\Package\\Client\n" +
		"    as Alias;\n" +
		"$client = new Alias();\n")

	parsed := parsePHPImports(content, "multiline-use.php", resolver)

	if parsed.unresolvedCount != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, parsed.unresolvedCount)
	}
	if len(parsed.imports) != 1 {
		t.Fatalf("expected one multiline use import, got %#v", parsed.imports)
	}
	got := parsed.imports[0]
	if got.Module != `Vendor\Package\Client` || got.Local != "Alias" || got.Wildcard {
		t.Fatalf("expected multiline alias use import, got %#v", got)
	}
	usage := shared.CountUsage(content, parsed.imports)
	if usage["Alias"] != 1 {
		t.Fatalf("expected alias usage to be counted once, got usage=%#v imports=%#v", usage, parsed.imports)
	}
}

func TestParsePHPImportsBoundsMalformedUseScan(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		declared:       map[string]struct{}{"vendor/package": {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < 2500; i++ {
		content.WriteString("use Vendor\\Package\\Missing")
	}
	content.WriteString("\nuse Vendor\\Package\\Client;\n")

	start := time.Now()
	parsed := parsePHPImports([]byte(content.String()), "malformed-use.php", resolver)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("expected malformed use scan to stay bounded, took %s", elapsed)
	}
	if parsed.unresolvedCount != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, parsed.unresolvedCount)
	}
	if len(parsed.imports) != 1 || parsed.imports[0].Module != `Vendor\Package\Client` {
		t.Fatalf("expected valid import after malformed use line, got %#v", parsed.imports)
	}
}

func TestParsePHPImportsBoundsAdversarialUseStatements(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPUseStatementsPerFile+17; i++ {
		fmt.Fprintf(&content, "use Vendor\\Lib\\Thing%d;\n", i)
	}

	parsed := parsePHPImports([]byte(content.String()), "adversarial-use.php", resolver)

	if len(parsed.imports) != testMaxPHPUseStatementsPerFile {
		t.Fatalf("expected exactly %d bounded use imports, got %d", testMaxPHPUseStatementsPerFile, len(parsed.imports))
	}
	if parsed.imports[len(parsed.imports)-1].Location.Line != testMaxPHPUseStatementsPerFile+1 {
		t.Fatalf("expected last bounded use import on line %d, got %#v", testMaxPHPUseStatementsPerFile+1, parsed.imports[len(parsed.imports)-1])
	}
	for _, imp := range parsed.imports {
		if imp.Wildcard {
			t.Fatalf("did not expect namespace-reference import while parsing use-statement adversary, got %#v", imp)
		}
	}
}

func TestScanPHPUseStatementsForImportsMasksStatementsBeyondMatchLimit(t *testing.T) {
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPUseStatementsPerFile+17; i++ {
		fmt.Fprintf(&content, "use Vendor\\Lib\\Thing%d;\n", i)
	}

	scan, masked := scanPHPUseStatementsForImports(content.String(), testMaxPHPUseStatementsPerFile+1)
	if len(scan.matches) != testMaxPHPUseStatementsPerFile+1 {
		t.Fatalf("expected %d retained matches, got %d", testMaxPHPUseStatementsPerFile+1, len(scan.matches))
	}
	if len(scan.ranges) != 0 {
		t.Fatalf("expected no retained use ranges, got %#v", scan.ranges)
	}
	if strings.Contains(masked, "use Vendor\\Lib\\Thing") {
		t.Fatalf("expected statements beyond the retained match cap to remain masked")
	}
}

func TestParsePHPImportsTracksClassContextNearLinearly(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	padding := strings.Repeat(" ", 400)
	for i := 0; i < testMaxPHPUseStatementsPerFile; i++ {
		content.WriteString(padding)
		fmt.Fprintf(&content, "use Vendor\\Lib\\Thing%d;\n", i)
	}

	start := time.Now()
	parsed := parsePHPImports([]byte(content.String()), "distributed-use.php", resolver)
	elapsed := time.Since(start)

	if elapsed > 12*time.Second {
		t.Fatalf("expected bounded context tracking to finish quickly, took %s", elapsed)
	}
	if len(parsed.imports) != testMaxPHPUseStatementsPerFile {
		t.Fatalf("expected %d use imports, got %d", testMaxPHPUseStatementsPerFile, len(parsed.imports))
	}
	for _, imp := range parsed.imports {
		if imp.Wildcard {
			t.Fatalf("did not expect top-level imports to be treated as class-body trait uses, got %#v", imp)
		}
	}
}

func TestParsePHPImportsScansManyBraceContextsNearLinearly(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	padding := strings.Repeat(" ", maxPHPNamespaceAncestorBytes)
	for i := 0; i < 24; i++ {
		content.WriteString("if ($flag) ")
		content.WriteString(padding)
		content.WriteString("{ }\n")
	}
	content.WriteString("use Vendor\\Lib\\Thing;\n")

	start := time.Now()
	parsed := parsePHPImports([]byte(content.String()), "brace-contexts.php", resolver)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("expected many brace contexts to scan quickly, took %s", elapsed)
	}
	if len(parsed.imports) != 1 {
		t.Fatalf("expected one import after brace-heavy prefix, got %#v", parsed.imports)
	}
	if parsed.imports[0].Wildcard {
		t.Fatalf("expected top-level use after brace-heavy prefix to remain an import declaration, got %#v", parsed.imports[0])
	}
}

func TestFindNamespaceDeclarationsScansSameLineCandidatesNearLinearly(t *testing.T) {
	const declarationCount = 3000
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < declarationCount; i++ {
		fmt.Fprintf(&content, "namespace Vendor\\Package%d; ", i)
	}

	start := time.Now()
	declarations := findNamespaceDeclarations(content.String())
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("expected same-line namespace declarations to scan quickly, took %s", elapsed)
	}
	if len(declarations) != declarationCount {
		t.Fatalf("expected %d namespace declarations, got %d", declarationCount, len(declarations))
	}
	if declarations[0].name != `Vendor\Package0` || declarations[len(declarations)-1].name != `Vendor\Package2999` {
		t.Fatalf("unexpected declaration endpoints: first=%#v last=%#v", declarations[0], declarations[len(declarations)-1])
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
	for i := 0; i < testMaxPHPUseStatementsPerFile+17; i++ {
		if i > 0 {
			content.WriteString(", ")
		}
		fmt.Fprintf(&content, "Thing%d", i)
	}
	content.WriteString("};\n")

	parsed := parsePHPImports([]byte(content.String()), "adversarial-grouped-use.php", resolver)

	if len(parsed.imports) != testMaxPHPUseStatementsPerFile {
		t.Fatalf("expected exactly %d bounded grouped use imports, got %d", testMaxPHPUseStatementsPerFile, len(parsed.imports))
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
		for i := 0; i < testMaxPHPUseStatementsPerFile; i++ {
			if i > 0 {
				content.WriteString(", ")
			}
			fmt.Fprintf(&content, "Thing%d", i)
		}
		content.WriteString("};\n")
	}

	parsed := parsePHPImports([]byte(content.String()), "adversarial-unresolved-grouped-use.php", resolver)

	if len(parsed.imports) != 0 {
		t.Fatalf("expected unresolved grouped use parts to emit no imports, got %d", len(parsed.imports))
	}
	if parsed.unresolvedCount != testMaxPHPUseStatementsPerFile {
		t.Fatalf("expected unresolved count to stop at %d, got %d", testMaxPHPUseStatementsPerFile, parsed.unresolvedCount)
	}
}

func TestParsePHPImportsBoundsAdversarialNamespaceReferences(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		declared:       map[string]struct{}{helpersVendorLibDependency: {}},
	}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPNamespaceReferencesPerFile+23; i++ {
		content.WriteString("$client = new \\Vendor\\Lib\\Client();\n")
	}

	parsed := parsePHPImports([]byte(content.String()), "adversarial-namespace.php", resolver)

	if len(parsed.imports) != testMaxPHPNamespaceReferencesPerFile {
		t.Fatalf("expected exactly %d bounded namespace imports, got %d", testMaxPHPNamespaceReferencesPerFile, len(parsed.imports))
	}
	if parsed.imports[0].Location.Line != 2 {
		t.Fatalf("expected first namespace import on line 2, got %#v", parsed.imports[0])
	}
	if parsed.imports[len(parsed.imports)-1].Location.Line != testMaxPHPNamespaceReferencesPerFile+1 {
		t.Fatalf("expected last bounded namespace import on line %d, got %#v", testMaxPHPNamespaceReferencesPerFile+1, parsed.imports[len(parsed.imports)-1])
	}
}

func TestScanRepoWarnsWhenPHPImportScansAreBounded(t *testing.T) {
	repo := t.TempDir()
	var useContent strings.Builder
	useContent.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPUseStatementsPerFile+1; i++ {
		fmt.Fprintf(&useContent, "use Vendor\\Lib\\UseThing%d;\n", i)
	}
	writeFile(t, filepath.Join(repo, "src", "use-adversary.php"), useContent.String())

	var namespaceContent strings.Builder
	namespaceContent.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPNamespaceReferencesPerFile+1; i++ {
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
	testutil.MustWritePaddedFile(t, sourcePath, helpersPHPHeader, testMaxScannablePHPFile+1)

	if _, _, err := readPHPFile(repo, sourcePath); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized PHP source to fail with ErrFileTooLarge, got %v", err)
	}
}

func TestReadPHPFileAcceptsExactLimitSource(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", "exact.php")
	testutil.MustWritePaddedFile(t, sourcePath, helpersPHPHeader, testMaxScannablePHPFile)

	content, relPath, err := readPHPFile(repo, sourcePath)
	if err != nil {
		t.Fatalf("read exact-limit PHP source: %v", err)
	}
	if relPath != filepath.Join("src", "exact.php") {
		t.Fatalf("unexpected rel path: %q", relPath)
	}
	if int64(len(content)) != testMaxScannablePHPFile {
		t.Fatalf("expected exact-limit PHP source read to return %d bytes, got %d", testMaxScannablePHPFile, len(content))
	}
}

func TestScanRepoSkipsOversizedPHPSourceWithWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWritePaddedFile(t, filepath.Join(repo, "src", "oversized.php"), helpersPHPHeader, testMaxScannablePHPFile+1)

	scan, err := scanRepo(context.Background(), repo, composerData{DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}}})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	if len(scan.Files) != 0 {
		t.Fatalf("expected oversized PHP source to be skipped, got %#v", scan.Files)
	}
	if !containsWarning(scan.Warnings, "skipped 1 large PHP file") || !containsWarning(scan.Warnings, fmt.Sprintf("%d bytes", testMaxScannablePHPFile)) {
		t.Fatalf("expected oversized PHP warning with byte limit, got %#v", scan.Warnings)
	}
	if !usageIncompleteForTest(t, scan) {
		t.Fatal("expected oversized PHP source to mark scan usage incomplete")
	}
}

func TestScanRepoMarksUsageIncompleteWhenUseStatementLimitHit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPUseStatementsPerFile+1; i++ {
		fmt.Fprintf(&content, "use Vendor\\Lib\\Thing%d;\n", i)
	}
	writeFile(t, filepath.Join(repo, "src", "adversarial-use.php"), content.String())

	scan := scanVendorLibRepo(t, repo, map[string]string{"Vendor\\Lib": helpersVendorLibDependency})
	assertIncompleteScanWarning(t, scan, "use statement cap", "stopped PHP use import scan")

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

func TestScanRepoMarksUsageIncompleteWhenNamespaceDeclarationLimitHit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	for i := 0; i < testMaxPHPNamespaceDeclarationsPerFile+1; i++ {
		content.WriteString("namespace {}\n")
	}
	writeFile(t, filepath.Join(repo, "src", "adversarial-namespaces.php"), content.String())

	scan := scanVendorLibRepo(t, repo, map[string]string{"Vendor\\Lib": helpersVendorLibDependency})
	assertIncompleteScanWarning(t, scan, "namespace declaration cap", "stopped PHP namespace declaration scan")
}

func TestScanRepoMarksUsageIncompleteWhenNamespaceResolutionSegmentLimitHit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	content := helpersPHPHeader + "$client = new \\" + deepNamespaceForTest("Vendor\\Lib", maxPHPNamespaceSegmentsPerLookup+1) + "();\n"
	writeFile(t, filepath.Join(repo, "src", "deep-namespace.php"), content)

	scan := scanVendorLibRepo(t, repo, map[string]string{"Vendor\\Lib": helpersVendorLibDependency})
	assertBoundedNamespaceResolutionWarning(t, scan, "deep namespace resolution cap")
}

func TestScanRepoMarksUsageIncompleteWhenNamespaceResolutionByteLimitHit(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, helpersComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, helpersVendorLibDependency))
	module := exactSegmentNamespaceWithHugeSegmentForTest(maxPHPNamespaceSegmentsPerLookup, int(maxScannablePHPFile)-4096)
	content := helpersPHPHeader + "$client = new \\" + module + "();\n"
	if int64(len(content)) >= maxScannablePHPFile {
		t.Fatalf("test fixture must stay below PHP file scan limit, got %d", len(content))
	}
	writeFile(t, filepath.Join(repo, "src", "wide-namespace.php"), content)

	scan := scanVendorLibRepo(t, repo, map[string]string{"Vendor": helpersVendorLibDependency})
	assertBoundedNamespaceResolutionWarning(t, scan, "wide namespace resolution cap")
}

func TestBuildDependencyReportSuppressesRemovalAdviceWhenUsageIncomplete(t *testing.T) {
	scan := scanResult{
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

	setUsageIncompleteForTest(t, &scan)

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

func scanVendorLibRepo(t *testing.T, repo string, namespaceToDep map[string]string) scanResult {
	t.Helper()
	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		NamespaceToDep:       namespaceToDep,
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf(helpersScanRepoErr, err)
	}
	return scan
}

func assertIncompleteScanWarning(t *testing.T, scan scanResult, reason string, warning string) {
	t.Helper()
	if !usageIncompleteForTest(t, scan) {
		t.Fatalf("expected %s to mark scan usage incomplete", reason)
	}
	if !containsWarning(scan.Warnings, warning) {
		t.Fatalf("expected %s warning %q, got %#v", reason, warning, scan.Warnings)
	}
}

func assertBoundedNamespaceResolutionWarning(t *testing.T, scan scanResult, reason string) {
	t.Helper()
	assertIncompleteScanWarning(t, scan, reason, "stopped PHP namespace resolution after")
	if containsWarning(scan.Warnings, "unable to map 1 PHP import namespace(s)") {
		t.Fatalf("did not expect bounded namespace resolution to be reported as unresolved mapping, got %#v", scan.Warnings)
	}
}

func TestShouldSkipDirAndDependencyHelpers(t *testing.T) {
	if !shouldSkipDir("vendor") {
		t.Fatalf("expected vendor to be skipped")
	}
	if !shouldSkipDir(".lopper-cache") {
		t.Fatalf("expected .lopper-cache to be skipped")
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

func TestComposerResolverUsesNamespaceAncestorsForPrefixLookup(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{
			"Vendor":          "vendor/root",
			"Vendor\\Package": "vendor/package",
		},
		localNamespace: map[string]struct{}{
			"App": {},
		},
	}

	if got := resolver.resolveWithPSR4(`Vendor\Package\Service\Client`); got != "vendor/package" {
		t.Fatalf("expected most-specific namespace dependency, got %q", got)
	}
	if got := resolver.resolveWithPSR4(`Vendor\Other\Client`); got != "vendor/root" {
		t.Fatalf("expected ancestor namespace dependency, got %q", got)
	}
	if resolver.isLocalNamespace(`Application\Service`) {
		t.Fatal("local namespace lookup must require a namespace boundary")
	}
	if !resolver.isLocalNamespace(`App\Service\Client`) {
		t.Fatal("expected local namespace ancestor to match")
	}
}

func TestComposerResolverBoundsCumulativeAncestorBytes(t *testing.T) {
	module := exactSegmentNamespaceWithHugeSegmentForTest(maxPHPNamespaceSegmentsPerLookup, int(maxScannablePHPFile)-4096)
	if segments := strings.Count(module, `\`) + 1; segments != maxPHPNamespaceSegmentsPerLookup {
		t.Fatalf("expected exact segment limit fixture, got %d segment(s)", segments)
	}

	resolver := composerResolver{
		namespaceToDep: map[string]string{"Nope": helpersVendorLibDependency},
		localNamespace: map[string]struct{}{"Local": {}},
	}
	start := time.Now()
	for i := 0; i < 20; i++ {
		if resolution := resolver.resolveModule(module); !resolution.limitHit {
			t.Fatalf("expected namespace byte-work limit hit, got %#v", resolution)
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("expected bounded namespace lookup to finish quickly, took %s", elapsed)
	}

	if ancestors, limitHit := namespaceAncestors(module, maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes); !limitHit || len(ancestors) != 0 {
		t.Fatalf("expected ancestor byte-work limit hit without ancestors, limitHit=%v ancestors=%d", limitHit, len(ancestors))
	}
}

func TestComposerResolverLimitAndPrefixBranches(t *testing.T) {
	module := exactSegmentNamespaceWithHugeSegmentForTest(maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes+1)
	resolver := composerResolver{
		namespaceToDep: map[string]string{"App\\Lib\\": helpersVendorLibDependency},
		localNamespace: map[string]struct{}{"App\\": {}},
	}
	if resolver.isLocalNamespace(module) {
		t.Fatalf("expected namespace limit hit not to report local namespace")
	}
	if got := resolver.resolveWithPSR4(module); got != "" {
		t.Fatalf("expected namespace limit hit not to resolve PSR-4 dependency, got %q", got)
	}
	if !hasNamespacePrefix([]string{"App"}, resolver.localNamespace) {
		t.Fatalf("expected trailing-backslash namespace prefix to match")
	}
	if ancestors, limitHit := namespaceAncestors("", maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes); limitHit || len(ancestors) != 0 {
		t.Fatalf("expected empty namespace to return no ancestors without limit, ancestors=%#v limit=%v", ancestors, limitHit)
	}
	if ancestors, limitHit := namespaceAncestors("App\\Lib", 0, maxPHPNamespaceAncestorBytes); !limitHit || len(ancestors) != 0 {
		t.Fatalf("expected non-positive segment limit to hit, ancestors=%#v limit=%v", ancestors, limitHit)
	}
	if ancestors, limitHit := namespaceAncestors("A\\B\\C", 10, len("A\\B\\C")+1); !limitHit || len(ancestors) != 0 {
		t.Fatalf("expected cumulative ancestor byte limit to hit, ancestors=%#v limit=%v", ancestors, limitHit)
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

func TestParseUseStatementAcceptsNonASCIINamespaceInitial(t *testing.T) {
	const dependency = "editeur/package"
	resolver := composerResolver{namespaceToDep: map[string]string{"Éditeur\\Package": dependency}}
	imports, _, unresolved := parseUseStatement("Éditeur\\Package\\Client", "x.php", 1, resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 1 {
		t.Fatalf("expected one import, got %#v", imports)
	}
	if imports[0].Dependency != dependency || imports[0].Module != "Éditeur\\Package\\Client" {
		t.Fatalf("expected non-ASCII namespace import to resolve, got %#v", imports[0])
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

func TestParseNamespaceReferencesSkipsSemicolonSeparatedUseDeclarations(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{
			"Foo":             "foo/a",
			"Vendor\\Package": "vendor/package",
		},
	}
	imports, unresolved := parseNamespaceReferences([]byte(helpersPHPHeader+"use Foo\\A; use Vendor\\Package\\B;\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 0 {
		t.Fatalf("expected no namespace imports from semicolon-separated use declarations, got %#v", imports)
	}
}

func TestParsePHPImportsIgnoresUseStatementsInInactiveTemplateText(t *testing.T) {
	content := []byte("<div>To use Vendor\\Package\\Client;</div>\n" +
		"<p>\\Vendor\\Package\\Factory is documentation only.</p>\n" +
		"<?php echo 'active code without imports';\n")

	assertParsedVendorPackageModules(t, "template.php", content, nil, "expected inactive template namespaces to be ignored")
}

func TestParsePHPImportsParsesValidMultiRegionPHPOnly(t *testing.T) {
	content := []byte("<div>use Vendor\\Package\\TemplateOnly;</div>\n" +
		"<?php\n" +
		"use Vendor\\Package\\Client as ClientAlias;\n" +
		"?>\n" +
		"<span>\\Vendor\\Package\\HtmlOnly</span>\n" +
		"<?php\n" +
		"$factory = new \\Vendor\\Package\\Factory();\n")

	assertParsedVendorPackageModules(t, "multi-region.php", content,
		[]string{"Vendor\\Package\\Client", "Vendor\\Package\\Factory"},
		"expected one active use import and one active namespace reference")
}

func TestParsePHPImportsKeepsPHPActiveAcrossHeredocCloseTagText(t *testing.T) {
	content := []byte("<?php\n" +
		"$html = <<<HTML\n" +
		"<div>use Vendor\\Package\\TemplateOnly;</div>\n" +
		"?>\n" +
		"HTML;\n" +
		"use Vendor\\Package\\Client;\n")

	assertParsedVendorPackageModules(t, "heredoc-region.php", content,
		[]string{"Vendor\\Package\\Client"},
		"expected only the post-heredoc active import")
}

func TestParsePHPImportsMasksSecondHeredocOnTerminatorLineBeforeTraitUse(t *testing.T) {
	content := []byte("<?php\n" +
		"final class Service\n" +
		"{\n" +
		"    public function template(): string\n" +
		"    {\n" +
		"        return <<<ONE\n" +
		"}\n" +
		"ONE; $second = <<<'TWO'\n" +
		"}\n" +
		"TWO;\n" +
		"    }\n" +
		"\n" +
		"    use \\Vendor\\Package\\FeatureTrait {\n" +
		"        handle as private;\n" +
		"    }\n" +
		"}\n")

	assertParsedVendorPackageModules(t, "same-line-second-heredoc.php", content,
		[]string{"Vendor\\Package\\FeatureTrait"},
		"expected one class body trait use import")
}

func TestParsePHPImportsKeepsPHPActiveAfterFlexibleHeredocTerminators(t *testing.T) {
	tests := []struct {
		name       string
		terminator string
	}{
		{name: "expression delimiter", terminator: "HTML, 'suffix'];"},
		{name: "whitespace and line comment", terminator: "   HTML   ; // done"},
		{name: "block comment before delimiter", terminator: "HTML /* comment */ ;"},
		{name: "logical word operator", terminator: "HTML and print \"x\";"},
		{name: "instanceof word operator", terminator: "HTML instanceof Stringable;"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("<?php\n" +
				"$html = [<<<HTML\n" +
				"}\n" +
				tc.terminator + "\n" +
				"use Vendor\\Package\\Client;\n")

			assertParsedVendorPackageModules(t, tc.name+".php", content,
				[]string{"Vendor\\Package\\Client"},
				"expected import after flexible heredoc terminator")
		})
	}
}

func TestParsePHPImportsReturnsToTemplateAfterHeredocTerminatorCloseTag(t *testing.T) {
	tests := []struct {
		name   string
		opener string
		closer string
	}{
		{
			name:   "semicolon close tag",
			opener: "$html = <<<TXT\n",
			closer: "TXT; ?>",
		},
		{
			name:   "line comment close tag",
			opener: "$html = <<<TXT\n",
			closer: "TXT; // done ?>",
		},
		{
			name:   "hash comment close tag",
			opener: "$html = <<<'TXT'\n",
			closer: "    TXT; # done ?>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte("<?php\n" +
				tc.opener +
				"<div>use Vendor\\Package\\HeredocBody;</div>\n" +
				tc.closer + "\n" +
				"<template>use Vendor\\Package\\TemplateOnly;</template>\n" +
				"<?php use Vendor\\Package\\Client;\n")

			assertParsedVendorPackageModules(t, tc.name+".php", content,
				[]string{"Vendor\\Package\\Client"},
				"expected only the reopened active PHP import")
		})
	}
}

func TestParsePHPImportsDoesNotTreatArbitraryHeredocLabelTailAsTerminator(t *testing.T) {
	content := []byte("<?php\n" +
		"$html = <<<HTML\n" +
		"}\n" +
		"HTML garbage\n" +
		"use Vendor\\Package\\Client;\n")

	assertParsedVendorPackageModules(t, "invalid-heredoc-tail.php", content, nil,
		"expected unterminated heredoc body to mask later imports")
}

func TestParsePHPImportsParsesConfiguredShortOpenTags(t *testing.T) {
	content := []byte("<section>use Vendor\\Package\\TemplateOnly;</section>\n" +
		"<? use Vendor\\Package\\Client;\n" +
		"$factory = new \\Vendor\\Package\\Factory(); ?>\n")

	resolver := composerResolver{
		namespaceToDep:        map[string]string{"Vendor\\Package": "vendor/package"},
		allowPHPShortOpenTags: true,
	}
	parsed := parsePHPImports(content, "short-open.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\Client", "Vendor\\Package\\Factory"})
}

func TestParsePHPImportsHonorsDisabledShortOpenTags(t *testing.T) {
	content := []byte("<?php(use Vendor\\Package\\TemplateOnly; ?>\n" +
		"<?php use Vendor\\Package\\Client;\n")

	assertParsedVendorPackageModules(t, "short-disabled.php", content,
		[]string{"Vendor\\Package\\Client"},
		"expected malformed long PHP tags to stay inactive when short tags are disabled")
}

func TestParsePHPImportsResolvesNamespaceRelativeTraitUseAsLocalWithShortOpenTag(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep:        map[string]string{"Vendor\\Package": "vendor/package"},
		localNamespace:        map[string]struct{}{"App": {}},
		allowPHPShortOpenTags: true,
	}
	parsed := parsePHPImports([]byte("<? namespace App; class C { use Vendor\\Package\\FeatureTrait; }"), "short-tag-trait.php", resolver)
	if parsed.unresolvedCount != 0 {
		t.Fatalf("expected short-tag namespace-relative trait use to resolve locally, got %d unresolved", parsed.unresolvedCount)
	}
	if len(parsed.imports) != 0 {
		t.Fatalf("expected namespace-relative trait use to stay local, got %#v", parsed.imports)
	}
}

func TestParsePHPImportsResolvesNamespaceRelativeTraitUseAfterEmptyPHPRegions(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"App": {}},
	}
	parsed := parsePHPImports([]byte("<?php ?><?php namespace App; class C { use Vendor\\Package\\FeatureTrait; }"), "empty-regions-trait.php", resolver)
	if parsed.unresolvedCount != 0 || len(parsed.imports) != 0 {
		t.Fatalf("expected namespace-relative trait use after empty PHP regions to stay local, got %#v", parsed)
	}
}

func TestParsePHPImportsParsesEmptyTraitAdaptationBlock(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	parsed := parsePHPImports([]byte("<?php class C { use Vendor\\Package\\Feature {} }"), "empty-trait-adaptation.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\Feature"})
}

func TestParsePHPImportsParsesUseStatementTerminatedByPHPCloseTag(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	parsed := parsePHPImports([]byte("<?php use Vendor\\Package\\Client ?>"), "close-tag-import.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\Client"})
}

func TestParsePHPImportsResolvesNamespaceRelativeTraitUseAfterNamespaceCloseTag(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"App": {}},
	}
	parsed := parsePHPImports([]byte("<?php namespace App ?><?php class C { use Vendor\\Package\\FeatureTrait; }"), "close-tag-namespace.php", resolver)
	if parsed.unresolvedCount != 0 || len(parsed.imports) != 0 {
		t.Fatalf("expected close-tag namespace-relative trait use to stay local, got %#v", parsed)
	}
}

func TestParsePHPImportsTracksSameLineNamespaceAfterStatement(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"B\\Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"A\\Vendor": {}},
	}
	parsed := parsePHPImports([]byte("<?php namespace A; echo 1; namespace B; class C { use Vendor\\Package\\FeatureTrait; }"), "same-line-namespace.php", resolver)
	assertImportModules(t, parsed.imports, []string{"B\\Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsTracksNamespaceAcrossSameLineCloseOpenTransition(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"B\\Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"A\\Vendor": {}},
	}
	parsed := parsePHPImports([]byte("<?php namespace A ?><?php namespace B ?><?php class C { use Vendor\\Package\\FeatureTrait; }"), "close-open-namespace.php", resolver)
	assertImportModules(t, parsed.imports, []string{"B\\Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsTracksNamespaceAcrossSameLineShortTagTransition(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep:        map[string]string{"B\\Vendor\\Package": "vendor/package"},
		localNamespace:        map[string]struct{}{"A\\Vendor": {}},
		allowPHPShortOpenTags: true,
	}
	parsed := parsePHPImports([]byte("<? namespace A ?><? namespace B ?><? class C { use Vendor\\Package\\FeatureTrait; }"), "short-tag-close-open-namespace.php", resolver)
	assertImportModules(t, parsed.imports, []string{"B\\Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsTracksNamespaceAcrossSameLineInlineHTMLTransition(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"B\\Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"A\\Vendor": {}},
	}
	parsed := parsePHPImports([]byte("<?php namespace A ?>hello<?php namespace B; class C { use Vendor\\Package\\FeatureTrait; }"), "inline-html-close-open-namespace.php", resolver)
	assertImportModules(t, parsed.imports, []string{"B\\Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsResolvesNamespaceRelativeTraitUseInGlobalNamespace(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	parsed := parsePHPImports([]byte("<?php namespace { class C { use namespace\\Vendor\\Package\\FeatureTrait; } }"), "global-namespace-relative.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsResolvesNamespaceRelativeTraitUseInNamedNamespace(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"App\\Vendor\\Package": "vendor/package"}}
	parsed := parsePHPImports([]byte("<?php namespace App; class C { use namespace\\Vendor\\Package\\FeatureTrait; }"), "named-namespace-relative.php", resolver)
	assertImportModules(t, parsed.imports, []string{"App\\Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsPreservesClassContextAcrossLongHeaders(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"App\\Vendor": {}},
	}
	content := "<?php namespace App; class C " + strings.Repeat(" ", maxPHPNamespaceAncestorBytes+1) + "{ use Vendor\\Package\\FeatureTrait; }"
	parsed := parsePHPImports([]byte(content), "long-class-header.php", resolver)
	if parsed.unresolvedCount != 0 || len(parsed.imports) != 0 {
		t.Fatalf("expected trait in long class header to resolve as local, got %#v", parsed)
	}
}

func TestParsePHPImportsSkipsEmptyGroupedNamespaceAliases(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"App\\Package": {}},
	}
	content := []byte("<?php namespace App; use Vendor\\Package\\{Client,}; class C { use Package\\FeatureTrait; }")
	parsed := parsePHPImports(content, "trailing-grouped-use.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\Client"})
}

func TestParsePHPImportsResolvesImportedAliasBeforeTraitNamespaceLookup(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{
		"Acme\\Other\\Package": "acme/other-package",
		"Vendor\\Package":      "vendor/package",
	}}
	parsed := parsePHPImports([]byte("<?php use Acme\\Other as Vendor; class C { use Vendor\\Package\\FeatureTrait; }"), "trait-import-alias.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Acme\\Other\\Package\\FeatureTrait"})
}

func TestParsePHPImportsResetsAliasesForSeparateGlobalNamespaceBlocks(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{
		"Acme\\Other":          "acme/other",
		"Acme\\Other\\Package": "acme/other-package",
		"Vendor\\Package":      "vendor/package",
	}}
	content := []byte("<?php namespace { use Acme\\Other as Vendor; } namespace { class C { use Vendor\\Package\\FeatureTrait; } }")
	parsed := parsePHPImports(content, "separate-global-namespaces.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Acme\\Other", "Vendor\\Package\\FeatureTrait"})
}

func TestParsePHPImportsExcludesFunctionAndConstAliasesFromTraitNamespaceLookup(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{
		"Vendor\\Package":      "vendor/package",
		"VendorConst\\Package": "vendor/const-package",
	}}
	content := []byte("<?php use function Acme\\Other as Vendor; class FunctionAlias { use Vendor\\Package\\FeatureTrait; } use const Acme\\Constants as VendorConst; class ConstAlias { use VendorConst\\Package\\FeatureTrait; }")
	parsed := parsePHPImports(content, "trait-nonclass-alias.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\FeatureTrait", "VendorConst\\Package\\FeatureTrait"})
}

func TestParsePHPImportsExcludesWhitespaceSeparatedNonClassAliasesFromTraitNamespaceLookup(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{
		"Vendor\\Package":      "vendor/package",
		"VendorConst\\Package": "vendor/const-package",
	}}
	content := []byte("<?php use function\tAcme\\Other as Vendor; class FunctionAlias { use Vendor\\Package\\FeatureTrait; } use const\nAcme\\Constants as VendorConst; class ConstAlias { use VendorConst\\Package\\FeatureTrait; }")
	parsed := parsePHPImports(content, "trait-whitespace-nonclass-alias.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\FeatureTrait", "VendorConst\\Package\\FeatureTrait"})
}

func TestHasUseImportQualifierAcceptsPHPWhitespace(t *testing.T) {
	for _, separator := range []string{" ", "\t", "\n", "\r", "\v", "\f"} {
		if !hasUseImportQualifier("function"+separator+"Acme\\Other", "function") {
			t.Fatalf("expected function qualifier followed by %q to be recognized", separator)
		}
		if !hasUseImportQualifier("const"+separator+"Acme\\Other", "const") {
			t.Fatalf("expected const qualifier followed by %q to be recognized", separator)
		}
	}
}

func TestParsePHPImportsTreatsXMLCallAsShortTagPHPWhenEnabled(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep:        map[string]string{"Vendor\\Package": "vendor/package"},
		allowPHPShortOpenTags: true,
	}
	for _, tc := range []struct {
		name   string
		opener string
	}{
		{name: "static call", opener: "<?xml ::foo();"},
		{name: "hyphenated call", opener: "<?xml-foo();"},
		{name: "stylesheet-like call without a close tag", opener: "<?xml-stylesheet?foo():bar();"},
		{name: "stylesheet-like call after whitespace", opener: "<?xml-stylesheet ();"},
		{name: "stylesheet-like call without target whitespace", opener: "<?xml-stylesheettype==\"x\";"},
		{name: "stylesheet-like invalid pseudo-attribute", opener: "<?xml-stylesheet -foo==\"x\";"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(tc.opener + "\nuse Vendor\\Package\\Client;\n")
			parsed := parsePHPImports(content, "xml-call.php", resolver)
			assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\Client"})
		})
	}
}

func TestParsePHPImportsReturnsToTemplateAfterCommentCloseTags(t *testing.T) {
	tests := []struct {
		name   string
		region string
	}{
		{name: "php line comment", region: "<?php // done ?>"},
		{name: "php hash comment", region: "<?php # done ?>"},
		{name: "php block comment", region: "<?php /* done */ ?>"},
		{name: "php string before close", region: "<?php echo '?>'; ?>"},
		{name: "echo tag line comment", region: "<?= // done ?>"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(tc.region + "\n" +
				"<template>use Vendor\\Package\\TemplateOnly;</template>\n" +
				"<?php use Vendor\\Package\\Client;\n")

			assertParsedVendorPackageModules(t, tc.name+".php", content,
				[]string{"Vendor\\Package\\Client"},
				"expected template namespace after close tag to stay inactive")
		})
	}
}

func TestParsePHPImportsExcludesXMLDeclarationsWhenShortOpenTagsAreSupported(t *testing.T) {
	content := []byte("<?xml version=\"1.0\"?>\n" +
		"<root>use Vendor\\Package\\TemplateOnly;</root>\n" +
		"<?xml-stylesheet type=\"text/xsl\" href=\"style.xsl\"?>\n" +
		"<? use Vendor\\Package\\Client; ?>\n")

	resolver := composerResolver{
		namespaceToDep:        map[string]string{"Vendor\\Package": "vendor/package"},
		allowPHPShortOpenTags: true,
	}
	parsed := parsePHPImports(content, "xml-template.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Vendor\\Package\\Client"})
}

func TestXMLStylesheetProcessingInstructionRequiresXMLNameStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{name: "ASCII name", text: "<?xml-stylesheet type=\"text/xsl\"?>", want: true},
		{name: "Unicode name", text: "<?xml-stylesheet π=\"text/xsl\"?>", want: true},
		{name: "hyphen", text: "<?xml-stylesheet -foo=\"x\"?>", want: false},
		{name: "digit", text: "<?xml-stylesheet 2foo=\"x\"?>", want: false},
		{name: "dollar", text: "<?xml-stylesheet $foo=\"x\"?>", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isXMLStylesheetProcessingInstructionOpenTag(tc.text, 0); got != tc.want {
				t.Fatalf("isXMLStylesheetProcessingInstructionOpenTag() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMaskInactivePHPRegionsHelperBranches(t *testing.T) {
	masked := maskInactivePHPRegions("<div>use Vendor\\Package\\Client;</div>\n<?php use Vendor\\Package\\Real; ?>")
	if strings.Contains(masked, "Client") {
		t.Fatalf("expected inactive template content to be masked, got %q", masked)
	}
	if !strings.Contains(masked, "Vendor\\Package\\Real") {
		t.Fatalf("expected active PHP content to remain visible, got %q", masked)
	}
}

func TestImportParserUseStatementMaskAndLimitHelpers(t *testing.T) {
	if got := maskUseStatementRanges("", nil, nil); got != "" {
		t.Fatalf("expected empty mask result, got %q", got)
	}
	chained := findPHPUseStatementMatches("<?php use Foo\\A; use Foo\\B;", 2)
	if len(chained) != 2 {
		t.Fatalf("expected capped same-line use chain to return two matches, got %#v", chained)
	}
	kept := appendPHPUseStatementMatch([]phpUseStatementMatch{{start: 1}}, phpUseStatementMatch{start: 2}, 1)
	if len(kept) != 1 || kept[0].start != 1 {
		t.Fatalf("expected append to respect existing limit, got %#v", kept)
	}
}

func TestImportParserRejectsMalformedSameLineUseStatements(t *testing.T) {
	for _, text := range []string{"use", "use ($capture);", "use Foo\\A"} {
		assertNoSameLineUseStatement(t, text)
	}
	assertNoSameLineUseStatement(t, "\nuse Foo\\A;")
}

func TestImportParserAcceptsValidSameLineUseStatement(t *testing.T) {
	if match, ok := nextSameLineUseStatement("  use Foo\\A;", 0); !ok || match.statementStart != len("  use ") {
		t.Fatalf("expected same-line use declaration to parse, got %#v ok=%v", match, ok)
	}
}

func TestImportParserRejectsEmbeddedUseKeyword(t *testing.T) {
	if match, ok := phpUseStatementAt("abuse Foo\\A;", 2); ok || match != (phpUseStatementMatch{}) {
		t.Fatalf("expected embedded use keyword to be rejected, got %#v", match)
	}
}

func TestImportParserScansPastMalformedUseStatement(t *testing.T) {
	malformed := scanPHPUseStatements("<?php use Vendor\\Package\\Missing\nuse Vendor\\Package\\Client;", 0)
	if len(malformed.ranges) != 2 || len(malformed.matches) != 1 {
		t.Fatalf("expected malformed use range plus valid match, got %#v", malformed)
	}
}

func TestParsePHPImportsContinuesMultilineUseWhenNamespaceSegmentIsUse(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{
		"Foo\\Bar": "foo/bar",
		"Use\\Baz": "use/baz",
	}}
	content := []byte(helpersPHPHeader +
		"use Foo\\Bar,\n" +
		"    Use\\Baz;\n")

	parsed := parsePHPImports(content, "use-segment.php", resolver)
	assertImportModules(t, parsed.imports, []string{"Foo\\Bar", "Use\\Baz"})
}

func assertNoSameLineUseStatement(t *testing.T, text string) {
	t.Helper()
	if match, ok := nextSameLineUseStatement(text, 0); ok || match != (phpUseStatementMatch{}) {
		t.Fatalf("expected %q not to parse as same-line use declaration, got %#v ok=%v", text, match, ok)
	}
}

func assertImportModules(t *testing.T, imports []importBinding, want []string) {
	t.Helper()
	got := make([]string, 0, len(imports))
	for _, imp := range imports {
		got = append(got, imp.Module)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("expected import modules %v, got %v from %#v", want, got, imports)
	}
}

func assertParsedVendorPackageModules(t *testing.T, filePath string, content []byte, want []string, reason string) {
	t.Helper()
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	parsed := parsePHPImports(content, filePath, resolver)
	if parsed.unresolvedCount != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, parsed.unresolvedCount)
	}
	if len(parsed.imports) != len(want) {
		t.Fatalf("%s, got %#v", reason, parsed.imports)
	}
	assertImportModules(t, parsed.imports, want)
}

func TestImportParserContextTrackerHelperBranches(t *testing.T) {
	trackerText := "namespace App { class C {} }"
	declarations, _ := findNamespaceDeclarationsWithShortOpenTags(trackerText, false)
	tracker := newPHPContextTracker(trackerText, declarations)
	if context := tracker.advanceTo(len(trackerText) + 10); context.namespace != "" || context.classBody {
		t.Fatalf("expected bracketed namespace context to restore after close, got %#v", context)
	}
	if context := tracker.advanceTo(1); context.namespace != "" || context.classBody {
		t.Fatalf("expected backward context lookup to keep current context, got %#v", context)
	}
	tracker.popBraceFrame()

	anonymousClass := "return new class($factory, function () { return new \\stdClass(); }) extends \\stdClass {"
	classLikeBraces := findPHPClassLikeBraceOffsets(anonymousClass)
	if _, ok := classLikeBraces[strings.IndexByte(anonymousClass, '{')]; ok {
		t.Fatalf("expected linear brace index to skip closure argument body")
	}
	if _, ok := classLikeBraces[len(anonymousClass)-1]; !ok {
		t.Fatalf("expected linear brace index to mark anonymous class body")
	}
	if got := traitUseList("Vendor\\Package\\FeatureTrait { handle as alias"); got != "Vendor\\Package\\FeatureTrait" {
		t.Fatalf("expected trait adaptation list to keep only trait names, got %q", got)
	}
}

func TestClassLikeDeclarationScanStartDelimiterBranches(t *testing.T) {
	balancedBracket := "return new class([function () { return 1; }]) {"
	classLikeBraces := findPHPClassLikeBraceOffsets(balancedBracket)
	if _, ok := classLikeBraces[len(balancedBracket)-1]; !ok {
		t.Fatalf("expected balanced bracket expression inside anonymous class declaration to stay class-like")
	}
}

func TestImportParserNamespaceDeclarationHelperBranches(t *testing.T) {
	completion := phpNamespaceLineCompletion{}
	completion.advanceTo("namespace A; ", len("namespace A; "))
	if !completion.followsCompletedStatement() {
		t.Fatalf("expected semicolon namespace declaration to allow following same-line namespace")
	}
	completion = phpNamespaceLineCompletion{}
	completion.advanceTo("namespace A { class C {} } ", len("namespace A { class C {} } "))
	if !completion.followsCompletedStatement() {
		t.Fatalf("expected completed namespace block to allow following same-line namespace")
	}
	completion = phpNamespaceLineCompletion{}
	completion.advanceTo("declare(strict_types=1); namespace A; ", len("declare(strict_types=1); namespace A; "))
	if !completion.followsCompletedStatement() {
		t.Fatalf("expected namespace after previous same-line statement to allow following namespace")
	}
	completion = phpNamespaceLineCompletion{}
	completion.advanceTo("doWork(); ", len("doWork(); "))
	if !completion.followsCompletedStatement() {
		t.Fatalf("expected a completed same-line statement to allow following namespace")
	}
	completion = phpNamespaceLineCompletion{}
	completion.advanceTo("namespace A ", len("namespace A "))
	if completion.followsCompletedStatement() {
		t.Fatalf("expected incomplete namespace declaration not to allow following namespace")
	}
}

func TestImportParserMaskLineAndSplitHelperBranches(t *testing.T) {
	if masked := maskMatchedGroup("abc", nil, [][]int{{0}}); len(masked) != 0 {
		t.Fatalf("expected malformed mask range to be ignored, got %q", string(masked))
	}
	lineIndex := newPHPLineIndex("abc")
	if got := lineIndex.lineNumberAt(99); got != 1 {
		t.Fatalf("expected out-of-range offset to clamp to line 1, got %d", got)
	}
	if parts, limitHit := splitUseParts("Vendor\\Lib\\A", 0); !limitHit || len(parts) != 0 {
		t.Fatalf("expected non-positive part limit to hit limit, parts=%#v limit=%v", parts, limitHit)
	}
	trailingCommaUse := strings.Repeat("Vendor\\Lib\\A,", testMaxPHPUseStatementsPerFile)
	if parts, limitHit := splitUseParts(trailingCommaUse, testMaxPHPUseStatementsPerFile); limitHit || len(parts) != testMaxPHPUseStatementsPerFile {
		t.Fatalf("expected trailing comma after part limit to stay complete, parts=%d limit=%v", len(parts), limitHit)
	}
	if parts, limitHit := splitUseParts(trailingCommaUse+"Vendor\\Lib\\B", testMaxPHPUseStatementsPerFile); !limitHit || len(parts) != testMaxPHPUseStatementsPerFile {
		t.Fatalf("expected nonempty part after limit to remain incomplete, parts=%d limit=%v", len(parts), limitHit)
	}
	if got := lineTextAt("a\nb", 3); got != "" {
		t.Fatalf("expected out-of-range line text to be empty, got %q", got)
	}
	if masked := withMaskedPHPHeredocRange("abc", nil, 2, 2); len(masked) != 0 {
		t.Fatalf("expected empty heredoc mask range to stay nil, got %q", string(masked))
	}
	if got := usePartLocalLine("Thing as Alias", 0, "Alias"); got != 0 {
		t.Fatalf("expected non-positive base line to remain unchanged, got %d", got)
	}
	if got := usePartLocalLine("Thing as Alias", 7, "Missing"); got != 7 {
		t.Fatalf("expected missing local token to keep base line, got %d", got)
	}
	if got := usePartLocalLine("Thing as Alias", 7, ""); got != 7 {
		t.Fatalf("expected empty local token to keep base line, got %d", got)
	}
}

func TestPHPHeredocNowdocMaskingKeepsPlainTextUnchanged(t *testing.T) {
	plain := "final class Service {}\n"
	if got := maskPHPHeredocNowdocBodies(plain); got != plain {
		t.Fatalf("expected text without heredoc to remain unchanged, got %q", got)
	}
}

func TestPHPHeredocNowdocMaskingMasksBodyOnly(t *testing.T) {
	content := "return <<<\"HTML\"\n}\nHTML;\nfinal class Service {}\n"
	masked := maskPHPHeredocNowdocBodies(content)
	if strings.Contains(masked, "\n}\n") {
		t.Fatalf("expected heredoc body brace to be masked, got %q", masked)
	}
	if !strings.Contains(masked, "final class Service") {
		t.Fatalf("expected code after heredoc to remain visible, got %q", masked)
	}
}

func TestPHPHeredocNowdocMaskingScansSecondOpenerOnTerminatorLine(t *testing.T) {
	content := "return <<<ONE\n" +
		"class FirstBody {}\n" +
		"ONE; $second = <<<'TWO'\n" +
		"class SecondBody {}\n" +
		"TWO;\n" +
		"final class Service {}\n"

	masked := maskPHPHeredocNowdocBodies(content)
	if strings.Contains(masked, "FirstBody") || strings.Contains(masked, "SecondBody") {
		t.Fatalf("expected both heredoc bodies to be masked, got %q", masked)
	}
	if !strings.Contains(masked, "$second = <<<'TWO'") {
		t.Fatalf("expected same-line second opener to remain visible, got %q", masked)
	}
	if !strings.Contains(masked, "final class Service") {
		t.Fatalf("expected code after second heredoc to remain visible, got %q", masked)
	}
}

func TestPHPHeredocNowdocMaskingMasksUnterminatedBody(t *testing.T) {
	unterminated := "return <<<TXT\n}\n"
	if got := maskPHPHeredocNowdocBodies(unterminated); strings.Contains(got, "}") {
		t.Fatalf("expected unterminated heredoc body to be masked, got %q", got)
	}
}

func TestPHPHeredocNowdocRejectsInvalidOpeners(t *testing.T) {
	for _, line := range []string{"return 1;", "return <<<;", "return <<<'TXT;"} {
		if label, ok := heredocNowdocLabel(line); ok || label != "" {
			t.Fatalf("expected invalid heredoc opener %q to be rejected, label=%q ok=%v", line, label, ok)
		}
	}
}

func TestPHPHeredocNowdocHelperBranches(t *testing.T) {
	if label, ok := heredocNowdocLabel("return <<<'TXT'"); !ok || label != "TXT" {
		t.Fatalf("expected nowdoc label to parse, label=%q ok=%v", label, ok)
	}
	if label, ok := parseHeredocNowdocLabelAfterMarker(""); ok || label != "" {
		t.Fatalf("expected empty heredoc marker suffix to be rejected, label=%q ok=%v", label, ok)
	}
	if end := findHeredocNowdocTerminator("body\n", 0, "TXT"); end != -1 {
		t.Fatalf("expected missing heredoc terminator to return -1, got %d", end)
	}
	if !isHeredocNowdocTerminatorLine("TXT\r", "TXT") {
		t.Fatalf("expected CR-terminated heredoc label to be accepted")
	}
	for _, line := range []string{"TXT   ;   // comment", "TXT, 'next'", "TXT /* comment */ )\r", "TXT # comment", "TXT and print \"x\"", "TXT instanceof Stringable", "TXT as $item"} {
		if !isHeredocNowdocTerminatorLine(line, "TXT") {
			t.Fatalf("expected flexible heredoc terminator %q to be accepted", line)
		}
	}
	for _, line := range []string{"TXT_MORE;", "TXT garbage", "TXT /* unclosed", "NOTTXT;"} {
		if isHeredocNowdocTerminatorLine(line, "TXT") {
			t.Fatalf("expected arbitrary heredoc terminator tail %q to be rejected", line)
		}
	}
	if end := nextPHPLineEnd("abc", 0); end != 3 {
		t.Fatalf("expected no-newline line end to be len, got %d", end)
	}
	if start := nextPHPLineStart("abc", 3); start != 3 {
		t.Fatalf("expected terminal line start to remain at end, got %d", start)
	}
}

func TestPHPCodeStateHelperBranches(t *testing.T) {
	state := phpStateLineComment
	if next := advancePHPCodeState("\n", 0, &state); next != 1 || state != phpStateCode {
		t.Fatalf("expected line comment newline to reset state, next=%d state=%v", next, state)
	}
	state = phpStateBlockComment
	if next := advancePHPCodeState("*/", 0, &state); next != 2 || state != phpStateCode {
		t.Fatalf("expected block comment terminator to reset state, next=%d state=%v", next, state)
	}
	state = phpStateCode
	if next := advancePHPCodeState("`cmd`", 0, &state); next != 1 || state != phpStateBacktick {
		t.Fatalf("expected backtick to enter quoted state, next=%d state=%v", next, state)
	}
	if next := advancePHPCodeState("`cmd`", 4, &state); next != 5 || state != phpStateCode {
		t.Fatalf("expected backtick terminator to reset state, next=%d state=%v", next, state)
	}
	state = phpCodeState(99)
	if next := advancePHPCodeState("x", 0, &state); next != 1 || state != phpStateCode {
		t.Fatalf("expected unknown state to reset to code, next=%d state=%v", next, state)
	}
}

func TestImportParserUseResolutionLimitHelperBranches(t *testing.T) {
	hugeModule := exactSegmentNamespaceWithHugeSegmentForTest(maxPHPNamespaceSegmentsPerLookup, maxPHPNamespaceAncestorBytes+1)
	limitResolver := composerResolver{}
	if _, _, _, resolutionLimitHit := parseUseParts([]string{hugeModule}, "", "x.php", 1, limitResolver, false); !resolutionLimitHit {
		t.Fatalf("expected ordinary use part to report namespace resolution limit")
	}
	if _, _, _, resolutionLimitHit := parseClassBodyUseParts([]string{hugeModule}, "x.php", 1, limitResolver, "", nil); !resolutionLimitHit {
		t.Fatalf("expected class-body use part to report namespace resolution limit")
	}
	if _, _, _, unresolved, _ := parseClassBodyUsePart("Unknown\\Pkg\\Trait", "x.php", 1, composerResolver{}, "", nil); !unresolved {
		t.Fatalf("expected unresolved class-body trait use to be counted")
	}
	if _, _, unresolved, resolutionLimitHit := parseClassBodyUseParts([]string{"Unknown\\Pkg\\Trait"}, "x.php", 1, composerResolver{}, "", nil); unresolved == 0 || resolutionLimitHit {
		t.Fatalf("expected class-body use parts to count unresolved trait without limit, unresolved=%d limit=%v", unresolved, resolutionLimitHit)
	}
	if binding, dep, ok, unresolved, limitHit := parseClassBodyUsePart("", "x.php", 1, composerResolver{}, "", nil); ok || unresolved || limitHit || dep != "" || binding != (importBinding{}) {
		t.Fatalf("expected empty class-body use part to be ignored, binding=%#v dep=%q unresolved=%v limit=%v ok=%v", binding, dep, unresolved, limitHit, ok)
	}
	if binding, dep, ok, unresolved, limitHit := parseUsePart(hugeModule, "", "x.php", 1, limitResolver); ok || unresolved || !limitHit || dep != "" || binding != (importBinding{}) {
		t.Fatalf("expected ordinary use part limit hit, binding=%#v dep=%q unresolved=%v limit=%v ok=%v", binding, dep, unresolved, limitHit, ok)
	}

	parsed := parsePHPImports([]byte(helpersPHPHeader+"use "+hugeModule+";\n"), "x.php", limitResolver)
	if !parsed.namespaceResolutionLimitHit {
		t.Fatalf("expected parse result to record namespace resolution limit")
	}
}

func TestParseNamespaceReferencesSkipsNamespaceDeclaration(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	imports, unresolved := parseNamespaceReferences([]byte(helpersPHPHeader+"namespace Vendor\\Package;\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 0 {
		t.Fatalf("expected no namespace imports from namespace declaration, got %#v", imports)
	}
}

func TestParseNamespaceReferencesSkipsSameLineDeclareNamespaceDeclaration(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	imports, unresolved := parseNamespaceReferences([]byte("<?php declare(strict_types=1); namespace Vendor\\Package;\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 0 {
		t.Fatalf("expected no namespace imports from same-line declare namespace declaration, got %#v", imports)
	}
}

func TestParseNamespaceReferencesSkipsSameLineNamespaceAfterStatement(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	imports, unresolved := parseNamespaceReferences([]byte("<?php doWork(); namespace Vendor\\Package;\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 0 {
		t.Fatalf("expected namespace declaration after a completed same-line statement to be skipped, got %#v", imports)
	}
}

func TestParseNamespaceReferencesDoesNotMaskClosureUseCapture(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	content := helpersPHPHeader +
		"$callback = function ()\n" +
		"    use ($service) {\n" +
		"        \\Vendor\\Package\\Used::run();\n" +
		"    };\n"

	imports, unresolved := parseNamespaceReferences([]byte(content), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 1 {
		t.Fatalf("expected closure body namespace reference to remain visible, got %#v", imports)
	}
	if imports[0].Module != "Vendor\\Package\\Used" {
		t.Fatalf("expected module %q, got %#v", "Vendor\\Package\\Used", imports[0])
	}
	if imports[0].Location.Line != 4 {
		t.Fatalf("expected closure body namespace import on line 4, got %#v", imports[0])
	}
}

func TestParseNamespaceReferencesKeepsSameLineReferenceAfterUseImport(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Monolog": helpersMonologDependency}}
	content := helpersPHPHeader + "use Monolog\\Logger; $logger = new \\Monolog\\Logger(\"app\");\n"

	imports, unresolved := parseNamespaceReferences([]byte(content), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 1 {
		t.Fatalf("expected one post-import namespace reference, got %#v", imports)
	}
	if imports[0].Module != helpersMonologLogger {
		t.Fatalf("expected module %q, got %#v", helpersMonologLogger, imports[0])
	}
}

func TestParsePHPImportsKeepsBracketedNamespaceUseAsDeclaration(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"}}
	content := []byte(helpersPHPHeader +
		"namespace App {\n" +
		"    use \\Vendor\\Package\\FeatureTrait;\n" +
		"}\n")

	parsed := parsePHPImports(content, "x.php", resolver)
	if parsed.unresolvedCount != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, parsed.unresolvedCount)
	}
	if len(parsed.imports) != 1 {
		t.Fatalf("expected one namespace import declaration, got %#v", parsed.imports)
	}
	if parsed.imports[0].Wildcard {
		t.Fatalf("expected bracketed namespace use to remain declaration usage, got %#v", parsed.imports[0])
	}
	usage := shared.CountUsage(content, parsed.imports)
	if usage["FeatureTrait"] != 0 {
		t.Fatalf("expected unused namespace import declaration, got usage=%#v imports=%#v", usage, parsed.imports)
	}
}

func TestParsePHPImportsTracksSubsequentSameLineNamespaceDeclarations(t *testing.T) {
	resolver := composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		localNamespace: map[string]struct{}{"B\\Vendor": {}},
	}
	content := []byte(helpersPHPHeader +
		`namespace A { final class One {} } namespace B { final class Service { use Vendor\Package\LocalTrait; } }` + "\n")

	parsed := parsePHPImports(content, "x.php", resolver)
	if parsed.unresolvedCount != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, parsed.unresolvedCount)
	}
	if len(parsed.imports) != 0 {
		t.Fatalf("expected same-line namespace-relative trait use to resolve as local, got %#v", parsed.imports)
	}
}

func TestParseNamespaceReferencesDoesNotLetUseLinesExhaustReferenceLimit(t *testing.T) {
	resolver := composerResolver{namespaceToDep: map[string]string{"Monolog": helpersMonologDependency}}
	var content strings.Builder
	content.WriteString(helpersPHPHeader)
	content.WriteString("<?php use Monolog\\InlineLogger;\n")
	for i := 0; i < testMaxPHPUseStatementsPerFile*2; i++ {
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
	expectedLine := testMaxPHPUseStatementsPerFile*2 + 3
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

func TestParseNamespaceReferencesAcceptsNonASCIINamespaceInitial(t *testing.T) {
	const dependency = "editeur/package"
	resolver := composerResolver{namespaceToDep: map[string]string{"Éditeur\\Package": dependency}}
	imports, unresolved := parseNamespaceReferences([]byte(helpersPHPHeader+"$client = new \\Éditeur\\Package\\Client();\n"), "x.php", resolver)
	if unresolved != 0 {
		t.Fatalf(helpersUnexpectedUnresolvedFmt, unresolved)
	}
	if len(imports) != 1 {
		t.Fatalf("expected one namespace import, got %#v", imports)
	}
	if imports[0].Dependency != dependency || imports[0].Module != "Éditeur\\Package\\Client" {
		t.Fatalf("expected non-ASCII namespace reference to resolve, got %#v", imports[0])
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

	imp, dep, ok, unresolvedImport, limitHit := parseUsePart("", "", "x.php", 1, resolver)
	if ok || unresolvedImport || limitHit || dep != "" || imp.Dependency != "" {
		t.Fatalf("expected empty use part to be ignored")
	}

	imp, dep, ok, unresolvedImport, limitHit = parseUsePart(`Unknown\Pkg\Thing`, "", "x.php", 1, resolver)
	if ok || dep != "" || !unresolvedImport || limitHit || imp.Dependency != "" {
		t.Fatalf("expected unresolved import branch, got ok=%v dep=%q unresolved=%v limitHit=%v", ok, dep, unresolvedImport, limitHit)
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

	setUsageIncompleteForTest(t, &scan)
	top, warnings = buildTopPHPDependencies(1, scan, 40, report.DefaultRemovalCandidateWeights())
	if len(top) != 2 || top[0].Name != "a/pkg" || top[1].Name != "b/pkg" {
		t.Fatalf("expected deterministic unranked reports from incomplete usage, got %#v", top)
	}
	if !containsWarning(warnings, "top-N removal ranking disabled") {
		t.Fatalf("expected incomplete top-N warning, got %#v", warnings)
	}
	for _, dependency := range top {
		if dependency.RemovalCandidate != nil {
			t.Fatalf("did not expect incomplete top-N report to be scored, got %#v", dependency)
		}
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

func TestPHPParserAndConfigBoundaryBranches(t *testing.T) {
	testPHPUseStatementBoundaryBranches(t)
	testPHPNamespaceBoundaryBranches(t)
	testPHPTagAndHeredocBoundaryBranches(t)
	testPHPConfigBoundaryBranches(t)
	testPHPExcludedPathBoundaryBranches(t)
}

func testPHPUseStatementBoundaryBranches(t *testing.T) {
	t.Helper()

	masked := []byte("abc\n")
	maskPHPUseStatementRange(masked, -1, len(masked)+1)
	if got := string(masked); got != "   \n" {
		t.Fatalf("expected bounded use-statement mask, got %q", got)
	}
	if isPHPTraitAdaptationBlockStart("{", 0, 0) || isPHPTraitAdaptationBlockStart(`\{`, 0, 1) {
		t.Fatal("expected a leading or escaped brace not to start a trait adaptation block")
	}
	if depth, ended := advancePHPTraitAdaptationDepth("{{", 0, 1, 1); depth != 2 || ended {
		t.Fatalf("expected nested trait adaptation brace depth, got depth=%d ended=%v", depth, ended)
	}
	if useStatementContinuesAfterNewline("\n", 0, 0) || !useStatementContinuesAfterNewline(", \n", 0, 2) {
		t.Fatal("expected only a trailing comma to continue a multiline use statement")
	}
}

func testPHPNamespaceBoundaryBranches(t *testing.T) {
	t.Helper()

	tracker := phpContextTracker{currentNamespaceUses: map[string]string{}}
	tracker.addNamespaceUses(`function Vendor\Ignored`, 4)
	tracker.addNamespaceUses(`Vendor\{, Item\Thing}`, 4)
	if _, ok := tracker.currentNamespaceUses["thing"]; !ok {
		t.Fatalf("expected non-empty grouped alias to be retained, got %#v", tracker.currentNamespaceUses)
	}

	for _, text := range []string{"declare strict_types=1", "declare(", "declare(strict_types=1) "} {
		if _, ok := parseDeclarePreludeAt(text, 0, len(text), len(text)); ok {
			t.Fatalf("expected malformed declare prelude %q to be rejected", text)
		}
	}
	for _, text := range []string{"namespace Vendor", "namespace Vendor:"} {
		if _, ok := parseNamespaceDeclarationAt(text, 0); ok {
			t.Fatalf("expected malformed namespace declaration %q to be rejected", text)
		}
	}
	if _, ok := parseNamespaceDeclarationNameEnd(`Vendor\`, 0); ok {
		t.Fatal("expected a trailing namespace separator to be rejected")
	}
	if got := phpStatementTerminatorLengthAt("x", 1); got != 0 {
		t.Fatalf("expected no terminator beyond input, got %d", got)
	}
}

func testPHPTagAndHeredocBoundaryBranches(t *testing.T) {
	t.Helper()

	if _, _, ok := nextPHPOpenTag(`<?xml version="1.0"?>`, 0, true); ok {
		t.Fatal("expected XML declaration not to be treated as a PHP open tag")
	}
	if _, _, ok := nextPHPOpenTag("<?", 0, false); ok {
		t.Fatal("expected unsupported short PHP open tag to be rejected")
	}
	if isXMLDeclarationOpenTag("<?xml", 0) {
		t.Fatal("expected incomplete XML declaration not to be accepted")
	}
	if isXMLDeclarationOpenTag(`<?xml encoding="UTF-8"?>`, 0) {
		t.Fatal("expected an XML declaration without version assignment to be rejected")
	}
	if _, ok := xmlProcessingInstructionAttributeNameEnd("", 0); ok {
		t.Fatal("expected an empty XML processing instruction attribute name to be rejected")
	}
	if !isHeredocNowdocTerminatorContinuation("") || !isHeredocNowdocTerminatorTail(" ") || isHeredocNowdocTerminatorTail("/*") {
		t.Fatal("expected empty heredoc continuation and malformed block comment handling")
	}
	if findHeredocNowdocTerminator("body\nEOF;\n", 0, "EOF") < 0 {
		t.Fatal("expected heredoc terminator to be found")
	}
	if got := maskPHPHeredocNowdocBodies("<<<\n"); got != "<<<\n" {
		t.Fatalf("expected malformed heredoc marker to stay unchanged, got %q", got)
	}
	if stack, _, _ := popPHPClassLikeDelimiter([]byte{'('}, []int{0}, []int{-1}, '['); len(stack) != 1 {
		t.Fatalf("expected unmatched delimiter stack to stay intact, got %q", stack)
	}
	if got, ok := expandNamespaceUseAlias("Alias", map[string]string{"alias": `Vendor\Package`}); !ok || got != `Vendor\Package` {
		t.Fatalf("expected alias-only namespace use to expand, got %q ok=%v", got, ok)
	}
}

func testPHPConfigBoundaryBranches(t *testing.T) {
	t.Helper()

	policy := phpShortOpenTagPolicy{
		dirSettings:    map[string]phpShortOpenTagDirSetting{"dir": {enabled: true, priority: 3}},
		incompleteDirs: map[string]int{"dir": 3},
	}
	policy.setIncompleteDir("dir", 2)
	policy.setDirSetting("dir", false, 2)
	if !policy.dirSettings["dir"].enabled || policy.incompleteDirs["dir"] != 3 {
		t.Fatalf("expected higher priority PHP configuration to win, got %#v", policy)
	}
	if got := phpConfigFilePriority(".htaccess"); got != 3 {
		t.Fatalf("expected .htaccess PHP config priority to be three, got %d", got)
	}
	if got := phpConfigFilePriority("not-a-php-config"); got != 0 {
		t.Fatalf("expected unknown PHP config priority to be zero, got %d", got)
	}
	if enabled, found, incomplete := phpConfigBooleanSetting("maybe"); enabled || found || !incomplete {
		t.Fatalf("expected unknown PHP boolean setting to be incomplete, got enabled=%v found=%v incomplete=%v", enabled, found, incomplete)
	}
	if enabled, found, incomplete := parseShortOpenTagSetting("unrelated_setting = on"); enabled || found || incomplete {
		t.Fatalf("expected unrelated PHP setting to be ignored, got enabled=%v found=%v incomplete=%v", enabled, found, incomplete)
	}

	repo := t.TempDir()
	if enabled, found, incomplete, err := phpConfigShortOpenTagSetting(repo, filepath.Join(repo, "missing.ini")); err != nil || enabled || found || incomplete {
		t.Fatalf("expected missing PHP config to be ignored, got enabled=%v found=%v incomplete=%v err=%v", enabled, found, incomplete, err)
	}
	if err := scanPHPShortOpenTagConfigEntry(repo, repo, nil, errors.New("walk failure"), &policy, nil); err == nil {
		t.Fatal("expected config walk error to be returned")
	}
	writeFile(t, filepath.Join(repo, "not-a-config.txt"), "ignored")
	if err := scanPHPShortOpenTagConfigEntry(repo, filepath.Join(repo, "not-a-config.txt"), mustPHPDirEntry(t, repo, "not-a-config.txt"), nil, &policy, nil); err != nil {
		t.Fatalf("expected non-config file to be ignored, got %v", err)
	}
}

func testPHPExcludedPathBoundaryBranches(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "nested", helpersComposerJSON), "{}")
	if err := scanPHPShortOpenTagConfigDir(repo, filepath.Join(repo, "nested"), mustPHPDirEntry(t, repo, "nested")); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected nested Composer package config directory to be skipped, got %v", err)
	}
	writeFile(t, filepath.Join(repo, "ordinary", "child.txt"), "")
	if err := walkPHPDetectionEntry(filepath.Join(repo, "ordinary"), mustPHPDirEntry(t, repo, "ordinary"), map[string]struct{}{}, &language.Detection{}, new(int), maxDetectFiles); err != nil {
		t.Fatalf("expected ordinary directory detection entry to continue, got %v", err)
	}
	excludedFile := filepath.Join(repo, "excluded.php")
	writeFile(t, excludedFile, helpersPHPHeader)
	coordinator := newScanCoordinatorWithExcludedPaths(repo, composerData{}, map[string]struct{}{excludedFile: {}})
	if err := coordinator.scanEntry(excludedFile, mustPHPDirEntry(t, repo, "excluded.php")); err != nil {
		t.Fatalf("expected excluded PHP file to be ignored, got %v", err)
	}
	if err := scanDirEntryWithExcludedPaths(repo, filepath.Join(repo, "nested"), mustPHPDirEntry(t, repo, "nested"), &scanState{}, map[string]struct{}{filepath.Join(repo, "nested"): {}}); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected explicitly excluded directory to be skipped, got %v", err)
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(contextErr(cancelledCtx), context.Canceled) {
		t.Fatal("expected cancelled scan context to stop traversal")
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
	imports, grouped, unresolved := parseUseStatement(`Vendor\Package\{Client, Broken`, "x.php", 1, composerResolver{
		namespaceToDep: map[string]string{"Vendor\\Package": "vendor/package"},
		declared:       map[string]struct{}{"vendor/package": {}},
	})
	if len(imports) != 0 || len(grouped) != 0 || unresolved != 0 {
		t.Fatalf("expected malformed grouped use statement to be ignored, imports=%#v grouped=%#v unresolved=%d", imports, grouped, unresolved)
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
	if !usageIncompleteForTest(t, scan) {
		t.Fatal("expected bounded scan to mark usage incomplete")
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

func deepNamespaceForTest(prefix string, totalSegments int) string {
	prefix = normalizeNamespace(prefix)
	segments := strings.Split(prefix, `\`)
	if totalSegments < len(segments) {
		totalSegments = len(segments)
	}
	var builder strings.Builder
	builder.WriteString(prefix)
	for i := len(segments); i < totalSegments; i++ {
		builder.WriteString(`\Seg`)
	}
	return builder.String()
}

func exactSegmentNamespaceWithHugeSegmentForTest(totalSegments, hugeSegmentBytes int) string {
	if totalSegments < 2 {
		totalSegments = 2
	}
	segments := make([]string, 0, totalSegments)
	segments = append(segments, "Vendor", strings.Repeat("A", hugeSegmentBytes))
	for len(segments) < totalSegments {
		segments = append(segments, "S")
	}
	return strings.Join(segments, `\`)
}

func usageIncompleteForTest(t *testing.T, value any) bool {
	t.Helper()
	field := reflect.ValueOf(value).FieldByName("UsageIncomplete")
	if !field.IsValid() || field.Kind() != reflect.Bool {
		return false
	}
	return field.Bool()
}

func setUsageIncompleteForTest(t *testing.T, value any) {
	t.Helper()
	field := reflect.ValueOf(value).Elem().FieldByName("UsageIncomplete")
	if !field.IsValid() || field.Kind() != reflect.Bool || !field.CanSet() {
		t.Fatalf("expected settable UsageIncomplete field")
	}
	field.SetBool(true)
}

func isPureOversizedFileErrorForTest(err error) bool {
	return shared.IsPureSentinelError(err, safeio.ErrFileTooLarge)
}
