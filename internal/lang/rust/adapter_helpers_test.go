package rust

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

const (
	cargoManifestFile    = "Cargo.toml"
	cargoLockFile        = "Cargo.lock"
	workspaceSection     = "[workspace]"
	demoPackageManifest  = "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n"
	rustLibFile          = "lib.rs"
	localmodRSFile       = "localmod.rs"
	srcMainRS            = "src/main.rs"
	srcLibRS             = "src/lib.rs"
	rustRunFn            = "pub fn run() {}\n"
	cargoLockVersion3    = "version = 3\n"
	serdeJSONDep         = "serde-json"
	unknownCrateID       = "unknown-crate"
	dirWithManifest      = "dir-with-manifest"
	workspaceMembersGlob = "crates/*"
)

func TestAdapterIdentityAndDetect(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), demoPackageManifest)
	writeFile(t, filepath.Join(repo, "src", rustLibFile), rustRunFn)

	adapter := NewAdapter()
	if adapter.ID() != "rust" {
		t.Fatalf("expected rust id, got %q", adapter.ID())
	}
	if !slices.Contains(adapter.Aliases(), "cargo") {
		t.Fatalf("expected cargo alias, got %#v", adapter.Aliases())
	}
	matched, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !matched {
		t.Fatalf("expected detect=true")
	}
}

func TestManifestParsingHelpers(t *testing.T) {
	manifest := strings.Join([]string{
		"[package]",
		`name = "demo"`,
		"",
		workspaceSection,
		`members = [`,
		`  "crates/a",`,
		`  "crates/b",`,
		`  "crates/a"`,
		"]",
		"",
		"[dependencies]",
		fmt.Sprintf(`serde_json = { package = "%s", version = "1.0" }`, serdeJSONDep),
		`local_dep = { path = "./crates/local_dep" }`,
		"",
		"[target.'cfg(unix)'.dependencies]",
		`clap = "4"`,
		"",
	}, "\n")

	meta := parseCargoManifestContent(manifest)
	if !meta.HasPackage {
		t.Fatalf("expected package section detection")
	}
	if len(meta.WorkspaceMembers) != 2 {
		t.Fatalf("expected deduped workspace members, got %#v", meta.WorkspaceMembers)
	}

	deps := parseCargoDependencies(manifest)
	if deps[serdeJSONDep].Canonical != serdeJSONDep {
		t.Fatalf("expected canonical serde-json mapping, got %#v", deps[serdeJSONDep])
	}
	if !deps["local-dep"].LocalPath {
		t.Fatalf("expected local path dependency handling, got %#v", deps["local-dep"])
	}
	if deps["clap"].Canonical != "clap" {
		t.Fatalf("expected target dependencies parsing for clap")
	}

	key, value, ok := parseTomlAssignment(`foo = "bar"`)
	if !ok || key != "foo" || value != `"bar"` {
		t.Fatalf("unexpected toml assignment parse: %q %q %v", key, value, ok)
	}
	if _, _, ok := parseTomlAssignment("broken"); ok {
		t.Fatalf("expected invalid assignment")
	}

	fields := parseInlineFields(fmt.Sprintf(`{ package = "%s", path = "./x" }`, serdeJSONDep))
	if fields["package"] != serdeJSONDep || fields["path"] != "./x" {
		t.Fatalf("unexpected inline fields: %#v", fields)
	}

	if got := stripTomlComment(`name = "x#y" # comment`); strings.TrimSpace(got) != `name = "x#y"` {
		t.Fatalf("unexpected toml comment stripping: %q", got)
	}
	if got := extractQuotedStrings(`["a", 'b', "a"]`); len(got) != 2 {
		t.Fatalf("expected quoted string extraction dedupe, got %#v", got)
	}
}

func TestManifestDiscoveryAndWorkspaceResolution(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), strings.Join([]string{
		workspaceSection,
		`members = ["crates/*", "missing/*"]`,
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "crates", "a", cargoManifestFile), "[package]\nname = \"a\"\nversion = \"0.1.0\"\n")

	paths, warnings, err := discoverManifestPaths(repo)
	if err != nil {
		t.Fatalf("discover manifests: %v", err)
	}
	if len(paths) != 1 || !strings.Contains(paths[0], filepath.Join("crates", "a", cargoManifestFile)) {
		t.Fatalf("unexpected workspace manifest paths: %#v", paths)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for unresolved workspace member pattern")
	}

	members := resolveWorkspaceMembers(repo, "crates/*")
	if len(members) != 1 || !strings.HasSuffix(members[0], filepath.Join("crates", "a")) {
		t.Fatalf("unexpected resolved members: %#v", members)
	}

	noRoot := t.TempDir()
	paths, warnings, err = discoverManifestPaths(noRoot)
	if err != nil {
		t.Fatalf("discover manifests no-root: %v", err)
	}
	if len(paths) != 0 || len(warnings) == 0 {
		t.Fatalf("expected no manifests + warning, got paths=%#v warnings=%#v", paths, warnings)
	}
}

func TestManifestDiscoveryCapWarning(t *testing.T) {
	repo := t.TempDir()
	for i := range maxManifestCount + 1 {
		writeFile(t, filepath.Join(repo, "crates", fmt.Sprintf("crate-%03d", i), cargoManifestFile), "[package]\nname = \"x\"\nversion = \"0.1.0\"\n")
	}
	_, warnings, err := discoverManifestPaths(repo)
	if err != nil {
		t.Fatalf("discover manifests cap: %v", err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "capped at 256 manifests") {
		t.Fatalf("expected capped warning, got %#v", warnings)
	}
}

func TestUseClauseAndImportHelpers(t *testing.T) {
	entries := parseUseClause("serde::{de::DeserializeOwned, self as serde_self, *}")
	if len(entries) == 0 {
		t.Fatalf("expected parsed use entries")
	}
	hasWildcard := false
	for _, entry := range entries {
		if entry.Wildcard {
			hasWildcard = true
		}
	}
	if !hasWildcard {
		t.Fatalf("expected wildcard entry in parsed use clause: %#v", entries)
	}

	if got := splitTopLevel("a::{b,c},d", ','); len(got) != 2 {
		t.Fatalf("expected top-level split behavior, got %#v", got)
	}
	if got := splitTopLevel("a::{b,c}},d", ','); len(got) != 1 {
		t.Fatalf("expected malformed brace sequence to avoid top-level split, got %#v", got)
	}
	if joinPath("a::b", "c") != "a::b::c" {
		t.Fatalf("unexpected join path result")
	}
	if lastPathSegment("a::b::c") != "c" {
		t.Fatalf("unexpected last path segment")
	}

	content := "line1\nline2\nline3"
	line, col := lineColumn(content, strings.Index(content, "line3"))
	if line != 3 || col != 1 {
		t.Fatalf("unexpected line/column for line3: %d:%d", line, col)
	}
}

func TestResolveDependencyBranches(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", localmodRSFile), rustRunFn)
	lookup := map[string]dependencyInfo{
		"serde": {Canonical: "serde"},
		"local": {Canonical: "local", LocalPath: true},
	}
	scan := &scanResult{UnresolvedImports: map[string]int{}}

	cases := []struct {
		path string
		want string
	}{
		{path: "std::fmt", want: ""},
		{path: "crate::x", want: ""},
		{path: "self::x", want: ""},
		{path: "super::x", want: ""},
		{path: "localmod::x", want: ""},
		{path: "serde::de::Deserialize", want: "serde"},
		{path: "local::x", want: ""},
		{path: "unknown_crate::x", want: unknownCrateID},
	}
	for _, tc := range cases {
		got := resolveDependency(tc.path, repo, lookup, scan)
		if got != tc.want {
			t.Fatalf("resolveDependency(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	if scan.UnresolvedImports[unknownCrateID] != 1 {
		t.Fatalf("expected unresolved count, got %#v", scan.UnresolvedImports)
	}
}

func TestBuildDependencyReportBranches(t *testing.T) {
	scan := scanResult{
		Files: []fileScan{
			{
				Path: srcMainRS,
				Imports: []importBinding{
					{
						Dependency: "serde",
						Module:     "serde",
						Name:       "*",
						Local:      "serde",
						Wildcard:   true,
						Location:   report.Location{File: srcMainRS, Line: 1, Column: 1},
					},
					{
						Dependency: "serde",
						Module:     "serde",
						Name:       "Deserialize",
						Local:      "Deserialize",
						Location:   report.Location{File: srcMainRS, Line: 2, Column: 1},
					},
				},
				Usage: map[string]int{"serde": 1},
			},
		},
		RenamedAliasesByDep:    map[string][]string{"serde": {"serde_json"}},
		MacroAmbiguityDetected: true,
	}
	dep := buildDependencyReport("serde", scan, 80)
	cues := make([]string, 0, len(dep.RiskCues))
	for _, cue := range dep.RiskCues {
		cues = append(cues, cue.Code)
	}
	if !slices.Contains(cues, "broad-imports") || !slices.Contains(cues, "renamed-crate") || !slices.Contains(cues, "macro-ambiguity") {
		t.Fatalf("expected risk cues, got %#v", dep.RiskCues)
	}
	if !hasRecommendation(dep, "prefer-explicit-imports") || !hasRecommendation(dep, "document-crate-rename") || !hasRecommendation(dep, "reduce-rust-surface-area") {
		t.Fatalf("expected recommendation set, got %#v", dep.Recommendations)
	}

	removeScan := scanResult{
		Files: []fileScan{
			{
				Path: srcLibRS,
				Imports: []importBinding{
					{
						Dependency: "anyhow",
						Module:     "anyhow",
						Name:       "Result",
						Local:      "Result",
						Location:   report.Location{File: srcLibRS, Line: 1, Column: 1},
					},
				},
				Usage: map[string]int{},
			},
		},
		RenamedAliasesByDep: map[string][]string{},
	}
	removeDep := buildDependencyReport("anyhow", removeScan, 50)
	if !hasRecommendation(removeDep, "remove-unused-dependency") {
		t.Fatalf("expected remove-unused-dependency recommendation, got %#v", removeDep.Recommendations)
	}
}

func TestLowerLevelHelpers(t *testing.T) {
	if normalizeDependencyID("Serde_JSON ") != serdeJSONDep {
		t.Fatalf("expected normalized dependency id")
	}
	if !shouldSkipDir("vendor") || shouldSkipDir("src") {
		t.Fatalf("unexpected skip dir behavior")
	}

	if got := uniquePaths([]string{" a ", "b", "a"}); len(got) != 2 {
		t.Fatalf("expected deduped paths, got %#v", got)
	}
	if got := dedupeWarnings([]string{" x ", "x", ""}); len(got) != 1 {
		t.Fatalf("expected deduped warnings, got %#v", got)
	}
	if got := dedupeStrings([]string{"a", "a", ""}); len(got) != 1 {
		t.Fatalf("expected deduped strings, got %#v", got)
	}

	repo := t.TempDir()
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if !isSubPath(repo, sub) {
		t.Fatalf("expected subpath=true")
	}
	if isSubPath(repo, filepath.Dir(repo)) {
		t.Fatalf("expected parent not subpath")
	}
	if !samePath(repo, filepath.Clean(repo)) {
		t.Fatalf("expected samePath true")
	}
	if resolveMinUsageRecommendationThreshold(nil) != thresholds.Defaults().MinUsagePercentForRecommendations {
		t.Fatalf("expected default threshold resolution")
	}
	if recommendationPriorityRank("high") != 0 || recommendationPriorityRank("medium") != 1 || recommendationPriorityRank("low") != 2 {
		t.Fatalf("unexpected recommendation priority rank")
	}
}

func TestSummarizeUnresolvedLimitAndSort(t *testing.T) {
	warnings := summarizeUnresolved(map[string]int{
		"a": 1,
		"b": 2,
		"c": 3,
		"d": 4,
		"e": 5,
		"f": 6,
	})
	if len(warnings) != maxWarningSamples {
		t.Fatalf("expected capped warnings, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "f") {
		t.Fatalf("expected highest-count unresolved first, got %#v", warnings)
	}
}

func TestImportParsingAndResolveWarnings(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", localmodRSFile), rustRunFn)
	lookup := map[string]dependencyInfo{
		"serde": {Canonical: "serde"},
	}
	scan := &scanResult{UnresolvedImports: map[string]int{}}

	content := strings.Join([]string{
		"extern crate serde as serde_alias;",
		"use serde::de::DeserializeOwned;",
		"use unknown_crate::Thing;",
		"use crate::localmod::run;",
		"",
	}, "\n")

	extern := parseExternCrateImports(content, srcLibRS, repo, lookup, scan)
	if len(extern) != 1 || extern[0].Dependency != "serde" {
		t.Fatalf("expected extern crate parse for serde, got %#v", extern)
	}

	imports := parseRustImports(content, srcLibRS, repo, lookup, scan)
	if len(imports) < 2 {
		t.Fatalf("expected parsed rust imports, got %#v", imports)
	}
	if scan.UnresolvedImports[unknownCrateID] == 0 {
		t.Fatalf("expected unresolved alias tracking, got %#v", scan.UnresolvedImports)
	}
}

func TestDetectAndScanBranches(t *testing.T) {
	repo := setupDetectAndScanRepo(t)
	detection, roots := assertDetectAndScanRootSignals(t, repo)
	assertDetectAndScanWalkBranches(t, repo, &detection, roots)
	assertDetectAndScanResults(t, repo)
}

func setupDetectAndScanRepo(t *testing.T) string {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), strings.Join([]string{
		workspaceSection,
		fmt.Sprintf(`members = ["%s"]`, workspaceMembersGlob),
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, cargoLockFile), cargoLockVersion3)
	writeFile(t, filepath.Join(repo, "crates", "a", cargoManifestFile), "[package]\nname=\"a\"\nversion=\"0.1.0\"\n")
	writeFile(t, filepath.Join(repo, "crates", "a", "src", rustLibFile), "use anyhow::Result;\nmy_macro!();\n")
	writeFile(t, filepath.Join(repo, "target", "ignored.rs"), "use ignored::X;\n")
	return repo
}

func assertDetectAndScanRootSignals(t *testing.T, repo string) (language.Detection, map[string]struct{}) {
	detection := language.Detection{}
	roots := map[string]struct{}{}
	workspaceOnly, err := applyRustRootSignals(repo, &detection, roots)
	if err != nil {
		t.Fatalf("apply root signals: %v", err)
	}
	if !workspaceOnly {
		t.Fatalf("expected workspace-only root behavior")
	}
	if !detection.Matched || detection.Confidence == 0 {
		t.Fatalf("expected root signal match + confidence, got %#v", detection)
	}

	adapter := NewAdapter()
	dd, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !dd.Matched || len(dd.Roots) == 0 {
		t.Fatalf("expected detection roots, got %#v", dd)
	}
	return detection, roots
}

func assertDetectAndScanWalkBranches(t *testing.T, repo string, detection *language.Detection, roots map[string]struct{}) {
	targetDirEntry := mustFindDirEntryByName(t, repo, "target")
	visited := 0
	if got := walkRustDetectionEntry(filepath.Join(repo, "target"), targetDirEntry, repo, false, roots, detection, &visited); got != filepath.SkipDir {
		t.Fatalf("expected skip dir for target, got %v", got)
	}

	fileEntries, err := os.ReadDir(filepath.Join(repo, "crates", "a", "src"))
	if err != nil {
		t.Fatalf("readdir src: %v", err)
	}
	visited = maxDetectionEntries
	if got := walkRustDetectionEntry(filepath.Join(repo, "crates", "a", "src", rustLibFile), fileEntries[0], repo, false, roots, detection, &visited); got != fs.SkipAll {
		t.Fatalf("expected scan bound skip all, got %v", got)
	}
}

func assertDetectAndScanResults(t *testing.T, repo string) {
	scan, err := scanRepo(context.Background(), repo, []string{filepath.Join(repo, "crates", "a", cargoManifestFile)}, map[string]dependencyInfo{"anyhow": {Canonical: "anyhow"}}, map[string][]string{})
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(scan.Files) == 0 {
		t.Fatalf("expected scanned rust files")
	}
	if !scan.MacroAmbiguityDetected {
		t.Fatalf("expected macro ambiguity detection")
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanRepo(cancelCtx, repo, []string{filepath.Join(repo, "crates", "a", cargoManifestFile)}, map[string]dependencyInfo{}, map[string][]string{}); err == nil {
		t.Fatalf("expected canceled context error")
	}
}

func mustFindDirEntryByName(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("expected dir entry %q in %s", name, dir)
	return nil
}

func TestScanRustSourceFileLargeAndManifestDataAmbiguity(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), fmt.Sprintf("%s\nmembers = [\"%s\"]\n", workspaceSection, workspaceMembersGlob))
	writeFile(t, filepath.Join(repo, "crates", "a", cargoManifestFile), "[package]\nname=\"a\"\nversion=\"0.1.0\"\n[dependencies]\nfoo = { package = \"crate-a\", version = \"1\" }\n")
	writeFile(t, filepath.Join(repo, "crates", "b", cargoManifestFile), "[package]\nname=\"b\"\nversion=\"0.1.0\"\n[dependencies]\nfoo = { package = \"crate-b\", version = \"1\" }\n")
	writeFile(t, filepath.Join(repo, "crates", "a", "src", rustLibFile), "use foo::X;\n")

	largePath := filepath.Join(repo, "crates", "a", "src", "huge.rs")
	if err := os.MkdirAll(filepath.Dir(largePath), 0o750); err != nil {
		t.Fatalf("mkdir huge: %v", err)
	}
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", maxScannableRustFile+1)), 0o600); err != nil {
		t.Fatalf("write huge: %v", err)
	}

	result := &scanResult{UnresolvedImports: map[string]int{}}
	if err := scanRustSourceFile(repo, filepath.Join(repo, "crates", "a"), largePath, map[string]dependencyInfo{}, result); err != nil {
		t.Fatalf("scan huge rust source: %v", err)
	}
	if result.SkippedLargeFiles != 1 {
		t.Fatalf("expected skipped large file count, got %d", result.SkippedLargeFiles)
	}

	manifestPaths, lookup, renamed, warnings, err := collectManifestData(repo)
	if err != nil {
		t.Fatalf("collect manifest data: %v", err)
	}
	if len(manifestPaths) < 2 {
		t.Fatalf("expected multiple manifest paths, got %#v", manifestPaths)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected ambiguity warning for alias foo")
	}
	if len(lookup) == 0 || len(renamed) == 0 {
		t.Fatalf("expected parsed lookup and renamed aliases, got lookup=%#v renamed=%#v", lookup, renamed)
	}

	if _, _, err := parseCargoManifest(filepath.Join(repo, "missing", cargoManifestFile), repo); err == nil {
		t.Fatalf("expected parseCargoManifest missing file error")
	}
}

func TestBuildRequestedRustDependenciesNoTargetAndLineColumn(t *testing.T) {
	deps, warnings := buildRequestedRustDependencies(language.Request{}, scanResult{})
	if deps != nil || len(warnings) != 1 {
		t.Fatalf("expected no-target warning, got deps=%#v warnings=%#v", deps, warnings)
	}
	line, col := lineColumn("abc", -1)
	if line != 1 || col != 1 {
		t.Fatalf("expected line/col 1:1 for negative offset, got %d:%d", line, col)
	}
}

func TestRefactorHelperBranches(t *testing.T) {
	meta := manifestMeta{}
	if parseWorkspaceMembersLine(`members = ["a"]`, "package", false, &meta) {
		t.Fatalf("expected false outside workspace section")
	}
	if parseWorkspaceMembersLine(`exclude = ["x"]`, "workspace", false, &meta) {
		t.Fatalf("expected false for non-members workspace field")
	}
	if _, ok := workspaceMembersAssignmentValue("members"); ok {
		t.Fatalf("expected missing assignment to be rejected")
	}
	if _, ok := workspaceMembersAssignmentValue(`members2 = ["a"]`); ok {
		t.Fatalf("expected non-members key to be rejected")
	}
	if got, ok := workspaceMembersAssignmentValue(`members = ["a"]`); !ok || got != `["a"]` {
		t.Fatalf("unexpected workspace members assignment parse: %q %v", got, ok)
	}

	deps := map[string]dependencyInfo{}
	addDependencyFromLine(deps, "package", `serde = "1.0"`)
	if len(deps) != 0 {
		t.Fatalf("expected no deps outside dependency section, got %#v", deps)
	}
	if _, _, ok := parseDependencyInfo(`"" = "1.0"`); ok {
		t.Fatalf("expected invalid empty alias from quoted key")
	}

	if _, _, _, ok := parseUseStatementIndex("abc", []int{0, 1, 0}); ok {
		t.Fatalf("expected parseUseStatementIndex false for short index")
	}
	if _, _, _, ok := parseUseStatementIndex("abc", []int{0, 1, 0, 99}); ok {
		t.Fatalf("expected parseUseStatementIndex false for invalid bounds")
	}

	repo := t.TempDir()
	root := filepath.Join(repo, "crate")
	writeFile(t, filepath.Join(root, "src", rustLibFile), rustRunFn)
	scanned := map[string]struct{}{}
	result := &scanResult{UnresolvedImports: map[string]int{}}
	count := 0
	if err := scanRepoFileEntry(repo, root, filepath.Join(root, "README.md"), nil, scanned, &count, result); err != nil {
		t.Fatalf("scan non-rs entry: %v", err)
	}
	rsPath := filepath.Join(root, "src", rustLibFile)
	scanned[rsPath] = struct{}{}
	if err := scanRepoFileEntry(repo, root, rsPath, nil, scanned, &count, result); err != nil {
		t.Fatalf("scan duplicate rs entry: %v", err)
	}
}

func TestDetectWithConfidenceDefaultRepoAndCanceledContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), demoPackageManifest)
	writeFile(t, filepath.Join(repo, "src", rustLibFile), rustRunFn)

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), "")
	if err != nil {
		t.Fatalf("detect default repo path: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected detection matched in cwd")
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(cancelCtx, repo); err == nil {
		t.Fatalf("expected canceled context error")
	}
}

func TestDiscoverManifestPathsRootPackageAndResolveMembersErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), demoPackageManifest)

	paths, warnings, err := discoverManifestPaths(repo)
	if err != nil {
		t.Fatalf("discover root package manifest: %v", err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(repo, cargoManifestFile) {
		t.Fatalf("expected root manifest path, got %#v", paths)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for simple package, got %#v", warnings)
	}

	if got := resolveWorkspaceMembers(repo, "["); got != nil {
		t.Fatalf("expected nil on invalid glob, got %#v", got)
	}

	roots := map[string]struct{}{}
	addWorkspaceMemberRoot(repo, "", roots)
	if len(roots) != 0 {
		t.Fatalf("expected no roots for empty workspace member")
	}
}

func TestScanRepoNoRootsNoFilesAndBoundedLimit(t *testing.T) {
	repo := t.TempDir()
	scan, err := scanRepo(context.Background(), repo, nil, map[string]dependencyInfo{}, map[string][]string{})
	if err != nil {
		t.Fatalf("scan repo empty: %v", err)
	}
	if len(scan.Warnings) == 0 {
		t.Fatalf("expected warning for no rust files")
	}

	bigRepo := t.TempDir()
	writeFile(t, filepath.Join(bigRepo, cargoManifestFile), demoPackageManifest)
	for i := range maxScanFiles + 1 {
		writeFile(t, filepath.Join(bigRepo, "src", fmt.Sprintf("f_%04d.rs", i)), "use serde::Deserialize;\n")
	}
	scan, err = scanRepo(context.Background(), bigRepo, []string{filepath.Join(bigRepo, cargoManifestFile)}, map[string]dependencyInfo{}, map[string][]string{})
	if err != nil {
		t.Fatalf("scan repo bounded: %v", err)
	}
	if !scan.SkippedFilesByBoundLimit {
		t.Fatalf("expected bounded scan flag")
	}
}

func TestScanRustSourceFileOutsideRootAndUseExpansionBranches(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.rs")
	if err := os.WriteFile(outside, []byte("use serde::Deserialize;"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	result := &scanResult{UnresolvedImports: map[string]int{}}
	if scanRustSourceFile(repo, repo, outside, map[string]dependencyInfo{}, result) == nil {
		t.Fatalf("expected scan error for outside file")
	}

	entries := parseUseClause("pub serde::{self as s, de::*, value as self}")
	if len(entries) == 0 {
		t.Fatalf("expected entries from expanded use clause")
	}
	if !isSubPath(repo, repo) {
		t.Fatalf("expected repo to be subpath of itself")
	}
}

func TestCollectManifestDataPrefersExternalOverLocalPathAlias(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), fmt.Sprintf("%s\nmembers = [\"a\", \"b\"]\n", workspaceSection))
	writeFile(t, filepath.Join(repo, "a", cargoManifestFile), "[package]\nname=\"a\"\nversion=\"0.1.0\"\n[dependencies]\nfoo = { path = \"../foo\" }\n")
	writeFile(t, filepath.Join(repo, "b", cargoManifestFile), "[package]\nname=\"b\"\nversion=\"0.1.0\"\n[dependencies]\nfoo = \"1.0\"\n")

	_, lookup, _, _, err := collectManifestData(repo)
	if err != nil {
		t.Fatalf("collect manifest data: %v", err)
	}
	if info, ok := lookup["foo"]; !ok || info.LocalPath {
		t.Fatalf("expected external alias to replace local path alias, got %#v", lookup["foo"])
	}
}

func TestDiscoveryAndRootSignalErrorBranches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based branch test is POSIX-specific")
	}

	repo := t.TempDir()
	restricted := filepath.Join(repo, "restricted")
	if err := os.MkdirAll(restricted, 0o700); err != nil {
		t.Fatalf("mkdir restricted: %v", err)
	}
	if err := os.Chmod(restricted, 0o000); err != nil {
		t.Fatalf("chmod restricted: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(restricted, 0o700) })
	if _, err := os.ReadDir(restricted); err == nil {
		t.Skip("skipping permission-based test: directory remains readable after chmod 000")
	}

	_, _, err := discoverManifestPaths(restricted)
	if err == nil {
		t.Fatalf("expected discoverManifestPaths error on unreadable directory")
	}

	detection := language.Detection{}
	roots := map[string]struct{}{}
	_, err = applyRustRootSignals(restricted, &detection, roots)
	if err == nil {
		t.Fatalf("expected applyRustRootSignals error on unreadable directory")
	}
}

func TestAnalyseErrorBranches(t *testing.T) {
	adapter := NewAdapter()

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Analyse(cancelCtx, language.Request{RepoPath: t.TempDir(), TopN: 1}); err == nil {
		t.Fatalf("expected analyse error for canceled context")
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })
	deadDir := t.TempDir()
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir deaddir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove deaddir: %v", err)
	}
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: ".", TopN: 1}); err == nil {
		t.Fatalf("expected analyse normalize path error from deleted cwd")
	}
}

func TestAdditionalHelperBranches(t *testing.T) {
	if got := joinPath("", "::serde"); got != "serde" {
		t.Fatalf("unexpected joinPath empty-prefix result: %q", got)
	}
	if got := joinPath("serde", ""); got != "serde" {
		t.Fatalf("unexpected joinPath empty-value result: %q", got)
	}
	line, col := lineColumn("abc", 99)
	if line != 1 || col != 4 {
		t.Fatalf("unexpected line/column for past-end offset: %d:%d", line, col)
	}
	if dep := resolveDependency("", "", map[string]dependencyInfo{}, &scanResult{UnresolvedImports: map[string]int{}}); dep != "" {
		t.Fatalf("expected empty path resolution to be empty, got %q", dep)
	}
}

func TestRootSignalsAndManifestDiscoveryVariants(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoManifestFile), strings.Join([]string{
		"[package]",
		`name = "root-pkg"`,
		`version = "0.1.0"`,
		"",
		workspaceSection,
		`members = ["crates/a"]`,
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, cargoLockFile), cargoLockVersion3)
	writeFile(t, filepath.Join(repo, "crates", "a", cargoManifestFile), "[package]\nname=\"a\"\nversion=\"0.1.0\"\n")

	detection := language.Detection{}
	roots := map[string]struct{}{}
	workspaceOnly, err := applyRustRootSignals(repo, &detection, roots)
	if err != nil {
		t.Fatalf("apply root signals: %v", err)
	}
	if workspaceOnly {
		t.Fatalf("expected package+workspace repo not to be workspace-only")
	}
	if _, ok := roots[repo]; !ok {
		t.Fatalf("expected root repo included in roots")
	}

	paths, warnings, err := discoverManifestPaths(repo)
	if err != nil {
		t.Fatalf("discover manifest variants: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected root + member manifests, got %#v", paths)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for resolvable workspace members, got %#v", warnings)
	}
}

func TestApplyRustRootSignalsLockOnlyAndWorkspaceRootSkips(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoLockFile), cargoLockVersion3)
	detection := language.Detection{}
	roots := map[string]struct{}{}
	workspaceOnly, err := applyRustRootSignals(repo, &detection, roots)
	if err != nil {
		t.Fatalf("apply root signals lock only: %v", err)
	}
	if workspaceOnly {
		t.Fatalf("expected lock-only repo not to be workspace-only")
	}
	if !detection.Matched {
		t.Fatalf("expected lock-only detection match")
	}

	memberRoots := map[string]struct{}{}
	addWorkspaceMemberRoot(repo, workspaceMembersGlob, memberRoots)
	if len(memberRoots) != 0 {
		t.Fatalf("expected no member roots when glob has no matches")
	}
}

func TestWorkspaceResolutionAndParsingEdgeBranches(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "members"), 0o750); err != nil {
		t.Fatalf("mkdir members: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "members", "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file member: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "members", "dir-no-manifest"), 0o750); err != nil {
		t.Fatalf("mkdir dir-no-manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "members", dirWithManifest), 0o750); err != nil {
		t.Fatalf("mkdir dir-with-manifest: %v", err)
	}
	writeFile(t, filepath.Join(repo, "members", dirWithManifest, cargoManifestFile), "[package]\nname=\"x\"\nversion=\"0.1.0\"\n")

	roots := resolveWorkspaceMembers(repo, "members/*")
	if len(roots) != 1 || !strings.HasSuffix(roots[0], filepath.Join("members", dirWithManifest)) {
		t.Fatalf("expected only dir with manifest, got %#v", roots)
	}

	rootMap := map[string]struct{}{}
	addWorkspaceMemberRoot(repo, "members/*", rootMap)
	if len(rootMap) != 1 {
		t.Fatalf("expected one workspace member root, got %#v", rootMap)
	}

	if key, value, ok := parseTomlAssignment("'x' = \"1\""); !ok || key != "x" || value != `"1"` {
		t.Fatalf("unexpected quoted toml assignment parse: %q %q %v", key, value, ok)
	}
	if fields := parseInlineFields("{ version = \"1.0\" }"); fields["version"] != "1.0" {
		t.Fatalf("expected inline field extraction for version, got %#v", fields)
	}
}

func TestPathAndScanErrorEdges(t *testing.T) {
	if isSubPath("\x00", "/tmp") {
		t.Fatalf("expected invalid root path to fail subpath check")
	}
	if !samePath("\x00", "\x00") {
		t.Fatalf("expected fallback samePath compare for invalid abs paths")
	}
	if seg := lastPathSegment("  "); seg != "" {
		t.Fatalf("expected empty last path segment for blank input, got %q", seg)
	}

	repo := t.TempDir()
	result := &scanResult{UnresolvedImports: map[string]int{}}
	if scanRustSourceFile(repo, repo, filepath.Join(repo, "missing.rs"), map[string]dependencyInfo{}, result) == nil {
		t.Fatalf("expected scanRustSourceFile stat error on missing file")
	}
}

func TestLocalModuleCacheBranches(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", localmodRSFile), rustRunFn)
	scan := &scanResult{UnresolvedImports: map[string]int{}}

	if !isLocalRustModuleWithCache(scan, repo, "localmod") {
		t.Fatalf("expected local module detection for localmod")
	}
	if len(scan.LocalModuleCache) != 1 {
		t.Fatalf("expected one cached local-module entry, got %d", len(scan.LocalModuleCache))
	}
	if !isLocalRustModuleWithCache(scan, repo, "localmod") {
		t.Fatalf("expected cached local module detection for localmod")
	}
	if len(scan.LocalModuleCache) != 1 {
		t.Fatalf("expected cache reuse without extra entries, got %d", len(scan.LocalModuleCache))
	}
	if isLocalRustModuleWithCache(scan, repo, "missing_mod") {
		t.Fatalf("did not expect missing module to resolve as local")
	}
	if len(scan.LocalModuleCache) != 2 {
		t.Fatalf("expected cached miss entry as well, got %d", len(scan.LocalModuleCache))
	}
}

func TestParseRustImportsWildcardDefaults(t *testing.T) {
	scan := &scanResult{UnresolvedImports: map[string]int{}}
	content := "use ::serde::de::*;\n"
	imports := parseRustImports(content, srcLibRS, "", map[string]dependencyInfo{"serde": {Canonical: "serde"}}, scan)
	if len(imports) != 1 {
		t.Fatalf("expected one wildcard import, got %#v", imports)
	}
	if imports[0].Name != "*" || imports[0].Local != "*" {
		t.Fatalf("expected wildcard import parsing for ::serde::de::*, got %#v", imports[0])
	}
}
