package js

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

			deps, roots, warnings := listDependencies(repo, ScanResult{})
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
}

func TestWorkspaceManifestReaders(t *testing.T) {
	t.Parallel()

	t.Run("pnpm parse warning", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, jsPnpmWorkspaceFile), "packages: [\n")

		_, found, warning := readPnpmWorkspaceManifest(repo)
		if found {
			t.Fatalf("expected invalid pnpm manifest to be ignored")
		}
		if !strings.Contains(warning, "failed to parse "+jsPnpmWorkspaceFile) {
			t.Fatalf("expected pnpm parse warning, got %q", warning)
		}
	})

	t.Run("pnpm directory warning", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, jsPnpmWorkspaceFile), 0o755); err != nil {
			t.Fatalf("mkdir pnpm workspace dir: %v", err)
		}

		_, found, warning := readPnpmWorkspaceManifest(repo)
		if found {
			t.Fatalf("expected pnpm directory manifest to be ignored")
		}
		if !strings.Contains(warning, "failed to parse "+jsPnpmWorkspaceFile) {
			t.Fatalf("expected pnpm directory warning, got %q", warning)
		}
	})

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

	t.Run("yarn parse warning", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, jsYarnRCFile), "catalog: [\n")

		_, found, warning := readYarnCatalogManifest(repo)
		if found {
			t.Fatalf("expected invalid yarn config to be ignored")
		}
		if !strings.Contains(warning, "failed to parse "+jsYarnRCFile) {
			t.Fatalf("expected yarn parse warning, got %q", warning)
		}
	})

	t.Run("yarn directory warning", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, jsYarnRCFile), 0o755); err != nil {
			t.Fatalf("mkdir yarn rc dir: %v", err)
		}

		_, found, warning := readYarnCatalogManifest(repo)
		if found {
			t.Fatalf("expected yarn directory manifest to be ignored")
		}
		if !strings.Contains(warning, "failed to parse "+jsYarnRCFile) {
			t.Fatalf("expected yarn directory warning, got %q", warning)
		}
	})
}

func TestWorkspacePatternHelpers(t *testing.T) {
	t.Parallel()

	patterns := parseWorkspacePatterns(map[string]any{
		"packages": []any{"packages/*", "packages/*", "", 7},
	})
	if !slices.Equal(patterns, []string{"packages/*"}) {
		t.Fatalf("unexpected workspace patterns: %#v", patterns)
	}

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

	compiled, warnings = compileWorkspacePatterns([]string{string([]byte{0xff})})
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

	catalog := loadWorkspaceDependencyCatalog(repo)
	assertWorkspaceCatalogDeclarationKeys(t, catalog.declarations, "leaf-dep", "leaf-optional", "pnpm-catalog", "root-dep", "root-peer", "yarn-catalog")
	if len(catalog.warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", catalog.warnings)
	}
}

func TestDiscoverWorkspacePackageDirsHonorsExcludesAndSkipsNodeModules(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeWorkspaceCatalogFiles(t, repo, map[string]string{
		filepath.Join("packages", "web", testPackageJSONName):    `{"name":"web"}`,
		filepath.Join("packages", "legacy", testPackageJSONName): `{"name":"legacy"}`,
		filepath.Join("node_modules", "skip", testPackageJSONName): `{
  "name": "skip"
}`,
	})

	dirs, warnings := discoverWorkspacePackageDirs(repo, []string{"packages/*", "!packages/legacy"})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
	want := []string{filepath.Join(repo, "packages", "web")}
	if !slices.Equal(dirs, want) {
		t.Fatalf("unexpected workspace dirs: got %#v want %#v", dirs, want)
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

	dirs, warnings := discoverWorkspacePackageDirs(repo, []string{"packages/*"})
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
}

func TestDependencyCollectorRecordResolvedRootTracksMultipleRoots(t *testing.T) {
	t.Parallel()

	collector := newDependencyCollector()
	collector.recordResolvedRoot("", filepath.Join("node_modules", "react"))
	collector.recordResolvedRoot("react", "")
	if len(collector.roots) != 0 {
		t.Fatalf("expected blank root writes to be ignored, got %#v", collector.roots)
	}

	firstRoot := filepath.Join("node_modules", "react")
	secondRoot := filepath.Join("packages", "web", "node_modules", "react")
	collector.recordResolvedRoot("react", firstRoot)
	collector.recordResolvedRoot("react", firstRoot)
	if got := collector.roots["react"]; got != firstRoot {
		t.Fatalf("unexpected first dependency root: %q", got)
	}
	if _, ok := collector.multiRoot["react"]; ok {
		t.Fatalf("did not expect duplicate root to mark multi-root")
	}

	collector.recordResolvedRoot("react", secondRoot)
	if _, ok := collector.multiRoot["react"]; !ok {
		t.Fatalf("expected differing roots to mark react as multi-root")
	}
}

func TestResolveDependencyRootAtDirAndIsPathWithin(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	installWorkspaceCatalogDependencies(t, repo, "react")

	root, ok := resolveDependencyRootAtDir(repo, "react")
	if !ok || root != filepath.Join(repo, "node_modules", "react") {
		t.Fatalf("unexpected resolved root: root=%q ok=%v", root, ok)
	}

	badRoot := filepath.Join(repo, "node_modules", "bad", testPackageJSONName)
	if err := os.MkdirAll(badRoot, 0o755); err != nil {
		t.Fatalf("mkdir bad root: %v", err)
	}
	if _, ok := resolveDependencyRootAtDir(repo, "bad"); ok {
		t.Fatalf("expected directory package.json to be rejected")
	}

	if !isPathWithin(filepath.Join(repo, "packages", "web"), repo) {
		t.Fatalf("expected descendant path to be within repo")
	}
	if isPathWithin(filepath.Dir(repo), repo) {
		t.Fatalf("expected parent path to be outside repo")
	}
	if isPathWithin("packages/web", repo) {
		t.Fatalf("expected relative path comparison against absolute root to fail")
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

	catalog := loadWorkspaceDependencyCatalog("")
	if len(catalog.declarations) != 0 || len(catalog.warnings) != 0 {
		t.Fatalf("expected empty catalog for blank repo path, got %#v", catalog)
	}
}

func TestLoadWorkspaceDependencyCatalogKeepsWarningsWithoutWorkspaceSignals(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testPackageJSONName), "{")
	testutil.MustWriteFile(t, filepath.Join(repo, jsYarnRCFile), "catalog: [\n")

	catalog := loadWorkspaceDependencyCatalog(repo)
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

	catalog := loadWorkspaceDependencyCatalog(repo)
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

	_, found, warning := readWorkspacePackageJSON(repo, "")
	if found || warning != "" {
		t.Fatalf("expected blank manifest path to be ignored without warnings, found=%v warning=%q", found, warning)
	}

	invalidPath := string([]byte{0})
	_, found, warning = readWorkspacePackageJSON(repo, invalidPath)
	if found {
		t.Fatalf("expected invalid manifest path to be rejected")
	}
	if !strings.Contains(warning, "unable to read workspace manifest") {
		t.Fatalf("expected invalid manifest warning, got %q", warning)
	}

	_, found, warning = readPnpmWorkspaceManifest(invalidPath)
	if found {
		t.Fatalf("expected invalid pnpm repo path to be rejected")
	}
	if !strings.Contains(warning, "unable to read "+jsPnpmWorkspaceFile) {
		t.Fatalf("expected invalid pnpm path warning, got %q", warning)
	}

	_, found, warning = readYarnCatalogManifest(invalidPath)
	if found {
		t.Fatalf("expected invalid yarn repo path to be rejected")
	}
	if !strings.Contains(warning, "unable to read "+jsYarnRCFile) {
		t.Fatalf("expected invalid yarn path warning, got %q", warning)
	}

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

func writeWorkspaceCatalogFiles(t *testing.T, repo string, files map[string]string) {
	t.Helper()

	for relativePath, content := range files {
		testutil.MustWriteFile(t, filepath.Join(repo, relativePath), content)
	}
}

func installWorkspaceCatalogDependencies(t *testing.T, repo string, names ...string) {
	t.Helper()

	for _, name := range names {
		if err := writeDependency(repo, name, testModuleExportsStub); err != nil {
			t.Fatalf("write dependency %s: %v", name, err)
		}
	}
}

func assertWorkspaceCatalogDependencies(t *testing.T, deps []string, expected []string, present bool) {
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
