package rust

import (
	"path/filepath"
	"strings"
	"testing"
)

type dependencyExpectation struct {
	name      string
	canonical string
	localPath bool
	renamed   bool
}

func assertManifestMeta(t *testing.T, got manifestMeta, wantHasPackage bool, wantWorkspaceMembers []string) {
	t.Helper()

	if got.HasPackage != wantHasPackage || !stringSlicesEqual(got.WorkspaceMembers, wantWorkspaceMembers) {
		t.Fatalf("unexpected manifest meta: %#v", got)
	}
}

func assertDependencyInfo(t *testing.T, deps map[string]dependencyInfo, want dependencyExpectation) {
	t.Helper()

	got := deps[want.name]
	if got.Canonical != want.canonical || got.LocalPath != want.localPath || got.Renamed != want.renamed {
		t.Fatalf("unexpected dependency %q: %#v", want.name, got)
	}
}

func assertStringFields(t *testing.T, got, want map[string]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("unexpected flattened fields: %#v", got)
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("unexpected flattened fields: %#v", got)
		}
	}
}

func assertBool(t *testing.T, name string, got, want bool) {
	t.Helper()

	if got != want {
		t.Fatalf("unexpected %s result: got %t want %t", name, got, want)
	}
}

func assertStringSlice(t *testing.T, name string, got, want []string) {
	t.Helper()

	if !stringSlicesEqual(got, want) {
		t.Fatalf("unexpected %s: got %#v want %#v", name, got, want)
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func assertWorkspaceMemberPatternMatch(t *testing.T, pattern, member string, want bool) {
	t.Helper()

	got, err := workspaceMemberPatternMatches(pattern, member)
	if err != nil {
		t.Fatalf("match workspace member pattern %q: %v", pattern, err)
	}
	if got != want {
		t.Fatalf("unexpected workspace member match for %q and %q: got %t want %t", pattern, member, got, want)
	}
}

func assertWorkspaceMemberPatternError(t *testing.T, pattern, member string) {
	t.Helper()

	if _, err := workspaceMemberPatternMatches(pattern, member); err == nil {
		t.Fatalf("expected workspace member glob %q to return an error", pattern)
	}
}

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
	assertManifestMeta(t, cargoManifestMeta(document), true, []string{"crates/a", "crates/b"})

	deps := cargoManifestDependencies(document)
	for _, want := range []dependencyExpectation{
		{name: "serde", canonical: "serde"},
		{name: "serde-json-alias", canonical: "serde-json", localPath: true, renamed: true},
		{name: "workspace-dep", canonical: "workspace-dep"},
		{name: "clap-alias", canonical: "clap", renamed: true},
	} {
		assertDependencyInfo(t, deps, want)
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
	for _, want := range []dependencyExpectation{
		{name: "build-dep", canonical: "build-dep"},
		{name: "plain-dep", canonical: "plain-dep"},
		{name: "weird-dep", canonical: "weird-dep"},
	} {
		assertDependencyInfo(t, direct, want)
	}

	fields := flattenTomlStringFields(map[string]any{
		" version ": map[string]any{" ref ": "shared"},
		"ignored":   12,
		" ":         "skip",
	})
	assertStringFields(t, fields, map[string]string{"version.ref": "shared"})

	assertManifestMeta(t, parseCargoManifestContent("not valid = "), false, nil)
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
	assertBool(t, "root subpath", isSubPath(repo, repo), true)
	assertBool(t, "child subpath", isSubPath(repo, inside), true)
	assertBool(t, "outside subpath", isSubPath(repo, outside), false)
	assertBool(t, "invalid root subpath", isSubPath("\x00", inside), false)
	assertBool(t, "invalid child subpath", isSubPath(repo, "\x00"), false)
	assertBool(t, "same invalid path", samePath("\x00", "\x00"), true)
	assertBool(t, "different invalid path", samePath("\x00", "\x00-other"), false)
	assertStringSlice(t, "unique paths", uniquePaths([]string{" b ", "a", "", "b", "a"}), []string{".", "a", "b"})

	assertWorkspaceMemberPatternMatch(t, "", "crate", false)
	assertWorkspaceMemberPatternError(t, "[", "crate")

	meta := manifestMeta{}
	assertBool(t, "workspace member continuation", parseWorkspaceMembersLine(`"crates/a"]`, "workspace", true, &meta), false)
	assertStringSlice(t, "workspace members", meta.WorkspaceMembers, []string{"crates/a"})

	for _, section := range []string{"target.cfg.dependencies", "target.cfg.dev-dependencies", "target.cfg.build-dependencies"} {
		assertBool(t, section, isDependencySection(section), true)
	}
	assertBool(t, "target.cfg.profile", isDependencySection("target.cfg.profile"), false)
	if got := strings.TrimSpace(stripTomlComment(`name = 'x#y' # comment`)); got != `name = 'x#y'` {
		t.Fatalf("expected single-quoted hash to be preserved while stripping comment, got %q", got)
	}
}
