package js

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestListDependenciesIncludesPnpmWorkspaceCatalogDeclarations(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, jsPnpmWorkspaceFile), `
packages:
  - packages/*
catalog:
  react: ^18.3.1
catalogs:
  tooling:
    typescript: ^5.6.3
`)
	testutil.MustWriteFile(t, filepath.Join(repo, testPackageJSONName), `{"name":"root","private":true}`)
	testutil.MustWriteFile(t, filepath.Join(repo, "packages", "web", testPackageJSONName), `{
  "name": "web",
  "dependencies": {
    "react": "catalog:"
  },
  "devDependencies": {
    "typescript": "catalog:tooling"
  }
}`)
	if err := writeDependency(repo, "react", testModuleExportsStub); err != nil {
		t.Fatalf("write react dependency: %v", err)
	}
	if err := writeDependency(repo, "typescript", testModuleExportsStub); err != nil {
		t.Fatalf("write typescript dependency: %v", err)
	}

	deps, roots, warnings := listDependencies(repo, ScanResult{})
	for _, dependency := range []string{"react", "typescript"} {
		if !slices.Contains(deps, dependency) {
			t.Fatalf("expected dependency %q in workspace catalog list, got %#v", dependency, deps)
		}
		if got := roots[dependency]; got == "" {
			t.Fatalf("expected dependency root for %q, got %#v", dependency, roots)
		}
	}
	if strings.Contains(strings.Join(warnings, "\n"), "dependency not found in node_modules") {
		t.Fatalf("did not expect missing dependency warnings, got %#v", warnings)
	}
}

func TestListDependenciesIncludesYarnWorkspaceCatalogDeclarations(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, jsYarnRCFile), `
catalog:
  eslint: ^9.5.0
catalogs:
  react18:
    react: ^18.3.1
`)
	testutil.MustWriteFile(t, filepath.Join(repo, testPackageJSONName), `{
  "name": "root",
  "private": true,
  "packageManager": "yarn@4.10.0",
  "workspaces": ["packages/*"]
}`)
	testutil.MustWriteFile(t, filepath.Join(repo, "packages", "app", testPackageJSONName), `{
  "name": "app",
  "dependencies": {
    "react": "catalog:react18"
  }
}`)
	if err := writeDependency(repo, "eslint", testModuleExportsStub); err != nil {
		t.Fatalf("write eslint dependency: %v", err)
	}
	if err := writeDependency(repo, "react", testModuleExportsStub); err != nil {
		t.Fatalf("write react dependency: %v", err)
	}

	deps, roots, warnings := listDependencies(repo, ScanResult{})
	for _, dependency := range []string{"eslint", "react"} {
		if !slices.Contains(deps, dependency) {
			t.Fatalf("expected dependency %q in workspace catalog list, got %#v", dependency, deps)
		}
		if got := roots[dependency]; got != filepath.Join(repo, "node_modules", dependency) {
			t.Fatalf("unexpected dependency root for %q: %q", dependency, got)
		}
	}
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings, got %#v", warnings)
	}
}

func TestListDependenciesIgnoresRootManifestWhenNoWorkspaceSignals(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testPackageJSONName), `{
  "name": "single-package",
  "dependencies": {
    "lodash": "^4.17.21"
  }
}`)
	if err := writeDependency(repo, "lodash", testModuleExportsStub); err != nil {
		t.Fatalf("write lodash dependency: %v", err)
	}

	deps, roots, warnings := listDependencies(repo, ScanResult{})
	if len(deps) != 0 {
		t.Fatalf("expected no workspace-derived dependencies, got %#v", deps)
	}
	if len(roots) != 0 {
		t.Fatalf("expected no dependency roots, got %#v", roots)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
}
