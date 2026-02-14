package js

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const requireToken = "require("

func TestFirstNonSpaceByteBranches(t *testing.T) {
	if got := firstNonSpaceByte(" \t\r"); got != 0 {
		t.Fatalf("expected zero for whitespace-only string, got %q", got)
	}
	if got := firstNonSpaceByte("  x"); got != 'x' {
		t.Fatalf("expected first non-space byte x, got %q", got)
	}
}

func TestCollectNativeMetadataIndicatorsAndFilesystemSignals(t *testing.T) {
	pkg := packageJSON{
		Gypfile: true,
		Scripts: map[string]string{
			"install":     "node-gyp rebuild",
			"postinstall": "cmake-js build",
			"preinstall":  "",
		},
	}
	details := collectNativeMetadataIndicators(pkg)
	if len(details) < 3 {
		t.Fatalf("expected metadata-native details, got %#v", details)
	}

	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "binding.gyp"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(depRoot, "build"), 0o750); err != nil {
		t.Fatalf("mkdir build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "build", "addon.node"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write addon.node: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(depRoot, "node_modules", "skip"), 0o750); err != nil {
		t.Fatalf("mkdir node_modules skip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "node_modules", "skip", "ignored.node"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write ignored.node: %v", err)
	}

	binding, err := detectBindingGyp(depRoot)
	if err != nil || len(binding) != 1 {
		t.Fatalf("expected binding.gyp detection, got %#v err=%v", binding, err)
	}
	nodeBinary, err := detectNodeBinary(depRoot)
	if err != nil {
		t.Fatalf("detect node binary: %v", err)
	}
	if nodeBinary != "addon.node" {
		t.Fatalf("expected addon.node detection, got %q", nodeBinary)
	}
}

func TestImportUsageFlagsAndReplacementThresholdFloor(t *testing.T) {
	dep := report.DependencyReport{
		UsedImports: []report.ImportUse{
			{Name: "default", Module: "lodash"},
			{Name: "map", Module: "lodash/map"},
		},
		UnusedImports: []report.ImportUse{
			{Name: "*", Module: "lodash"},
		},
	}
	root, subpath, wildcard := importUsageFlags("lodash", dep)
	if !root || !subpath || !wildcard {
		t.Fatalf("expected root/subpath/wildcard flags true, got root=%v subpath=%v wildcard=%v", root, subpath, wildcard)
	}
	if got := replacementThreshold(3); got != 0 {
		t.Fatalf("expected replacement threshold floor at 0, got %d", got)
	}
}

func TestHasDynamicCallBranches(t *testing.T) {
	if hasDynamicCall("require('x')", requireToken) {
		t.Fatalf("did not expect dynamic call when argument is static string literal")
	}
	if !hasDynamicCall("require(loader())", requireToken) {
		t.Fatalf("expected dynamic call detection")
	}
	if hasDynamicCall("myrequire(loader())", requireToken) {
		t.Fatalf("did not expect token match inside identifier")
	}
	if hasDynamicCall("// require(loader())", requireToken) {
		t.Fatalf("did not expect token inside comment")
	}
}

func TestDetectNativeModuleIndicatorsNoNativeAndErrors(t *testing.T) {
	depRoot := t.TempDir()
	isNative, details, err := detectNativeModuleIndicators(depRoot, packageJSON{})
	if err != nil {
		t.Fatalf("detect native indicators: %v", err)
	}
	if isNative || len(details) != 0 {
		t.Fatalf("expected no native indicators, got isNative=%v details=%#v", isNative, details)
	}

	if _, err := detectNodeBinary(filepath.Join(depRoot, "missing")); err == nil {
		t.Fatalf("expected detectNodeBinary error for missing root")
	}
}

func TestDetectDynamicLoaderUsageAndErrors(t *testing.T) {
	depRoot := t.TempDir()
	entry := filepath.Join(depRoot, "index.js")
	if err := os.WriteFile(entry, []byte("const x = require(loader())\n"), 0o600); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	count, samples, err := detectDynamicLoaderUsage([]string{filepath.Join(depRoot, "notes.txt"), entry})
	if err != nil {
		t.Fatalf("detect dynamic loader usage: %v", err)
	}
	if count != 1 || len(samples) != 1 {
		t.Fatalf("expected one dynamic usage sample, got count=%d samples=%#v", count, samples)
	}

	if _, _, err := detectDynamicLoaderUsage([]string{filepath.Join(depRoot, "missing.js")}); err == nil {
		t.Fatalf("expected read error for missing dynamic-loader entrypoint")
	}
}

func TestDetectNodeBinaryBoundedWalk(t *testing.T) {
	depRoot := t.TempDir()
	filesRoot := filepath.Join(depRoot, "files")
	if err := os.MkdirAll(filesRoot, 0o750); err != nil {
		t.Fatalf("mkdir files root: %v", err)
	}
	for i := 0; i < 620; i++ {
		name := filepath.Join(filesRoot, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}
	binary, err := detectNodeBinary(depRoot)
	if err != nil {
		t.Fatalf("detect node binary bounded walk: %v", err)
	}
	if binary != "" {
		t.Fatalf("expected no node binary when only text files exist, got %q", binary)
	}
}

func TestLoadDependencyPackageJSONBranches(t *testing.T) {
	depRoot := t.TempDir()
	_, warnings := loadDependencyPackageJSON(depRoot)
	if len(warnings) == 0 {
		t.Fatalf("expected warning for missing package.json")
	}

	pkgPath := filepath.Join(depRoot, "package.json")
	if err := os.WriteFile(pkgPath, []byte("{invalid-json"), 0o600); err != nil {
		t.Fatalf("write invalid package.json: %v", err)
	}
	_, warnings = loadDependencyPackageJSON(depRoot)
	if len(warnings) == 0 {
		t.Fatalf("expected warning for invalid package.json")
	}

	if err := os.WriteFile(pkgPath, []byte(`{"dependencies":{"a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write valid package.json: %v", err)
	}
	pkg, warnings := loadDependencyPackageJSON(depRoot)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings for valid package.json, got %#v", warnings)
	}
	if pkg.Dependencies["a"] != "1.0.0" {
		t.Fatalf("expected dependency parse from package.json, got %#v", pkg.Dependencies)
	}
}

func TestAppendNativeRiskCueBranches(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "binding.gyp"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}

	cues, warnings := appendNativeRiskCue(nil, nil, "dep", depRoot, packageJSON{})
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings for valid native cue scan, got %#v", warnings)
	}
	if len(cues) != 1 || cues[0].Code != riskCodeNativeModule {
		t.Fatalf("expected native-module cue, got %#v", cues)
	}

	_, warnings = appendNativeRiskCue(nil, nil, "dep", filepath.Join(depRoot, "missing"), packageJSON{})
	if len(warnings) == 0 {
		t.Fatalf("expected warning when native cue scan fails")
	}
}
