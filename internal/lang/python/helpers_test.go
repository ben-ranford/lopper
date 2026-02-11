package python

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestPythonImportParsingHelpers(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "localpkg", "__init__.py"), "# local module\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "single.py"), "# local module\n")

	bindings := parseImportLine("requests as req, localpkg", "a.py", repo, 0, "import requests as req, localpkg")
	if len(bindings) != 1 || bindings[0].Dependency != "requests" || bindings[0].Local != "req" {
		t.Fatalf("unexpected import line bindings: %#v", bindings)
	}

	fromBindings := parseFromImportLine("numpy", "array as arr, *", "b.py", repo, 2, "from numpy import array as arr, *")
	if len(fromBindings) != 2 {
		t.Fatalf("expected two from-import bindings, got %#v", fromBindings)
	}
	if !fromBindings[1].Wildcard {
		t.Fatalf("expected wildcard from-import binding")
	}
	if got := stripComment("import os # comment"); got != "import os " {
		t.Fatalf("unexpected strip comment result: %q", got)
	}
	if parts := splitCSV("a, b, , c"); !slices.Equal(parts, []string{"a", "b", "c"}) {
		t.Fatalf("unexpected splitCSV result: %#v", parts)
	}
	if name, local := parseImportPart("pkg.mod as pm"); name != "pkg.mod" || local != "pm" {
		t.Fatalf("unexpected parseImportPart alias result: name=%q local=%q", name, local)
	}
	if name, local := parseImportPart("value"); name != "value" || local != "" {
		t.Fatalf("unexpected parseImportPart result: name=%q local=%q", name, local)
	}
	if dependencyFromModule(repo, "os") != "" {
		t.Fatalf("expected stdlib import to be ignored")
	}
	if dependencyFromModule(repo, "localpkg.module") != "" {
		t.Fatalf("expected local package import to be ignored")
	}
	if dependencyFromModule(repo, "requests.sessions") != "requests" {
		t.Fatalf("expected third-party dependency normalization")
	}
	if !isLocalModule(repo, "localpkg") || !isLocalModule(repo, "single") {
		t.Fatalf("expected local module detection")
	}
	if isLocalModule(repo, "missing") {
		t.Fatalf("did not expect missing module to be local")
	}
}

func TestPythonDirectoryAndRecommendationsHelpers(t *testing.T) {
	if !shouldSkipDir(".venv") || shouldSkipDir("src") {
		t.Fatalf("unexpected shouldSkipDir behavior")
	}

	dep, warnings := buildDependencyReport("requests", scanResult{})
	if dep.Name != "requests" {
		t.Fatalf("unexpected dependency name: %q", dep.Name)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning when no imports are present")
	}

	recs := buildRecommendations(report.DependencyReport{
		Name:          "x",
		UsedImports:   nil,
		UnusedImports: []report.ImportUse{{Name: "*", Module: "x"}},
	})
	if len(recs) < 2 {
		t.Fatalf("expected removal and wildcard recommendations, got %#v", recs)
	}
}

func TestPythonRepoScanAndBoundaryBranches(t *testing.T) {
	if _, err := scanRepo(context.Background(), ""); err == nil {
		t.Fatalf("expected empty repo path error")
	}

	repo := t.TempDir()
	result, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan empty repo: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected warning for repo without python files")
	}

	clean, err := enforceRepoBoundary(repo, filepath.Join(repo, "a.py"))
	if err != nil {
		t.Fatalf("enforce boundary in-repo: %v", err)
	}
	if clean == "" {
		t.Fatalf("expected cleaned path")
	}
	if _, err := enforceRepoBoundary(repo, filepath.Join(filepath.Dir(repo), "outside.py")); err == nil {
		t.Fatalf("expected outside-repo boundary error")
	}
}

func TestPythonReadAndParseEdgeBranches(t *testing.T) {
	repo := t.TempDir()
	pyPath := filepath.Join(repo, "mod.py")
	testutil.MustWriteFile(t, pyPath, "import requests\n")

	content, rel, err := readPythonFile(repo, pyPath)
	if err != nil {
		t.Fatalf("read python file: %v", err)
	}
	if len(content) == 0 || rel != "mod.py" {
		t.Fatalf("unexpected read result content=%q rel=%q", string(content), rel)
	}

	if _, _, err := readPythonFile(repo, filepath.Join(repo, "missing.py")); err == nil {
		t.Fatalf("expected missing file read error")
	}

	if got := parseFromImportLine(".local", "name", "x.py", repo, 0, "from .local import name"); got != nil {
		t.Fatalf("expected relative from-import to be ignored, got %#v", got)
	}
	if got := parseFromImportLine("os", "path", "x.py", repo, 0, "from os import path"); got != nil {
		t.Fatalf("expected stdlib from-import to be ignored, got %#v", got)
	}
	if got := parseImportLine("  ", "x.py", repo, 0, "import "); len(got) != 0 {
		t.Fatalf("expected empty import line parse result, got %#v", got)
	}
	if name, local := parseImportPart(""); name != "" || local != "" {
		t.Fatalf("expected empty parseImportPart, got name=%q local=%q", name, local)
	}
}

func TestPythonRequestedDependencyBranches(t *testing.T) {
	deps, warnings := buildRequestedPythonDependencies(language.Request{}, scanResult{})
	if deps != nil {
		t.Fatalf("expected nil dependencies when no target is provided")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning when neither dependency nor topN is provided")
	}
}

func TestPythonDetectAndWalkBranches(t *testing.T) {
	adapter := NewAdapter()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	emptyWD := t.TempDir()
	if err := os.Chdir(emptyWD); err != nil {
		t.Fatalf("chdir emptyWD: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })
	if detection, err := adapter.DetectWithConfidence(context.Background(), ""); err != nil || detection.Matched {
		t.Fatalf("expected default repo '.' to be processed without match in empty cwd, detection=%#v err=%v", detection, err)
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pyproject.toml"), "[project]\nname='x'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "requirements.txt"), "requests\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "setup.py"), "from setuptools import setup\n")
	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence != 95 {
		t.Fatalf("expected matched detection capped at 95, got %#v", detection)
	}

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	var fileEntry fs.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			fileEntry = entry
			break
		}
	}
	if fileEntry == nil {
		t.Fatalf("expected file entry in repo")
	}

	visited := 1
	roots := make(map[string]struct{})
	detect := &language.Detection{}
	err = walkPythonDetectionEntry(filepath.Join(repo, fileEntry.Name()), fileEntry, roots, detect, &visited, 1)
	if !errors.Is(err, fs.SkipAll) {
		t.Fatalf("expected fs.SkipAll when maxFiles exceeded, got %v", err)
	}
}
