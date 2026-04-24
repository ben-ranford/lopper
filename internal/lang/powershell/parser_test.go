package powershell

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseRequiredModulesParsesStaticReferencesAndWarnsOnDynamic(t *testing.T) {
	content := []byte(`@{
  RootModule = 'Demo.psm1'
  RequiredModules = @(
    'Pester',
    @{ ModuleName = 'Az.Accounts'; RequiredVersion = '2.13.0' },
    @{ ModuleName = $dynamicModule },
    $(Get-ModuleName),
    './local.psm1',
    'Az.Resources/Submodule'
  )
}
`)

	modules, warnings := parseRequiredModules(content, "module.psd1")
	want := []string{"az.accounts", "az.resources/submodule", "pester"}
	if !reflect.DeepEqual(modules, want) {
		t.Fatalf("unexpected RequiredModules parse result: got=%#v want=%#v", modules, want)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected dynamic-module warnings, got none")
	}
	joined := strings.Join(warnings, "\n")
	for _, expected := range []string{"module.psd1:3", "dynamic", "ignored"} {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(expected)) {
			t.Fatalf("expected warning content %q, got %q", expected, joined)
		}
	}
}

func TestParseRequiredModulesHandlesMultilineAssignmentAndIncompleteExpression(t *testing.T) {
	content := []byte(`@{
  RequiredModules =
    @(
      'Pester',
      'Az.Accounts',
`)

	modules, warnings := parseRequiredModules(content, "manifest.psd1")
	if len(modules) != 0 {
		t.Fatalf("expected incomplete expression to avoid static module parsing, got %#v", modules)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for incomplete RequiredModules expression")
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "could not fully parse RequiredModules expression") {
		t.Fatalf("expected incomplete-expression warning, got %#v", warnings)
	}
}

func TestParseRequiredModulesEmptyExpressionWarns(t *testing.T) {
	content := []byte("RequiredModules = # none\n")
	modules, warnings := parseRequiredModules(content, "manifest.psd1")
	if len(modules) != 0 {
		t.Fatalf("expected no modules from empty RequiredModules expression, got %#v", modules)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, "\n"), "expression was empty") {
		t.Fatalf("expected empty-expression warning, got %#v", warnings)
	}
}

func TestParsePowerShellImportsParsesImportUsingAndRequiresDirectives(t *testing.T) {
	content := []byte(`
#Requires -Modules @('Pester', @{ ModuleName = 'Az.Accounts' }, $(Resolve-Module))
Import-Module -Name 'Pester'
using module 'Az.Accounts'
Import-Module .\local\module.psd1
Import-Module $dynamicModule
using module $dynamicUsing
`)
	declared := map[string]struct{}{"pester": {}, "az.accounts": {}}

	imports, warnings := parsePowerShellImports(content, "script.ps1", declared)
	if len(imports) != 4 {
		t.Fatalf("expected 4 imports (requires + import + using), got %#v", imports)
	}

	sources := make([]string, 0, len(imports))
	deps := make([]string, 0, len(imports))
	for _, imported := range imports {
		sources = append(sources, imported.Source)
		deps = append(deps, imported.Record.Dependency)
		if imported.Record.Location.File != "script.ps1" {
			t.Fatalf("expected script.ps1 location file, got %#v", imported.Record.Location)
		}
	}
	slices.Sort(sources)
	slices.Sort(deps)
	wantSources := []string{usageSourceImportModule, usageSourceRequiresModule, usageSourceRequiresModule, usageSourceUsingModule}
	if !reflect.DeepEqual(sources, wantSources) {
		t.Fatalf("unexpected usage sources: got=%#v want=%#v", sources, wantSources)
	}
	wantDeps := []string{"az.accounts", "az.accounts", "pester", "pester"}
	if !reflect.DeepEqual(deps, wantDeps) {
		t.Fatalf("unexpected parsed dependency names: got=%#v want=%#v", deps, wantDeps)
	}

	joinedWarnings := strings.ToLower(strings.Join(warnings, "\n"))
	for _, expected := range []string{"dynamic", "script.ps1:2", "script.ps1:6", "script.ps1:7"} {
		if !strings.Contains(joinedWarnings, strings.ToLower(expected)) {
			t.Fatalf("expected warning to contain %q, got %q", expected, joinedWarnings)
		}
	}
}

func TestParsePowerShellLineHandlesInlineCommentsAndMissingRequiresModules(t *testing.T) {
	declared := map[string]struct{}{"pester": {}}
	imports, warnings := parsePowerShellLine("Import-Module Pester # comment", "run.ps1", 4, declared)
	if len(imports) != 1 || len(warnings) != 0 {
		t.Fatalf("expected static import with comment to parse, imports=%#v warnings=%#v", imports, warnings)
	}

	imports, warnings = parsePowerShellLine("#Requires -Modules", "run.ps1", 8, declared)
	if len(imports) != 0 || len(warnings) != 1 {
		t.Fatalf("expected #Requires warning for missing modules, imports=%#v warnings=%#v", imports, warnings)
	}
	if !strings.Contains(warnings[0], "had no module list") {
		t.Fatalf("unexpected #Requires warning: %#v", warnings)
	}
}

func TestParseImportModuleDependencySupportsFlagAndPositionalForms(t *testing.T) {
	declared := map[string]struct{}{"pester": {}, "az.accounts": {}}
	cases := []struct {
		name      string
		input     string
		wantDep   string
		wantMod   string
		wantDyn   bool
		wantEmpty bool
	}{
		{name: "name flag", input: "-Name 'Pester'", wantDep: "pester", wantMod: "Pester"},
		{name: "name colon", input: "-Name:'Az.Accounts'", wantDep: "az.accounts", wantMod: "Az.Accounts"},
		{name: "positional", input: "Pester", wantDep: "pester", wantMod: "Pester"},
		{name: "missing candidate", input: "-Force", wantDyn: true},
		{name: "dynamic candidate", input: "$mod", wantDyn: true},
		{name: "local path", input: "./local.psm1", wantEmpty: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dep, mod, dyn := parseImportModuleDependency(tc.input, declared)
			if dep != tc.wantDep || mod != tc.wantMod || dyn != tc.wantDyn {
				t.Fatalf("unexpected parse result for %q: dep=%q mod=%q dynamic=%v", tc.input, dep, mod, dyn)
			}
			if tc.wantEmpty && (dep != "" || mod != "" || dyn) {
				t.Fatalf("expected empty non-dynamic result for %q, got dep=%q mod=%q dynamic=%v", tc.input, dep, mod, dyn)
			}
		})
	}
}

func TestParseUsingModuleDependencyAndRequiresDependencies(t *testing.T) {
	declared := map[string]struct{}{"az.accounts": {}, "pester": {}}
	dep, mod, dynamic := parseUsingModuleDependency("'Az.Accounts'", declared)
	if dep != "az.accounts" || mod != "Az.Accounts" || dynamic {
		t.Fatalf("unexpected using module parse: dep=%q mod=%q dynamic=%v", dep, mod, dynamic)
	}

	dep, mod, dynamic = parseUsingModuleDependency("$moduleName", declared)
	if dep != "" || mod != "" || !dynamic {
		t.Fatalf("expected dynamic using module parse, got dep=%q mod=%q dynamic=%v", dep, mod, dynamic)
	}

	deps, warnings := parseRequiresModulesDependencies("@('Pester', @{ ModuleName = 'Az.Accounts' }, $(Resolve-Module))", declared)
	if !reflect.DeepEqual(deps, []string{"az.accounts", "pester"}) {
		t.Fatalf("unexpected #Requires module dependencies: %#v", deps)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for dynamic #Requires module expression")
	}
}

func TestParseModuleExpressionAndItemsCoverNestedCases(t *testing.T) {
	modules, warnings := parseModuleExpression("@('Pester', @('Az.Accounts'), @{ModuleName='ThreadJob'}, @{Other='x'})")
	if !reflect.DeepEqual(modules, []string{"Pester", "Az.Accounts", "ThreadJob"}) {
		t.Fatalf("unexpected module expression parse modules: %#v", modules)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, "\n"), "did not include ModuleName") {
		t.Fatalf("expected hashtable warning for missing ModuleName key, got %#v", warnings)
	}

	module, dynamic, warning := parseModuleExpressionItem("@('Pester','Az.Accounts')")
	if module != "" || dynamic || warning == "" {
		t.Fatalf("expected nested list warning for multi-item nested value, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}

	module, dynamic, warning = parseModuleExpressionItem("@{ ModuleName = $mod }")
	if module != "" || !dynamic || !strings.Contains(strings.ToLower(warning), "dynamic") {
		t.Fatalf("expected dynamic hashtable module warning, got module=%q dynamic=%v warning=%q", module, dynamic, warning)
	}
}

func TestParseStaticModuleTokenAndResolveDependencyHelpers(t *testing.T) {
	declared := map[string]struct{}{"az": {}, "pester": {}}
	if dep := resolveDependency("Az/Accounts", declared); dep != "az" {
		t.Fatalf("expected root dependency resolution for nested module path, got %q", dep)
	}
	if dep := resolveDependency("Pester", declared); dep != "pester" {
		t.Fatalf("expected declared dependency resolution, got %q", dep)
	}
	if dep := resolveDependency("", declared); dep != "" {
		t.Fatalf("expected empty dependency resolution for blank module, got %q", dep)
	}

	cases := []struct {
		name      string
		token     string
		wantValue string
		wantDyn   bool
	}{
		{name: "single quoted", token: "'Pester'", wantValue: "Pester"},
		{name: "double quoted", token: "\"Az.Accounts\"", wantValue: "Az.Accounts"},
		{name: "double quoted with variable", token: "\"$Module\"", wantDyn: true},
		{name: "parenthesized", token: "('Pester')", wantValue: "Pester"},
		{name: "hashtable", token: "@{ ModuleName = 'Pester' }", wantValue: "Pester"},
		{name: "dynamic variable", token: "$moduleName", wantDyn: true},
		{name: "dynamic expression", token: "$(Resolve-Module)", wantDyn: true},
		{name: "array literal", token: "@( 'Pester' )", wantDyn: true},
		{name: "typed expression", token: "[string]::Format('x')", wantDyn: true},
		{name: "local path psm1", token: "./module.psm1", wantValue: ""},
		{name: "local path absolute", token: "C:\\modules\\demo.psd1", wantValue: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, dynamic := parseStaticModuleToken(tc.token)
			if value != tc.wantValue || dynamic != tc.wantDyn {
				t.Fatalf("unexpected static token parse for %q: value=%q dynamic=%v", tc.token, value, dynamic)
			}
		})
	}
}

func TestParserPrimitiveHelpers(t *testing.T) {
	assertParserExpressionPrimitives(t)
	assertParserSplitHelpers(t)
	assertParserFlagValueHelpers(t)
	assertParserCommentHelpers(t)
	assertParserTokenClassifierHelpers(t)
}

func TestParseRequiresLineWithoutModulesReturnsNoData(t *testing.T) {
	imports, warnings := parseRequiresLine(" -Version 7.0", "#Requires -Version 7.0", "script.ps1", 5, map[string]struct{}{})
	if len(imports) != 0 || len(warnings) != 0 {
		t.Fatalf("expected #Requires without -Modules to produce no imports/warnings, imports=%#v warnings=%#v", imports, warnings)
	}
}

func TestParseRequiresLineStripsTrailingComment(t *testing.T) {
	imports, warnings := parseRequiresLine(" -Modules Pester # comment", "#Requires -Modules Pester # comment", "script.ps1", 5, nil)
	if len(warnings) != 0 || len(imports) != 1 {
		t.Fatalf("expected one #Requires import and no warnings, imports=%#v warnings=%#v", imports, warnings)
	}
	if imports[0].Record.Dependency != "pester" {
		t.Fatalf("expected trailing comment to be excluded from dependency, got %#v", imports[0])
	}
}

func TestParseRequiresLineIgnoresTrailingRequiresOptions(t *testing.T) {
	imports, warnings := parseRequiresLine(" -Modules Pester -Version 7.0", "#Requires -Modules Pester -Version 7.0", "script.ps1", 5, nil)
	if len(warnings) != 0 || len(imports) != 1 {
		t.Fatalf("expected one #Requires import and no warnings, imports=%#v warnings=%#v", imports, warnings)
	}
	if imports[0].Record.Dependency != "pester" {
		t.Fatalf("expected trailing #Requires options to be excluded from dependency, got %#v", imports[0])
	}
}

func TestParseRequiresLineParsesAllModulesBeforeTrailingOption(t *testing.T) {
	imports, warnings := parseRequiresLine(" -Modules Pester, Az.Accounts -RunAsAdministrator", "#Requires -Modules Pester, Az.Accounts -RunAsAdministrator", "script.ps1", 5, nil)
	if len(warnings) != 0 || len(imports) != 2 {
		t.Fatalf("expected two #Requires imports and no warnings, imports=%#v warnings=%#v", imports, warnings)
	}

	dependencies := make([]string, 0, len(imports))
	for _, imp := range imports {
		dependencies = append(dependencies, imp.Record.Dependency)
	}
	slices.Sort(dependencies)
	if !reflect.DeepEqual(dependencies, []string{"az.accounts", "pester"}) {
		t.Fatalf("unexpected #Requires dependencies from module list, got %#v", dependencies)
	}
}

func TestNewImportBindingNormalizesDependencyAndDefaultsModule(t *testing.T) {
	binding := newImportBinding(" Pester ", "", "script.ps1", 3, "Import-Module Pester", usageSourceImportModule)
	if binding.Record.Dependency != "pester" || binding.Record.Module != "pester" {
		t.Fatalf("expected normalized dependency/module in import binding, got %#v", binding.Record)
	}
	if binding.Source != usageSourceImportModule {
		t.Fatalf("expected source attribution to be preserved, got %#v", binding)
	}
}

func assertParserExpressionPrimitives(t *testing.T) {
	t.Helper()
	if !expressionComplete("@('Pester')") {
		t.Fatalf("expected balanced expression to be complete")
	}
	if expressionComplete("@('Pester',") {
		t.Fatalf("expected trailing-comma expression to be incomplete")
	}
	if expressionComplete("'unterminated") {
		t.Fatalf("expected unterminated quote expression to be incomplete")
	}

	if got, ok := unwrapArrayExpression("@( 'Pester', 'Az.Accounts' )"); !ok || !strings.Contains(got, "Pester") {
		t.Fatalf("expected wrapped array to unwrap, got value=%q ok=%v", got, ok)
	}
	if got, ok := unwrapArrayExpression("'Pester'"); ok || got != "" {
		t.Fatalf("expected non-array expression to not unwrap, got value=%q ok=%v", got, ok)
	}

	if got := trimOuterParentheses("(( 'Pester' ))"); got != "'Pester'" {
		t.Fatalf("unexpected trimmed parenthesized value: %q", got)
	}
}

func assertParserSplitHelpers(t *testing.T) {
	t.Helper()
	parts := splitArguments("-Name 'Pester' -ErrorAction Stop")
	if !reflect.DeepEqual(parts, []string{"-Name", "'Pester'", "-ErrorAction", "Stop"}) {
		t.Fatalf("unexpected split arguments output: %#v", parts)
	}
	parts = splitTopLevel("'a,b',@(1,2),x", ',')
	if !reflect.DeepEqual(parts, []string{"'a,b'", "@(1,2)", "x"}) {
		t.Fatalf("unexpected top-level comma split result: %#v", parts)
	}
}

func assertParserFlagValueHelpers(t *testing.T) {
	t.Helper()
	if value, ok := flagValue("-Name:'Pester' -Force", "name"); !ok || value != "'Pester'" {
		t.Fatalf("expected colon-flag value parse, got value=%q ok=%v", value, ok)
	}
	if value, ok := flagValue("-Name 'Pester' -Force", "name"); !ok || value != "'Pester'" {
		t.Fatalf("expected spaced-flag value parse, got value=%q ok=%v", value, ok)
	}
	if value, ok := flagValue("-Force", "name"); ok || value != "" {
		t.Fatalf("expected missing-flag value to be absent, got value=%q ok=%v", value, ok)
	}
}

func assertParserCommentHelpers(t *testing.T) {
	t.Helper()
	if line := stripPowerShellInlineComment("Import-Module 'Pester' # comment"); strings.TrimSpace(line) != "Import-Module 'Pester'" {
		t.Fatalf("unexpected comment stripping result: %q", line)
	}
	if line := stripPowerShellInlineComment("Import-Module 'P#ester'"); !strings.Contains(line, "P#ester") {
		t.Fatalf("expected quoted hash to be preserved, got %q", line)
	}
}

func assertParserTokenClassifierHelpers(t *testing.T) {
	t.Helper()
	if !isQuoted("'value'", '\'') || !isQuoted("\"value\"", '"') {
		t.Fatalf("expected quoted helper checks to pass")
	}
	if isQuoted("value", '\'') {
		t.Fatalf("expected unquoted helper check to fail")
	}
	if !isHashtableExpression("@{ ModuleName = 'x' }") {
		t.Fatalf("expected hashtable expression helper checks to pass")
	}
	if isHashtableExpression("x") || isHashtableExpression("{ ModuleName = 'x' }") {
		t.Fatalf("expected non-hashtable expression helper checks to fail")
	}

	if !isDynamicToken("$x") || !isDynamicToken("$(Get-Module)") || !isDynamicToken("[string]::Join(',')") || !isDynamicToken("(Get-Module)") {
		t.Fatalf("expected dynamic token helper checks to pass")
	}
	if isDynamicToken("Pester") {
		t.Fatalf("expected static token to not be treated as dynamic")
	}
	if !isLocalModulePath("..\\demo\\module.psd1") || !isLocalModulePath("/modules/demo.psm1") {
		t.Fatalf("expected local module path helper checks to pass")
	}
	if isLocalModulePath("Pester") {
		t.Fatalf("expected package name to not be treated as local path")
	}
}
