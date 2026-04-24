package python

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	testMainPy           = "main.py"
	testDirOwnerOnlyMode = 0o700
	testDirBlockedMode   = 0o000
)

func TestAdapterDetectWithPythonSource(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), "import requests\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected python detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %d", detection.Confidence)
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	source := "import requests\nfrom numpy import array, mean\narray([1])\nrequests.get('x')\n"
	dep := analysePythonDependency(t, source, "numpy")
	assertDependencyReport(t, dep, dependencyReportExpectation{language: "python", used: 1, total: 2})
}

func TestParseImportsHandlesParenthesizedFromImports(t *testing.T) {
	repo := t.TempDir()
	source := "from django.db import (models, migrations)\nfrom requests import (\n    Session,\n    Response as Resp,\n)\n"

	imports := parseImports([]byte(source), testMainPy, repo)
	if len(imports) != 4 {
		t.Fatalf("expected four parsed from-import bindings, got %#v", imports)
	}

	expected := []importBinding{
		{Dependency: "django", Module: "django.db", Name: "models", Local: "models"},
		{Dependency: "django", Module: "django.db", Name: "migrations", Local: "migrations"},
		{Dependency: "requests", Module: "requests", Name: "Session", Local: "Session"},
		{Dependency: "requests", Module: "requests", Name: "Response", Local: "Resp"},
	}
	for index, want := range expected {
		assertImportBinding(t, imports[index], want)
	}

	for _, imported := range imports {
		if strings.ContainsAny(imported.Name, "()") || strings.ContainsAny(imported.Local, "()") {
			t.Fatalf("expected sanitized symbol names without parentheses, got %#v", imported)
		}
	}
}

func assertImportBinding(t *testing.T, got importBinding, want importBinding) {
	t.Helper()
	if got.Dependency != want.Dependency || got.Module != want.Module || got.Name != want.Name || got.Local != want.Local {
		t.Fatalf("unexpected import binding: got %#v, want dependency=%q module=%q name=%q local=%q", got, want.Dependency, want.Module, want.Name, want.Local)
	}
}

func TestAdapterAnalyseDependencyWithParenthesizedFromImports(t *testing.T) {
	source := "from requests import (\n    Session,\n    Response,\n)\nSession()\n"
	dep := analysePythonDependency(t, source, "requests")
	assertDependencyReport(t, dep, dependencyReportExpectation{name: "requests", used: 1, total: 2})
}

type dependencyReportExpectation struct {
	name     string
	language string
	used     int
	total    int
}

func assertDependencyReport(t *testing.T, got report.DependencyReport, want dependencyReportExpectation) {
	t.Helper()
	if want.name != "" && got.Name != want.name {
		t.Fatalf("expected dependency report for %q, got %q", want.name, got.Name)
	}
	if want.language != "" && got.Language != want.language {
		t.Fatalf("expected language %q, got %q", want.language, got.Language)
	}
	if got.UsedExportsCount != want.used || got.TotalExportsCount != want.total {
		t.Fatalf("expected used/total %d/%d, got %d/%d", want.used, want.total, got.UsedExportsCount, got.TotalExportsCount)
	}
}

func analysePythonDependency(t *testing.T, source string, dependency string) report.DependencyReport {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), source)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	return reportData.Dependencies[0]
}

func TestAdapterAnalyseTopNRequiresRequirementsDependencies(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "requirements.txt"), "requests==2.32.0\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse requirements.txt repo: %v", err)
	}

	found := false
	for _, dep := range reportData.Dependencies {
		if dep.Name == "requests" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected declared dependency from requirements.txt in topN report, got %#v", dependencyNames(reportData))
	}
	for _, warning := range reportData.Warnings {
		if warning == "no dependency data available for top-N ranking" {
			t.Fatalf("unexpected top-N ranking warning: %#v", reportData.Warnings)
		}
	}
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	source := "import requests\nimport numpy as np\nnp.array([1])\n"
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), source)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     2,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 2 {
		t.Fatalf("expected two dependencies, got %d", len(reportData.Dependencies))
	}
	names := []string{reportData.Dependencies[0].Name, reportData.Dependencies[1].Name}
	for _, dependency := range []string{"numpy", "requests"} {
		if !slices.Contains(names, dependency) {
			t.Fatalf("expected dependency %q in %#v", dependency, names)
		}
	}
}

func TestAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "python" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if len(adapter.Aliases()) == 0 || adapter.Aliases()[0] != "py" {
		t.Fatalf("unexpected adapter aliases: %#v", adapter.Aliases())
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "requirements.txt"), "requests\n")
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true with requirements.txt")
	}

	pipenvRepo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(pipenvRepo, "Pipfile"), "[packages]\nrequests='*'\n")
	ok, err = adapter.Detect(context.Background(), pipenvRepo)
	if err != nil {
		t.Fatalf("detect Pipfile repo: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true with Pipfile")
	}
}

func TestNormalizeDependencyID(t *testing.T) {
	if got := normalizeDependencyID(" My_Package.Name "); got != "my-package-name" {
		t.Fatalf("unexpected normalized dependency ID: %q", got)
	}
}

func TestAnalyseRejectsInvalidRepoPath(t *testing.T) {
	_, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"})
	if err == nil {
		t.Fatal("expected invalid repo path error")
	}
}

func TestScanRepoRejectsEmptyPath(t *testing.T) {
	if _, err := scanRepo(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "repo path is empty") {
		t.Fatalf("expected empty repo path error, got %v", err)
	}
}

func TestScanRepoWarnsWithoutPythonFiles(t *testing.T) {
	result, err := scanRepo(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if !slices.Contains(result.Warnings, "no Python files found for analysis") {
		t.Fatalf("expected no-python-files warning, got %#v", result.Warnings)
	}
}

func TestImportParsersSkipLocalAndStdlibImports(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "localmod.py"), "")

	imports := parseImportLine(" , requests as rq, localmod, os", testMainPy, repo, 0, "import requests as rq, localmod, os")
	if len(imports) != 1 {
		t.Fatalf("expected only external import binding, got %#v", imports)
	}
	if imports[0].Dependency != "requests" || imports[0].Local != "rq" {
		t.Fatalf("unexpected import binding %#v", imports[0])
	}

	defaultLocal := parseImportLine("requests", testMainPy, repo, 0, "import requests")
	if len(defaultLocal) != 1 || defaultLocal[0].Local != "requests" {
		t.Fatalf("expected default local binding, got %#v", defaultLocal)
	}

	fromImports := parseFromImportLine("requests", ", Session as Sess, Session, *", testMainPy, repo, 0, "from requests import Session as Sess, Session, *")
	if len(fromImports) != 3 {
		t.Fatalf("expected three from-import bindings, got %#v", fromImports)
	}
	if fromImports[0].Local != "Sess" || fromImports[1].Local != "Session" || fromImports[1].Wildcard || !fromImports[2].Wildcard {
		t.Fatalf("unexpected from-import bindings %#v", fromImports)
	}

	if bindings := parseFromImportLine(".localmod", "Thing", testMainPy, repo, 0, "from .localmod import Thing"); len(bindings) != 0 {
		t.Fatalf("expected no bindings for relative import, got %#v", bindings)
	}
	if bindings := parseFromImportLine("localmod", "Thing", testMainPy, repo, 0, "from localmod import Thing"); len(bindings) != 0 {
		t.Fatalf("expected no bindings for local module import, got %#v", bindings)
	}

	if _, _, err := readPythonFile(repo, filepath.Join(repo, "missing.py")); err == nil {
		t.Fatal("expected read error for missing python file")
	}
}

func TestReadPythonFileFallsBackWhenRelativePathComputationFails(t *testing.T) {
	repoPath, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	content, relativePath, err := readPythonFile(repoPath, "adapter.go")
	if err != nil {
		t.Fatalf("read python file: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("expected adapter.go content")
	}
	if relativePath != "adapter.go" {
		t.Fatalf("expected fallback relative path, got %q", relativePath)
	}
}

func TestScanRepoCancelsDuringPythonWalk(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testMainPy), "import requests\n")

	if _, err := scanRepo(&countingContext{errAt: 3, err: context.Canceled}, repo); !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected scanRepo to stop on cancellation, got %v", err)
	}
}

func TestScanRepoPropagatesWalkCallbackErrors(t *testing.T) {
	repo := t.TempDir()
	blockedDir := filepath.Join(repo, "zz_noaccess")
	if err := os.Mkdir(blockedDir, testDirOwnerOnlyMode); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	defer func() {
		if err := os.Chmod(blockedDir, testDirOwnerOnlyMode); err != nil {
			t.Errorf("restore blocked dir permissions: %v", err)
		}
	}()

	ctx := &countingContext{
		errAt:  0,
		hookAt: 3,
		hook: func() {
			if err := os.Chmod(blockedDir, testDirBlockedMode); err != nil {
				t.Errorf("chmod blocked dir: %v", err)
			}
		},
	}
	if _, err := scanRepo(ctx, repo); err == nil {
		t.Fatal("expected walk callback error")
	}
}

type countingContext struct {
	count  int
	errAt  int
	err    error
	hookAt int
	hook   func()
}

func (c *countingContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *countingContext) Done() <-chan struct{} {
	return nil
}

func (c *countingContext) Err() error {
	c.count++
	if c.hook != nil && c.count == c.hookAt {
		c.hook()
	}
	if c.errAt > 0 && c.count >= c.errAt {
		return c.err
	}
	return nil
}

func (c *countingContext) Value(any) any {
	return nil
}
