package powershell

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestExpressionCompleteCoversEscapesAndBracketDepthBranches(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want bool
	}{
		{name: "escaped quote in double string", expr: "\"value `\" quoted\"", want: true},
		{name: "escaped hash in single string", expr: "'value `# hash'", want: true},
		{name: "unbalanced paren", expr: "@('a'", want: false},
		{name: "unbalanced square", expr: "[string", want: false},
		{name: "unbalanced brace", expr: "@{ ModuleName = 'x'", want: false},
		{name: "balanced mixed expression", expr: "@{ ModuleName = \"x\" }", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expressionComplete(tc.expr); got != tc.want {
				t.Fatalf("unexpected expression completeness for %q: got=%v want=%v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestCollectAssignmentExpressionCoversBlankAndMultilinePaths(t *testing.T) {
	lines := []string{
		"RequiredModules =",
		"",
		"@('Pester')",
	}
	expr, consumed, complete := collectAssignmentExpression(lines, 0, "")
	if expr != "@('Pester')" || consumed != 2 || !complete {
		t.Fatalf("unexpected collected assignment expression: expr=%q consumed=%d complete=%v", expr, consumed, complete)
	}

	lines = []string{"RequiredModules =", "", ""}
	expr, consumed, complete = collectAssignmentExpression(lines, 0, "")
	if expr != "" || consumed != 2 || complete {
		t.Fatalf("expected empty incomplete expression branch, got expr=%q consumed=%d complete=%v", expr, consumed, complete)
	}
}

func TestParseModuleExpressionItemNestedSingleAndEmptyBranches(t *testing.T) {
	module, dynamic, warning := parseModuleExpressionItem("@('Pester')")
	if module != "Pester" || dynamic || warning != "" {
		t.Fatalf("expected single nested module result, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}

	module, dynamic, warning = parseModuleExpressionItem("@()")
	if module != "" || dynamic || warning != "" {
		t.Fatalf("expected empty nested module result, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}
}

func TestParseImportModuleDependencyCoversUnresolvedModuleBranch(t *testing.T) {
	dep, mod, dynamic := parseImportModuleDependency("Az.Accounts/SubModule", map[string]struct{}{"pester": {}})
	if dep != "az.accounts/submodule" || mod != "Az.Accounts/SubModule" || dynamic {
		t.Fatalf("expected unresolved dependency to map to normalized module name, got dep=%q mod=%q dynamic=%v", dep, mod, dynamic)
	}
}

func TestParseUsingModuleDependencyCoversNoopBranch(t *testing.T) {
	dep, mod, dynamic := parseUsingModuleDependency("./local.psm1", nil)
	if dep != "" || mod != "" || dynamic {
		t.Fatalf("expected local module path to return empty non-dynamic dependency, got dep=%q mod=%q dynamic=%v", dep, mod, dynamic)
	}
}

func TestParsePowerShellLineCoversBlankCommentAndNoMatchBranches(t *testing.T) {
	imports, warnings := parsePowerShellLine("   ", "script.ps1", 1, nil)
	if len(imports) != 0 || len(warnings) != 0 {
		t.Fatalf("expected blank line to produce no imports/warnings, got imports=%#v warnings=%#v", imports, warnings)
	}

	imports, warnings = parsePowerShellLine("# full line comment", "script.ps1", 2, nil)
	if len(imports) != 0 || len(warnings) != 0 {
		t.Fatalf("expected comment line to produce no imports/warnings, got imports=%#v warnings=%#v", imports, warnings)
	}

	imports, warnings = parsePowerShellLine("Write-Host 'hello'", "script.ps1", 3, nil)
	if len(imports) != 0 || len(warnings) != 0 {
		t.Fatalf("expected non-import line to produce no imports/warnings, got imports=%#v warnings=%#v", imports, warnings)
	}
}

func TestSplitTopLevelCoversEscapesNestedAndSpaceSeparatorBranches(t *testing.T) {
	spaceParts := splitTopLevel("Import-Module   -Name   'Pester'", ' ')
	if !reflect.DeepEqual(spaceParts, []string{"Import-Module", "-Name", "'Pester'"}) {
		t.Fatalf("unexpected space split output: %#v", spaceParts)
	}

	commaParts := splitTopLevel("'a,b',`\"c,d`\",@(1,2),[x,y],{k=v},plain", ',')
	want := []string{"'a,b'", "`\"c", "d`\"", "@(1,2)", "[x,y]", "{k=v}", "plain"}
	if !reflect.DeepEqual(commaParts, want) {
		t.Fatalf("unexpected comma split output: got=%#v want=%#v", commaParts, want)
	}
}

func TestTrimOuterParenthesesAndDynamicHelpersCoverRemainingBranches(t *testing.T) {
	if got := trimOuterParentheses("(())"); got != "()" {
		t.Fatalf("expected nested parens to trim to inner expression, got %q", got)
	}
	if got := trimOuterParentheses("()"); got != "()" {
		t.Fatalf("expected empty inner expression to preserve parens, got %q", got)
	}
	if got := trimOuterParentheses("('x',)"); got != "('x',)" {
		t.Fatalf("expected trailing-comma inner expression to preserve outer parens, got %q", got)
	}

	if !isDynamicToken("@(Get-Thing)") {
		t.Fatalf("expected array expression to be dynamic")
	}
	if !isDynamicToken("(Get-Thing)") {
		t.Fatalf("expected parenthesized expression to be dynamic")
	}
	if isDynamicToken("") {
		t.Fatalf("expected empty token to not be dynamic")
	}
}

func TestIsLocalModulePathCoversRootAndWindowsBranches(t *testing.T) {
	for _, value := range []string{"/module.psm1", "\\module.psm1", "D:\\module.ps1"} {
		if !isLocalModulePath(value) {
			t.Fatalf("expected %q to be treated as local module path", value)
		}
	}
}

func TestFlagValueCoversMissingValueBranch(t *testing.T) {
	if value, ok := flagValue("-Name", "name"); ok || value != "" {
		t.Fatalf("expected -Name without value to return not found, got value=%q ok=%v", value, ok)
	}
}

func TestStripPowerShellInlineCommentCoversEscapedAndQuotedBranches(t *testing.T) {
	if got := stripPowerShellInlineComment("\"quoted # hash\""); got != "\"quoted # hash\"" {
		t.Fatalf("expected quoted # to be preserved, got %q", got)
	}
	if got := stripPowerShellInlineComment("'quoted # hash'"); got != "'quoted # hash'" {
		t.Fatalf("expected single-quoted # to be preserved, got %q", got)
	}
	if got := stripPowerShellInlineComment("Write-Host `#escaped #comment"); strings.TrimSpace(got) != "Write-Host `#escaped" {
		t.Fatalf("expected escaped # to be preserved before comment, got %q", got)
	}
}

func TestApplyImportSourceAttributionNilAndMissingSourceBranches(t *testing.T) {
	applyImportSourceAttribution("pester", nil, scanResult{})

	dependency := report.DependencyReport{
		Name: "pester",
		UsedImports: []report.ImportUse{{
			Name:   "pester",
			Module: "pester",
		}},
	}
	applyImportSourceAttribution("pester", &dependency, scanResult{})
	if len(dependency.UsedImports[0].Provenance) != 0 {
		t.Fatalf("expected no provenance when no import source mapping exists, got %#v", dependency.UsedImports)
	}
}

func TestResolveRemovalCandidateWeightsCustomBranch(t *testing.T) {
	weights := resolveRemovalCandidateWeights(&report.RemovalCandidateWeights{Usage: 2, Impact: 3, Confidence: 5})
	if weights.Usage != 0.2 || weights.Impact != 0.3 || weights.Confidence != 0.5 {
		t.Fatalf("expected normalized custom weights, got %#v", weights)
	}
}

func TestAdapterAnalyseAndScanRepoErrorBranches(t *testing.T) {
	adapter := NewAdapter()
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: string([]byte{'b', 'a', 'd', 0x00}), TopN: 1}); err == nil {
		t.Fatalf("expected analyse to fail for invalid repo path")
	}

	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "script.ps1"), "Import-Module Pester\n")
	if _, err := scanRepo(testutil.CanceledContext(), repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected scanRepo to surface canceled context error, got %v", err)
	}
}
