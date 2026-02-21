package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const serdeDePath = "serde::de"

func TestRustAdapterCoverageHelpers(t *testing.T) {
	assertRustWorkspaceAndScanHelpers(t)
	assertRustUseImportHelpers(t)
	assertRustDependencyResolutionHelpers(t)
	assertRustPathAndWeightsHelpers(t)
	assertRustAdapterAndParseHelpers(t)
	assertRustSummarizeHelpers(t)
}

func TestRustAdapterCoverageHelpersMore(t *testing.T) {
	repo := t.TempDir()
	cargoAsDir := filepath.Join(repo, cargoTomlName)
	if err := os.MkdirAll(cargoAsDir, 0o755); err != nil {
		t.Fatalf("mkdir Cargo.toml dir: %v", err)
	}
	if _, err := applyRustRootSignals(repo, &language.Detection{}, map[string]struct{}{}); err == nil {
		t.Fatalf("expected applyRustRootSignals to fail when Cargo.toml is a directory")
	}

	addWorkspaceMemberRoot(repo, "[", map[string]struct{}{})
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "mod"), 0o755); err != nil {
		t.Fatalf("mkdir outside mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "mod", cargoTomlName), []byte("[package]\nname=\"m\"\n"), 0o600); err != nil {
		t.Fatalf("write outside manifest: %v", err)
	}
	roots := map[string]struct{}{}
	addWorkspaceMemberRoot(repo, filepath.Join("..", filepath.Base(outside), "mod"), roots)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanRepoRoot(ctx, repo, repo, map[string]dependencyInfo{}, map[string]struct{}{}, new(int), &scanResult{}); err == nil {
		t.Fatalf("expected canceled scanRepoRoot context error")
	}

	targetDir := filepath.Join(repo, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := scanRepoRoot(context.Background(), repo, repo, map[string]dependencyInfo{}, map[string]struct{}{}, new(int), &scanResult{}); err != nil {
		t.Fatalf("scanRepoRoot with skipped directories: %v", err)
	}

	scan := &scanResult{UnresolvedImports: map[string]int{}}
	extern := parseExternCrateImports("extern crate std;\n", "lib.rs", repo, map[string]dependencyInfo{}, scan)
	if len(extern) != 0 {
		t.Fatalf("expected std extern crate to be ignored, got %#v", extern)
	}

	entries := make([]usePathEntry, 0)
	expandUsePart("", "", &entries)
	expandUsePart("{serde, crate::x}", "", &entries)
	if len(entries) == 0 {
		t.Fatalf("expected brace-group use expansion entries")
	}
}

func assertRustWorkspaceAndScanHelpers(t *testing.T) {
	t.Helper()
	if paths, warning := resolveWorkspaceMemberManifestPaths(t.TempDir(), "missing/*"); len(paths) != 0 || warning == "" {
		t.Fatalf("expected unresolved workspace-member warning")
	}
	if roots := scanRoots(nil, "/repo"); len(roots) != 1 || roots[0] != "/repo" {
		t.Fatalf("expected scanRoots fallback to repo root, got %#v", roots)
	}
	warnings := compileScanWarnings(scanResult{
		SkippedLargeFiles:        1,
		SkippedFilesByBoundLimit: true,
		UnresolvedImports:        map[string]int{"x": 1},
	})
	if len(warnings) == 0 {
		t.Fatalf("expected compileScanWarnings output")
	}
}

func assertRustUseImportHelpers(t *testing.T) {
	t.Helper()
	if _, ok := makeUseImportBinding(usePathEntry{}, useImportContext{}); ok {
		t.Fatalf("expected empty use entry to fail binding")
	}
	name, local := normalizeUseSymbolNames(usePathEntry{Path: serdeDePath + "::*", Wildcard: true}, serdeDePath)
	if name != "*" || local != "de" {
		t.Fatalf("expected wildcard symbol normalization, got name=%q local=%q", name, local)
	}
	if expandUseBraceGroup("serde", "", &[]usePathEntry{}) {
		t.Fatalf("expected non-brace use part not to expand")
	}
	if expandUsePrefixedBraceGroup("serde::x", "", &[]usePathEntry{}) {
		t.Fatalf("expected non-prefixed brace group not to expand")
	}
	if path, prefix, wildcard := normalizeUseWildcard("*", serdeDePath); !wildcard || path != serdeDePath || prefix != "" {
		t.Fatalf("unexpected wildcard normalization result: path=%q prefix=%q wildcard=%v", path, prefix, wildcard)
	}
}

func assertRustDependencyResolutionHelpers(t *testing.T) {
	t.Helper()
	extern := parseExternCrateImports("extern crate unknown;", "lib.rs", t.TempDir(), map[string]dependencyInfo{}, &scanResult{
		UnresolvedImports: map[string]int{},
	})
	if len(extern) != 1 {
		t.Fatalf("expected unresolved extern crate import to be surfaced, got %#v", extern)
	}
	if resolveDependency("::", "", map[string]dependencyInfo{}, &scanResult{UnresolvedImports: map[string]int{}}) != "" {
		t.Fatalf("expected empty normalized root dependency to resolve to empty")
	}
	scan := &scanResult{UnresolvedImports: map[string]int{}}
	if resolveDependency("::", "", map[string]dependencyInfo{}, scan) != "" {
		t.Fatalf("expected empty normalized dependency for root path")
	}
}

func assertRustPathAndWeightsHelpers(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	if !isLocalRustModuleWithCache(nil, repo, "missing_mod") {
		// nil scan path executes non-cached branch; missing module should be false
	}
	if isLocalRustModuleWithCache(nil, repo, "missing_mod") {
		t.Fatalf("expected missing local module to be false")
	}
	if resolveRemovalCandidateWeights(nil) != report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected default removal weights when nil")
	}
	if !isSubPath(repo, repo) {
		t.Fatalf("expected repo to be a subpath of itself")
	}
	if samePath(filepath.Join(repo, "a"), filepath.Join(repo, "b")) {
		t.Fatalf("expected different paths not to be same")
	}
	if got := resolveRemovalCandidateWeights(&report.RemovalCandidateWeights{Usage: 2, Impact: 1, Confidence: 1}); got.Usage <= 0 {
		t.Fatalf("expected normalized non-nil removal weights")
	}
}

func assertRustAdapterAndParseHelpers(t *testing.T) {
	t.Helper()
	adapter := NewAdapter()
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail for invalid repo path")
	}
	if _, _, ok := parseDependencyInfo("no-assignment"); ok {
		t.Fatalf("expected parseDependencyInfo to reject malformed dependency line")
	}
	if _, _, ok := parseDependencyInfo(`"" = "1"`); ok {
		t.Fatalf("expected parseDependencyInfo to reject empty alias")
	}
}

func assertRustSummarizeHelpers(t *testing.T) {
	t.Helper()
	if warnings := summarizeUnresolved(map[string]int{"b": 1, "a": 1}); len(warnings) != 2 {
		t.Fatalf("expected summarized unresolved warnings, got %#v", warnings)
	}
	if got := uniquePaths([]string{"", "  ", "/tmp/a", "/tmp/a"}); len(got) != 2 {
		t.Fatalf("expected uniquePaths to drop empty duplicates, got %#v", got)
	}
	if !samePath("\x00", "\x00") {
		t.Fatalf("expected samePath fallback on invalid absolute paths")
	}
}
