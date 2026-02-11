package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRiskHelperFunctions(t *testing.T) {
	if dependencyPath("@scope/pkg") != filepath.Join("@scope", "pkg") {
		t.Fatalf("expected scoped dependency path")
	}
	if dependencyPath("lodash") != "lodash" {
		t.Fatalf("expected unscoped dependency path")
	}

	if !hasDynamicCall("const x = require(dep)", "require(") {
		t.Fatalf("expected dynamic require call detection")
	}
	if hasDynamicCall("const x = myrequire(dep)", "require(") {
		t.Fatalf("did not expect identifier-prefixed token to count as dynamic call")
	}
	if hasDynamicCall("const x = require('fixed')", "require(") {
		t.Fatalf("did not expect static require to be detected as dynamic")
	}
	if hasDynamicCall("// require(dep)", "require(") {
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
	if err := os.WriteFile(filepath.Join(root, "binding.gyp"), []byte("{}"), 0o600); err != nil {
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
	if len(binding) != 1 || binding[0] != "binding.gyp" {
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
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte("{"), 0o600); err != nil {
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

	if _, err := os.Stat(filepath.Join(depRoot, "binding.gyp")); !os.IsNotExist(err) {
		t.Fatalf("expected no binding.gyp in fixture")
	}
	native, details, err := detectNativeModuleIndicators(depRoot, pkg)
	if err != nil {
		t.Fatalf("detect native module indicators: %v", err)
	}
	if !native || len(details) == 0 {
		t.Fatalf("expected native indicators from package metadata")
	}

	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"name":"pkg","dependencies":{"a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}
	childRoot := filepath.Join(depRoot, "node_modules", "a")
	if err := os.MkdirAll(childRoot, 0o755); err != nil {
		t.Fatalf("mkdir child root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "package.json"), []byte(`{"name":"a"}`), 0o600); err != nil {
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
