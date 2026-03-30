package python

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestParsePyprojectDependenciesModernSections(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pyproject.toml"), `
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

	dependencies, warnings, err := parsePyprojectDependencies(repo, filepath.Join(repo, "pyproject.toml"))
	if err != nil {
		t.Fatalf("parse pyproject dependencies: %v", err)
	}

	for _, want := range []string{"requests", "zope-interface", "pytest", "ruff", "mypy"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf("expected dependency %q in %#v", want, dependencies)
		}
	}
	joinedWarnings := strings.Join(warnings, "\n")
	if !strings.Contains(joinedWarnings, "project.optional-dependencies") {
		t.Fatalf("expected optional dependency warning, got %#v", warnings)
	}
}

func TestParsePipfileDependenciesAndLock(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Pipfile"), `
[packages]
Requests = ">=2"

[dev-packages]
pytest = "*"
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Pipfile.lock"), `{
  "_meta": {"hash": {"sha256": "x"}},
  "default": {"requests": {"version": "==2.32.0"}},
  "develop": {"pytest": {"version": "==8.4.0"}}
}`)

	dependencies, warnings, err := parsePipfileDependencies(repo, filepath.Join(repo, "Pipfile"))
	if err != nil {
		t.Fatalf("parse Pipfile dependencies: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no Pipfile warnings, got %#v", warnings)
	}
	for _, want := range []string{"requests", "pytest"} {
		if _, ok := dependencies[want]; !ok {
			t.Fatalf("expected dependency %q in %#v", want, dependencies)
		}
	}

	lockDependencies, lockWarnings, err := parsePipfileLockDependencies(repo, filepath.Join(repo, "Pipfile.lock"))
	if err != nil {
		t.Fatalf("parse Pipfile.lock dependencies: %v", err)
	}
	if len(lockWarnings) != 0 {
		t.Fatalf("expected no Pipfile.lock warnings, got %#v", lockWarnings)
	}
	for _, want := range []string{"requests", "pytest"} {
		if _, ok := lockDependencies[want]; !ok {
			t.Fatalf("expected lock dependency %q in %#v", want, lockDependencies)
		}
	}
}

func TestPythonAnalyseTopNIncludesPoetryDependencies(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pyproject.toml"), `
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
			t.Fatalf("expected dependency %q in %#v", want, names)
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
			t.Fatalf("expected warning containing %q, got %#v", want, reportData.Warnings)
		}
	}
}

func TestPythonAnalyseUsesUVLockFallbackWhenManifestDeclarationsMissing(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pyproject.toml"), `
[project]
name = "demo"
dynamic = ["dependencies"]

[project.optional-dependencies]
docs = ["mkdocs>=1"]

[tool.uv]
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "uv.lock"), `
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
			t.Fatalf("expected dependency %q in %#v", want, names)
		}
	}

	joinedWarnings := strings.Join(reportData.Warnings, "\n")
	for _, want := range []string{"project.optional-dependencies", "using uv.lock package entries as a fallback"} {
		if !strings.Contains(joinedWarnings, want) {
			t.Fatalf("expected warning containing %q, got %#v", want, reportData.Warnings)
		}
	}
}

func dependencyNames(reportData report.Report) []string {
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dependency := range reportData.Dependencies {
		names = append(names, dependency.Name)
	}
	return names
}
