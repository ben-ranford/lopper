package js

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDetectLicenseAndProvenanceFromPackageJSON(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{
  "name": "demo",
  "version": "1.2.3",
  "license": "MIT OR Apache-2.0"
}`)

	license, provenance, warnings := detectLicenseAndProvenance(depRoot, false)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
	if license == nil || license.SPDX != "MIT OR APACHE-2.0" || license.Unknown {
		t.Fatalf("unexpected license detection: %#v", license)
	}
	if provenance == nil || provenance.Source != "local-manifest" {
		t.Fatalf("unexpected provenance: %#v", provenance)
	}
}

func TestDetectLicenseFromFallbackLicenseFile(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"name":"demo","version":"0.1.0"}`)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License\nPermission is hereby granted...")

	license, _, _ := detectLicenseAndProvenance(depRoot, false)
	if license == nil || license.SPDX != "MIT" || license.Source != "license-file" {
		t.Fatalf("expected MIT fallback from LICENSE file, got %#v", license)
	}
}

func TestDetectProvenanceWithRegistryHeuristics(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{
  "name": "pkg",
  "version": "1.0.0",
  "license": "ISC",
  "_resolved": "https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz",
  "_integrity": "sha512-abc",
  "publishConfig": { "registry": "https://registry.npmjs.org/" }
}`)

	_, provenance, _ := detectLicenseAndProvenance(depRoot, true)
	if provenance == nil || provenance.Source != "local+registry-heuristics" {
		t.Fatalf("expected registry provenance source, got %#v", provenance)
	}
}

func TestParsePackageJSONLicenseVariants(t *testing.T) {
	if got := parsePackageJSONLicense(map[string]any{"type": "BSD-3-Clause"}); got != "BSD-3-Clause" {
		t.Fatalf("expected map type license, got %q", got)
	}
	raw, err := json.Marshal("MPL-2.0")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := parsePackageJSONLicense(json.RawMessage(raw)); got != "MPL-2.0" {
		t.Fatalf("expected raw message license, got %q", got)
	}
	if got := parsePackageJSONLicense(json.RawMessage(`{"bad":`)); got != "" {
		t.Fatalf("expected empty license for invalid raw json, got %q", got)
	}
}

func TestDetectSPDXFromLicenseContentCases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Apache License Version 2.0", "APACHE-2.0"},
		{"GNU GENERAL PUBLIC LICENSE", "GPL-3.0-OR-LATER"},
		{"Mozilla Public License", "MPL-2.0"},
		{"ISC License", "ISC"},
		{"Redistribution and use in source and binary forms", "BSD-3-CLAUSE"},
	}
	for _, tc := range cases {
		if got, _ := detectSPDXFromLicenseContent(tc.input); got != tc.want {
			t.Fatalf("expected %s for %q, got %s", tc.want, tc.input, got)
		}
	}
	if got, _ := detectSPDXFromLicenseContent("custom text"); got != "" {
		t.Fatalf("expected empty detection for unknown text, got %s", got)
	}
}

func TestHasRepositorySignal(t *testing.T) {
	if !hasRepositorySignal("https://github.com/example/repo") {
		t.Fatalf("expected repository signal for non-empty string")
	}
	if !hasRepositorySignal(map[string]any{"url": "https://github.com/example/repo"}) {
		t.Fatalf("expected repository signal for url object")
	}
	if hasRepositorySignal(map[string]any{"url": ""}) {
		t.Fatalf("did not expect repository signal for empty url")
	}
	if hasRepositorySignal(42) {
		t.Fatalf("did not expect repository signal for unsupported type")
	}
}

func TestDetectLicenseAndProvenanceMissingRoot(t *testing.T) {
	license, provenance, warnings := detectLicenseAndProvenance("", false)
	if license == nil || !license.Unknown {
		t.Fatalf("expected unknown license for missing root, got %#v", license)
	}
	if provenance == nil || provenance.Source != "unknown" {
		t.Fatalf("expected unknown provenance for missing root, got %#v", provenance)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for missing root")
	}
}

func TestDetectLicenseFromPackageJSONLicensesFallback(t *testing.T) {
	pkg := packageJSON{
		Licenses: []any{map[string]any{"type": "Apache-2.0"}},
	}
	license := detectLicenseFromPackageJSON(pkg)
	if license == nil || license.SPDX != "APACHE-2.0" {
		t.Fatalf("expected apache license from licenses fallback, got %#v", license)
	}
}

func TestDetectLicenseFromPackageJSONNoLicense(t *testing.T) {
	if got := detectLicenseFromPackageJSON(packageJSON{}); got != nil {
		t.Fatalf("expected nil when no license metadata is present, got %#v", got)
	}
}

func TestDetectLicenseFromPackageJSONUnknownExpression(t *testing.T) {
	pkg := packageJSON{
		License: "custom-license@2026",
	}
	license := detectLicenseFromPackageJSON(pkg)
	if license == nil || license.SPDX != "CUSTOM-LICENSE2026" || license.Unknown {
		t.Fatalf("expected normalized SPDX-like token, got %#v", license)
	}
}

func TestDetectLicenseFromPackageJSONCompletelyUnknown(t *testing.T) {
	pkg := packageJSON{
		License: "!!!",
	}
	license := detectLicenseFromPackageJSON(pkg)
	if license == nil || !license.Unknown || license.SPDX != "" || license.Confidence != "medium" {
		t.Fatalf("expected unknown license classification, got %#v", license)
	}
}

func TestLoadDependencyPackageJSONErrorBranches(t *testing.T) {
	root := t.TempDir()
	if _, warnings := loadDependencyPackageJSON(root); len(warnings) == 0 {
		t.Fatalf("expected warning for missing package.json")
	}
	if err := os.Mkdir(filepath.Join(root, "package.json"), 0o755); err != nil {
		t.Fatalf("mkdir package.json dir: %v", err)
	}
	if _, warnings := loadDependencyPackageJSON(root); len(warnings) == 0 {
		t.Fatalf("expected warning for unreadable package.json path")
	}
	if err := os.Remove(filepath.Join(root, "package.json")); err != nil {
		t.Fatalf("remove package.json dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(root, "package.json"), "{")
	if _, warnings := loadDependencyPackageJSON(root); len(warnings) == 0 {
		t.Fatalf("expected warning for malformed package.json")
	}
}

func TestNormalizeSPDXExpression(t *testing.T) {
	if got := normalizeSPDXExpression(" ( mit and apache-2.0 ) "); got != "( MIT AND APACHE-2.0 )" {
		t.Fatalf("unexpected normalized expression: %q", got)
	}
	if got := normalizeSPDXExpression("$$$"); got != "" {
		t.Fatalf("expected empty normalization for invalid input, got %q", got)
	}
}

func TestFindLicenseFilesSkipsNestedNodeModules(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "LICENSE"), "MIT License")
	testutil.MustWriteFile(t, filepath.Join(root, "node_modules", "dep", "LICENSE"), "MIT License")

	files := findLicenseFiles(root)
	if len(files) == 0 {
		t.Fatalf("expected at least one license file")
	}
	for _, file := range files {
		if filepath.ToSlash(file) == filepath.ToSlash(filepath.Join(root, "node_modules", "dep", "LICENSE")) {
			t.Fatalf("expected nested node_modules license to be skipped, got %q", file)
		}
	}
}

func TestFindLicenseFilesLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 7; i++ {
		testutil.MustWriteFile(t, filepath.Join(root, "LICENSE_"+string(rune('A'+i))), "MIT")
	}
	files := findLicenseFiles(root)
	if len(files) > 5 {
		t.Fatalf("expected at most five files, got %d", len(files))
	}
}

func TestDetectLicenseFromFilesNoMatch(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "LICENSE"), "custom internal license text")
	if got := detectLicenseFromFiles(root); got != nil {
		t.Fatalf("expected nil fallback for unknown license text, got %#v", got)
	}
}

func TestFindLicenseFilesMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if files := findLicenseFiles(root); len(files) != 0 {
		t.Fatalf("expected no files for missing root, got %#v", files)
	}
}
