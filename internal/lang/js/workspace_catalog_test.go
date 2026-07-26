package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

type workspaceCatalogScenario struct {
	files          map[string]string
	installedDeps  []string
	wantDeps       []string
	wantAbsentDeps []string
	wantRootDeps   []string
}

func TestListDependenciesWorkspaceCatalogScenarios(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		scenario workspaceCatalogScenario
	}{
		{
			name: "includes pnpm workspace catalog declarations",
			scenario: workspaceCatalogScenario{
				files: map[string]string{
					jsPnpmWorkspaceFile: `packages:
  - packages/*
catalog:
  react: ^18.3.1
catalogs:
  tooling:
    typescript: ^5.6.3
`,
					testPackageJSONName: `{"name":"root","private":true}`,
					filepath.Join("packages", "web", testPackageJSONName): `{
  "name": "web",
  "dependencies": {
    "react": "catalog:"
  },
  "devDependencies": {
    "typescript": "catalog:tooling"
  }
}`,
				},
				installedDeps: []string{"react", "typescript"},
				wantDeps:      []string{"react", "typescript"},
				wantRootDeps:  []string{"react", "typescript"},
			},
		},
		{
			name: "includes yarn workspace catalog declarations",
			scenario: workspaceCatalogScenario{
				files: map[string]string{
					jsYarnRCFile: `catalog:
  eslint: ^9.5.0
catalogs:
  react18:
    react: ^18.3.1
`,
					testPackageJSONName: `{
  "name": "root",
  "private": true,
  "packageManager": "yarn@4.10.0",
  "workspaces": ["packages/*"]
}`,
					filepath.Join("packages", "app", testPackageJSONName): `{
  "name": "app",
  "dependencies": {
    "react": "catalog:react18"
  }
}`,
				},
				installedDeps: []string{"eslint", "react"},
				wantDeps:      []string{"eslint", "react"},
				wantRootDeps:  []string{"eslint", "react"},
			},
		},
		{
			name: "does not treat nested package json as workspace without patterns",
			scenario: workspaceCatalogScenario{
				files: map[string]string{
					jsYarnRCFile: `catalog:
  eslint: ^9.5.0
`,
					testPackageJSONName: `{
  "name": "root",
  "private": true,
  "packageManager": "yarn@4.10.0"
}`,
					filepath.Join("examples", "demo", testPackageJSONName): `{
  "name": "demo",
  "dependencies": {
    "react": "^18.3.1"
  }
}`,
				},
				installedDeps:  []string{"eslint", "react"},
				wantDeps:       []string{"eslint"},
				wantAbsentDeps: []string{"react"},
				wantRootDeps:   []string{"eslint"},
			},
		},
		{
			name: "ignores root manifest dependencies without workspace signals",
			scenario: workspaceCatalogScenario{
				files: map[string]string{
					testPackageJSONName: `{
  "name": "single-package",
  "dependencies": {
    "lodash": "^4.17.21"
  }
}`,
				},
				installedDeps:  []string{"lodash"},
				wantAbsentDeps: []string{"lodash"},
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			repo := t.TempDir()
			writeWorkspaceCatalogFiles(t, repo, testCase.scenario.files)
			installWorkspaceCatalogDependencies(t, repo, testCase.scenario.installedDeps...)

			deps, roots, warnings := mustListDependencies(t, repo, ScanResult{})
			assertWorkspaceCatalogDependencies(t, deps, testCase.scenario.wantDeps, true)
			assertWorkspaceCatalogDependencies(t, deps, testCase.scenario.wantAbsentDeps, false)
			assertWorkspaceCatalogRoots(t, repo, roots, testCase.scenario.wantRootDeps)
			assertWorkspaceCatalogRootAbsence(t, roots, testCase.scenario.wantAbsentDeps)
			if len(warnings) != 0 {
				t.Fatalf("expected no warnings, got %#v", warnings)
			}
		})
	}
}

func TestReadWorkspacePackageJSONWarnings(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	directoryPath := filepath.Join(repo, "fixtures")
	if err := os.MkdirAll(directoryPath, 0o755); err != nil {
		t.Fatalf("mkdir fixtures: %v", err)
	}

	_, found, warning := readWorkspacePackageJSON(repo, directoryPath)
	if found {
		t.Fatalf("expected directory package manifest to be ignored")
	}
	if !strings.Contains(warning, "directory") {
		t.Fatalf("expected directory warning, got %q", warning)
	}

	invalidJSONPath := filepath.Join(repo, testPackageJSONName)
	testutil.MustWriteFile(t, invalidJSONPath, "{")
	_, found, warning = readWorkspacePackageJSON(repo, invalidJSONPath)
	if found {
		t.Fatalf("expected invalid package manifest to be ignored")
	}
	if !strings.Contains(warning, "failed to parse workspace manifest") {
		t.Fatalf("expected parse warning, got %q", warning)
	}

	_, found, warning = readWorkspacePackageJSON(repo, filepath.Join(repo, "missing-package.json"))
	if found || warning != "" {
		t.Fatalf("expected missing package manifest to be ignored without warnings, found=%v warning=%q", found, warning)
	}

	oversizedPath := filepath.Join(repo, "oversized-package.json")
	testutil.MustWriteFile(t, oversizedPath, `{"name":"`+strings.Repeat("x", int(jsWorkspaceManifestReadMaxBytes))+`"}`)
	_, found, warning = readWorkspacePackageJSON(repo, oversizedPath)
	if found {
		t.Fatalf("expected oversized package manifest to be skipped")
	}
	want := "skipped workspace manifest oversized-package.json above"
	if !strings.Contains(warning, want) {
		t.Fatalf("expected oversized manifest warning containing %q, got %q", want, warning)
	}
}

func TestWorkspaceManifestReaders(t *testing.T) {
	t.Parallel()

	manifestWarningCases := []workspaceManifestWarningCase{
		invalidWorkspaceManifestWarningCase("pnpm parse warning", jsPnpmWorkspaceFile, "packages: [\n", "expected invalid pnpm manifest to be ignored", "expected pnpm parse warning", readPnpmWorkspaceManifestWarning),
		directoryWorkspaceManifestWarningCase("pnpm directory warning", jsPnpmWorkspaceFile, "mkdir pnpm workspace dir", "expected pnpm directory manifest to be ignored", "expected pnpm directory warning", readPnpmWorkspaceManifestWarning),
		invalidWorkspaceManifestWarningCase("yarn parse warning", jsYarnRCFile, "catalog: [\n", "expected invalid yarn config to be ignored", "expected yarn parse warning", readYarnCatalogManifestWarning),
		directoryWorkspaceManifestWarningCase("yarn directory warning", jsYarnRCFile, "mkdir yarn rc dir", "expected yarn directory manifest to be ignored", "expected yarn directory warning", readYarnCatalogManifestWarning),
	}
	for _, testCase := range manifestWarningCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			repo := t.TempDir()
			testCase.writeManifest(t, repo)
			assertWorkspaceManifestReaderWarning(t, repo, testCase)
		})
	}

	t.Run("yarn reader ignores non-catalog files", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, jsYarnRCFile), "nodeLinker: pnp\n")

		manifest, found, warning := readYarnCatalogManifest(repo)
		if found {
			t.Fatalf("expected non-catalog yarn config to be ignored, got %#v", manifest)
		}
		if warning != "" {
			t.Fatalf("expected no warning for non-catalog yarn config, got %q", warning)
		}
	})

	oversizedManifestCases := []struct {
		name        string
		fileName    string
		content     string
		readWarning func(string) (bool, string)
	}{
		{
			name:        "pnpm reader skips oversized manifest",
			fileName:    jsPnpmWorkspaceFile,
			content:     "packages:\n  - \"" + strings.Repeat("x", int(jsWorkspaceManifestReadMaxBytes)) + "\"\n",
			readWarning: readPnpmWorkspaceManifestWarning,
		},
		{
			name:        "yarn reader skips oversized manifest",
			fileName:    jsYarnRCFile,
			content:     "catalog:\n  dep: \"" + strings.Repeat("x", int(jsWorkspaceManifestReadMaxBytes)) + "\"\n",
			readWarning: readYarnCatalogManifestWarning,
		},
	}
	for _, testCase := range oversizedManifestCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			repo := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repo, testCase.fileName), testCase.content)

			found, warning := testCase.readWarning(repo)
			if found {
				t.Fatalf("expected oversized %s to be skipped", testCase.fileName)
			}
			want := "skipped " + testCase.fileName + " above"
			if !strings.Contains(warning, want) {
				t.Fatalf("expected oversized %s warning containing %q, got %q", testCase.fileName, want, warning)
			}
		})
	}

}

func TestWorkspacePatternHelpers(t *testing.T) {
	t.Parallel()

	t.Run("parse workspace patterns", testParseWorkspacePatterns)
	t.Run("match include and exclude patterns", testWorkspacePatternMatching)
	t.Run("compile regex and normalize pattern", testWorkspacePatternRegexAndNormalization)
	t.Run("reject invalid patterns and default-match", testWorkspacePatternInvalidPatterns)
	t.Run("derive search roots from patterns", testWorkspacePatternSearchRoots)
}

func testParseWorkspacePatterns(t *testing.T) {
	t.Helper()

	patterns := parseWorkspacePatterns(map[string]any{
		"packages": []any{"packages/*", "packages/*", "", 7},
	})
	if !slices.Equal(patterns, []string{"packages/*"}) {
		t.Fatalf("unexpected workspace patterns: %#v", patterns)
	}
}

func testWorkspacePatternMatching(t *testing.T) {
	t.Helper()

	compiled, warnings := compileWorkspacePatterns([]string{" packages/* ", "!packages/legacy", "./apps/**/"})
	if len(warnings) != 0 {
		t.Fatalf("expected no compile warnings, got %#v", warnings)
	}
	if !matchesWorkspacePatterns("packages/web", compiled) {
		t.Fatalf("expected packages/web to match %#v", compiled)
	}
	if matchesWorkspacePatterns("packages/legacy", compiled) {
		t.Fatalf("expected packages/legacy to be excluded by %#v", compiled)
	}
	if !matchesWorkspacePatterns("apps/admin/api", compiled) {
		t.Fatalf("expected apps/admin/api to match %#v", compiled)
	}

	excludeOnly, warnings := compileWorkspacePatterns([]string{"!packages/generated"})
	if len(warnings) != 0 {
		t.Fatalf("expected no compile warnings, got %#v", warnings)
	}
	if !matchesWorkspacePatterns("packages/web", excludeOnly) {
		t.Fatalf("expected exclude-only rules to default to matching unmatched paths")
	}
	if matchesWorkspacePatterns("packages/generated", excludeOnly) {
		t.Fatalf("expected explicit exclusion to win")
	}
}

func testWorkspacePatternRegexAndNormalization(t *testing.T) {
	t.Helper()

	regex, err := compileWorkspacePatternRegex("file?.ts")
	if err != nil {
		t.Fatalf("compile workspace regex: %v", err)
	}
	if !regex.MatchString("file1.ts") {
		t.Fatalf("expected single-character wildcard match")
	}
	if regex.MatchString("file10.ts") {
		t.Fatalf("expected multi-character segment to fail single-character wildcard match")
	}

	normalized, exclude := normalizeWorkspacePattern(" !./packages/*/ ")
	if normalized != "packages/*" || !exclude {
		t.Fatalf("unexpected normalized pattern: %q exclude=%v", normalized, exclude)
	}
}

func testWorkspacePatternInvalidPatterns(t *testing.T) {
	t.Helper()

	compiled, warnings := compileWorkspacePatterns([]string{string([]byte{0xff})})
	if len(compiled) != 0 {
		t.Fatalf("expected invalid utf-8 pattern to be rejected, got %#v", compiled)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to parse workspace pattern") {
		t.Fatalf("expected invalid utf-8 warning, got %#v", warnings)
	}
	if !matchesWorkspacePatterns("any/path", nil) {
		t.Fatalf("expected empty workspace pattern set to match by default")
	}
}

func testWorkspacePatternSearchRoots(t *testing.T) {
	t.Helper()

	repo := filepath.Join(t.TempDir(), "repo")
	searchRoots := workspacePatternSearchRoots(repo, []string{"packages/*", "packages/web/*", "./apps/**/", "!packages/legacy"})
	want := []string{
		filepath.Join(repo, "apps"),
		filepath.Join(repo, "packages"),
	}
	if !slices.Equal(searchRoots, want) {
		t.Fatalf("unexpected workspace search roots: got %#v want %#v", searchRoots, want)
	}

	excludeOnlyRoots := workspacePatternSearchRoots(repo, []string{"!packages/generated"})
	if !slices.Equal(excludeOnlyRoots, []string{filepath.Clean(repo)}) {
		t.Fatalf("unexpected exclude-only search roots: %#v", excludeOnlyRoots)
	}

	outsideRoots := workspacePatternSearchRoots(repo, []string{"../outside/*"})
	if !slices.Equal(outsideRoots, []string{filepath.Clean(repo)}) {
		t.Fatalf("unexpected outside-pattern search roots: %#v", outsideRoots)
	}

	if got := workspacePatternLiteralRoot("packages//web/*"); got != filepath.Join("packages", "web") {
		t.Fatalf("unexpected literal root for repeated separators: %q", got)
	}
	if got := workspacePatternLiteralRoot("././packages/web/*"); got != filepath.Join("packages", "web") {
		t.Fatalf("unexpected literal root for repeated dot prefixes: %q", got)
	}
	if got := workspacePatternLiteralRoot("apps/./web/*"); got != filepath.Join("apps", "web") {
		t.Fatalf("unexpected literal root for dot segment: %q", got)
	}
	if got := workspacePatternLiteralRoot("*"); got != "" {
		t.Fatalf("expected wildcard-only pattern to have no literal root, got %q", got)
	}
}

func TestLoadWorkspaceDependencyCatalogAggregatesManifestAndCatalogDeclarations(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		testPackageJSONName: `{
  "name": "root",
  "private": true,
  "workspaces": {
    "packages": ["packages/*"]
  },
  "dependencies": {
    "root-dep": "^1.0.0"
  },
  "peerDependencies": {
    "root-peer": "^2.0.0"
  }
}`,
		jsPnpmWorkspaceFile: `packages:
  - packages/*
catalog:
  pnpm-catalog: ^1.0.0
`,
		jsYarnRCFile: `catalogs:
  tools:
    yarn-catalog: ^2.0.0
`,
		filepath.Join("packages", "app", testPackageJSONName): `{
  "name": "app",
  "dependencies": {
    "leaf-dep": "^3.0.0"
  },
  "optionalDependencies": {
    "leaf-optional": "^4.0.0"
  }
}`,
	})

	catalog := mustLoadWorkspaceDependencyCatalog(t, repo)
	assertWorkspaceCatalogDeclarationKeys(t, catalog.declarations, "leaf-dep", "leaf-optional", "pnpm-catalog", "root-dep", "root-peer", "yarn-catalog")
	if len(catalog.warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", catalog.warnings)
	}
}

func TestDiscoverWorkspacePackageDirsHonorsExcludesAndSkipsNodeModules(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		filepath.Join("packages", "web", testPackageJSONName):                    `{"name":"web"}`,
		filepath.Join("packages", "legacy", testPackageJSONName):                 `{"name":"legacy"}`,
		filepath.Join("packages", "node_modules", "nested", testPackageJSONName): `{"name":"nested-skip"}`,
		filepath.Join("node_modules", "skip", testPackageJSONName): `{
  "name": "skip"
}`,
	})

	dirs, warnings := mustDiscoverWorkspacePackageDirs(t, repo, []string{"packages/*", "!packages/legacy"})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
	want := []string{filepath.Join(repo, "packages", "web")}
	if !slices.Equal(dirs, want) {
		t.Fatalf("unexpected workspace dirs: got %#v want %#v", dirs, want)
	}
}

func TestDiscoverWorkspacePackageDirsSharesStreamingBudgetAcrossDescents(t *testing.T) {
	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		filepath.Join("packages", "mobile", testPackageJSONName): `{"name":"mobile"}`,
		filepath.Join("packages", "web", testPackageJSONName):    `{"name":"web"}`,
	})

	dirs, warnings := mustDiscoverWorkspacePackageDirsWithEntryLimit(t, repo, []string{"packages/*"}, 3)
	wantDirs := []string{filepath.Join(repo, "packages", "mobile")}
	if !slices.Equal(dirs, wantDirs) {
		t.Fatalf("expected only the budgeted workspace manifest, got %#v want %#v", dirs, wantDirs)
	}
	wantWarning := workspaceWalkBudgetWarning(jsWalkSummary{entriesVisited: 3, truncated: true})
	if !slices.Equal(warnings, []string{wantWarning}) {
		t.Fatalf("expected deterministic partial-results warning, got %#v want %q", warnings, wantWarning)
	}

	dirs, warnings = mustDiscoverWorkspacePackageDirsWithEntryLimit(t, repo, []string{"packages/*"}, 0)
	if len(dirs) != 0 {
		t.Fatalf("expected zero budget to discover no workspaces, got %#v", dirs)
	}
	wantWarning = workspaceWalkBudgetWarning(jsWalkSummary{truncated: true})
	if !slices.Equal(warnings, []string{wantWarning}) {
		t.Fatalf("expected zero-budget partial-results warning, got %#v want %q", warnings, wantWarning)
	}
}

func TestAdapterAnalysePropagatesWorkspaceWalkCancellation(t *testing.T) {
	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		testPackageJSONName: `{"private":true,"workspaces":["packages/*"]}`,
	})
	if err := os.Mkdir(filepath.Join(repo, "packages"), 0o700); err != nil {
		t.Fatalf("mkdir packages: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := &boundedRootWalkReadDirFile{total: 1_000_000, cancelOnRead: cancel}
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
	}
	originalOpenWorkspaceSearchRoot := openWorkspaceSearchRoot
	openWorkspaceSearchRoot = func(string) (safeio.Root, error) {
		return root, nil
	}
	t.Cleanup(func() {
		openWorkspaceSearchRoot = originalOpenWorkspaceSearchRoot
	})

	_, err := NewAdapter().Analyse(ctx, language.Request{RepoPath: repo, TopN: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected public analysis to return context cancellation, got %v", err)
	}
	if reader.enumerated != jsWalkReadDirBatchSize || reader.totalNameCalls() != 0 {
		t.Fatalf("expected cancellation within one untouched batch, enumerated=%d names=%d", reader.enumerated, reader.totalNameCalls())
	}
}

func TestDiscoverWorkspacePackageDirsNoFollowRootGuards(t *testing.T) {
	repo := t.TempDir()
	rootManifestPath := filepath.Join(repo, testPackageJSONName)
	ignoredRoot := filepath.Join(repo, "node_modules")
	if err := os.Mkdir(ignoredRoot, 0o700); err != nil {
		t.Fatalf("mkdir ignored root: %v", err)
	}
	dirs, warnings := mustDiscoverWorkspacePackageDirsInRoot(t, repo, rootManifestPath, ignoredRoot, nil)
	if len(dirs) != 0 || len(warnings) != 0 {
		t.Fatalf("expected ignored search root to be skipped, dirs=%#v warnings=%#v", dirs, warnings)
	}

	targetRoot := filepath.Join(repo, "packages")
	if err := os.Mkdir(targetRoot, 0o700); err != nil {
		t.Fatalf("mkdir target root: %v", err)
	}
	symlinkRoot := filepath.Join(repo, "linked-packages")
	if err := os.Symlink(targetRoot, symlinkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dirs, warnings = mustDiscoverWorkspacePackageDirsInRoot(t, repo, rootManifestPath, symlinkRoot, nil)
	if len(dirs) != 0 {
		t.Fatalf("expected symlinked search root not to be traversed, got %#v", dirs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to scan workspace package manifests") {
		t.Fatalf("expected symlinked search-root warning, got %#v", warnings)
	}
}

func TestDiscoverWorkspacePackageDirsScopesWalkToWorkspaceRoots(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		filepath.Join("packages", "web", testPackageJSONName): `{"name":"web"}`,
		filepath.Join("vendor", "locked", testPackageJSONName): `{
  "name": "locked"
}`,
	})

	lockedDir := filepath.Join(repo, "vendor", "locked")
	if err := os.Chmod(lockedDir, 0); err != nil {
		t.Fatalf("chmod locked dir: %v", err)
	}
	defer func() {
		if err := os.Chmod(lockedDir, 0o755); err != nil {
			t.Fatalf("restore locked dir permissions: %v", err)
		}
	}()

	dirs, warnings := mustDiscoverWorkspacePackageDirs(t, repo, []string{"packages/*"})
	if len(warnings) != 0 {
		t.Fatalf("expected scoped walk to avoid unrelated warnings, got %#v", warnings)
	}
	want := []string{filepath.Join(repo, "packages", "web")}
	if !slices.Equal(dirs, want) {
		t.Fatalf("unexpected workspace dirs from scoped walk: got %#v want %#v", dirs, want)
	}
}

func TestDiscoverWorkspacePackageDirsInRootGuardsAndFiltering(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	rootManifestPath := filepath.Join(repo, testPackageJSONName)
	testutil.MustWriteFile(t, rootManifestPath, `{"name":"root"}`)
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		filepath.Join("packages", "web", testPackageJSONName):    `{"name":"web"}`,
		filepath.Join("packages", "legacy", testPackageJSONName): `{"name":"legacy"}`,
		filepath.Join("packages", "web", "README.md"):            "notes",
	})

	compiled, warnings := compileWorkspacePatterns([]string{"packages/web"})
	if len(warnings) != 0 {
		t.Fatalf("expected no compile warnings, got %#v", warnings)
	}

	_, invalidRootWarnings := mustDiscoverWorkspacePackageDirsInRoot(t, repo, rootManifestPath, string([]byte{0}), compiled)
	if len(invalidRootWarnings) != 1 || !strings.Contains(invalidRootWarnings[0], "unable to access workspace search root") {
		t.Fatalf("expected invalid search-root warning, got %#v", invalidRootWarnings)
	}

	notDirPath := filepath.Join(repo, "not-a-dir")
	testutil.MustWriteFile(t, notDirPath, "x")
	notDirDirs, notDirWarnings := mustDiscoverWorkspacePackageDirsInRoot(t, repo, rootManifestPath, notDirPath, compiled)
	if len(notDirDirs) != 0 || len(notDirWarnings) != 0 {
		t.Fatalf("expected file search root to be ignored without warnings, dirs=%#v warnings=%#v", notDirDirs, notDirWarnings)
	}

	dirs, rootWarnings := mustDiscoverWorkspacePackageDirsInRoot(t, repo, rootManifestPath, repo, compiled)
	if len(rootWarnings) != 0 {
		t.Fatalf("expected no root walk warnings, got %#v", rootWarnings)
	}
	if len(dirs) != 1 {
		t.Fatalf("expected one matched workspace dir, got %#v", dirs)
	}
	if _, ok := dirs[filepath.Join(repo, "packages", "web")]; !ok {
		t.Fatalf("expected matched workspace dir to include packages/web, got %#v", dirs)
	}

	outsideDir := t.TempDir()
	if workspacePackageDirMatches(repo, outsideDir, compiled) {
		t.Fatalf("expected outside dir to fail workspace package match")
	}
}

func TestValidateWorkspaceSearchRootSkipsMissingPathWithoutWarning(t *testing.T) {
	t.Parallel()

	warning, skip := validateWorkspaceSearchRoot(filepath.Join(t.TempDir(), "missing"))
	if !skip || warning != "" {
		t.Fatalf("expected missing search root to be skipped without warning, got skip=%v warning=%q", skip, warning)
	}
}

func TestWorkspacePackageDirWalkerReturnsWalkError(t *testing.T) {
	t.Parallel()

	walkErr := errors.New("walk failed")
	dirs := make(map[string]struct{})
	walker := workspacePackageDirWalker(t.TempDir(), filepath.Join(t.TempDir(), testPackageJSONName), nil, dirs)

	if err := walker("", nil, walkErr); !errors.Is(err, walkErr) {
		t.Fatalf("expected walker to return walk error, got %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("expected walk error to leave dirs empty, got %#v", dirs)
	}
}

func TestWorkspacePackageDirWalkerSkipsIgnoredDirectories(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	nodeModules := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	var dirEntry fs.DirEntry
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == "node_modules" {
			dirEntry = entry
			break
		}
	}
	if dirEntry == nil {
		t.Fatal("expected node_modules directory entry")
	}

	walker := workspacePackageDirWalker(repo, filepath.Join(repo, testPackageJSONName), nil, map[string]struct{}{})
	if err := walker(nodeModules, dirEntry, nil); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected node_modules workspace walk to skip dir, got %v", err)
	}
}

func TestDiscoverWorkspacePackageDirsReportsWalkErrors(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	lockedDir := filepath.Join(repo, "packages", "locked")
	if err := os.MkdirAll(lockedDir, 0o755); err != nil {
		t.Fatalf("mkdir locked dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(lockedDir, testPackageJSONName), `{"name":"locked"}`)
	if err := os.Chmod(lockedDir, 0); err != nil {
		t.Fatalf("chmod locked dir: %v", err)
	}
	defer func() {
		if err := os.Chmod(lockedDir, 0o755); err != nil {
			t.Fatalf("restore locked dir permissions: %v", err)
		}
	}()

	dirs, warnings := mustDiscoverWorkspacePackageDirs(t, repo, []string{"packages/*"})
	if len(dirs) != 0 {
		t.Fatalf("expected unreadable workspace directory to be skipped, got %#v", dirs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to scan workspace package manifests") {
		t.Fatalf("expected walk warning, got %#v", warnings)
	}
}

func TestResolveDependencyRootFromDir(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	workspaceDir := filepath.Join(repo, "packages", "web")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace dir: %v", err)
	}
	installWorkspaceCatalogDependencies(t, repo, "react")

	if got := resolveDependencyRootFromDir(repo, workspaceDir, "react"); got != filepath.Join(repo, "node_modules", "react") {
		t.Fatalf("unexpected dependency root: %q", got)
	}
	if got := resolveDependencyRootFromDir("", workspaceDir, "react"); got != "" {
		t.Fatalf("expected blank repo path to return empty root, got %q", got)
	}
	if got := resolveDependencyRootFromDir(repo, t.TempDir(), "react"); got != "" {
		t.Fatalf("expected outside workspace dir to return empty root, got %q", got)
	}
	if got := resolveDependencyRootFromDir(repo, repo, "missing"); got != "" {
		t.Fatalf("expected unresolved dependency at repo root to return empty root, got %q", got)
	}
	if got := resolveDependencyRootFromDir(string([]byte{0}), workspaceDir, "react"); got != "" {
		t.Fatalf("expected invalid repo path to return empty root, got %q", got)
	}
	if got := resolveDependencyRootFromDir(repo, string([]byte{0}), "react"); got != "" {
		t.Fatalf("expected invalid start dir to return empty root, got %q", got)
	}
}

func TestDependencyCollectorRecordResolvedRootTracksMultipleRoots(t *testing.T) {
	t.Parallel()

	collector := newDependencyCollector()
	collector.recordResolvedRoot("", filepath.Join("node_modules", "react"))
	collector.recordResolvedRoot("react", "")
	if len(collector.roots) != 0 {
		t.Fatalf("expected blank root writes to be ignored, got %#v", collector.roots)
	}

	root := filepath.Join("node_modules", "react")
	otherRoot := filepath.Join("packages", "web", "node_modules", "react")
	collector.recordResolvedRoot("react", root)
	collector.recordResolvedRoot("react", root)
	if got := collector.roots["react"]; got != root {
		t.Fatalf("unexpected first dependency root: %q", got)
	}
	if _, ok := collector.multiRoot["react"]; ok {
		t.Fatalf("did not expect duplicate root to mark multi-root")
	}

	collector.recordResolvedRoot("react", otherRoot)
	if _, ok := collector.multiRoot["react"]; !ok {
		t.Fatalf("expected differing roots to mark react as multi-root")
	}
}

func TestDependencyCollectorMergeWorkspaceDeclarationsRespectsResolution(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	workspaceDir := filepath.Join(repo, "packages", "web")

	unresolved := newDependencyCollector()
	unresolved.missing["react"] = struct{}{}
	unresolved.mergeWorkspaceDeclarations(repo, map[string]workspaceDependencyDeclaration{
		"react": {declarationDirs: map[string]struct{}{workspaceDir: {}}},
	})
	if _, ok := unresolved.found["react"]; ok {
		t.Fatalf("expected unresolved workspace dependency to remain unfound")
	}
	if _, ok := unresolved.missing["react"]; !ok {
		t.Fatalf("expected unresolved workspace dependency to remain missing")
	}

	alreadyFound := newDependencyCollector()
	alreadyFound.found["react"] = struct{}{}
	alreadyFound.mergeWorkspaceDeclarations(repo, map[string]workspaceDependencyDeclaration{
		"react": {declarationDirs: map[string]struct{}{workspaceDir: {}}},
	})
	if _, ok := alreadyFound.missing["react"]; ok {
		t.Fatalf("expected already-found dependency not to be marked missing")
	}

	installWorkspaceCatalogDependencies(t, repo, "eslint")
	resolved := newDependencyCollector()
	resolved.mergeWorkspaceDeclarations(repo, map[string]workspaceDependencyDeclaration{
		"eslint": {declarationDirs: map[string]struct{}{repo: {}}},
	})
	if _, ok := resolved.found["eslint"]; !ok {
		t.Fatalf("expected resolved workspace dependency to be marked found")
	}
	if _, ok := resolved.missing["eslint"]; ok {
		t.Fatalf("expected resolved workspace dependency not to remain missing")
	}
	if got := resolved.roots["eslint"]; got != filepath.Join(repo, "node_modules", "eslint") {
		t.Fatalf("unexpected resolved root for workspace dependency: %q", got)
	}
}

func TestDependencyCollectorMergeWorkspaceDeclarationsUnsafeDominatesMissing(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	missingDir := filepath.Join(repo, "packages", "missing")
	unsafeDir := filepath.Join(repo, "packages", "unsafe")

	outside := t.TempDir()
	outsideDepRoot := filepath.Join(outside, "node_modules", "linked")
	if err := os.MkdirAll(outsideDepRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, testPackageJSONName), `{"name":"linked","main":"index.js"}`)
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, testIndexJS), "module.exports = function linked() {}\n")

	unsafeNodeModules := filepath.Join(unsafeDir, "node_modules")
	if err := os.MkdirAll(unsafeNodeModules, 0o755); err != nil {
		t.Fatalf("mkdir unsafe node_modules: %v", err)
	}
	if err := os.Symlink(outsideDepRoot, filepath.Join(unsafeNodeModules, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	collector := newDependencyCollector()
	collector.missing["linked"] = struct{}{}
	collector.mergeWorkspaceDeclarations(repo, map[string]workspaceDependencyDeclaration{
		"linked": {declarationDirs: map[string]struct{}{missingDir: {}, unsafeDir: {}}},
	})
	if _, ok := collector.unsafe["linked"]; !ok {
		t.Fatalf("expected unsafe workspace declaration to be recorded, got %#v", collector)
	}
	if _, ok := collector.missing["linked"]; ok {
		t.Fatalf("expected unsafe workspace declaration to clear missing status, got %#v", collector)
	}

	collector = newDependencyCollector()
	collector.unsafe["linked"] = struct{}{}
	collector.mergeWorkspaceDeclarations(repo, map[string]workspaceDependencyDeclaration{
		"linked": {declarationDirs: map[string]struct{}{missingDir: {}}},
	})
	if _, ok := collector.missing["linked"]; ok {
		t.Fatalf("expected existing unsafe status to suppress later missing status, got %#v", collector)
	}
}

func TestResolveDependencyRootAtDirAndIsPathWithin(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	installWorkspaceCatalogDependencies(t, repo, "react")

	assertResolvedDependencyRootAtDir(t, repo, "react", filepath.Join(repo, "node_modules", "react"))

	badRoot := filepath.Join(repo, "node_modules", "bad", testPackageJSONName)
	if err := os.MkdirAll(badRoot, 0o755); err != nil {
		t.Fatalf("mkdir bad root: %v", err)
	}
	assertRejectedDependencyRootAtDir(t, repo, "bad", "directory package.json")

	symlinkTarget := filepath.Join(repo, "outside-react")
	if err := os.MkdirAll(symlinkTarget, 0o755); err != nil {
		t.Fatalf("mkdir symlink target: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(symlinkTarget, testPackageJSONName), "{}\n")
	symlinkPath := filepath.Join(repo, "node_modules", "linked")
	if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assertResolvedDependencyRootAtDir(t, repo, "linked", symlinkPath)

	for _, testCase := range []struct {
		name string
		path string
		want bool
	}{
		{name: "descendant path", path: filepath.Join(repo, "packages", "web"), want: true},
		{name: "parent path", path: filepath.Dir(repo), want: false},
		{name: "relative path", path: "packages/web", want: false},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := isPathWithin(testCase.path, repo); got != testCase.want {
				t.Fatalf("isPathWithin(%q, %q) = %v, want %v", testCase.path, repo, got, testCase.want)
			}
		})
	}
}

func assertResolvedDependencyRootAtDir(t *testing.T, repo, dependency, want string) {
	t.Helper()

	if root, ok := resolveDependencyRootAtDir(repo, dependency); !ok || root != want {
		t.Fatalf("unexpected resolved root for %q: root=%q ok=%v want=%q", dependency, root, ok, want)
	}
}

func assertRejectedDependencyRootAtDir(t *testing.T, repo, dependency, context string) {
	t.Helper()

	if root, ok := resolveDependencyRootAtDir(repo, dependency); ok || root != "" {
		t.Fatalf("expected %s to be rejected, got root=%q ok=%v", context, root, ok)
	}
}

func TestDependencyRootValidationHelpers(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	nodeModules := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}

	filePath := filepath.Join(repo, "README.md")
	testutil.MustWriteFile(t, filePath, "root\n")

	pkgPath := filepath.Join(repo, testPackageJSONName)
	testutil.MustWriteFile(t, pkgPath, "{}\n")
	assertDirectoryValidationCases(t, repo, filePath)
	assertRegularFileValidationCases(t, repo, nodeModules, pkgPath)
	assertSymlinkValidationCases(t, repo, nodeModules, pkgPath)
	assertValidatedDependencyRootCases(t, repo, nodeModules, filePath)
	if got := resolveDependencyRootFromDir(repo, filepath.Join(repo, "packages"), ""); got != "" {
		t.Fatalf("expected blank dependency to resolve to empty root, got %q", got)
	}
}

func assertDirectoryValidationCases(t *testing.T, repo, filePath string) {
	t.Helper()

	for _, testCase := range []struct {
		name          string
		path          string
		wantSubstring string
	}{
		{name: "file path rejected", path: filePath, wantSubstring: "not a directory"},
		{name: "blank path rejected", path: "", wantSubstring: "path is empty"},
		{name: "missing directory rejected", path: filepath.Join(repo, "missing-dir")},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			_, err := validateDirectoryPathNoFollow(testCase.path)
			assertValidationError(t, err, testCase.wantSubstring, "directory path")
		})
	}
}

func assertRegularFileValidationCases(t *testing.T, repo, nodeModules, pkgPath string) {
	t.Helper()

	if err := validateRegularFileNoFollow(pkgPath); err != nil {
		t.Fatalf("expected regular package file to validate, got %v", err)
	}
	for _, testCase := range []struct {
		name          string
		path          string
		wantSubstring string
	}{
		{name: "directory rejected", path: nodeModules, wantSubstring: "not a regular file"},
		{name: "missing file rejected", path: filepath.Join(repo, "missing.json")},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			err := validateRegularFileNoFollow(testCase.path)
			assertValidationError(t, err, testCase.wantSubstring, "regular file")
		})
	}
}

func assertSymlinkValidationCases(t *testing.T, repo, nodeModules, pkgPath string) {
	t.Helper()

	linkedDirTarget := filepath.Join(repo, "outside-linked")
	if err := os.MkdirAll(linkedDirTarget, 0o755); err != nil {
		t.Fatalf("mkdir linked dir target: %v", err)
	}
	linkedDir := filepath.Join(repo, "linked-dir")
	if err := os.Symlink(linkedDirTarget, linkedDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkedFile := filepath.Join(repo, "linked-package.json")
	if err := os.Symlink(pkgPath, linkedFile); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := validateDirectoryPathNoFollow(linkedDir); err == nil || !strings.Contains(err.Error(), "symlinked path component") {
		t.Fatalf("expected symlinked directory path to be rejected, got %v", err)
	}
	if err := validateRegularFileNoFollow(linkedFile); err == nil || !strings.Contains(err.Error(), "symlinked file path") {
		t.Fatalf("expected symlinked file path to be rejected, got %v", err)
	}
	if _, err := validateDirectoryPathNoFollowFromBase(repo, "README.md"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected file path component to be rejected from base, got %v", err)
	}
	if got, err := validateDirectoryPathNoFollowFromBase(repo, ".", "node_modules"); err != nil || got != nodeModules {
		t.Fatalf("expected dot path component to be ignored, got path=%q err=%v", got, err)
	}
}

func assertValidatedDependencyRootCases(t *testing.T, repo, nodeModules, filePath string) {
	t.Helper()

	scopedDir := filepath.Join(nodeModules, "@scope")
	if err := os.MkdirAll(scopedDir, 0o755); err != nil {
		t.Fatalf("mkdir scoped dir: %v", err)
	}
	scopedTarget := filepath.Join(repo, "outside-scoped")
	if err := os.MkdirAll(scopedTarget, 0o755); err != nil {
		t.Fatalf("mkdir scoped target: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(scopedTarget, testPackageJSONName), "{}\n")
	if err := os.Symlink(scopedTarget, filepath.Join(scopedDir, "pkg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, err := validatedDependencyRootAtDir(repo, "@scope/pkg"); err != nil || got != filepath.Join(scopedDir, "pkg") {
		t.Fatalf("expected scoped dependency symlink inside repo to resolve safely, got path=%q err=%v", got, err)
	}
	for _, testCase := range []struct {
		name          string
		repo          string
		dependency    string
		wantSubstring string
	}{
		{name: "blank dependency rejected", repo: repo, dependency: ""},
		{name: "file repo rejected", repo: filePath, dependency: "pkg", wantSubstring: "not a directory"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			_, err := validatedDependencyRootAtDir(testCase.repo, testCase.dependency)
			assertValidationError(t, err, testCase.wantSubstring, "validated dependency root")
		})
	}
}

func assertValidationError(t *testing.T, err error, wantSubstring, context string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected %s validation error", context)
	}
	if wantSubstring != "" && !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected %s validation error containing %q, got %v", context, wantSubstring, err)
	}
}

func TestWorkspacePathAndDedupeHelpers(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	insideDir := filepath.Join(repo, "packages", "web")
	if err := os.MkdirAll(insideDir, 0o755); err != nil {
		t.Fatalf("mkdir inside dir: %v", err)
	}
	rel, ok := workspaceRelativeDir(repo, insideDir)
	if !ok || rel != filepath.ToSlash(filepath.Join("packages", "web")) {
		t.Fatalf("unexpected relative dir: rel=%q ok=%v", rel, ok)
	}

	outsideDir := t.TempDir()
	if _, ok := workspaceRelativeDir(repo, outsideDir); ok {
		t.Fatalf("expected outside directory to be rejected")
	}

	displayInside := workspaceDisplayPath(repo, filepath.Join(insideDir, testPackageJSONName))
	if displayInside != filepath.Join("packages", "web", testPackageJSONName) {
		t.Fatalf("unexpected workspace display path: %q", displayInside)
	}
	if got := workspaceDisplayPath(repo, filepath.Join(outsideDir, testPackageJSONName)); got != testPackageJSONName {
		t.Fatalf("expected outside display path to fall back to basename, got %q", got)
	}

	warnings := dedupeWorkspaceWarnings([]string{"", "alpha", "alpha", "beta"})
	if !slices.Equal(warnings, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected deduped warnings: %#v", warnings)
	}

	patterns := dedupeWorkspacePatterns([]string{"", "packages/*", "packages/*", " apps/* "})
	if !slices.Equal(patterns, []string{"packages/*", "apps/*"}) {
		t.Fatalf("unexpected deduped patterns: %#v", patterns)
	}

	catalog := mustLoadWorkspaceDependencyCatalog(t, "")
	if len(catalog.declarations) != 0 || len(catalog.warnings) != 0 {
		t.Fatalf("expected empty catalog for blank repo path, got %#v", catalog)
	}
}

func TestLoadWorkspaceDependencyCatalogKeepsWarningsWithoutWorkspaceSignals(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testPackageJSONName), "{")
	testutil.MustWriteFile(t, filepath.Join(repo, jsYarnRCFile), "catalog: [\n")

	catalog := mustLoadWorkspaceDependencyCatalog(t, repo)
	if len(catalog.declarations) != 0 {
		t.Fatalf("expected no declarations, got %#v", catalog.declarations)
	}
	if len(catalog.warnings) != 2 {
		t.Fatalf("expected parse warnings to be preserved, got %#v", catalog.warnings)
	}
}

func TestLoadWorkspaceDependencyCatalogCollectsWorkspaceWarnings(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		testPackageJSONName: `{
  "name": "root",
  "private": true,
  "workspaces": ["packages/*"]
}`,
		jsPnpmWorkspaceFile: "packages: [\n",
		filepath.Join("packages", "app", testPackageJSONName): `{
  "name": "app",
  "dependencies": {
    "react": "^18.3.1"
  }
}`,
		filepath.Join("packages", "broken", testPackageJSONName): "{",
	})

	catalog := mustLoadWorkspaceDependencyCatalog(t, repo)
	assertWorkspaceCatalogDeclarationKeys(t, catalog.declarations, "react")
	if len(catalog.warnings) != 2 {
		t.Fatalf("expected workspace manifest warnings to be preserved, got %#v", catalog.warnings)
	}
	joinedWarnings := strings.Join(catalog.warnings, "\n")
	if !strings.Contains(joinedWarnings, "failed to parse "+jsPnpmWorkspaceFile) {
		t.Fatalf("expected pnpm warning in %#v", catalog.warnings)
	}
	if !strings.Contains(joinedWarnings, filepath.Join("packages", "broken", testPackageJSONName)) {
		t.Fatalf("expected broken workspace manifest warning in %#v", catalog.warnings)
	}
}

func TestReadWorkspacePackageJSONRejectsOutsideRepoFiles(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outsideRepo := t.TempDir()
	manifestPath := filepath.Join(outsideRepo, testPackageJSONName)
	testutil.MustWriteFile(t, manifestPath, `{"name":"outside"}`)

	_, found, warning := readWorkspacePackageJSON(repo, manifestPath)
	if found {
		t.Fatalf("expected outside-repo manifest to be rejected")
	}
	if !strings.Contains(warning, "unable to read workspace manifest") {
		t.Fatalf("expected outside-repo warning, got %q", warning)
	}
}

func TestWorkspaceCatalogHelperGuardBranches(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()

	invalidPath := string([]byte{0})
	assertWorkspaceManifestGuard(t, "blank-manifest", "", "", func() (bool, string) {
		_, found, warning := readWorkspacePackageJSON(repo, "")
		return found, warning
	})
	assertWorkspaceManifestGuard(t, "invalid-manifest", "unable to read workspace manifest", invalidPath, func() (bool, string) {
		_, found, warning := readWorkspacePackageJSON(repo, invalidPath)
		return found, warning
	})
	assertWorkspaceManifestGuard(t, "invalid-pnpm", "unable to read "+jsPnpmWorkspaceFile, invalidPath, func() (bool, string) {
		_, found, warning := readPnpmWorkspaceManifest(invalidPath)
		return found, warning
	})
	assertWorkspaceManifestGuard(t, "invalid-yarn", "unable to read "+jsYarnRCFile, invalidPath, func() (bool, string) {
		_, found, warning := readYarnCatalogManifest(invalidPath)
		return found, warning
	})

	catalog := workspaceDependencyCatalog{declarations: make(map[string]workspaceDependencyDeclaration)}
	catalog.addDependency("./bad", repo)
	if len(catalog.declarations) != 0 {
		t.Fatalf("expected invalid dependency name to be ignored, got %#v", catalog.declarations)
	}

	compiled, warnings := compileWorkspacePatterns([]string{"", "   "})
	if len(compiled) != 0 || len(warnings) != 0 {
		t.Fatalf("expected blank patterns to be ignored, got compiled=%#v warnings=%#v", compiled, warnings)
	}

	if normalized, exclude := normalizeWorkspacePattern("   "); normalized != "" || exclude {
		t.Fatalf("expected blank normalized pattern, got normalized=%q exclude=%v", normalized, exclude)
	}

	if _, ok := workspaceRelativeDir("", repo); ok {
		t.Fatalf("expected empty repo root to fail relative path resolution")
	}

	if got := workspaceDisplayPath("", filepath.Join(repo, testPackageJSONName)); got != testPackageJSONName {
		t.Fatalf("expected display path to fall back to basename, got %q", got)
	}
}

func assertWorkspaceManifestGuard(t *testing.T, name, wantWarning, input string, read func() (bool, string)) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Helper()

		found, warning := read()
		if found {
			t.Fatalf("expected %q input %q to be rejected", name, input)
		}
		if wantWarning == "" {
			if warning != "" {
				t.Fatalf("expected %q input %q to return no warning, got %q", name, input, warning)
			}
			return
		}
		if !strings.Contains(warning, wantWarning) {
			t.Fatalf("expected %q warning containing %q, got %q", name, wantWarning, warning)
		}
	})
}

func writeWorkspaceCatalogFiles(t *testing.T, repo string, files map[string]string) {
	t.Helper()

	for relativePath, content := range files {
		testutil.MustWriteFile(t, filepath.Join(repo, relativePath), content)
	}
}

func mustListDependencies(t *testing.T, repo string, scan ScanResult) ([]string, map[string]string, []string) {
	t.Helper()

	deps, roots, warnings, err := listDependencies(context.Background(), repo, scan)
	if err != nil {
		t.Fatalf("list dependencies: %v", err)
	}
	return deps, roots, warnings
}

func mustLoadWorkspaceDependencyCatalog(t *testing.T, repo string) workspaceDependencyCatalog {
	t.Helper()

	catalog, err := loadWorkspaceDependencyCatalog(context.Background(), repo)
	if err != nil {
		t.Fatalf("load workspace dependency catalog: %v", err)
	}
	return catalog
}

func mustDiscoverWorkspacePackageDirs(t *testing.T, repo string, patterns []string) ([]string, []string) {
	t.Helper()

	dirs, warnings, err := discoverWorkspacePackageDirs(context.Background(), repo, patterns)
	if err != nil {
		t.Fatalf("discover workspace package dirs: %v", err)
	}
	return dirs, warnings
}

func mustDiscoverWorkspacePackageDirsWithEntryLimit(t *testing.T, repo string, patterns []string, entryLimit int) ([]string, []string) {
	t.Helper()

	dirs, warnings, err := discoverWorkspacePackageDirsWithEntryLimit(context.Background(), repo, patterns, entryLimit)
	if err != nil {
		t.Fatalf("discover workspace package dirs with entry limit: %v", err)
	}
	return dirs, warnings
}

func mustDiscoverWorkspacePackageDirsInRoot(t *testing.T, repo, rootManifestPath, searchRoot string, patterns []workspacePattern) (map[string]struct{}, []string) {
	t.Helper()

	dirs, warnings, err := discoverWorkspacePackageDirsInRoot(context.Background(), repo, rootManifestPath, searchRoot, patterns)
	if err != nil {
		t.Fatalf("discover workspace package dirs in root: %v", err)
	}
	return dirs, warnings
}

func installWorkspaceCatalogDependencies(t *testing.T, repo string, names ...string) {
	t.Helper()

	for _, name := range names {
		if err := writeDependency(repo, name, testModuleExportsStub); err != nil {
			t.Fatalf("write dependency %s: %v", name, err)
		}
	}
}

func assertWorkspaceCatalogDependencies(t *testing.T, deps, expected []string, present bool) {
	t.Helper()

	for _, dependency := range expected {
		if slices.Contains(deps, dependency) != present {
			state := "absent"
			if present {
				state = "present"
			}
			t.Fatalf("expected dependency %q to be %s in %#v", dependency, state, deps)
		}
	}
}

func assertWorkspaceCatalogRoots(t *testing.T, repo string, roots map[string]string, expected []string) {
	t.Helper()

	for _, dependency := range expected {
		want := filepath.Join(repo, "node_modules", dependency)
		if got := roots[dependency]; got != want {
			t.Fatalf("unexpected dependency root for %q: got %q want %q", dependency, got, want)
		}
	}
}

func assertWorkspaceCatalogRootAbsence(t *testing.T, roots map[string]string, unexpected []string) {
	t.Helper()

	for _, dependency := range unexpected {
		if _, ok := roots[dependency]; ok {
			t.Fatalf("did not expect dependency root for %q, got %#v", dependency, roots)
		}
	}
}

func assertWorkspaceCatalogDeclarationKeys(t *testing.T, declarations map[string]workspaceDependencyDeclaration, expected ...string) {
	t.Helper()

	keys := make([]string, 0, len(declarations))
	for key := range declarations {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	slices.Sort(expected)
	if !slices.Equal(keys, expected) {
		t.Fatalf("unexpected declaration keys: got %#v want %#v", keys, expected)
	}
}

func readPnpmWorkspaceManifestWarning(repo string) (bool, string) {
	_, found, warning := readPnpmWorkspaceManifest(repo)
	return found, warning
}

func readYarnCatalogManifestWarning(repo string) (bool, string) {
	_, found, warning := readYarnCatalogManifest(repo)
	return found, warning
}

type workspaceManifestWarningCase struct {
	name           string
	fileName       string
	writeManifest  func(t *testing.T, repo string)
	readManifest   func(string) (bool, string)
	wantMissingMsg string
	wantWarningMsg string
}

func invalidWorkspaceManifestWarningCase(name, fileName, content, wantMissingMsg, wantWarningMsg string, readManifest func(string) (bool, string)) workspaceManifestWarningCase {
	return workspaceManifestWarningCase{
		name:     name,
		fileName: fileName,
		writeManifest: func(t *testing.T, repo string) {
			t.Helper()
			testutil.MustWriteFile(t, filepath.Join(repo, fileName), content)
		},
		readManifest:   readManifest,
		wantMissingMsg: wantMissingMsg,
		wantWarningMsg: wantWarningMsg,
	}
}

func directoryWorkspaceManifestWarningCase(name, fileName, mkdirErrorMsg, wantMissingMsg, wantWarningMsg string, readManifest func(string) (bool, string)) workspaceManifestWarningCase {
	return workspaceManifestWarningCase{
		name:     name,
		fileName: fileName,
		writeManifest: func(t *testing.T, repo string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(repo, fileName), 0o755); err != nil {
				t.Fatalf("%s: %v", mkdirErrorMsg, err)
			}
		},
		readManifest:   readManifest,
		wantMissingMsg: wantMissingMsg,
		wantWarningMsg: wantWarningMsg,
	}
}

func assertWorkspaceManifestReaderWarning(t *testing.T, repo string, testCase workspaceManifestWarningCase) {
	t.Helper()

	found, warning := testCase.readManifest(repo)
	if found {
		t.Fatal(testCase.wantMissingMsg)
	}
	if !strings.Contains(warning, "failed to parse "+testCase.fileName) {
		t.Fatalf("%s, got %q", testCase.wantWarningMsg, warning)
	}
}
