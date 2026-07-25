package js

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	licenseTestPackageJSONFileName = "package.json"
	licenseTestMPL20               = "MPL-2.0"
	licenseTestRepositoryURL       = "https://github.com/example/repo"
)

type closingLicenseRoot struct {
	safeio.Root
	closeErr error
}

func (r *closingLicenseRoot) Close() error {
	var baseErr error
	if r.Root != nil {
		baseErr = r.Root.Close()
	}
	if baseErr != nil && r.closeErr != nil {
		return errors.Join(baseErr, r.closeErr)
	}
	if r.closeErr != nil {
		return r.closeErr
	}
	return baseErr
}

func TestDetectLicenseAndProvenanceFromPackageJSON(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{
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
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{"name":"demo","version":"0.1.0"}`)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License\nPermission is hereby granted...")

	license, _, _ := detectLicenseAndProvenance(depRoot, false)
	if license == nil || license.SPDX != "MIT" || license.Source != "license-file" {
		t.Fatalf("expected MIT fallback from LICENSE file, got %#v", license)
	}
}

func TestDetectLicenseAndProvenanceSkipsOversizedLicenseCandidate(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{"name":"demo","version":"0.1.0"}`)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "COPYING"), string(bytes.Repeat([]byte("x"), int(licenseFileReadMaxBytes)+1)))
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License\nPermission is hereby granted...")

	license, provenance, warnings := detectLicenseAndProvenance(depRoot, false)
	if license == nil || license.SPDX != "MIT" || license.Source != "license-file" {
		t.Fatalf("expected MIT fallback after oversized license candidate skip, got %#v", license)
	}
	if provenance == nil || provenance.Source != "local-manifest" {
		t.Fatalf("expected provenance from local manifest, got %#v", provenance)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped license candidate COPYING above") {
		t.Fatalf("expected stable oversized-license warning, got %#v", warnings)
	}
}

func TestDetectLicenseAndProvenanceReturnsWarningsWhenAllFallbackCandidatesAreSkipped(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{"name":"demo","version":"0.1.0"}`)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "COPYING"), string(bytes.Repeat([]byte("x"), int(licenseFileReadMaxBytes)+1)))

	license, provenance, warnings := detectLicenseAndProvenance(depRoot, false)
	if license == nil || !license.Unknown || license.Source != "unknown" {
		t.Fatalf("expected unknown license when every fallback candidate is skipped, got %#v", license)
	}
	if provenance == nil || provenance.Source != "local-manifest" {
		t.Fatalf("expected manifest provenance, got %#v", provenance)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped license candidate COPYING above") {
		t.Fatalf("expected oversized candidate warning to be preserved, got %#v", warnings)
	}
}

func TestDetectProvenanceWithRegistryHeuristics(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{
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

func TestDetectLicenseAndProvenanceRejectsSymlinkedDependencyRoot(t *testing.T) {
	depRoot := filepath.Join(t.TempDir(), "pkg")
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, licenseTestPackageJSONFileName), `{"name":"outside","version":"9.9.9","license":"GPL-3.0-only"}`)
	if err := os.Symlink(outside, depRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	license, provenance, warnings := detectLicenseAndProvenance(depRoot, true)
	if license == nil || !license.Unknown || license.Source != "unknown" {
		t.Fatalf("expected unknown license for symlinked dependency root, got %#v", license)
	}
	if provenance == nil || provenance.Source != "unknown" {
		t.Fatalf("expected unknown provenance for symlinked dependency root, got %#v", provenance)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], dependencyRootOpaqueLayoutWarning) {
		t.Fatalf("expected stable symlink rejection warning, got %#v", warnings)
	}
}

func TestFindLicenseFilesRejectsInvalidDependencyRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if got := findLicenseFiles(missing); len(got) != 0 {
		t.Fatalf("expected missing dependency root to return nil files, got %#v", got)
	}
}

func TestFindLicenseFilesDiscardsResultsWhenRootCloseFails(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License")

	originalOpen := openDependencyRootNoFollow
	openDependencyRootNoFollow = func(path string) (safeio.Root, error) {
		root, err := safeio.OpenRootNoFollow(path)
		if err != nil {
			return nil, err
		}
		return &closingLicenseRoot{Root: root, closeErr: errors.New("close failed")}, nil
	}
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	if files := findLicenseFiles(depRoot); len(files) != 0 {
		t.Fatalf("expected close failure to discard license files, got %#v", files)
	}
}

func TestFindLicenseFilesReturnsCollectedFilesOnWalkError(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License\n")

	blockedDir := filepath.Join(depRoot, "blocked")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	if err := os.Chmod(blockedDir, 0o000); err != nil {
		t.Fatalf("chmod blocked dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blockedDir, 0o755); err != nil {
			t.Fatalf("restore blocked dir permissions: %v", err)
		}
	})

	files := findLicenseFiles(depRoot)
	if !slices.Contains(files, filepath.Join(depRoot, "LICENSE")) {
		t.Fatalf("expected collected license files to be preserved on walk error, got %#v", files)
	}
}

func TestParsePackageJSONLicenseVariants(t *testing.T) {
	if got := parsePackageJSONLicense(map[string]any{"type": "BSD-3-Clause"}); got != "BSD-3-Clause" {
		t.Fatalf("expected map type license, got %q", got)
	}
	raw, err := json.Marshal(licenseTestMPL20)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := parsePackageJSONLicense(json.RawMessage(raw)); got != licenseTestMPL20 {
		t.Fatalf("expected raw message license, got %q", got)
	}
	if got := parsePackageJSONLicense(json.RawMessage(`{"bad":`)); got != "" {
		t.Fatalf("expected empty license for invalid raw json, got %q", got)
	}
}

func TestPackageJSONLicenseRawWhitespacePrimaryFallsBackToLicenses(t *testing.T) {
	pkg := packageJSON{
		License:  "   ",
		Licenses: []any{"MIT"},
	}
	if got := packageJSONLicenseRaw(pkg); got != "MIT" {
		t.Fatalf("expected licenses fallback when primary field is whitespace, got %q", got)
	}
	if got := detectLicenseFromPackageJSON(pkg); got == nil || got.SPDX != "MIT" {
		t.Fatalf("expected MIT license from licenses fallback, got %#v", got)
	}
}

func TestDetectSPDXFromLicenseContentCases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Apache License Version 2.0", "APACHE-2.0"},
		{"GNU GENERAL PUBLIC LICENSE", "GPL-3.0-OR-LATER"},
		{"Mozilla Public License", licenseTestMPL20},
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
	if !hasRepositorySignal(licenseTestRepositoryURL) {
		t.Fatalf("expected repository signal for non-empty string")
	}
	if !hasRepositorySignal(map[string]any{"url": licenseTestRepositoryURL}) {
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
	if err := os.Mkdir(filepath.Join(root, licenseTestPackageJSONFileName), 0o755); err != nil {
		t.Fatalf("mkdir package.json dir: %v", err)
	}
	if _, warnings := loadDependencyPackageJSON(root); len(warnings) == 0 {
		t.Fatalf("expected warning for unreadable package.json path")
	}
	if err := os.Remove(filepath.Join(root, licenseTestPackageJSONFileName)); err != nil {
		t.Fatalf("remove package.json dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(root, licenseTestPackageJSONFileName), "{")
	if _, warnings := loadDependencyPackageJSON(root); len(warnings) == 0 {
		t.Fatalf("expected warning for malformed package.json")
	}
}

func TestLoadDependencyPackageJSONReturnsWarningWhenCloseFails(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, licenseTestPackageJSONFileName), `{"name":"demo"}`)

	originalOpen := openLicenseValidatedRoot
	openLicenseValidatedRoot = func(path string) (safeio.Root, string, error) {
		baseRoot, validatedRoot, err := openValidatedRootNoFollow(path)
		if err != nil {
			return nil, "", err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: errors.New("close failed")}, validatedRoot, nil
	}
	t.Cleanup(func() {
		openLicenseValidatedRoot = originalOpen
	})

	pkg, warnings := loadDependencyPackageJSON(root)
	if pkg.Name != "" {
		t.Fatalf("expected close failure to clear package metadata, got %#v", pkg)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read dependency metadata") {
		t.Fatalf("expected close failure warning, got %#v", warnings)
	}
}

func TestFindLicenseFilesWithinRootContinuesPastUnreadableChild(t *testing.T) {
	depRoot := t.TempDir()
	blockedDir := filepath.Join(depRoot, "blocked")
	if err := os.Mkdir(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, licensePath, "MIT License\n")

	blockedInfo, err := os.Lstat(blockedDir)
	if err != nil {
		t.Fatalf("lstat blocked dir: %v", err)
	}
	licenseInfo, err := os.Lstat(licensePath)
	if err != nil {
		t.Fatalf("lstat license file: %v", err)
	}

	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{
				entries: []fs.DirEntry{
					&fakeDirEntry{name: "blocked", mode: blockedInfo.Mode(), info: blockedInfo},
					&fakeDirEntry{name: "LICENSE", mode: licenseInfo.Mode(), info: licenseInfo},
				},
			}, nil
		},
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case "blocked":
				return blockedInfo, nil
			case "LICENSE":
				return licenseInfo, nil
			default:
				return nil, errors.New("unexpected lstat path")
			}
		},
		openRoot: func(name string) (safeio.Root, error) {
			if name != "blocked" {
				return nil, errors.New("unexpected child root path")
			}
			return nil, errors.New("blocked subtree")
		},
	}

	files := findLicenseFilesWithinRoot(root, depRoot)
	if len(files) != 1 || files[0] != licensePath {
		t.Fatalf("expected best-effort license walk to keep later readable license, got %#v", files)
	}
}

func TestLicenseFileProbeWrappersClearResultsWhenRootCloseFails(t *testing.T) {
	depRoot := t.TempDir()
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, licensePath, "MIT License\n")

	originalOpen := openLicenseValidatedRoot
	openLicenseValidatedRoot = func(path string) (safeio.Root, string, error) {
		baseRoot, validatedRoot, err := openValidatedRootNoFollow(path)
		if err != nil {
			return nil, "", err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: errors.New("close failed")}, validatedRoot, nil
	}
	t.Cleanup(func() {
		openLicenseValidatedRoot = originalOpen
	})

	if license := detectLicenseFromFiles(depRoot); license != nil {
		t.Fatalf("expected close failure to clear file-detected license, got %#v", license)
	}
	if probe := probeLicenseFiles(depRoot); probe != nil {
		t.Fatalf("expected close failure to clear file probe, got %#v", probe)
	}
	if probe := probeLicenseCandidates(depRoot, []string{licensePath}); probe != nil {
		t.Fatalf("expected close failure to clear candidate probe, got %#v", probe)
	}
	if probe := probeLicenseCandidate(depRoot, licensePath); probe != nil {
		t.Fatalf("expected close failure to clear single-candidate probe, got %#v", probe)
	}
}

func TestLicenseFileProbeWrappersReturnDetectedLicenseSignals(t *testing.T) {
	depRoot := t.TempDir()
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, licensePath, "MIT License\n")

	license := detectLicenseFromFiles(depRoot)
	if license == nil || license.SPDX != "MIT" {
		t.Fatalf("expected license detection from readable license file, got %#v", license)
	}

	probe := probeLicenseFiles(depRoot)
	if probe == nil || filepath.Base(probe.path) != "LICENSE" || probe.spdx != "MIT" {
		t.Fatalf("expected file probe for readable license file, got %#v", probe)
	}

	candidateProbe := probeLicenseCandidates(depRoot, []string{licensePath})
	if candidateProbe == nil || filepath.Base(candidateProbe.path) != "LICENSE" || candidateProbe.spdx != "MIT" {
		t.Fatalf("expected candidate probe for readable license file, got %#v", candidateProbe)
	}

	singleProbe := probeLicenseCandidate(depRoot, licensePath)
	if singleProbe == nil || filepath.Base(singleProbe.path) != "LICENSE" || singleProbe.spdx != "MIT" {
		t.Fatalf("expected single-candidate probe for readable license file, got %#v", singleProbe)
	}
}

func TestDetectLicenseAndProvenanceWarnsWhenCloseFails(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, licenseTestPackageJSONFileName), `{"name":"demo","version":"1.0.0","license":"MIT"}`)

	originalOpen := openLicenseValidatedRoot
	openLicenseValidatedRoot = func(path string) (safeio.Root, string, error) {
		baseRoot, validatedRoot, err := openValidatedRootNoFollow(path)
		if err != nil {
			return nil, "", err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: errors.New("close failed")}, validatedRoot, nil
	}
	t.Cleanup(func() {
		openLicenseValidatedRoot = originalOpen
	})

	license, provenance, warnings := detectLicenseAndProvenance(root, false)
	if license == nil || license.SPDX != "MIT" {
		t.Fatalf("expected license detection to succeed before close failure, got %#v", license)
	}
	if provenance == nil || provenance.Source != "local-manifest" {
		t.Fatalf("expected provenance detection to succeed before close failure, got %#v", provenance)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "failed to close dependency root after license/provenance detection") {
		t.Fatalf("expected close failure warning, got %#v", warnings)
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
	if got := detectLicenseFromFiles(filepath.Join(root, "missing")); got != nil {
		t.Fatalf("expected missing root to return nil fallback, got %#v", got)
	}
}

func TestProbeLicenseCandidatesSkipsUnknownUntilMatch(t *testing.T) {
	root := t.TempDir()
	unknown := filepath.Join(root, "LICENSE")
	known := filepath.Join(root, "COPYING")
	testutil.MustWriteFile(t, unknown, "custom internal license text")
	testutil.MustWriteFile(t, known, "MIT License\nPermission is hereby granted...")

	probe := probeLicenseCandidates(root, []string{unknown, known})
	if probe == nil {
		t.Fatalf("expected probe result for known license candidate")
	}
	if probe.path != known || probe.spdx != "MIT" || probe.confidence != "medium" {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
	if probe := probeLicenseCandidates(filepath.Join(root, "missing"), []string{known}); probe != nil {
		t.Fatalf("expected missing root to return no candidate probe, got %#v", probe)
	}
}

func TestProbeLicenseFileWrappers(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "LICENSE")
	testutil.MustWriteFile(t, candidate, "MIT License\nPermission is hereby granted...")
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatalf("resolve candidate: %v", err)
	}

	if probe := probeLicenseFiles(root); probe == nil || probe.path != resolvedCandidate {
		t.Fatalf("expected probeLicenseFiles wrapper to return license candidate, got %#v", probe)
	}
	if probe := probeLicenseCandidate(root, candidate); probe == nil || probe.spdx != "MIT" {
		t.Fatalf("expected probeLicenseCandidate wrapper to detect SPDX, got %#v", probe)
	}
	if probe := probeLicenseFiles(filepath.Join(root, "missing")); probe != nil {
		t.Fatalf("expected missing root to return no file probe, got %#v", probe)
	}
	if probe := probeLicenseCandidate(filepath.Join(root, "missing"), candidate); probe != nil {
		t.Fatalf("expected missing root to return no candidate probe, got %#v", probe)
	}
}

func TestProbeLicenseCandidateWithinRootIgnoresEscapingPath(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, licenseTestPackageJSONFileName), `{"name":"demo","version":"0.1.0"}`)

	safeRoot, validatedRoot, err := openValidatedRootNoFollow(root)
	if err != nil {
		t.Fatalf("open validated root: %v", err)
	}
	defer func() {
		if closeErr := safeRoot.Close(); closeErr != nil {
			t.Fatalf("close safe root: %v", closeErr)
		}
	}()

	outsideCandidate := filepath.Join(t.TempDir(), "LICENSE")
	testutil.MustWriteFile(t, outsideCandidate, "MIT License\nPermission is hereby granted...")

	probe, warning := probeLicenseCandidateWithinRoot(safeRoot, validatedRoot, outsideCandidate)
	if probe != nil || warning != "" {
		t.Fatalf("expected escaping candidate path to be ignored, got probe=%#v warning=%q", probe, warning)
	}
}

func TestCollectRegistryProvenanceSignals(t *testing.T) {
	pkg := packageJSON{
		Resolved:   "https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz",
		Integrity:  "sha512-abc",
		Repository: map[string]any{"url": licenseTestRepositoryURL},
	}
	pkg.PublishConfig.Registry = " https://registry.npmjs.org/ "

	signals := collectRegistryProvenanceSignals(pkg)
	want := []string{
		"registry:https://registry.npmjs.org/",
		"resolved",
		"integrity",
		"repository",
	}
	if !slices.Equal(signals, want) {
		t.Fatalf("unexpected registry signals: got %#v want %#v", signals, want)
	}
}

func TestFindLicenseFilesMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if files := findLicenseFiles(root); len(files) != 0 {
		t.Fatalf("expected no files for missing root, got %#v", files)
	}
}
