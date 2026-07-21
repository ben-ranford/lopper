package analysis

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAnnotateDependencyIdentitiesUsesCargoLockEvidenceForDirectRegistryCrates(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, cargoManifestFileName), `[package]
name = "demo"
version = "0.1.0"

[workspace]

[dependencies]
serde_alias = { package = "serde_json", version = "1" }
case_alias = { package = "Case_Crate", version = "1" }
plain_crate = "2"
multi_crate = "1"
stale_crate = "1"
unlocked_crate = "1"
lookalike_crate = "1"
patched_crate = "1"
git_patched_crate = "1"
local_crate = { path = "./local" }
git_crate = { git = "https://example.com/git-crate", version = "1" }
custom_crate = { registry = "private", version = "1" }
workspace_alias = { workspace = true }
inherited_missing = { workspace = true }
invalid_crate = 42

[dev-dependencies]
dev_crate = "3"

[build-dependencies]
build_crate = "4"

[workspace.dependencies]
workspace_alias = { package = "workspace_crate", version = "5" }

[target.'cfg(unix)'.dependencies]
target_crate = "6"

[patch.crates-io]
patched_crate = { path = "./patched" }
git_patched_crate = { git = "https://example.com/git-patched-crate" }
`)
	lockContent := "version = 4\n\n" +
		cargoLockFixture("serde_json", "1.0.145") +
		cargoLockFixture("Case_Crate", "1.2.0") +
		cargoLockSourceFixture("plain_crate", "2.1.0", cargoCratesIOIndex) +
		cargoLockFixture("multi_crate", "1.0.0") +
		cargoLockFixture("multi_crate", "1.5.0") +
		cargoLockFixture("stale_crate", "2.0.0") +
		cargoLockSourceFixture("lookalike_crate", "1.0.0", cargoCratesIOGitIndex+"-evil") +
		cargoLockFixture("patched_crate", "1.5.0") +
		cargoLockSourceFixture("patched_crate", "1.1.0", "") +
		cargoLockFixture("git_patched_crate", "1.5.0") +
		cargoLockSourceFixture("git_patched_crate", "1.1.0", "git+https://example.com/git-patched-crate#abcdef") +
		cargoLockSourceFixture("dev_crate", "3.0.0", cargoCratesIOSparse) +
		cargoLockFixture("build_crate", "4.0.0") +
		cargoLockFixture("workspace_crate", "5.0.0") +
		cargoLockFixture("target_crate", "6.0.0") +
		cargoLockFixture("transitive_only", "9.0.0") +
		cargoLockSourceFixture("local_crate", "1.0.0", "") +
		cargoLockSourceFixture("git_crate", "1.0.0", "git+https://example.com/git-crate#abcdef") +
		cargoLockSourceFixture("custom_crate", "1.0.0", "registry+https://example.com/private-index")
	testutil.MustWriteFile(t, filepath.Join(repoPath, cargoLockFileName), lockContent)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "rust", Name: "serde-json"},
		{Language: "rust", Name: "case-crate"},
		{Language: "rust", Name: "plain-crate"},
		{Language: "rust", Name: "multi-crate"},
		{Language: "rust", Name: "dev-crate"},
		{Language: "rust", Name: "build-crate"},
		{Language: "rust", Name: "workspace-alias"},
		{Language: "rust", Name: "target-crate"},
		{Language: "rust", Name: "stale-crate"},
		{Language: "rust", Name: "unlocked-crate"},
		{Language: "rust", Name: "lookalike-crate"},
		{Language: "rust", Name: "patched-crate"},
		{Language: "rust", Name: "git-patched-crate"},
		{Language: "rust", Name: "transitive-only"},
		{Language: "rust", Name: "local-crate"},
		{Language: "rust", Name: "git-crate"},
		{Language: "rust", Name: "custom-crate"},
		{Language: "rust", Name: "inherited-missing"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	for _, want := range []struct {
		lookupName  string
		packageName string
		version     string
	}{
		{lookupName: "serde-json", packageName: "serde_json", version: "1.0.145"},
		{lookupName: "case-crate", packageName: "Case_Crate", version: "1.2.0"},
		{lookupName: "plain-crate", packageName: "plain_crate", version: "2.1.0"},
		{lookupName: "dev-crate", packageName: "dev_crate", version: "3.0.0"},
		{lookupName: "build-crate", packageName: "build_crate", version: "4.0.0"},
		{lookupName: "workspace-alias", packageName: "workspace_crate", version: "5.0.0"},
		{lookupName: "target-crate", packageName: "target_crate", version: "6.0.0"},
	} {
		assertResolvedCargoIdentity(t, reportData, want.lookupName, want.packageName, want.version, cargoLockFileName)
	}
	for _, name := range []string{"stale-crate", "unlocked-crate", "lookalike-crate", "patched-crate", "git-patched-crate", "transitive-only", "local-crate", "git-crate", "custom-crate", "inherited-missing"} {
		assertUnknownCargoIdentity(t, reportData, name)
	}

	multi := findIdentityDependency(t, reportData, "rust", "multi-crate").Identity
	if multi == nil || multi.VersionStatus != identityStatusConflicting || multi.PURLStatus != identityPURLUnavailable || multi.PURL != "" {
		t.Fatalf("expected duplicate compatible Cargo versions to remain conflicting, got %#v", multi)
	}
	for _, conflict := range []string{"1.0.0 from Cargo.lock", "1.5.0 from Cargo.lock"} {
		if !strings.Contains(strings.Join(multi.Conflicts, "\n"), conflict) {
			t.Fatalf("expected Cargo conflict %q, got %#v", conflict, multi.Conflicts)
		}
	}
}

func TestCargoIdentityUsesWorkspaceRootLockAndInheritedRename(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, cargoManifestFileName), `[workspace]
members = ["crates/*"]

[workspace.dependencies]
shared_alias = { package = "Exact_Crate", version = "1" }
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, cargoLockFileName), cargoLockFixture("Exact_Crate", "1.4.0"))
	memberPath := filepath.Join(repoPath, "crates", "member")
	testutil.MustWriteFile(t, filepath.Join(memberPath, cargoManifestFileName), `[package]
name = "member"
version = "0.1.0"

[dependencies]
shared_alias = { workspace = true }
`)
	testutil.MustWriteFile(t, filepath.Join(memberPath, cargoLockFileName), cargoLockFixture("Exact_Crate", "9.0.0"))
	independentPath := filepath.Join(repoPath, "independent")
	testutil.MustWriteFile(t, filepath.Join(independentPath, cargoManifestFileName), `[package]
name = "independent"
version = "0.1.0"

[workspace]

[dependencies]
independent_crate = "1"
`)
	testutil.MustWriteFile(t, filepath.Join(independentPath, cargoLockFileName), cargoLockFixture("independent_crate", "1.2.0"))
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "rust", Name: "shared-alias"},
		{Language: "rust", Name: "independent-crate"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertResolvedCargoIdentity(t, reportData, "shared-alias", "Exact_Crate", "1.4.0", cargoLockFileName)
	assertResolvedCargoIdentity(t, reportData, "independent-crate", "independent_crate", "1.2.0", "independent/Cargo.lock")
}

func TestCargoIdentityIgnoresLocksForAmbiguousWorkspaceDescendants(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rootManifest string
	}{
		{
			name: "implicit path member",
			rootManifest: `[package]
name = "root"
version = "0.1.0"

[workspace]

[dependencies]
member = { path = "crates/member" }
`,
		},
		{name: "unlisted descendant", rootManifest: "[workspace]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repoPath, cargoManifestFileName), tc.rootManifest)
			testutil.MustWriteFile(t, filepath.Join(repoPath, cargoLockFileName), cargoLockFixture("foo", "1.0.0"))
			memberPath := filepath.Join(repoPath, "crates", "member")
			testutil.MustWriteFile(t, filepath.Join(memberPath, cargoManifestFileName), "[dependencies]\nfoo = \"1\"\n")
			testutil.MustWriteFile(t, filepath.Join(memberPath, cargoLockFileName), cargoLockFixture("foo", "1.9.0"))
			reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "rust", Name: "foo"}}}

			annotateDependencyIdentities(repoPath, &reportData)

			assertUnknownCargoIdentity(t, reportData, "foo")
		})
	}
}

func TestCargoIdentityHonorsExplicitPackageWorkspace(t *testing.T) {
	repoPath := t.TempDir()
	workspacePath := filepath.Join(repoPath, "workspace")
	testutil.MustWriteFile(t, filepath.Join(workspacePath, cargoManifestFileName), "[workspace]\n")
	testutil.MustWriteFile(t, filepath.Join(workspacePath, cargoLockFileName), cargoLockFixture("linked_crate", "1.2.0"))
	memberPath := filepath.Join(repoPath, "member")
	testutil.MustWriteFile(t, filepath.Join(memberPath, cargoManifestFileName), `[package]
name = "member"
version = "0.1.0"
workspace = "../workspace"

[dependencies]
linked_crate = "1"
`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "rust", Name: "linked-crate"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertResolvedCargoIdentity(t, reportData, "linked-crate", "linked_crate", "1.2.0", "workspace/Cargo.lock")
}

func TestCargoIdentityCollectorsWarnOnMalformedAndMissingFiles(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		repoPath := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repoPath, cargoManifestFileName), "not valid = ")
		testutil.MustWriteFile(t, filepath.Join(repoPath, cargoLockFileName), "=")
		reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "rust", Name: "unknown"}}}

		annotateDependencyIdentities(repoPath, &reportData)

		assertWarningsExact(t, repoPath, reportData.Warnings, []string{
			"identity manifest parse failed for Cargo.lock: invalid TOML",
			"identity manifest parse failed for Cargo.toml: invalid TOML",
		})
	})

	t.Run("missing", func(t *testing.T) {
		repoPath := t.TempDir()
		warnings := newIdentityWarningCollector(repoPath)
		manifestPath := filepath.Join(repoPath, "missing", cargoManifestFileName)
		lockPath := filepath.Join(repoPath, "missing", cargoLockFileName)

		if model := collectCargoManifestModel(repoPath, manifestPath, warnings); model != nil {
			t.Fatalf("expected missing Cargo manifest to produce no model, got %#v", model)
		}
		collectCargoLockIdentityEvidence(repoPath, lockPath, identityIndex{}, nil, warnings)

		assertWarningsExact(t, repoPath, warnings.list(), []string{
			"identity manifest read failed for missing/Cargo.lock: not found",
			"identity manifest read failed for missing/Cargo.toml: not found",
		})
	})
}

func TestCargoIdentitySourceAndDeclarationHelpers(t *testing.T) {
	for source, want := range map[string]bool{
		cargoCratesIOGitIndex:                      true,
		cargoCratesIOIndex:                         true,
		cargoCratesIOSparse:                        true,
		cargoCratesIOGitIndex + "/":                true,
		cargoCratesIOGitIndex + "-evil":            false,
		"registry+https://example.com/index":       false,
		"git+https://github.com/example/repo#hash": false,
		"": false,
	} {
		if got := isCratesIOCargoSource(source); got != want {
			t.Fatalf("isCratesIOCargoSource(%q) = %t, want %t", source, got, want)
		}
	}
	for _, tc := range []struct {
		alias          string
		value          any
		allowInherited bool
		want           cargoDependencyDeclaration
		wantOK         bool
	}{
		{alias: "", value: "1"},
		{alias: "empty", value: ""},
		{alias: "inherited", value: map[string]any{"workspace": true}, allowInherited: true, want: cargoDependencyDeclaration{lookupName: "inherited", inherited: true}, wantOK: true},
		{alias: "inherited", value: map[string]any{"workspace": true}},
		{alias: "local", value: map[string]any{"path": ".", "version": "1"}},
		{alias: "plain_crate", value: map[string]any{"version": "1", "package": ""}, want: cargoDependencyDeclaration{lookupName: "plain-crate", packageName: "plain_crate", requirement: "1"}, wantOK: true},
	} {
		got, ok := cargoDependencyDeclarationFor(tc.alias, tc.value, tc.allowInherited)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("cargoDependencyDeclarationFor(%q, %#v, %t) = (%#v, %t), want (%#v, %t)", tc.alias, tc.value, tc.allowInherited, got, ok, tc.want, tc.wantOK)
		}
	}
	if got := cargoStringSlice("ignored"); len(got) != 0 {
		t.Fatalf("expected non-list Cargo workspace value to be ignored, got %#v", got)
	}
	if got := cargoWorkspaceDependencyDeclarations("ignored"); len(got) != 0 {
		t.Fatalf("expected non-table workspace dependencies to be ignored, got %#v", got)
	}
	if got := cargoWorkspaceDependencyDeclarations(map[string]any{"invalid": 42}); len(got) != 0 {
		t.Fatalf("expected invalid workspace dependency entries to be ignored, got %#v", got)
	}
	if got := cargoDirectDependencyDeclarations(map[string]any{"target": map[string]any{"cfg(invalid)": "ignored"}}); len(got) != 0 {
		t.Fatalf("expected invalid target dependency tables to be ignored, got %#v", got)
	}
}

func TestCargoWorkspaceOwnershipHelpers(t *testing.T) {
	repoPath := t.TempDir()
	rootPath := filepath.Join(repoPath, cargoManifestFileName)
	workspace := &cargoManifestModel{
		path:              rootPath,
		hasWorkspace:      true,
		workspaceMembers:  []string{"crates/*"},
		workspaceExcludes: []string{"crates/excluded"},
	}
	manifests := map[string]*cargoManifestModel{rootPath: workspace}
	if got := cargoOwningManifest(repoPath, workspace, manifests); got != workspace {
		t.Fatalf("workspace should own its lock, got %#v", got)
	}

	member := &cargoManifestModel{path: filepath.Join(repoPath, "crates", "member", cargoManifestFileName)}
	if got := cargoOwningManifest(repoPath, member, manifests); got != workspace {
		t.Fatalf("workspace member should use the workspace lock, got %#v", got)
	}
	excluded := &cargoManifestModel{path: filepath.Join(repoPath, "crates", "excluded", cargoManifestFileName)}
	if got := cargoOwningManifest(repoPath, excluded, manifests); got != excluded {
		t.Fatalf("excluded package should own its lock, got %#v", got)
	}
	standalone := &cargoManifestModel{path: filepath.Join(repoPath, "standalone", cargoManifestFileName)}
	if got := cargoOwningManifest(repoPath, standalone, manifests); got != nil {
		t.Fatalf("ambiguous descendant should not consume an adjacent lock, got %#v", got)
	}

	escaping := &cargoManifestModel{
		path:             filepath.Join(repoPath, "crates", "escape", cargoManifestFileName),
		packageWorkspace: "../../../outside",
	}
	if got := cargoOwningManifest(repoPath, escaping, manifests); got != nil {
		t.Fatalf("workspace path outside the repository should be rejected, got %#v", got)
	}
	missing := &cargoManifestModel{
		path:             filepath.Join(repoPath, "member", cargoManifestFileName),
		packageWorkspace: "../missing",
	}
	if got := cargoOwningManifest(repoPath, missing, manifests); got != nil {
		t.Fatalf("missing explicit workspace should be rejected, got %#v", got)
	}
	notWorkspacePath := filepath.Join(repoPath, "not-workspace", cargoManifestFileName)
	manifests[notWorkspacePath] = &cargoManifestModel{path: notWorkspacePath}
	invalid := &cargoManifestModel{
		path:             filepath.Join(repoPath, "member", cargoManifestFileName),
		packageWorkspace: "../not-workspace",
	}
	if got := cargoOwningManifest(repoPath, invalid, manifests); got != nil {
		t.Fatalf("explicit package.workspace should require a workspace manifest, got %#v", got)
	}

	if !cargoWorkspaceIncludesManifest(workspace, rootPath) {
		t.Fatal("workspace root should include itself")
	}
	outsidePath := filepath.Join(filepath.Dir(repoPath), "outside", cargoManifestFileName)
	if cargoWorkspaceIncludesManifest(workspace, outsidePath) {
		t.Fatal("workspace should not include a manifest outside its root")
	}
	if cargoWorkspacePatternMatches("[", "crates/member") {
		t.Fatal("invalid workspace pattern should not match")
	}
}

func TestCargoIdentityCollectorsSkipUnlockedManifestAndBlankLockVersion(t *testing.T) {
	repoPath := t.TempDir()
	manifestPath := filepath.Join(repoPath, cargoManifestFileName)
	testutil.MustWriteFile(t, manifestPath, "[dependencies]\nunlocked = \"1\"\n")
	warnings := newIdentityWarningCollector(repoPath)
	index := identityIndex{}

	collectCargoIdentityEvidenceFromSnapshot(repoPath, index, identityManifestSnapshot{cargoManifestFiles: []string{manifestPath}}, warnings)
	if len(index) != 0 || len(warnings.list()) != 0 {
		t.Fatalf("expected unlocked Cargo manifest to remain silent and unresolved, got index=%#v warnings=%#v", index, warnings.list())
	}

	lockPath := filepath.Join(repoPath, cargoLockFileName)
	testutil.MustWriteFile(t, lockPath, cargoLockFixture("unlocked", ""))
	direct := cargoLockDependencyIndex{"unlocked": {{lookupName: "unlocked", packageName: "unlocked", requirement: "1"}}}
	collectCargoLockIdentityEvidence(repoPath, lockPath, index, direct, warnings)
	if len(index) != 0 || len(warnings.list()) != 0 {
		t.Fatalf("expected blank Cargo lock version to remain silent and unresolved, got index=%#v warnings=%#v", index, warnings.list())
	}
}

func TestCargoVersionRequirementMatching(t *testing.T) {
	for _, tc := range []struct {
		version     string
		requirement string
		want        bool
	}{
		{version: "1.9.0", requirement: "1", want: true},
		{version: "2.0.0", requirement: "1"},
		{version: "0.2.9", requirement: "0.2.3", want: true},
		{version: "0.3.0", requirement: "0.2.3"},
		{version: "1.2.9", requirement: "~1.2.3", want: true},
		{version: "1.3.0", requirement: "~1.2.3"},
		{version: "1.2.9", requirement: "1.2.*", want: true},
		{version: "1.3.0", requirement: "1.2.*"},
		{version: "1.4.0", requirement: ">= 1.2, < 1.5", want: true},
		{version: "1.5.0", requirement: ">= 1.2, < 1.5"},
		{version: "1.2.3", requirement: "= 1.2.3", want: true},
		{version: "1.2.4", requirement: "= 1.2.3"},
		{version: "1.2.9", requirement: "= 1.2", want: true},
		{version: "1.3.0", requirement: "= 1.2"},
		{version: "1.2.3", requirement: "<= 1.2.3", want: true},
		{version: "1.2.9", requirement: "<= 1.2", want: true},
		{version: "1.3.0", requirement: "<= 1.2"},
		{version: "1.2.4", requirement: "> 1.2.3", want: true},
		{version: "1.9.0", requirement: "> 1"},
		{version: "2.0.0", requirement: "> 1", want: true},
		{version: "1.2.9", requirement: "> 1.2"},
		{version: "1.3.0", requirement: "> 1.2", want: true},
		{version: "1.0.0", requirement: "*", want: true},
		{version: "1.0.0", requirement: "*.1"},
		{version: "1.0.0", requirement: "*.*"},
		{version: "1.8.0", requirement: "1.*.*", want: true},
		{version: "1.8.0", requirement: "1.*.2"},
		{version: "1.2.9", requirement: "1.2.x", want: true},
		{version: "1.2.9", requirement: "1.2.X", want: true},
		{version: "0.9.0", requirement: "1.*"},
		{version: "0.0.3", requirement: "0.0.3", want: true},
		{version: "0.0.4", requirement: "0.0.3"},
		{version: "0.0.9", requirement: "0.0", want: true},
		{version: "1.4.0", requirement: "~1", want: true},
		{version: "1.0.0-alpha", requirement: "1"},
		{version: "1.0.0-alpha", requirement: "=1.0.0-alpha", want: true},
		{version: "1.0.0-beta", requirement: "1.0.0-alpha", want: true},
		{version: "1.0.1-alpha", requirement: "1.0.0-alpha"},
		{version: "1.1.0-alpha", requirement: ">=1.0.0-alpha, <2"},
		{version: "1.0.0-beta", requirement: ">=1.0.0-alpha, <2", want: true},
		{version: "1.0.0-next.1", requirement: "1.0.0-next", want: true},
		{version: "1.0.0-beta", requirement: "=1.0.0-alpha"},
		{version: "invalid", requirement: "1"},
		{version: "1.0.0", requirement: ","},
		{version: "1.0.0", requirement: ">= invalid"},
		{version: "1.0.0", requirement: "^invalid"},
		{version: "1.2.3.4", requirement: "1"},
		{version: "1.a", requirement: "1"},
		{version: "1.0.0-", requirement: "1"},
		{version: "1.0.0", requirement: ""},
	} {
		if got := cargoVersionSatisfiesRequirement(tc.version, tc.requirement); got != tc.want {
			t.Fatalf("cargoVersionSatisfiesRequirement(%q, %q) = %t, want %t", tc.version, tc.requirement, got, tc.want)
		}
	}
}

func cargoLockFixture(name, version string) string {
	return cargoLockSourceFixture(name, version, cargoCratesIOGitIndex)
}

func cargoLockSourceFixture(name, version, source string) string {
	fixture := "[[package]]\nname = \"" + name + "\"\nversion = \"" + version + "\"\n"
	if source != "" {
		fixture += "source = \"" + source + "\"\n"
	}
	return fixture + "\n"
}

func assertResolvedCargoIdentity(t *testing.T, reportData report.Report, lookupName, packageName, version, source string) {
	t.Helper()
	assertIdentity(t, findIdentityDependency(t, reportData, "rust", lookupName), report.DependencyIdentity{
		Ecosystem: "cargo", Name: packageName, Version: version, VersionStatus: identityStatusResolved,
		PURL: "pkg:cargo/" + packageName + "@" + version, PURLStatus: identityStatusResolved, Source: source, Confidence: "high",
	})
}

func assertUnknownCargoIdentity(t *testing.T, reportData report.Report, name string) {
	t.Helper()
	assertIdentity(t, findIdentityDependency(t, reportData, "rust", name), report.DependencyIdentity{
		Ecosystem: "cargo", Name: name, VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})
}
