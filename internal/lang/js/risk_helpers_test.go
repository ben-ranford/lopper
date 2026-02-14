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
	if !isCommented("abc // trailing") || isCommented("abc") {
		t.Fatalf("unexpected commented-line detection")
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
	if err := os.WriteFile(filepath.Join(root, "addon.node"), []byte("bin"), 0o600); err != nil {
		t.Fatalf("write node binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, bindingGypFile), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}

	binary, err := detectNodeBinary(root)
	if err != nil {
		t.Fatalf("detect node binary: %v", err)
	}
	if binary != "addon.node" {
		t.Fatalf("expected addon.node detection, got %q", binary)
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
	cues, warnings := assessRiskCues(repo, "", ExportSurface{})
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
	_, warnings = assessRiskCues(repo, "pkg", ExportSurface{EntryPoints: []string{filepath.Join(depRoot, "missing.js")}})
	if len(warnings) == 0 {
		t.Fatalf("expected warnings for invalid metadata and missing entrypoint")
	}
}

func TestDetectDynamicLoaderUsageReadError(t *testing.T) {
	_, _, err := detectDynamicLoaderUsage([]string{filepath.Join(t.TempDir(), "missing.js")})
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
	depth, err := estimateTransitiveDepth(repo, depRoot, packageJSON{Dependencies: map[string]string{"a": "1.0.0"}})
	if err != nil {
		t.Fatalf("estimate transitive depth: %v", err)
	}
	if depth < 2 {
		t.Fatalf("expected depth >= 2, got %d", depth)
	}
}

func TestTransitiveDepthBudgetAndCycleBranches(t *testing.T) {
	repo := t.TempDir()
	pkg := packageJSON{Dependencies: map[string]string{"missing": "1.0.0"}}

	memo := map[string]int{}
	visiting := map[string]struct{}{}
	depth, err := transitiveDepth(repo, filepath.Join(repo, "node_modules", "pkg"), pkg, memo, visiting, 0)
	if err != nil {
		t.Fatalf("transitive depth budget branch: %v", err)
	}
	if depth != 1 {
		t.Fatalf("expected depth 1 when budget is exhausted, got %d", depth)
	}

	visiting = map[string]struct{}{filepath.Join(repo, "node_modules", "pkg"): {}}
	depth, err = transitiveDepth(repo, filepath.Join(repo, "node_modules", "pkg"), pkg, memo, visiting, 10)
	if err != nil {
		t.Fatalf("transitive depth cycle branch: %v", err)
	}
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
