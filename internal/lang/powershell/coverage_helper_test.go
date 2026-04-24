package powershell

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCoverageHelperDetectionAndScanBranches(t *testing.T) {
	if err := applyRootSignals("\x00", &language.Detection{}, map[string]struct{}{}); err == nil {
		t.Fatalf("expected invalid repo path to fail root signals")
	}

	adapter := NewAdapter()
	if _, err := adapter.DetectWithConfidence(context.Background(), "\x00"); err == nil {
		t.Fatalf("expected invalid repo path to fail detect with confidence")
	}

	if _, err := scanRepo(context.Background(), "\x00"); err == nil {
		t.Fatalf("expected invalid repo path to fail scan")
	}

	emptyRepo := t.TempDir()
	emptyScan, err := scanRepo(context.Background(), emptyRepo)
	if err != nil {
		t.Fatalf("scan empty repo: %v", err)
	}
	assertContainsWarning(t, emptyScan.Warnings, "no PowerShell files found for analysis")
	assertContainsWarning(t, emptyScan.Warnings, "no PowerShell module declarations found")

	largeRepo := t.TempDir()
	largeFile := filepath.Join(largeRepo, "script.ps1")
	testutil.MustWriteFile(t, largeFile, strings.Repeat("a", maxScannablePowerShellBytes+1))
	largeScan, err := scanRepo(context.Background(), largeRepo)
	if err != nil {
		t.Fatalf("scan large file repo: %v", err)
	}
	assertContainsWarning(t, largeScan.Warnings, "skipped large PowerShell file")
}

func TestCoverageHelperParserBranches(t *testing.T) {
	assertCoverageHelperModuleExpressions(t)
	assertCoverageHelperDependencyParsing(t)
	assertCoverageHelperTokenClassification(t)
	assertCoverageHelperSplitTopLevel(t)
}

func assertContainsWarning(t *testing.T, warnings []string, want string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return
		}
	}
	t.Fatalf("expected warning containing %q, got %#v", want, warnings)
}

func assertCoverageHelperModuleExpressions(t *testing.T) {
	t.Helper()
	if module, dynamic, warning := parseModuleExpressionItem(""); module != "" || dynamic || warning != "" {
		t.Fatalf("expected blank module expression item to be ignored, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}
	if module, dynamic, warning := parseModuleExpressionItem("@('One','Two')"); module != "" || dynamic || !strings.Contains(warning, "multiple values") {
		t.Fatalf("expected nested module list warning, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}
	if module, dynamic, warning := parseModuleExpressionItem("@{ Something = 'x' }"); module != "" || dynamic || !strings.Contains(warning, "did not include ModuleName") {
		t.Fatalf("expected missing ModuleName warning, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}
}

func assertCoverageHelperDependencyParsing(t *testing.T) {
	t.Helper()
	if dependency, module, dynamic := parseImportModuleDependency("-Name", nil); dependency != "" || module != "" || !dynamic {
		t.Fatalf("expected incomplete -Name expression to be dynamic, got dep=%q module=%q dynamic=%v", dependency, module, dynamic)
	}
	if dependency, module, dynamic := parseImportModuleDependency("-Name $moduleName", nil); dependency != "" || module != "" || !dynamic {
		t.Fatalf("expected dynamic Import-Module -Name expression, got dep=%q module=%q dynamic=%v", dependency, module, dynamic)
	}
	if dependency, module, dynamic := parseImportModuleDependency("./local.psm1", nil); dependency != "" || module != "" || dynamic {
		t.Fatalf("expected local Import-Module path to be ignored, got dep=%q module=%q dynamic=%v", dependency, module, dynamic)
	}

	if dependency, module, dynamic := parseUsingModuleDependency("$(Get-ModuleName)", nil); dependency != "" || module != "" || !dynamic {
		t.Fatalf("expected dynamic using module expression, got dep=%q module=%q dynamic=%v", dependency, module, dynamic)
	}

	declared := map[string]struct{}{"modulea": {}}
	dependencies, warnings := parseRequiresModulesDependencies("'moduleA', $moduleB", declared)
	if len(dependencies) != 1 || dependencies[0] != "modulea" || len(warnings) == 0 {
		t.Fatalf("expected requires modules parsing to keep declared static modules and warn on dynamic values, got deps=%#v warnings=%#v", dependencies, warnings)
	}
}

func assertCoverageHelperTokenClassification(t *testing.T) {
	t.Helper()
	if value, dynamic := parseStaticModuleToken("\"$moduleName\""); value != "" || !dynamic {
		t.Fatalf("expected interpolated string token to be dynamic, got value=%q dynamic=%v", value, dynamic)
	}
	if value, dynamic := parseStaticModuleToken("C:\\modules\\tool.psm1"); value != "" || dynamic {
		t.Fatalf("expected local file token to be ignored, got value=%q dynamic=%v", value, dynamic)
	}

	if !isDynamicToken("$(Get-Thing)") || !isDynamicToken("[System.String]::Format('x')") || !isDynamicToken("(Get-Thing)") {
		t.Fatalf("expected dynamic token detection for invocation-style expressions")
	}
	if isDynamicToken("'module'") {
		t.Fatalf("expected quoted static token to be non-dynamic")
	}

	if !isLocalModulePath("\\\\server\\share\\module.psm1") {
		t.Fatalf("expected UNC-like path to be treated as local module path")
	}
	if isLocalModulePath("module.name") {
		t.Fatalf("expected bare module name to remain non-local")
	}
}

func assertCoverageHelperSplitTopLevel(t *testing.T) {
	t.Helper()
	segments := splitTopLevel("Name    Value   @(1,2)  @{A=1}", ' ')
	if len(segments) != 4 {
		t.Fatalf("expected splitTopLevel to collapse repeated spaces while preserving top-level segments, got %#v", segments)
	}
}
