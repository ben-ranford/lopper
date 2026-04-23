package powershell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestCoverageGapParserBranches(t *testing.T) {
	if expressionComplete("") {
		t.Fatalf("expected empty expression to be incomplete")
	}
	if !expressionComplete("[string]") {
		t.Fatalf("expected balanced square expression to be complete")
	}

	modules, warnings := parseModuleExpression("Pester,,Az.Accounts")
	if !reflect.DeepEqual(modules, []string{"Pester", "Az.Accounts"}) || len(warnings) != 0 {
		t.Fatalf("unexpected module-expression parse result, modules=%#v warnings=%#v", modules, warnings)
	}

	module, dynamic, warning := parseModuleExpressionItem("@(@{ Other = 'x' })")
	if module != "" || dynamic || !strings.Contains(strings.ToLower(warning), "modulename") {
		t.Fatalf("expected nested warning branch, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}

	imports, lineWarnings := parsePowerShellLine("using module './local.psm1'", "run.ps1", 3, nil)
	if len(imports) != 0 || len(lineWarnings) != 0 {
		t.Fatalf("expected local using-module path to be ignored, imports=%#v warnings=%#v", imports, lineWarnings)
	}

	dependency, parsedModule, dynamic := parseImportModuleDependency("   ", nil)
	if dependency != "" || parsedModule != "" || dynamic {
		t.Fatalf("expected blank import-module expression to be ignored, got dep=%q module=%q dynamic=%v", dependency, parsedModule, dynamic)
	}

	value, tokenDynamic := parseStaticModuleToken(",;")
	if value != "" || tokenDynamic {
		t.Fatalf("expected empty static token after trimming separators, got value=%q dynamic=%v", value, tokenDynamic)
	}

	value, tokenDynamic = parseStaticModuleToken("\"./local.psm1\"")
	if value != "" || tokenDynamic {
		t.Fatalf("expected quoted local path to be ignored, got value=%q dynamic=%v", value, tokenDynamic)
	}

	if !isDynamicToken("prefix$(Resolve-Module)") {
		t.Fatalf("expected contains-$() branch to classify token as dynamic")
	}
	if !isLocalModulePath("module.psm1") {
		t.Fatalf("expected extension-only module path to be treated as local path")
	}

	if parts := splitTopLevel("   ", ','); len(parts) != 0 {
		t.Fatalf("expected empty split for whitespace input, got %#v", parts)
	}
	if parts := splitTopLevel("\"a,b\",c", ','); !reflect.DeepEqual(parts, []string{"\"a,b\"", "c"}) {
		t.Fatalf("unexpected quoted comma split result: %#v", parts)
	}
}

func TestCoverageGapDetectionAndScanBranches(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "Demo.psm1"), "Import-Module Pester\n")
	writePowerShellFile(t, filepath.Join(repo, "run.ps1"), "Import-Module Pester\n")

	detection := language.Detection{}
	roots := map[string]struct{}{}
	if err := applyRootSignals(repo, &detection, roots); err != nil {
		t.Fatalf("apply root signals: %v", err)
	}
	if !detection.Matched || detection.Confidence != 18 {
		t.Fatalf("expected root script and module-script confidence signals, got %#v", detection)
	}
	if _, ok := roots[repo]; ok {
		t.Fatalf("expected roots to remain empty without root manifest signal, got %#v", roots)
	}

	cancelRepo := t.TempDir()
	writePowerShellFile(t, filepath.Join(cancelRepo, "run.ps1"), "Import-Module Pester\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(ctx, cancelRepo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled detect context error, got %v", err)
	}

	nonPowerShellRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonPowerShellRepo, "notes.txt"), []byte("note"), 0o600); err != nil {
		t.Fatalf("write non-powershell file: %v", err)
	}
	scan, err := scanRepo(context.Background(), nonPowerShellRepo)
	if err != nil {
		t.Fatalf("scan non-powershell repo: %v", err)
	}
	if !containsPowerShellWarning(scan.Warnings, "no PowerShell files found") {
		t.Fatalf("expected missing-powershell-files warning, got %#v", scan.Warnings)
	}

	symlinkRepo := t.TempDir()
	externalTarget := filepath.Join(t.TempDir(), "outside.ps1")
	if err := os.WriteFile(externalTarget, []byte("Import-Module Pester\n"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	linkPath := filepath.Join(symlinkRepo, "outside-link.ps1")
	if err := os.Symlink(externalTarget, linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, err := scanRepo(context.Background(), symlinkRepo); err == nil {
		t.Fatalf("expected scan to fail when reading symlink outside repo root")
	}
}
