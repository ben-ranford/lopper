package rust

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredCargoManifestParserBranches(t *testing.T) {
	content := []byte(`
[package]
name = "demo"

[workspace]
members = ["crates/a", "crates/b"]

[dependencies]
serde = "1"
serde_json_alias = { package = "serde_json", path = "./vendor/serde_json" }

[workspace.dependencies]
workspace_dep = "1"

[target.'cfg(unix)'.dev-dependencies]
clap_alias = { package = "clap", version = "4" }
`)
	document, err := parseCargoManifestDocument(content)
	if err != nil {
		t.Fatalf("parse manifest document: %v", err)
	}
	meta := cargoManifestMeta(document)
	if !meta.HasPackage || strings.Join(meta.WorkspaceMembers, ",") != "crates/a,crates/b" {
		t.Fatalf("unexpected manifest meta: %#v", meta)
	}
	deps := cargoManifestDependencies(document)
	if deps["serde"].Canonical != "serde" {
		t.Fatalf("expected string dependency to map canonically, got %#v", deps["serde"])
	}
	if deps["serde-json-alias"].Canonical != "serde-json" || !deps["serde-json-alias"].LocalPath || !deps["serde-json-alias"].Renamed {
		t.Fatalf("expected renamed local dependency from inline table, got %#v", deps["serde-json-alias"])
	}
	if deps["workspace-dep"].Canonical != "workspace-dep" {
		t.Fatalf("expected workspace dependency table to parse, got %#v", deps["workspace-dep"])
	}
	if deps["clap-alias"].Canonical != "clap" || !deps["clap-alias"].Renamed {
		t.Fatalf("expected target dependency table to parse, got %#v", deps["clap-alias"])
	}
	direct := map[string]dependencyInfo{}
	addTargetTomlDependencyTables(direct, map[string]any{
		"cfg(windows)": "ignored",
		"cfg(test)": map[string]any{
			"build-dependencies": map[string]any{"build_dep": "1"},
		},
	})
	addTomlDependencyTable(direct, "ignored")
	addTomlDependency(direct, "", "ignored")
	addTomlDependency(direct, "plain_dep", "1")
	addTomlDependency(direct, "weird_dep", map[string]any{"package": 12, "path": 13})
	if direct["build-dep"].Canonical != "build-dep" || direct["plain-dep"].Canonical != "plain-dep" {
		t.Fatalf("expected direct TOML dependency helpers to keep canonical aliases, got %#v", direct)
	}
	if direct["weird-dep"].Renamed || direct["weird-dep"].LocalPath {
		t.Fatalf("expected non-string inline fields to be ignored, got %#v", direct["weird-dep"])
	}
	fields := flattenTomlStringFields(map[string]any{
		" version ": map[string]any{" ref ": "shared"},
		"ignored":   12,
		" ":         "skip",
	})
	if fields["version.ref"] != "shared" || len(fields) != 1 {
		t.Fatalf("expected nested TOML string fields only, got %#v", fields)
	}

	if meta := parseCargoManifestContent("not valid = "); meta.HasPackage || len(meta.WorkspaceMembers) != 0 {
		t.Fatalf("expected invalid content helper to return zero meta, got %#v", meta)
	}
	if deps := parseCargoDependencies("not valid = "); len(deps) != 0 {
		t.Fatalf("expected invalid dependency content to return no deps, got %#v", deps)
	}
}

func TestParseCargoManifestReportsRelativeTOMLError(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, cargoTomlName)
	writeFile(t, manifestPath, "not valid = ")
	if _, _, err := parseCargoManifest(manifestPath, repo); err == nil || !strings.Contains(err.Error(), cargoTomlName) {
		t.Fatalf("expected relative Cargo.toml parse error, got %v", err)
	}
}

func TestInlineTomlFieldFallbackBranches(t *testing.T) {
	fields := parseInlineFields(`{ package = "serde_json", bad = provider("x"), path = "./vendor" }`)
	if fields["package"] != "serde_json" || fields["path"] != "./vendor" {
		t.Fatalf("expected fallback inline field parser to keep string fields, got %#v", fields)
	}
	if got, ok := parseTomlStringLiteral(`"serde_json"`); !ok || got != "serde_json" {
		t.Fatalf("expected quoted TOML literal to parse, got %q %t", got, ok)
	}
	if got, ok := parseTomlStringLiteral(`serde_json`); ok || got != "" {
		t.Fatalf("expected unquoted TOML literal to be rejected, got %q %t", got, ok)
	}
	if fields, err := parseInlineTomlFields("1"); err != nil || len(fields) != 0 {
		t.Fatalf("expected non-table inline TOML value to parse as empty fields, got %#v %v", fields, err)
	}
	if got := tomlStringSlice([]any{" a ", 12, ""}); strings.Join(got, ",") != "a" {
		t.Fatalf("expected TOML string slice helper to keep only non-empty strings, got %#v", got)
	}
}

func TestRustManifestHelperCoverageBranches(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, "src", "lib.rs")
	outside := filepath.Join(filepath.Dir(repo), "outside.rs")
	if !isSubPath(repo, repo) || !isSubPath(repo, inside) {
		t.Fatalf("expected repo and child paths to be within root")
	}
	if isSubPath(repo, outside) || isSubPath("\x00", inside) || isSubPath(repo, "\x00") {
		t.Fatalf("expected outside or invalid paths to be rejected")
	}
	if !samePath("\x00", "\x00") || samePath("\x00", "\x00-other") {
		t.Fatalf("expected samePath fallback to compare cleaned invalid paths")
	}
	if got := uniquePaths([]string{" b ", "a", "", "b", "a"}); strings.Join(got, ",") != ".,a,b" {
		t.Fatalf("expected unique paths to trim, sort, and dedupe, got %#v", got)
	}

	if matched, err := workspaceMemberPatternMatches("", "crate"); err != nil || matched {
		t.Fatalf("expected empty workspace member pattern to miss, got %t %v", matched, err)
	}
	if _, err := workspaceMemberPatternMatches("[", "crate"); err == nil {
		t.Fatalf("expected invalid workspace member glob to return an error")
	}
	meta := manifestMeta{}
	if parseWorkspaceMembersLine(`"crates/a"]`, "workspace", true, &meta) {
		t.Fatalf("expected closing workspace members continuation to report completion")
	}
	if len(meta.WorkspaceMembers) != 1 || meta.WorkspaceMembers[0] != "crates/a" {
		t.Fatalf("expected continuation workspace member to parse, got %#v", meta.WorkspaceMembers)
	}

	for _, section := range []string{"target.cfg.dependencies", "target.cfg.dev-dependencies", "target.cfg.build-dependencies"} {
		if !isDependencySection(section) {
			t.Fatalf("expected %s to be a dependency section", section)
		}
	}
	if isDependencySection("target.cfg.profile") {
		t.Fatalf("expected non-dependency target section to be ignored")
	}
	if got := stripTomlComment(`name = 'x#y' # comment`); strings.TrimSpace(got) != `name = 'x#y'` {
		t.Fatalf("expected single-quoted hash to be preserved while stripping comment, got %q", got)
	}
}
