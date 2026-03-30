package js

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	dynamicRequireToken = "require("
	bindingGypFile      = "binding.gyp"
	nodeBinaryFile      = "addon.node"
	packageJSONFile     = "package.json"
)

func TestRiskHelperFunctions(t *testing.T) {
	if dependencyPath("@scope/pkg") != filepath.Join("@scope", "pkg") {
		t.Fatalf("expected scoped dependency path")
	}
	if dependencyPath("lodash") != "lodash" {
		t.Fatalf("expected unscoped dependency path")
	}

	if !hasDynamicCall("const x = require(dep)", dynamicRequireToken) {
		t.Fatalf("expected dynamic require call detection")
	}
	if hasDynamicCall("const x = myrequire(dep)", dynamicRequireToken) {
		t.Fatalf("did not expect identifier-prefixed token to count as dynamic call")
	}
	if hasDynamicCall("const x = require('fixed')", dynamicRequireToken) {
		t.Fatalf("did not expect static require to be detected as dynamic")
	}
	if hasDynamicCall("// require(dep)", dynamicRequireToken) {
		t.Fatalf("did not expect commented token to be detected")
	}
	if !hasDynamicCall(`const url = "http://example.com//noop"; require(dep)`, dynamicRequireToken) {
		t.Fatalf("expected dynamic require after // inside string literal to be detected")
	}
	if !isCommented("abc // trailing") || isCommented("abc") {
		t.Fatalf("unexpected commented-line detection")
	}
	if isCommented(`const url = "http://example.com//noop";`) {
		t.Fatalf("did not expect // inside string literal to count as comment")
	}
	if firstNonSpaceByte("  \t\rX") != 'X' {
		t.Fatalf("expected first non-space byte detection")
	}
	if !isIdentifierByte('a') || isIdentifierByte('-') {
		t.Fatalf("unexpected identifier byte detection")
	}

	values := dedupeStrings([]string{"b", "a", "a"})
	if strings.Join(values, ",") != "a,b" {
		t.Fatalf("unexpected dedupe/sort result: %#v", values)
	}
}

func TestDetectNodeBinaryAndBindingGyp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, nodeBinaryFile), []byte("bin"), 0o600); err != nil {
		t.Fatalf("write node binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, bindingGypFile), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}

	binary, err := detectNodeBinary(root)
	if err != nil {
		t.Fatalf("detect node binary: %v", err)
	}
	if binary != nodeBinaryFile {
		t.Fatalf("expected %s detection, got %q", nodeBinaryFile, binary)
	}
	binding, err := detectBindingGyp(root)
	if err != nil {
		t.Fatalf("detect binding.gyp: %v", err)
	}
	if len(binding) != 1 || binding[0] != bindingGypFile {
		t.Fatalf("unexpected binding.gyp detection: %#v", binding)
	}
}

func TestAssessRiskCueWarningBranches(t *testing.T) {
	repo := t.TempDir()
	cues, warnings := assessRiskCues(repo, "", "", ExportSurface{})
	if len(cues) != 0 {
		t.Fatalf("expected no cues for invalid dependency root, got %#v", cues)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for invalid dependency root")
	}

	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid package.json: %v", err)
	}
	_, warnings = assessRiskCues(repo, "pkg", "", ExportSurface{EntryPoints: []string{filepath.Join(depRoot, "missing.js")}})
	if len(warnings) == 0 {
		t.Fatalf("expected warnings for invalid metadata and missing entrypoint")
	}
}

func TestDetectDynamicLoaderUsageReadError(t *testing.T) {
	depRoot := t.TempDir()
	_, _, err := detectDynamicLoaderUsage(depRoot, []string{filepath.Join(depRoot, "missing.js")})
	if err == nil {
		t.Fatalf("expected read error for missing entrypoint")
	}
}

func TestNativeMetadataAndDepthHelpers(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	pkg := packageJSON{
		Gypfile: true,
		Scripts: map[string]string{
			"install":     "node-gyp rebuild",
			"postinstall": "echo noop",
		},
	}
	indicators := collectNativeMetadataIndicators(pkg)
	if len(indicators) == 0 {
		t.Fatalf("expected native metadata indicators")
	}

	if _, err := os.Stat(filepath.Join(depRoot, bindingGypFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no binding.gyp in fixture")
	}
	native, details, err := detectNativeModuleIndicators(depRoot, pkg)
	if err != nil {
		t.Fatalf("detect native module indicators: %v", err)
	}
	if !native || len(details) == 0 {
		t.Fatalf("expected native indicators from package metadata")
	}

	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}
	childRoot := filepath.Join(depRoot, "node_modules", "a")
	if err := os.MkdirAll(childRoot, 0o755); err != nil {
		t.Fatalf("mkdir child root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, packageJSONFile), []byte(`{"name":"a"}`), 0o600); err != nil {
		t.Fatalf("write child package.json: %v", err)
	}
	depth := estimateTransitiveDepth(repo, depRoot, packageJSON{Dependencies: map[string]string{"a": "1.0.0"}})
	if depth < 2 {
		t.Fatalf("expected depth >= 2, got %d", depth)
	}
}

func TestTransitiveDepthBudgetAndCycleBranches(t *testing.T) {
	repo := t.TempDir()
	pkg := packageJSON{Dependencies: map[string]string{"missing": "1.0.0"}}

	memo := map[string]int{}
	visiting := map[string]struct{}{}
	depth := transitiveDepth(repo, filepath.Join(repo, "node_modules", "pkg"), pkg, memo, visiting, 0)
	if depth != 1 {
		t.Fatalf("expected depth 1 when budget is exhausted, got %d", depth)
	}

	visiting = map[string]struct{}{filepath.Join(repo, "node_modules", "pkg"): {}}
	depth = transitiveDepth(repo, filepath.Join(repo, "node_modules", "pkg"), pkg, memo, visiting, 10)
	if depth != 1 {
		t.Fatalf("expected depth 1 for cycle detection branch, got %d", depth)
	}
}

func TestDetectNodeBinaryMaxVisitedBranch(t *testing.T) {
	depRoot := t.TempDir()
	for i := 0; i < 650; i++ {
		name := "f-" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(depRoot, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}

	found, err := detectNodeBinary(depRoot)
	if err != nil {
		t.Fatalf("detect node binary with max visited cap: %v", err)
	}
	if found != "" {
		t.Fatalf("expected no .node file found, got %q", found)
	}
}

func TestRiskHelperAdditionalBranches(t *testing.T) {
	if firstNonSpaceByte("   \t\r") != 0 {
		t.Fatalf("expected firstNonSpaceByte to return 0 for blank input")
	}

	depRoot := t.TempDir()
	native, details, err := detectNativeModuleIndicators(depRoot, packageJSON{})
	if err != nil {
		t.Fatalf("detect native indicators without metadata: %v", err)
	}
	if native || len(details) != 0 {
		t.Fatalf("expected no native indicators, got native=%v details=%#v", native, details)
	}
}

func TestRiskHelperErrorBranches(t *testing.T) {
	// Use a regular file path as depRoot to trigger filesystem errors
	// without changing directory permissions.
	depRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(depRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write depRoot file: %v", err)
	}

	if _, _, err := detectNativeModuleIndicators(depRoot, packageJSON{}); err == nil {
		t.Fatalf("expected detectNativeModuleIndicators permission error")
	}

	if _, err := detectNodeBinary(filepath.Join(t.TempDir(), "missing-root")); err == nil {
		t.Fatalf("expected detectNodeBinary error for missing root")
	}
}

func TestIsCommentedBranches(t *testing.T) {
	if isCommented(`"not-a-comment"`) {
		// empty input without comment marker should not be flagged by this specific helper.
		t.Fatalf("expected no inline comment when no delimiter exists")
	}

	if isCommented("a"+"b`c // ignored` // comment") != true {
		t.Fatalf("expected comment after template literal to be detected")
	}

	if isCommented("'single-quoted // ignored'") {
		t.Fatalf("did not expect comment inside single-quoted string")
	}

	if isCommented("\"double-quoted \\\" // ignored\"") {
		t.Fatalf("did not expect comment after escaped quote in double-quoted string")
	}
}

func TestDetectNativeModuleIndicatorsNodeBinaryBranch(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(`{"name":"pkg"}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, nodeBinaryFile), []byte(""), 0o600); err != nil {
		t.Fatalf("write node binary: %v", err)
	}

	isNative, details, err := detectNativeModuleIndicators(depRoot, packageJSON{})
	if err != nil {
		t.Fatalf("detect native indicators with node binary: %v", err)
	}
	if !isNative {
		t.Fatal("expected package to be native due to .node binary")
	}
	if len(details) == 0 {
		t.Fatal("expected metadata detail for detected .node binary")
	}
	found := false
	for _, detail := range details {
		if detail == nodeBinaryFile {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %s detail, got %#v", nodeBinaryFile, details)
	}
}

func TestAppendDepthRiskCueSeverityHeuristic(t *testing.T) {
	repoRoot := t.TempDir()
	pkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir pkg root: %v", err)
	}

	chain := []string{"a", "b", "c", "d", "e", "f", "g"}
	for i, depName := range chain {
		next := ""
		if i+1 < len(chain) {
			next = chain[i+1]
		}

		depJSON := `{"name":"` + depName + `"}`
		if next != "" {
			depJSON = `{"name":"` + depName + `","dependencies":{"` + next + `":"1.0.0"}}`
		}

		depRoot := filepath.Join(repoRoot, "node_modules", depName)
		if err := os.MkdirAll(depRoot, 0o755); err != nil {
			t.Fatalf("mkdir dependency root: %v", err)
		}
		if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(depJSON), 0o600); err != nil {
			t.Fatalf("write dependency package.json for %s: %v", depName, err)
		}
	}

	if err := os.WriteFile(filepath.Join(pkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{"a": "1.0.0"}}
	cues, warnings := appendDepthRiskCue(nil, nil, "pkg", repoRoot, pkgRoot, rootPkg)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings: %#v", warnings)
	}
	if len(cues) != 1 {
		t.Fatalf("expected one deep graph cue, got %#v", cues)
	}
	if cues[0].Code != riskCodeDeepGraph {
		t.Fatalf("unexpected risk code: %q", cues[0].Code)
	}
	if cues[0].Severity != "high" {
		t.Fatalf("expected high severity for deep graph, got %q", cues[0].Severity)
	}
}

func TestTransitiveDepthChildWarningBranch(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir root package root: %v", err)
	}

	if err := os.WriteFile(filepath.Join(rootPkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"valid":"1.0.0","invalid":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package json: %v", err)
	}

	validRoot := filepath.Join(repoRoot, "node_modules", "valid")
	if err := os.MkdirAll(validRoot, 0o755); err != nil {
		t.Fatalf("mkdir valid dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validRoot, packageJSONFile), []byte(`{"name":"valid"}`), 0o600); err != nil {
		t.Fatalf("write valid package json: %v", err)
	}

	invalidRoot := filepath.Join(repoRoot, "node_modules", "invalid")
	if err := os.MkdirAll(invalidRoot, 0o755); err != nil {
		t.Fatalf("mkdir invalid dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidRoot, packageJSONFile), []byte(`{"name":"invalid"`), 0o600); err != nil {
		t.Fatalf("write invalid package json: %v", err)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{"valid": "1.0.0", "invalid": "1.0.0"}}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]int{}, map[string]struct{}{}, 4)
	if depth == 0 {
		t.Fatalf("expected positive depth for dependency graph")
	}
}

func TestTransitiveDepthSkipsMissingDependencyRoot(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir root package root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"missing":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package json: %v", err)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{"missing": "1.0.0"}}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]int{}, map[string]struct{}{}, 4)
	if depth != 1 {
		t.Fatalf("expected depth to remain 1 for missing child dep roots, got %d", depth)
	}
}

func TestTransitiveDepthResolvesNormalAndScopedDependencyNames(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir root package root: %v", err)
	}

	normalRoot := filepath.Join(repoRoot, "node_modules", "dep")
	if err := os.MkdirAll(normalRoot, 0o755); err != nil {
		t.Fatalf("mkdir normal dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(normalRoot, packageJSONFile), []byte(`{"name":"dep"}`), 0o600); err != nil {
		t.Fatalf("write normal package json: %v", err)
	}

	scopedRoot := filepath.Join(repoRoot, "node_modules", "@scope", "pkg")
	if err := os.MkdirAll(scopedRoot, 0o755); err != nil {
		t.Fatalf("mkdir scoped dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedRoot, packageJSONFile), []byte(`{"name":"@scope/pkg"}`), 0o600); err != nil {
		t.Fatalf("write scoped package json: %v", err)
	}

	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, "dep"); !ok || root != normalRoot {
		t.Fatalf("expected normal dependency root resolution, got root=%q ok=%v", root, ok)
	}
	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, "@scope/pkg"); !ok || root != scopedRoot {
		t.Fatalf("expected scoped dependency root resolution, got root=%q ok=%v", root, ok)
	}

	rootPkg := packageJSON{
		Dependencies: map[string]string{
			"dep":        "1.0.0",
			"@scope/pkg": "1.0.0",
		},
	}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]int{}, map[string]struct{}{}, 4)
	if depth != 2 {
		t.Fatalf("expected depth 2 for direct normal and scoped dependencies, got %d", depth)
	}
}

func TestTransitiveDepthRejectsTraversalDependencyNames(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir root package root: %v", err)
	}

	outsideRoot := filepath.Join(filepath.Dir(repoRoot), "out1")
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, packageJSONFile), []byte(`{"name":"out1"}`), 0o600); err != nil {
		t.Fatalf("write outside package json: %v", err)
	}

	traversalName := "../../../../out1"
	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, traversalName); ok || root != "" {
		t.Fatalf("expected traversal-shaped dependency name to be rejected, got root=%q ok=%v", root, ok)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{traversalName: "1.0.0"}}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]int{}, map[string]struct{}{}, 4)
	if depth != 1 {
		t.Fatalf("expected depth 1 when traversal-shaped dependency is rejected, got %d", depth)
	}
}
