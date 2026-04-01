package rust

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestRustHelperAdditionalBranches(t *testing.T) {
	if got := uniquePaths([]string{" ./b ", "./a", "./b"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("unexpected unique paths: %#v", got)
	}

	repo := t.TempDir()
	if isSubPath(repo, filepath.Dir(repo)) {
		t.Fatalf("expected parent directory to be outside repo root")
	}
	if !samePath(filepath.Join(repo, ".", "src"), filepath.Join(repo, "src")) {
		t.Fatalf("expected cleaned equivalent paths to compare equal")
	}

	deps := map[string]dependencyInfo{}
	addDependencyFromLine(deps, "dependencies", `serde = { package = "serde_json", path = "./vendor/serde_json" }`)
	info, ok := deps["serde"]
	if !ok || info.Canonical != "serde-json" || !info.Renamed || !info.LocalPath {
		t.Fatalf("unexpected parsed dependency info: %#v", info)
	}
	if canonical, ok := deps["serde-json"]; !ok || canonical.Canonical != "serde-json" || !canonical.LocalPath {
		t.Fatalf("expected canonical dependency alias to be added, got %#v", canonical)
	}

	if _, _, ok := parseDependencyInfo(`serde = { package = "serde_json", path = "./vendor/serde_json" }`); !ok {
		t.Fatalf("expected inline dependency info to parse")
	}
	if fields := parseInlineFields(`{ package = "serde_json", version = "1.0", path = "./vendor/serde_json" }`); len(fields) != 3 {
		t.Fatalf("expected inline dependency fields to include package/version/path, got %#v", fields)
	}

	lookup := map[string]dependencyInfo{"serde": {Canonical: "serde"}}
	if _, ok := parseExternCrateClause([]byte("serde as"), srcLibRS, "", lookup, nil, 1, 1); ok {
		t.Fatalf("expected missing extern crate alias identifier to fail")
	}
	if _, ok := parseExternCrateClause([]byte("1serde"), srcLibRS, "", lookup, nil, 1, 1); ok {
		t.Fatalf("expected invalid extern crate root identifier to fail")
	}

	if offset := skipRustVisibilityPrefix([]byte("pub(crate use serde::Deserialize;")); offset != 0 {
		t.Fatalf("expected malformed pub(crate visibility prefix to be ignored, got %d", offset)
	}

	line, col := lineColumnBytesFrom([]byte("abc"), 3, 0, 99)
	if line != 3 || col != 4 {
		t.Fatalf("expected oversized offset to clamp to end-of-line column, got %d:%d", line, col)
	}
}

func TestRustWorkspaceOnlyDetectionAndAnalysisBranches(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), "[workspace]\nmembers = [\"crates/a\"]\n")
	writeFile(t, filepath.Join(repo, cargoLockFile), cargoLockVersion3)
	writeFile(t, filepath.Join(repo, "crates", "a", cargoManifestFile), "[package]\nname = \"a\"\nversion = \"0.1.0\"\n[dependencies]\nserde = \"1\"\n")
	writeFile(t, filepath.Join(repo, "crates", "a", "src", rustLibFile), "extern crate serde;\nuse serde::de::DeserializeOwned;\n")

	detection := language.Detection{}
	roots := map[string]struct{}{}
	workspaceOnly, err := applyRustRootSignals(repo, &detection, roots)
	if err != nil {
		t.Fatalf("applyRustRootSignals: %v", err)
	}
	if !workspaceOnly {
		t.Fatalf("expected workspace-only root detection")
	}
	if _, ok := roots[repo]; ok {
		t.Fatalf("expected workspace-only root to avoid adding repo root, got %#v", roots)
	}

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	visited := 0
	for _, entry := range entries {
		if entry.Name() != cargoTomlName {
			continue
		}
		if err := walkRustDetectionEntry(filepath.Join(repo, cargoTomlName), entry, repo, workspaceOnly, roots, &detection, &visited); err != nil {
			t.Fatalf("walkRustDetectionEntry root Cargo.toml: %v", err)
		}
	}
	if _, ok := roots[repo]; ok {
		t.Fatalf("expected workspace-only root Cargo.toml walk to keep repo root excluded, got %#v", roots)
	}

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("Analyse workspace-only repo: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependency reports from workspace-only analysis, got %#v", reportData)
	}
}

func TestRustAdditionalExactBranchCoverage(t *testing.T) {
	repo := t.TempDir()
	regularDir := filepath.Join(repo, "src")
	if err := os.MkdirAll(regularDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	visited := 0
	for _, entry := range entries {
		if entry.Name() == "src" {
			if err := walkRustDetectionEntry(regularDir, entry, repo, false, map[string]struct{}{}, &language.Detection{}, &visited); err != nil {
				t.Fatalf("expected non-skipped directory walk to continue, got %v", err)
			}
		}
	}

	deps := map[string]dependencyInfo{}
	addDependencyFromLine(deps, "dependencies", "broken")
	if len(deps) != 0 {
		t.Fatalf("expected malformed dependency line to be ignored, got %#v", deps)
	}
	if _, _, ok := parseDependencyInfo("broken"); ok {
		t.Fatalf("expected malformed dependency info to fail parsing")
	}

	if _, _, ok := parseRustImportStatement([]byte("   "), 0, 3, 1, true); ok {
		t.Fatalf("expected whitespace-only line not to parse as import")
	}
	if _, ok := buildRustUseStatement([]byte("use "), 0, 1, len("use"), []byte("use ")); ok {
		t.Fatalf("expected incomplete use statement to fail")
	}
	if _, ok := matchExternCrateStatement([]byte("extern crate")); ok {
		t.Fatalf("expected missing trailing whitespace after crate to fail")
	}
	if _, ok := parseExternCrateClause([]byte("serde as 1alias"), srcLibRS, "", map[string]dependencyInfo{"serde": {Canonical: "serde"}}, nil, 1, 1); ok {
		t.Fatalf("expected invalid extern crate alias to fail")
	}

	line, col := lineColumnBytesFrom([]byte("abc"), 3, 4, 2)
	if line != 3 || col != 1 {
		t.Fatalf("expected offset before base offset to clamp to base position, got %d:%d", line, col)
	}
}
