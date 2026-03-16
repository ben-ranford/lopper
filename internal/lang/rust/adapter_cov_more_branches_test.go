package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestRustAdditionalManifestAndScanBranches(t *testing.T) {
	testRustDetectAndAnalysisPathErrors(t)
	testRustManifestAndScanErrors(t)
	testRustUseParsingAndPathHelpers(t)
	testRustManifestDiscoveryBranches(t)
	testRustWorkspaceMemberFileMatchBranch(t)
}

func testRustDetectAndAnalysisPathErrors(t *testing.T) {
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), "\x00"); err == nil {
		t.Fatalf("expected invalid repo path to fail detection")
	}
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo path to fail detection")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected invalid repo path to fail analysis")
	}
}

func testRustManifestAndScanErrors(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, cargoManifestFile), 0o755); err != nil {
		t.Fatalf("mkdir Cargo.toml dir: %v", err)
	}
	if _, _, _, _, err := collectManifestData(repo); err == nil {
		t.Fatalf("expected collectManifestData to fail when Cargo.toml is a directory")
	}
	if scanRepoRoot(context.Background(), repo, filepath.Join(repo, "missing"), map[string]dependencyInfo{}, map[string]struct{}{}, new(int), &scanResult{}) == nil {
		t.Fatalf("expected scanRepoRoot to fail for missing root")
	}
	if scanRustSourceFile(repo, repo, filepath.Join(repo, "missing.rs"), map[string]dependencyInfo{}, &scanResult{}) == nil {
		t.Fatalf("expected scanRustSourceFile to fail for missing file")
	}
}

func testRustUseParsingAndPathHelpers(t *testing.T) {
	repo := t.TempDir()
	if got := resolveDependency("dep::thing", repo, map[string]dependencyInfo{"dep": {Canonical: "dep", LocalPath: true}}, &scanResult{UnresolvedImports: map[string]int{}, LocalModuleCache: map[string]bool{}}); got != "" {
		t.Fatalf("expected local-path dependency to resolve to empty, got %q", got)
	}

	if _, _, _, ok := parseUseStatementIndex("use serde::Deserialize;", []int{0, 1, 2}); ok {
		t.Fatalf("expected short use-statement index slice to fail")
	}
	name, local := normalizeUseSymbolNames(usePathEntry{Wildcard: true}, "")
	if name != "*" || local != "" {
		t.Fatalf("expected wildcard fallback with empty module to keep empty local, got name=%q local=%q", name, local)
	}
	if _, _, _, ok := parseUseStatementIndex("use serde::Deserialize;", []int{0, 1, -1, 5}); ok {
		t.Fatalf("expected negative use-statement bounds to fail")
	}

	if isSubPath("\x00", repo) {
		t.Fatalf("expected invalid root path to fail subpath detection")
	}
	if samePath(repo, "\x00") {
		t.Fatalf("expected mismatched valid/invalid paths not to compare equal")
	}
}

func testRustManifestDiscoveryBranches(t *testing.T) {
	paths, warnings, err := discoverManifestsByWalk(t.TempDir())
	if err != nil {
		t.Fatalf("discoverManifestsByWalk empty repo: %v", err)
	}
	if len(paths) != 0 || len(warnings) == 0 {
		t.Fatalf("expected missing-manifest warning for empty repo, paths=%#v warnings=%#v", paths, warnings)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "target"), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("docs"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	paths, warnings, err = discoverManifestsByWalk(repo)
	if err != nil {
		t.Fatalf("discoverManifestsByWalk skip dirs: %v", err)
	}
	if len(paths) != 0 || len(warnings) == 0 {
		t.Fatalf("expected skipped directories and non-manifest files to yield no paths, got paths=%#v warnings=%#v", paths, warnings)
	}
}

func testRustWorkspaceMemberFileMatchBranch(t *testing.T) {
	fileMatchRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fileMatchRepo, "crates"), 0o755); err != nil {
		t.Fatalf("mkdir crates dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileMatchRepo, "crates", "not-a-dir"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write workspace file match: %v", err)
	}
	if roots := resolveWorkspaceMembers(fileMatchRepo, filepath.Join("crates", "*")); len(roots) != 0 {
		t.Fatalf("expected file glob matches to be ignored as workspace members, got %#v", roots)
	}
}

func TestRustWorkspaceMemberOutsideRepoBranch(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "mod"), 0o755); err != nil {
		t.Fatalf("mkdir outside mod: %v", err)
	}
	writeFile(t, filepath.Join(outside, "mod", cargoManifestFile), "[package]\nname = \"outside\"\nversion = \"0.1.0\"\n")

	roots := map[string]struct{}{}
	addWorkspaceMemberRoot(repo, filepath.Join("..", "outside", "mod"), roots)
	if len(roots) != 0 {
		t.Fatalf("expected outside workspace member to be ignored, got %#v", roots)
	}
}
