package python

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	expectedDependencyInSetFmt = "expected dependency %q in %#v"
	expectedNoDependenciesFmt  = "expected no dependencies, got %#v"
	expectedWarningFmt         = "expected warning containing %q, got %#v"
	collectDirectoryErrFmt     = "collect directory declared dependencies: %v"
	packagingTestDirMode       = 0o700
	packagingBlockedDirMode    = 0o000
)

func TestParsePyprojectDependenciesModernSections(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), `
[project]
name = "demo"
dependencies = ["Requests>=2", "zope.interface>=6"]

[project.optional-dependencies]
docs = ["mkdocs>=1"]

[dependency-groups]
dev = ["pytest>=8", "ruff"]

[tool.uv]
dev-dependencies = ["mypy>=1.0"]
`)

	dependencies, warnings, err := parsePyprojectDependencies(repo, filepath.Join(repo, pythonPyprojectFile))
	if err != nil {
		t.Fatalf("parse pyproject dependencies: %v", err)
	}

	for _, want := range []string{"requests", "zope-interface", "pytest", "ruff", "mypy"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf(expectedDependencyInSetFmt, want, dependencies)
		}
	}
	joinedWarnings := strings.Join(warnings, "\n")
	if !strings.Contains(joinedWarnings, "project.optional-dependencies") {
		t.Fatalf("expected optional dependency warning, got %#v", warnings)
	}
}

func TestParsePipfileDependenciesAndLock(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPipfileName), `
[packages]
Requests = ">=2"

[dev-packages]
pytest = "*"
`)
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPipfileLockName), `{
  "_meta": {"hash": {"sha256": "x"}},
  "default": {"requests": {"version": "==2.32.0"}},
  "develop": {"pytest": {"version": "==8.4.0"}}
}`)

	dependencies, warnings, err := parsePipfileDependencies(repo, filepath.Join(repo, pythonPipfileName))
	if err != nil {
		t.Fatalf("parse Pipfile dependencies: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no Pipfile warnings, got %#v", warnings)
	}
	for _, want := range []string{"requests", "pytest"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf(expectedDependencyInSetFmt, want, dependencies)
		}
	}

	lockDependencies, lockWarnings, err := parsePipfileLockDependencies(repo, filepath.Join(repo, pythonPipfileLockName))
	if err != nil {
		t.Fatalf("parse Pipfile.lock dependencies: %v", err)
	}
	if len(lockWarnings) != 0 {
		t.Fatalf("expected no Pipfile.lock warnings, got %#v", lockWarnings)
	}
	for _, want := range []string{"requests", "pytest"} {
		if _, ok := lockDependencies[want]; !ok {
			t.Fatalf(expectedDependencyInSetFmt, want, lockDependencies)
		}
	}
}

func TestParseRequirementsDependencies(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	testutil.MustWriteFile(t, path, `
# comment
requests==2.32.0
urllib3>=2.2.3

`)

	dependencies, warnings, err := parseRequirementsDependencies(repo, path)
	if err != nil {
		t.Fatalf("parse requirements dependencies: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no requirements warnings, got %#v", warnings)
	}
	for _, want := range []string{"requests", "urllib3"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf(expectedDependencyInSetFmt, want, dependencies)
		}
	}
}

func TestParseRequirementsDependenciesWarnsForUnsupportedOptionLines(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	testutil.MustWriteFile(t, path, `
-r base.txt
-c constraints.txt
-e git+https://example.test/demo.git#egg=demo
requests==2.32.0
`)

	dependencies, warnings, err := parseRequirementsDependencies(repo, path)
	if err != nil {
		t.Fatalf("parse requirements dependencies: %v", err)
	}
	if _, ok := dependencies["requests"]; !ok {
		t.Fatalf(expectedDependencyInSetFmt, "requests", dependencies)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one unsupported-format warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "skipped 3 requirements entries with unsupported format") {
		t.Fatalf(expectedWarningFmt, "skipped 3 requirements entries with unsupported format", warnings)
	}
}

func TestParseRequirementsDependenciesAcceptsLongLines(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	longRequirement := "requests==2.32.0" + strings.Repeat(" --hash=sha256:abcdef0123456789", 3000)
	testutil.MustWriteFile(t, path, longRequirement+"\n")

	dependencies, warnings, err := parseRequirementsDependencies(repo, path)
	if err != nil {
		t.Fatalf("parse requirements dependencies: %v", err)
	}
	if _, ok := dependencies["requests"]; !ok {
		t.Fatalf(expectedDependencyInSetFmt, "requests", dependencies)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
}

func TestCollectDirectoryDeclaredDependenciesFromRequirementsTxt(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonRequirementsTxt), `
requests==2.32.0
urllib3>=2.2.3
`)

	dependencies, warnings, err := collectDirectoryDeclaredDependencies(repo, repo)
	if err != nil {
		t.Fatalf(collectDirectoryErrFmt, err)
	}
	for _, want := range []string{"requests", "urllib3"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf(expectedDependencyInSetFmt, want, dependencies)
		}
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
}

func TestPythonAnalyseTopNIncludesPoetryDependencies(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), `
[tool.poetry]
name = "demo"
version = "0.1.0"

[tool.poetry.dependencies]
python = "^3.12"
requests = "^2.0"
numpy = { version = "^2.0", optional = true }

[tool.poetry.dev-dependencies]
pytest = "^8.0"

[tool.poetry.group.docs]
optional = true

[tool.poetry.group.docs.dependencies]
mkdocs = "^1.0"
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "main.py"), "import requests\nrequests.get('https://example.test')\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse Poetry repo: %v", err)
	}

	names := dependencyNames(reportData)
	for _, want := range []string{"requests", "pytest"} {
		if !slices.Contains(names, want) {
			t.Fatalf(expectedDependencyInSetFmt, want, names)
		}
	}
	for _, unexpected := range []string{"numpy", "mkdocs"} {
		if slices.Contains(names, unexpected) {
			t.Fatalf("did not expect optional Poetry dependency %q in %#v", unexpected, names)
		}
	}

	joinedWarnings := strings.Join(reportData.Warnings, "\n")
	for _, want := range []string{"optional Poetry dependency", "optional Poetry groups"} {
		if !strings.Contains(joinedWarnings, want) {
			t.Fatalf(expectedWarningFmt, want, reportData.Warnings)
		}
	}
}

func TestPythonAnalyseUsesUVLockFallbackWhenManifestDeclarationsMissing(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), `
[project]
name = "demo"
dynamic = ["dependencies"]

[project.optional-dependencies]
docs = ["mkdocs>=1"]

[tool.uv]
`)
	testutil.MustWriteFile(t, filepath.Join(repo, pythonUVLockName), `
version = 1

[[package]]
name = "requests"
version = "2.32.3"

[[package]]
name = "urllib3"
version = "2.2.1"
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse uv repo: %v", err)
	}

	names := dependencyNames(reportData)
	for _, want := range []string{"requests", "urllib3"} {
		if !slices.Contains(names, want) {
			t.Fatalf(expectedDependencyInSetFmt, want, names)
		}
	}

	joinedWarnings := strings.Join(reportData.Warnings, "\n")
	for _, want := range []string{"project.optional-dependencies", "using uv.lock package entries as a fallback"} {
		if !strings.Contains(joinedWarnings, want) {
			t.Fatalf(expectedWarningFmt, want, reportData.Warnings)
		}
	}
}

func TestCollectDirectoryDeclaredDependenciesPrefersManifest(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), `
[project]
dependencies = ["Requests>=2"]
`)
	testutil.MustWriteFile(t, filepath.Join(repo, pythonUVLockName), `
version = 1

[[package]]
name = "urllib3"
version = "2.2.1"
`)

	dependencies, warnings, err := collectDirectoryDeclaredDependencies(repo, repo)
	if err != nil {
		t.Fatalf(collectDirectoryErrFmt, err)
	}
	if _, ok := dependencies["requests"]; !ok {
		t.Fatalf(expectedDependencyInSetFmt, "requests", dependencies)
	}
	if _, ok := dependencies["urllib3"]; ok {
		t.Fatalf("did not expect lockfile fallback dependency in %#v", dependencies)
	}
	if strings.Contains(strings.Join(warnings, "\n"), "fallback") {
		t.Fatalf("did not expect fallback warning, got %#v", warnings)
	}
}

func TestCollectDirectoryDeclaredDependenciesUsesLockFallback(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPoetryLockName), `
[[package]]
name = "Requests"
version = "2.32.3"

[[package]]
version = "0.1.0"

[[package]]
name = "!!!"
version = "0.1.0"

[[package]]
name = ""
version = "0.1.0"
`)

	dependencies, warnings, err := collectDirectoryDeclaredDependencies(repo, repo)
	if err != nil {
		t.Fatalf(collectDirectoryErrFmt, err)
	}
	if _, ok := dependencies["requests"]; !ok {
		t.Fatalf(expectedDependencyInSetFmt, "requests", dependencies)
	}

	joinedWarnings := strings.Join(warnings, "\n")
	for _, want := range []string{
		"using poetry.lock package entries as a fallback",
		"skipped 2 lockfile package entries with unsupported metadata",
	} {
		if !strings.Contains(joinedWarnings, want) {
			t.Fatalf(expectedWarningFmt, want, warnings)
		}
	}
}

func TestParsePackageLockDependenciesUnsupportedShape(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonUVLockName), `package = "invalid"`)

	dependencies, warnings, err := parsePackageLockDependencies(repo, filepath.Join(repo, pythonUVLockName))
	if err != nil {
		t.Fatalf("parse package lock dependencies: %v", err)
	}
	if len(dependencies) != 0 {
		t.Fatalf(expectedNoDependenciesFmt, dependencies)
	}
	assertWarningContains(t, warnings, "unsupported lockfile shape")
}

func TestParsePackageLockDependenciesWithoutEntries(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPoetryLockName)
	testutil.MustWriteFile(t, path, `version = "1.0"`)

	dependencies, warnings, err := parsePackageLockDependencies(repo, path)
	if err != nil {
		t.Fatalf("parse package lock without entries: %v", err)
	}
	if len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("expected empty result without package entries, got deps=%#v warnings=%#v", dependencies, warnings)
	}
}

func TestParsePipfileLockDependenciesMissing(t *testing.T) {
	repo := t.TempDir()
	assertMissingParseResult(t, repo, filepath.Join(repo, "missing.lock"), "Pipfile.lock", parsePipfileLockDependencies)
}

func TestParsePipfileDependenciesMissing(t *testing.T) {
	repo := t.TempDir()
	assertMissingParseResult(t, repo, filepath.Join(repo, "missing.Pipfile"), "Pipfile", parsePipfileDependencies)
}

func TestParsePipfileLockDependenciesInvalidJSON(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPipfileLockName)
	assertInvalidParseWarning(t, repo, path, `{`, "invalid Pipfile.lock", "JSON decode error", parsePipfileLockDependencies)
}

func TestParsePyprojectDependenciesInvalidTOML(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPyprojectFile)
	assertInvalidParseWarning(t, repo, path, `[project`, "invalid "+pythonPyprojectFile, "decode error", parsePyprojectDependencies)
}

func TestParsePackageLockDependenciesMissing(t *testing.T) {
	repo := t.TempDir()
	assertMissingParseResult(t, repo, filepath.Join(repo, "missing.lock"), "package lock", parsePackageLockDependencies)
}

func TestParsePipfileDependenciesInvalidTOML(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPipfileName)
	assertInvalidParseWarning(t, repo, path, `[packages`, "invalid Pipfile", "decode error", parsePipfileDependencies)
}

func TestReadOptionalTOMLDocumentOutsideRepoFails(t *testing.T) {
	repo := t.TempDir()

	_, _, err := readOptionalTOMLDocument(repo, filepath.Join(repo, "..", "outside.toml"))
	if err == nil {
		t.Fatal("expected repo boundary read error")
	}
}

func TestReadOptionalTOMLDocumentMissing(t *testing.T) {
	repo := t.TempDir()

	document, warnings, err := readOptionalTOMLDocument(repo, filepath.Join(repo, "missing.toml"))
	if err != nil {
		t.Fatalf("read missing TOML document: %v", err)
	}
	if document != nil || len(warnings) != 0 {
		t.Fatalf("expected nil result for missing TOML document, got document=%#v warnings=%#v", document, warnings)
	}
}

func TestParsePackageLockDependenciesNonMapEntries(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonUVLockName)
	testutil.MustWriteFile(t, path, `package = [1]`)

	dependencies, warnings, err := parsePackageLockDependencies(repo, path)
	if err != nil {
		t.Fatalf("parse package lock non-map entries: %v", err)
	}
	if len(dependencies) != 0 {
		t.Fatalf(expectedNoDependenciesFmt, dependencies)
	}
	assertWarningContains(t, warnings, "skipped 1 lockfile package entries with unsupported metadata")
}

func TestParsePyprojectDependenciesWarnOnPoetryExtras(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPyprojectFile)
	testutil.MustWriteFile(t, path, `
[tool.poetry]
name = "demo"

[tool.poetry.dependencies]
requests = "^2.0"

[tool.poetry.extras]
docs = ["mkdocs"]
`)

	dependencies, warnings, err := parsePyprojectDependencies(repo, path)
	if err != nil {
		t.Fatalf("parse pyproject with extras: %v", err)
	}
	if _, ok := dependencies["requests"]; !ok {
		t.Fatalf(expectedDependencyInSetFmt, "requests", dependencies)
	}
	assertWarningContains(t, warnings, "skipped Poetry extras")
}

func TestPackagingHelperWarningsAndUtilities(t *testing.T) {
	dependencies := make(map[string]struct{})
	warnings := make([]string, 0)
	poetryGroups := map[string]any{
		"dev": map[string]any{
			"dependencies": map[string]any{
				"python": "^3.12",
				"pytest": "^8.0",
				"mkdocs": map[string]any{"optional": true},
			},
		},
		"docs":   map[string]any{"optional": true},
		"broken": "oops",
		"lint":   map[string]any{"dependencies": []any{"ruff"}},
	}
	dependencyGroups := map[string]any{
		"dev":    []any{"ruff"},
		"broken": true,
	}

	addRequirementList(dependencies, []string{"requests>=2", "!bad"}, "requirements", &warnings)
	addRequirementList(dependencies, 42, "requirements", &warnings)
	addPoetryGroups(dependencies, poetryGroups, pythonPyprojectFile, &warnings)
	addDependencyGroups(dependencies, dependencyGroups, pythonPyprojectFile, &warnings)

	for _, want := range []string{"requests", "pytest", "ruff"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf(expectedDependencyInSetFmt, want, dependencies)
		}
	}
	if _, ok := dependencies["python"]; ok {
		t.Fatalf("did not expect interpreter marker in %#v", dependencies)
	}

	joinedWarnings := strings.Join(warnings, "\n")
	for _, want := range []string{
		"unsupported format",
		"optional Poetry dependency entries",
		"optional Poetry groups",
		"skipped Poetry groups with unsupported metadata",
		"skipped dependency groups with unsupported metadata",
	} {
		if !strings.Contains(joinedWarnings, want) {
			t.Fatalf(expectedWarningFmt, want, warnings)
		}
	}

	if _, ok := stringSlice([]any{"ok", 1}); ok {
		t.Fatal("expected mixed []any slice to be rejected")
	}
	repo := t.TempDir()
	if got := relativePackagingPath(repo, repo); got != "." {
		t.Fatalf("expected repo root path label '.', got %q", got)
	}
	if got := nestedMap(map[string]any{"tool": "nope"}, "tool", "poetry"); got != nil {
		t.Fatalf("expected nil nested map, got %#v", got)
	}
	addPoetryDependencyTable(dependencies, nil, pythonPyprojectFile+" [tool.poetry.dependencies]", &warnings)
	addPoetryGroups(dependencies, nil, pythonPyprojectFile, &warnings)

	if got := relativePackagingPath(repo, pythonPyprojectFile); got != pythonPyprojectFile {
		t.Fatalf("expected fallback packaging path label, got %q", got)
	}
}

func TestCollectDeclaredDependenciesHonorsCanceledContext(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), `
[project]
dependencies = ["requests>=2"]
`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := collectDeclaredDependencies(ctx, repo)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}
}

func TestCollectDirectoryDeclaredDependenciesWithoutPackagingFiles(t *testing.T) {
	repo := t.TempDir()

	dependencies, warnings, err := collectDirectoryDeclaredDependencies(repo, repo)
	if err != nil {
		t.Fatalf(collectDirectoryErrFmt, err)
	}
	if dependencies != nil || len(warnings) != 0 {
		t.Fatalf("expected empty result without packaging files, got deps=%#v warnings=%#v", dependencies, warnings)
	}

	if _, err := pythonPackagingFiles(filepath.Join(repo, "missing")); err == nil {
		t.Fatal("expected read error for missing packaging directory")
	}
}

func TestCollectDeclaredDependenciesSkipsAndPropagatesErrors(t *testing.T) {
	t.Run("skips ignored directories", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, ".venv", pythonPyprojectFile), `
[project]
dependencies = ["requests>=2"]
`)

		dependencies, warnings, err := collectDeclaredDependencies(context.Background(), repo)
		if err != nil {
			t.Fatalf("collect declared dependencies: %v", err)
		}
		if len(dependencies) != 0 || len(warnings) != 0 {
			t.Fatalf("expected skipped .venv contents, got deps=%#v warnings=%#v", dependencies, warnings)
		}
	})

	t.Run("propagates directory read errors", func(t *testing.T) {
		repo := t.TempDir()
		blockedDir := filepath.Join(repo, "blocked")
		if err := os.Mkdir(blockedDir, packagingTestDirMode); err != nil {
			t.Fatalf("mkdir blocked dir: %v", err)
		}
		defer func() {
			if err := os.Chmod(blockedDir, packagingTestDirMode); err != nil {
				t.Errorf("restore blocked dir permissions: %v", err)
			}
		}()
		if err := os.Chmod(blockedDir, packagingBlockedDirMode); err != nil {
			t.Fatalf("chmod blocked dir: %v", err)
		}

		if _, _, err := collectDeclaredDependencies(context.Background(), repo); err == nil {
			t.Fatal("expected collectDeclaredDependencies to propagate directory read error")
		}
	})
}

func TestCollectDirectoryDeclaredDependenciesManifestErrorsPropagate(t *testing.T) {
	assertCollectDirectoryReadError(t, pythonPyprojectFile, `
[project]
dependencies = ["requests>=2"]
`)
}

func TestCollectDirectoryDeclaredDependenciesLockFallbackErrorsPropagate(t *testing.T) {
	assertCollectDirectoryReadError(t, pythonPipfileLockName, `{"default":{"requests":{"version":"==2.32.0"}}}`)
}

func TestCollectDirectoryDeclaredDependenciesEmptyFallbackSkipsWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPoetryLockName), `version = "1.0"`)

	dependencies, warnings, err := collectDirectoryDeclaredDependencies(repo, repo)
	if err != nil {
		t.Fatalf(collectDirectoryErrFmt, err)
	}
	if len(dependencies) != 0 {
		t.Fatalf(expectedNoDependenciesFmt, dependencies)
	}
	if strings.Contains(strings.Join(warnings, "\n"), "fallback") {
		t.Fatalf("did not expect fallback warning, got %#v", warnings)
	}
}

func TestPythonPackagingFilesIgnoreChildDirectories(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), "[project]\nname='demo'\n")
	if err := os.Mkdir(filepath.Join(repo, "nested"), packagingTestDirMode); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	files, err := pythonPackagingFiles(repo)
	if err != nil {
		t.Fatalf("pythonPackagingFiles: %v", err)
	}
	if _, ok := files[pythonPyprojectFile]; !ok {
		t.Fatalf("expected %s in %#v", pythonPyprojectFile, files)
	}
	if _, ok := files["nested"]; ok {
		t.Fatalf("did not expect child directory in %#v", files)
	}
}

func TestPackagingCollectionPropagatesReadErrors(t *testing.T) {
	repo := t.TempDir()
	destination := make(map[string]struct{})
	warnings := make([]string, 0)

	if err := appendParsedDependencies(repo, filepath.Join(repo, "..", "outside.toml"), parsePyprojectDependencies, destination, &warnings); err == nil {
		t.Fatal("expected appendParsedDependencies to propagate read error")
	}

	files := map[string]struct{}{pythonPyprojectFile: {}}
	if _, _, err := collectManifestDependencies(repo, filepath.Join(repo, ".."), files); err == nil {
		t.Fatal("expected collectManifestDependencies to propagate read error")
	}

	lockFiles := map[string]struct{}{pythonPipfileLockName: {}}
	if _, _, err := collectLockFallbacks(repo, filepath.Join(repo, ".."), lockFiles); err == nil {
		t.Fatal("expected collectLockFallbacks to propagate read error")
	}
}

func assertWarningContains(t *testing.T, warnings []string, want string) {
	t.Helper()
	if !strings.Contains(strings.Join(warnings, "\n"), want) {
		t.Fatalf(expectedWarningFmt, want, warnings)
	}
}

func assertMissingParseResult(t *testing.T, repoPath, path, description string, parser dependencyParser) {
	t.Helper()
	dependencies, warnings, err := parser(repoPath, path)
	if err != nil {
		t.Fatalf("parse missing %s: %v", description, err)
	}
	if len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("expected empty result for missing %s, got deps=%#v warnings=%#v", description, dependencies, warnings)
	}
}

func assertInvalidParseWarning(t *testing.T, repoPath, path, content, description, warning string, parser dependencyParser) {
	t.Helper()
	testutil.MustWriteFile(t, path, content)

	dependencies, warnings, err := parser(repoPath, path)
	if err != nil {
		t.Fatalf("parse %s: %v", description, err)
	}
	if len(dependencies) != 0 {
		t.Fatalf(expectedNoDependenciesFmt, dependencies)
	}
	assertWarningContains(t, warnings, warning)
}

func assertCollectDirectoryReadError(t *testing.T, fileName, content string) {
	t.Helper()
	repo := t.TempDir()
	outsideDir := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outsideDir, fileName), content)

	if _, _, err := collectDirectoryDeclaredDependencies(repo, outsideDir); err == nil {
		t.Fatal("expected collect directory read error")
	}
}

func dependencyNames(reportData report.Report) []string {
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dependency := range reportData.Dependencies {
		names = append(names, dependency.Name)
	}
	return names
}
