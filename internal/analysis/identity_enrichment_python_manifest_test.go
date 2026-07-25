package analysis

import (
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestPythonIdentityUsesExactPyprojectAndPipfilePins(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, pythonProjectFileName), `[project]
dependencies = [
  "Requests[security] == 2.32.3 ; python_version >= '3.10'",
  "parenthesized (== 1.7.0)",
  "ranged >= 1.0",
  "wildcard == 2.*",
  "direct @ https://example.com/direct.whl",
]

[dependency-groups]
dev = ["group-package==1.1.0"]

[tool.uv]
dev-dependencies = ["uv-package==0.5.0"]

[tool.poetry.dependencies]
python = "^3.12"
poetry-package = "1.4.0"
poetry-table = { version = "==2.0.0" }
poetry-optional = { version = "3.0.0", optional = true }
poetry-git = { version = "4.0.0", git = "https://example.com/repo.git" }
poetry-range = "^5.0.0"

[tool.poetry.dev-dependencies]
poetry-dev = "==6.0.0"

[tool.poetry.group.test]
optional = false

[tool.poetry.group.test.dependencies]
poetry-group = "7.0.0"

[tool.poetry.group.optional]
optional = true

[tool.poetry.group.optional.dependencies]
poetry-skipped = "8.0.0"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, pythonPipfileName), `[packages]
Flask = "==3.0.0"
httpx = { version = "==0.27.0", extras = ["http2"] }
pip-range = ">=1.0"
pip-wildcard = "*"
pip-path = { version = "==2.0.0", path = "." }

[dev-packages]
pip-test = "==8.3.0"
`)

	exactPins := map[string]struct {
		version string
		source  string
	}{
		"requests":       {version: "2.32.3", source: pythonProjectFileName},
		"parenthesized":  {version: "1.7.0", source: pythonProjectFileName},
		"group-package":  {version: "1.1.0", source: pythonProjectFileName},
		"uv-package":     {version: "0.5.0", source: pythonProjectFileName},
		"poetry-package": {version: "1.4.0", source: pythonProjectFileName},
		"poetry-table":   {version: "2.0.0", source: pythonProjectFileName},
		"poetry-dev":     {version: "6.0.0", source: pythonProjectFileName},
		"poetry-group":   {version: "7.0.0", source: pythonProjectFileName},
		"flask":          {version: "3.0.0", source: pythonPipfileName},
		"httpx":          {version: "0.27.0", source: pythonPipfileName},
		"pip-test":       {version: "8.3.0", source: pythonPipfileName},
	}
	unsupported := []string{"ranged", "wildcard", "direct", "poetry-optional", "poetry-git", "poetry-range", "poetry-skipped", "pip-range", "pip-wildcard", "pip-path"}
	reportData := report.Report{Dependencies: make([]report.DependencyReport, 0, len(exactPins)+len(unsupported))}
	for name := range exactPins {
		reportData.Dependencies = append(reportData.Dependencies, report.DependencyReport{Language: "python", Name: name})
	}
	for _, name := range unsupported {
		reportData.Dependencies = append(reportData.Dependencies, report.DependencyReport{Language: "python", Name: name})
	}

	annotateDependencyIdentities(repoPath, &reportData)

	for name, want := range exactPins {
		assertIdentity(t, findIdentityDependency(t, reportData, "python", name), report.DependencyIdentity{
			Ecosystem: "pypi", Name: name, Version: want.version, VersionStatus: identityStatusDeclared,
			PURL: "pkg:pypi/" + name + "@" + want.version, PURLStatus: identityStatusResolved, Source: want.source, Confidence: "high",
		})
	}
	for _, name := range unsupported {
		assertIdentity(t, findIdentityDependency(t, reportData, "python", name), report.DependencyIdentity{
			Ecosystem: "pypi", Name: name, VersionStatus: identityStatusUnknown,
			PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
		})
	}
}

func TestPythonManifestEvidenceDoesNotOverrideLockEvidence(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, pythonPipfileName), "[packages]\nrequests = \"==2.32.3\"\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Pipfile.lock"), `{"default":{"requests":{"version":"==2.32.3"}}}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "python", "requests"), report.DependencyIdentity{
		Ecosystem: "pypi", Name: "requests", Version: "2.32.3", VersionStatus: identityStatusResolved,
		PURL: "pkg:pypi/requests@2.32.3", PURLStatus: identityStatusResolved, Source: "Pipfile.lock", Confidence: "high",
	})
}

func TestPythonManifestDiscoveryMatchesAdapterDirectories(t *testing.T) {
	repoPath := t.TempDir()
	for _, dir := range []string{".git", ".idea", "node_modules", "dist", "build", "vendor", "__pycache__", ".venv", "venv", ".mypy_cache", ".pytest_cache"} {
		testutil.MustWriteFile(t, filepath.Join(repoPath, dir, pythonProjectFileName), "[project]\ndependencies = [\"ignored-package==9.9.9\"]\n")
	}
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", "nested", pythonProjectFileName), "[project]\ndependencies = [\"target-package==1.2.3\"]\n")
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "python", Name: "ignored-package"},
		{Language: "python", Name: "target-package"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "python", "ignored-package"), report.DependencyIdentity{
		Ecosystem: "pypi", Name: "ignored-package", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "python", "target-package"), report.DependencyIdentity{
		Ecosystem: "pypi", Name: "target-package", Version: "1.2.3", VersionStatus: identityStatusDeclared,
		PURL: "pkg:pypi/target-package@1.2.3", PURLStatus: identityStatusResolved,
		Source: "target/nested/pyproject.toml", Confidence: "high",
	})
}

func TestPythonManifestDiscoveryIsGatedByReportLanguage(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", pythonProjectFileName), "[project")
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "example.com/module"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected a non-Python report to ignore Python manifests, got %#v", reportData.Warnings)
	}
}

func TestPythonManifestEvidenceWarnsOnMalformedTOML(t *testing.T) {
	repoPath := t.TempDir()
	warnings := newIdentityWarningCollector(repoPath)
	for name, contents := range map[string]string{pythonProjectFileName: "[project", pythonPipfileName: "[packages"} {
		path := filepath.Join(repoPath, name)
		testutil.MustWriteFile(t, path, contents)
		if document, ok := readPythonManifestDocument(repoPath, path, warnings); ok || document != nil {
			t.Fatalf("expected malformed %s to produce no document, got %#v", name, document)
		}
	}
	if document, ok := readPythonManifestDocument(repoPath, filepath.Join(repoPath, "missing", pythonPipfileName), warnings); ok || document != nil {
		t.Fatalf("expected missing Pipfile to produce no document, got %#v", document)
	}

	assertWarningsExact(t, repoPath, warnings.list(), []string{
		"identity manifest parse failed for Pipfile: invalid TOML",
		"identity manifest parse failed for pyproject.toml: invalid TOML",
		"identity manifest read failed for missing/Pipfile: not found",
	})
}

func TestExactPythonVersionSpecRejectsRangesAndWildcards(t *testing.T) {
	for _, spec := range []string{"", "*", "==", "===1.0", "==1.*", "==1..2", "==1.0+bad..local", "==1.0,!=1.1", ">=1.0", "~=1.0", "^1.0", "==https://example.com/a.whl"} {
		if version, ok := exactPythonVersionSpec(spec, false); ok || version != "" {
			t.Fatalf("expected %q not to be an exact Python pin, got %q, %t", spec, version, ok)
		}
	}
	for _, test := range []struct {
		spec string
		want string
	}{
		{spec: "==1.2.3", want: "1.2.3"},
		{spec: " == 1!2.0rc1+local ", want: "1!2.0rc1+local"},
		{spec: "==1.0.post1.dev2", want: "1.0.post1.dev2"},
		{spec: "1.2.3", want: "1.2.3"},
	} {
		if version, ok := exactPythonVersionSpec(test.spec, test.spec == "1.2.3"); !ok || version != test.want {
			t.Fatalf("unexpected exact Python pin for %q: %q, %t", test.spec, version, ok)
		}
	}
}
