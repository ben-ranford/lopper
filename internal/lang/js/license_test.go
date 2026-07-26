package js

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
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

	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), depRoot, false)
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

	license, _, _ := detectLicenseAndProvenance(context.Background(), depRoot, false)
	if license == nil || license.SPDX != "MIT" || license.Source != "license-file" {
		t.Fatalf("expected MIT fallback from LICENSE file, got %#v", license)
	}
}

func TestDetectLicenseAndProvenanceSkipsOversizedLicenseCandidate(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{"name":"demo","version":"0.1.0"}`)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "COPYING"), string(bytes.Repeat([]byte("x"), int(licenseFileReadMaxBytes)+1)))
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License\nPermission is hereby granted...")

	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), depRoot, false)
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

	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), depRoot, false)
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

	_, provenance, _ := detectLicenseAndProvenance(context.Background(), depRoot, true)
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

	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), depRoot, true)
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
	if got, warnings := findLicenseFiles(context.Background(), missing); len(got) != 0 || len(warnings) != 0 {
		t.Fatalf("expected missing dependency root to return nil files without warnings, got files=%#v warnings=%#v", got, warnings)
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

	if files, warnings := findLicenseFiles(context.Background(), depRoot); len(files) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "failed to close dependency root after license file discovery") || !strings.Contains(warnings[0], "close failed") {
		t.Fatalf("expected close failure to discard license files and warn, got files=%#v warnings=%#v", files, warnings)
	}
}

func TestFindLicenseFilesReturnsNilWhenDependencyRootCannotBeOpened(t *testing.T) {
	depRoot := t.TempDir()
	readyErr := errors.New("open blocked")

	originalReady := dependencyRootOpenReadyFn
	dependencyRootOpenReadyFn = func() error { return readyErr }
	t.Cleanup(func() {
		dependencyRootOpenReadyFn = originalReady
	})

	files, warnings := findLicenseFiles(context.Background(), depRoot)
	if len(files) != 0 || len(warnings) != 0 {
		t.Fatalf("expected failed no-follow root open to fail closed, got files=%#v warnings=%#v", files, warnings)
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

	files, warnings := findLicenseFiles(context.Background(), depRoot)
	if !slices.Contains(files, filepath.Join(depRoot, "LICENSE")) {
		t.Fatalf("expected collected license files to be preserved on walk error, got %#v", files)
	}
	if len(warnings) != 1 || warnings[0] != "unable to inspect dependency license path blocked" {
		t.Fatalf("expected stable walk warning, got %#v", warnings)
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
	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), "", false)
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
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read dependency metadata") || !strings.Contains(warnings[0], "close failed") {
		t.Fatalf("expected close failure warning, got %#v", warnings)
	}
}

func TestLicenseWalkWarningFallsBackForBlankPath(t *testing.T) {
	if got := licenseWalkWarning(" \t "); got != dependencyLicenseWalkWarning {
		t.Fatalf("expected blank path warning fallback, got %q", got)
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

	files, warnings := findLicenseFilesWithinRoot(context.Background(), root, depRoot)
	if len(files) != 1 || files[0] != licensePath {
		t.Fatalf("expected best-effort license walk to keep later readable license, got %#v", files)
	}
	if len(warnings) != 1 || warnings[0] != "unable to inspect dependency license path blocked" {
		t.Fatalf("expected stable child subtree warning, got %#v", warnings)
	}
}

func TestFindLicenseFilesWithinRootHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	openCalls := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			openCalls++
			return nil, errors.New("unexpected open")
		},
	}
	files, warnings := findLicenseFilesWithinRoot(ctx, root, t.TempDir())
	if len(files) != 0 || !slices.Equal(warnings, []string{dependencyLicenseWalkWarning}) {
		t.Fatalf("unexpected canceled license result: files=%v warnings=%v", files, warnings)
	}
	if openCalls != 0 {
		t.Fatalf("expected canceled license walk not to open the root, got %d calls", openCalls)
	}
}

func TestDetectLicenseAndProvenanceWarnsOnUnreadableLicenseRoot(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{"name":"demo","version":"1.0.0"}`)

	pkgInfo, err := os.Lstat(filepath.Join(depRoot, licenseTestPackageJSONFileName))
	if err != nil {
		t.Fatalf("lstat package.json: %v", err)
	}
	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			switch name {
			case jsPackageFile:
				return os.Open(filepath.Join(depRoot, jsPackageFile))
			case ".":
				return nil, errors.New("open root failed")
			default:
				return nil, errors.New("unexpected open path")
			}
		},
		lstat: func(name string) (fs.FileInfo, error) {
			if name != jsPackageFile {
				return nil, errors.New("unexpected lstat path")
			}
			return pkgInfo, nil
		},
	}

	originalOpen := openLicenseValidatedRoot
	openLicenseValidatedRoot = func(string) (safeio.Root, string, error) {
		return root, depRoot, nil
	}
	t.Cleanup(func() {
		openLicenseValidatedRoot = originalOpen
	})

	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), depRoot, false)
	if license == nil || !license.Unknown || license.Source != "unknown" {
		t.Fatalf("expected unknown license when root walk fails, got %#v", license)
	}
	if provenance == nil || provenance.Source != "local-manifest" {
		t.Fatalf("expected manifest provenance to survive unreadable license root, got %#v", provenance)
	}
	wantWarning := dependencyLicenseWalkWarning
	if len(warnings) != 1 || warnings[0] != wantWarning {
		t.Fatalf("expected exact root-walk warning %q, got %#v", wantWarning, warnings)
	}
}

func TestLicenseFileProbeWrappersClearResultsWhenRootCloseFails(t *testing.T) {
	depRoot := t.TempDir()
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, licensePath, "MIT License\n")

	installClosingLicenseRoot(t, errors.New("close failed"))

	license, warnings := detectLicenseFromFiles(context.Background(), depRoot)
	assertNilLicenseWithSingleWarning(t, license, warnings, "failed to close dependency root after license file detection", "close failed")

	probe, warnings := probeLicenseFiles(context.Background(), depRoot)
	assertNilLicenseProbeWithSingleWarning(t, probe, warnings, "failed to close dependency root after license file probing", "close failed")

	probe, warnings = probeLicenseCandidates(context.Background(), depRoot, []string{licensePath})
	assertNilLicenseProbeWithSingleWarning(t, probe, warnings, "failed to close dependency root after license candidate probing", "close failed")

	probe, warnings = probeLicenseCandidate(context.Background(), depRoot, licensePath)
	assertNilLicenseProbeWithSingleWarning(t, probe, warnings, "failed to close dependency root after license candidate probing", "close failed")
}

func TestLicenseFileProbeWrappersPreserveOversizedAndCloseWarnings(t *testing.T) {
	depRoot := t.TempDir()
	oversized := filepath.Join(depRoot, "COPYING")
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, oversized, string(bytes.Repeat([]byte("x"), int(licenseFileReadMaxBytes)+1)))
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

	if license, warnings := detectLicenseFromFiles(context.Background(), depRoot); license != nil {
		t.Fatalf("expected close failure to clear detected license, got %#v", license)
	} else {
		assertExactWarnings(t, warnings, []string{
			"failed to close dependency root after license file detection: close failed",
			fmt.Sprintf("skipped license candidate COPYING above %d bytes", licenseFileReadMaxBytes),
		})
	}

	if probe, warnings := probeLicenseFiles(context.Background(), depRoot); probe != nil {
		t.Fatalf("expected close failure to clear file probe, got %#v", probe)
	} else {
		assertExactWarnings(t, warnings, []string{
			"failed to close dependency root after license file probing: close failed",
			fmt.Sprintf("skipped license candidate COPYING above %d bytes", licenseFileReadMaxBytes),
		})
	}

	if probe, warnings := probeLicenseCandidates(context.Background(), depRoot, []string{oversized, oversized}); probe != nil {
		t.Fatalf("expected oversized candidate probe to remain nil, got %#v", probe)
	} else {
		assertExactWarnings(t, warnings, []string{
			"failed to close dependency root after license candidate probing: close failed",
			fmt.Sprintf("skipped license candidate COPYING above %d bytes", licenseFileReadMaxBytes),
		})
	}

	if probe, warnings := probeLicenseCandidate(context.Background(), depRoot, oversized); probe != nil {
		t.Fatalf("expected oversized single-candidate probe to remain nil, got %#v", probe)
	} else {
		assertExactWarnings(t, warnings, []string{
			"failed to close dependency root after license candidate probing: close failed",
			fmt.Sprintf("skipped license candidate COPYING above %d bytes", licenseFileReadMaxBytes),
		})
	}
}

func TestLicenseFileProbeWrappersReturnDetectedLicenseSignals(t *testing.T) {
	depRoot := t.TempDir()
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, licensePath, "MIT License\n")

	license, warnings := detectLicenseFromFiles(context.Background(), depRoot)
	assertDetectedLicense(t, license, warnings)

	probe, warnings := probeLicenseFiles(context.Background(), depRoot)
	assertDetectedLicenseProbe(t, probe, warnings)

	probe, warnings = probeLicenseCandidates(context.Background(), depRoot, []string{licensePath})
	assertDetectedLicenseProbe(t, probe, warnings)

	probe, warnings = probeLicenseCandidate(context.Background(), depRoot, licensePath)
	assertDetectedLicenseProbe(t, probe, warnings)
}

func TestLicenseFileProbeWrappersPreserveWalkAndCloseWarnings(t *testing.T) {
	depRoot := t.TempDir()
	root := &closingLicenseRoot{
		Root: &fakeJSRoot{
			open: func(name string) (safeio.File, error) {
				if name == "." {
					return nil, errors.New("open root failed")
				}
				return nil, errors.New("unexpected open path")
			},
		},
		closeErr: errors.New("close failed"),
	}

	originalOpen := openLicenseValidatedRoot
	openLicenseValidatedRoot = func(string) (safeio.Root, string, error) {
		return root, depRoot, nil
	}
	t.Cleanup(func() {
		openLicenseValidatedRoot = originalOpen
	})

	if license, warnings := detectLicenseFromFiles(context.Background(), depRoot); license != nil {
		t.Fatalf("expected walk failure to leave license unset, got %#v", license)
	} else {
		assertExactWarnings(t, warnings, []string{
			"failed to close dependency root after license file detection: close failed",
			dependencyLicenseWalkWarning,
		})
	}

	if probe, warnings := probeLicenseFiles(context.Background(), depRoot); probe != nil {
		t.Fatalf("expected walk failure to leave file probe unset, got %#v", probe)
	} else {
		assertExactWarnings(t, warnings, []string{
			"failed to close dependency root after license file probing: close failed",
			dependencyLicenseWalkWarning,
		})
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

	license, provenance, warnings := detectLicenseAndProvenance(context.Background(), root, false)
	if license == nil || license.SPDX != "MIT" {
		t.Fatalf("expected license detection to succeed before close failure, got %#v", license)
	}
	if provenance == nil || provenance.Source != "local-manifest" {
		t.Fatalf("expected provenance detection to succeed before close failure, got %#v", provenance)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "failed to close dependency root after license/provenance detection") || !strings.Contains(warnings[0], "close failed") {
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

	files, warnings := findLicenseFiles(context.Background(), root)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings while collecting readable license files, got %#v", warnings)
	}
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
	files, warnings := findLicenseFiles(context.Background(), root)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for file-limit scan, got %#v", warnings)
	}
	if len(files) > 5 {
		t.Fatalf("expected at most five files, got %d", len(files))
	}
}

func TestDetectLicenseFromFilesNoMatch(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "LICENSE"), "custom internal license text")
	if got, warnings := detectLicenseFromFiles(context.Background(), root); got != nil || len(warnings) != 0 {
		t.Fatalf("expected nil fallback for unknown license text without warnings, got license=%#v warnings=%#v", got, warnings)
	}
	if got, warnings := detectLicenseFromFiles(context.Background(), filepath.Join(root, "missing")); got != nil || len(warnings) != 0 {
		t.Fatalf("expected missing root to return nil fallback without warnings, got license=%#v warnings=%#v", got, warnings)
	}
}

func TestProbeLicenseCandidatesSkipsUnknownUntilMatch(t *testing.T) {
	root := t.TempDir()
	unknown := filepath.Join(root, "LICENSE")
	known := filepath.Join(root, "COPYING")
	testutil.MustWriteFile(t, unknown, "custom internal license text")
	testutil.MustWriteFile(t, known, "MIT License\nPermission is hereby granted...")

	probe, warnings := probeLicenseCandidates(context.Background(), root, []string{unknown, known})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for readable candidate probe chain, got %#v", warnings)
	}
	if probe == nil {
		t.Fatalf("expected probe result for known license candidate")
	}
	if probe.path != known || probe.spdx != "MIT" || probe.confidence != "medium" {
		t.Fatalf("unexpected probe result: %#v", probe)
	}
	if probe, warnings := probeLicenseCandidates(context.Background(), filepath.Join(root, "missing"), []string{known}); probe != nil || len(warnings) != 0 {
		t.Fatalf("expected missing root to return no candidate probe without warnings, got probe=%#v warnings=%#v", probe, warnings)
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

	if probe, warnings := probeLicenseFiles(context.Background(), root); probe == nil || probe.path != resolvedCandidate || len(warnings) != 0 {
		t.Fatalf("expected probeLicenseFiles wrapper to return license candidate without warnings, got probe=%#v warnings=%#v", probe, warnings)
	}
	if probe, warnings := probeLicenseCandidate(context.Background(), root, candidate); probe == nil || probe.spdx != "MIT" || len(warnings) != 0 {
		t.Fatalf("expected probeLicenseCandidate wrapper to detect SPDX without warnings, got probe=%#v warnings=%#v", probe, warnings)
	}
	if probe, warnings := probeLicenseFiles(context.Background(), filepath.Join(root, "missing")); probe != nil || len(warnings) != 0 {
		t.Fatalf("expected missing root to return no file probe without warnings, got probe=%#v warnings=%#v", probe, warnings)
	}
	if probe, warnings := probeLicenseCandidate(context.Background(), filepath.Join(root, "missing"), candidate); probe != nil || len(warnings) != 0 {
		t.Fatalf("expected missing root to return no candidate probe without warnings, got probe=%#v warnings=%#v", probe, warnings)
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
	if probe != nil || warning != "skipped license candidate LICENSE because it could not be read safely" {
		t.Fatalf("expected escaping candidate path warning, got probe=%#v warning=%q", probe, warning)
	}
}

func TestProbeLicenseCandidateWithinRootWarnsOnUnreadableCandidate(t *testing.T) {
	depRoot := t.TempDir()
	licensePath := filepath.Join(depRoot, "LICENSE")
	testutil.MustWriteFile(t, filepath.Join(depRoot, licenseTestPackageJSONFileName), `{"name":"demo","version":"0.1.0"}`)

	root := &fakeJSRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "LICENSE" {
				return nil, errors.New("unexpected lstat path")
			}
			return nil, errors.New("lstat failed")
		},
	}

	probe, warning := probeLicenseCandidateWithinRoot(root, depRoot, licensePath)
	if probe != nil || warning != "skipped license candidate LICENSE because it could not be read safely" {
		t.Fatalf("expected unreadable candidate warning, got probe=%#v warning=%q", probe, warning)
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
	if files, warnings := findLicenseFiles(context.Background(), root); len(files) != 0 || len(warnings) != 0 {
		t.Fatalf("expected no files for missing root, got files=%#v warnings=%#v", files, warnings)
	}
}

func assertExactWarnings(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected warnings: got %#v want %#v", got, want)
	}
}

func installClosingLicenseRoot(t *testing.T, closeErr error) {
	t.Helper()

	originalOpen := openLicenseValidatedRoot
	openLicenseValidatedRoot = func(path string) (safeio.Root, string, error) {
		baseRoot, validatedRoot, err := openValidatedRootNoFollow(path)
		if err != nil {
			return nil, "", err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: closeErr}, validatedRoot, nil
	}
	t.Cleanup(func() {
		openLicenseValidatedRoot = originalOpen
	})
}

func assertNilLicenseWithSingleWarning(t *testing.T, license *report.DependencyLicense, warnings []string, fragments ...string) {
	t.Helper()

	if license != nil {
		t.Fatalf("expected nil license, got %#v", license)
	}
	assertSingleLicenseWarningContains(t, warnings, fragments...)
}

func assertNilLicenseProbeWithSingleWarning(t *testing.T, probe *licenseFileProbe, warnings []string, fragments ...string) {
	t.Helper()

	if probe != nil {
		t.Fatalf("expected nil probe, got %#v", probe)
	}
	assertSingleLicenseWarningContains(t, warnings, fragments...)
}

func assertSingleLicenseWarningContains(t *testing.T, warnings []string, fragments ...string) {
	t.Helper()

	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	for _, fragment := range fragments {
		if !strings.Contains(warnings[0], fragment) {
			t.Fatalf("expected warning %q to contain %q", warnings[0], fragment)
		}
	}
}

func assertDetectedLicense(t *testing.T, license *report.DependencyLicense, warnings []string) {
	t.Helper()

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for readable license detection, got %#v", warnings)
	}
	if license == nil || license.SPDX != "MIT" {
		t.Fatalf("expected license detection from readable license file, got %#v", license)
	}
}

func assertDetectedLicenseProbe(t *testing.T, probe *licenseFileProbe, warnings []string) {
	t.Helper()

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for readable license probe, got %#v", warnings)
	}
	if probe == nil || filepath.Base(probe.path) != "LICENSE" || probe.spdx != "MIT" {
		t.Fatalf("expected readable MIT license probe, got %#v", probe)
	}
}
