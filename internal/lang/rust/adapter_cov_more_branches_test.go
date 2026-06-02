package rust

import (
	"context"
	"errors"
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

func TestRustWorkspaceMemberPatternBranches(t *testing.T) {
	matched, err := workspaceMemberPatternMatches(filepath.Join("crates", "**", "tool"), filepath.Join("crates", "tool"))
	if err != nil || !matched {
		t.Fatalf("expected recursive glob to match zero intermediate segments, matched=%v err=%v", matched, err)
	}

	matched, err = workspaceMemberPatternMatches(filepath.Join("crates", "**", "tool"), filepath.Join("crates", "nested", "bin", "tool"))
	if err != nil || !matched {
		t.Fatalf("expected recursive glob to match nested segments, matched=%v err=%v", matched, err)
	}

	matched, err = workspaceMemberPatternMatches(filepath.Join("crates", "**", "tool"), filepath.Join("crates", "nested", "bin", "lib"))
	if err != nil || matched {
		t.Fatalf("expected non-matching recursive glob to fail, matched=%v err=%v", matched, err)
	}

	matched, err = matchWorkspaceMemberPatternParts(nil, nil)
	if err != nil || !matched {
		t.Fatalf("expected empty pattern parts to match empty candidate, matched=%v err=%v", matched, err)
	}

	if matched, err = workspaceMemberPatternMatches("[", "crate"); err == nil || matched {
		t.Fatalf("expected invalid workspace glob to report an error, matched=%v err=%v", matched, err)
	}

	if matched, err = workspaceMemberPatternMatches("crates/*", ""); err != nil || matched {
		t.Fatalf("expected empty candidate not to match, matched=%v err=%v", matched, err)
	}
}

func TestRustWorkspaceMemberCollectorBranches(t *testing.T) {
	repo := t.TempDir()
	collector := workspaceMemberCollector{
		repoPath: repo,
		pattern:  filepath.Join("crates", "*"),
		roots:    map[string]struct{}{},
	}

	if matched, err := collector.matchDirectory("", nil, context.Canceled); !errors.Is(err, context.Canceled) || matched {
		t.Fatalf("expected walk error to short-circuit collector, matched=%v err=%v", matched, err)
	}

	targetDir := filepath.Join(repo, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	targetEntry := rustDirEntry(t, repo, "target")
	if matched, err := collector.matchDirectory(targetDir, targetEntry, nil); !errors.Is(err, filepath.SkipDir) || matched {
		t.Fatalf("expected target dir to be skipped, matched=%v err=%v", matched, err)
	}

	filePath := filepath.Join(repo, "crates", "file.txt")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("mkdir crates: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("not a manifest"), 0o644); err != nil {
		t.Fatalf("write file entry: %v", err)
	}
	fileEntry := rustDirEntry(t, filepath.Dir(filePath), filepath.Base(filePath))
	if matched, err := collector.matchDirectory(filePath, fileEntry, nil); err != nil || matched {
		t.Fatalf("expected file entry not to match workspace member, matched=%v err=%v", matched, err)
	}

	noManifestDir := filepath.Join(repo, "crates", "no-manifest")
	if err := os.MkdirAll(noManifestDir, 0o755); err != nil {
		t.Fatalf("mkdir no-manifest dir: %v", err)
	}
	noManifestEntry := rustDirEntry(t, filepath.Dir(noManifestDir), filepath.Base(noManifestDir))
	if matched, err := collector.matchDirectory(noManifestDir, noManifestEntry, nil); err != nil || matched {
		t.Fatalf("expected directory without Cargo.toml not to match, matched=%v err=%v", matched, err)
	}

	memberDir := filepath.Join(repo, "crates", "member")
	if err := os.MkdirAll(memberDir, 0o755); err != nil {
		t.Fatalf("mkdir member dir: %v", err)
	}
	writeFile(t, filepath.Join(memberDir, cargoManifestFile), "[package]\nname = \"member\"\nversion = \"0.1.0\"\n")
	memberEntry := rustDirEntry(t, filepath.Dir(memberDir), filepath.Base(memberDir))
	if matched, err := collector.matchDirectory(memberDir, memberEntry, nil); err != nil || !matched {
		t.Fatalf("expected directory with Cargo.toml to match, matched=%v err=%v", matched, err)
	}
	if err := collector.walk(memberDir, memberEntry, nil); err != nil {
		t.Fatalf("collector walk: %v", err)
	}
	if _, ok := collector.roots[memberDir]; !ok {
		t.Fatalf("expected collector walk to record matched root, got %#v", collector.roots)
	}
}

func TestRustHelperBranchCoverage(t *testing.T) {
	repo := t.TempDir()
	nested := filepath.Join(repo, "nested", "crate")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested crate: %v", err)
	}
	other := filepath.Join(repo, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir other dir: %v", err)
	}

	if !isSubPath(repo, nested) {
		t.Fatalf("expected nested path to be a subpath")
	}
	if isSubPath(nested, repo) {
		t.Fatalf("did not expect repo root to be a subpath of nested path")
	}
	if !samePath(filepath.Join(repo, ".", "nested"), filepath.Join(repo, "nested")) {
		t.Fatalf("expected cleaned equivalent paths to compare equal")
	}
	if samePath(filepath.Join(repo, "nested"), other) {
		t.Fatalf("did not expect different directories to compare equal")
	}

	roots := dropNestedScanRoots([]string{repo, nested, other}, nested)
	if len(roots) != 2 || roots[0] != repo || roots[1] != other {
		t.Fatalf("expected only nested candidate root to be dropped, got %#v", roots)
	}

	if index := findRustUseAliasIndex("serde as de"); index <= 0 {
		t.Fatalf("expected alias index for valid use alias, got %d", index)
	}
	if index := findRustUseAliasIndex("serdeasde"); index != -1 {
		t.Fatalf("expected invalid alias form to be ignored, got %d", index)
	}

	base, local := parseUseLocalAlias("serde as de")
	if base != "serde" || local != "de" {
		t.Fatalf("expected parseUseLocalAlias to split base/local, got base=%q local=%q", base, local)
	}
	base, local = parseUseLocalAlias(" as de")
	if base != " as de" || local != "" {
		t.Fatalf("expected malformed alias to round-trip unchanged, got base=%q local=%q", base, local)
	}
}

func rustDirEntry(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("expected dir entry %s in %s", name, dir)
	return nil
}
