package js

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

const (
	testIndexJS            = "index.js"
	testPackageJSONName    = "package.json"
	testAnalyseErrFmt      = "analyse: %v"
	testDetectErrFmt       = "detect: %v"
	testWriteSourceErrFmt  = "write source: %v"
	testWriteEntrypointFmt = "write entrypoint: %v"
	testExpectedOneDepFmt  = "expected 1 dependency report, got %d"
	testPackageJSONMain    = "{\n  \"main\": \"index.js\"\n}\n"
	testModuleExportsStub  = "module.exports = {}\n"
)

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	source := "import { map, filter as f } from \"lodash\"\nmap([1], (x) => x)\nf([1], Boolean)\n"
	path := filepath.Join(repo, testIndexJS)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	depDir := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	packageJSON := testPackageJSONMain
	if err := os.WriteFile(filepath.Join(depDir, testPackageJSONName), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	entrypoint := "export function map() {}\nexport function filter() {}\n"
	if err := os.WriteFile(filepath.Join(depDir, testIndexJS), []byte(entrypoint), 0o644); err != nil {
		t.Fatalf(testWriteEntrypointFmt, err)
	}

	adapter := NewAdapter()
	report, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(report.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDepFmt, len(report.Dependencies))
	}

	dep := report.Dependencies[0]
	if dep.UsedExportsCount != 2 {
		t.Fatalf("expected 2 used exports, got %d (imports=%v)", dep.UsedExportsCount, dep.UsedImports)
	}

	found := make(map[string]bool)
	for _, imp := range dep.UsedImports {
		if imp.Module == "lodash" {
			found[imp.Name] = true
		}
	}
	if !found["map"] || !found["filter"] {
		t.Fatalf("expected used imports to include map and filter")
	}
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	source := "import { used } from \"alpha\"\nimport { unused } from \"beta\"\nused()\n"
	path := filepath.Join(repo, testIndexJS)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := writeDependency(repo, "alpha", "export function used() {}\n"); err != nil {
		t.Fatalf("write alpha dependency: %v", err)
	}
	if err := writeDependency(repo, "beta", "export function unused() {}\n"); err != nil {
		t.Fatalf("write beta dependency: %v", err)
	}

	adapter := NewAdapter()
	report, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     1,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(report.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDepFmt, len(report.Dependencies))
	}
	if report.Dependencies[0].Name != "beta" {
		t.Fatalf("expected top dependency to be beta, got %q", report.Dependencies[0].Name)
	}
}

func TestAdapterDetectWithPackageJSON(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, testPackageJSONName), []byte("{\"name\":\"fixture\"}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf(testDetectErrFmt, err)
	}
	if !ok {
		t.Fatalf("expected detect=true when package.json exists")
	}
}

func TestAdapterDetectWithJSSource(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, testIndexJS), []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatalf("write index.js: %v", err)
	}

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf(testDetectErrFmt, err)
	}
	if !ok {
		t.Fatalf("expected detect=true when JS sources exist")
	}
}

func TestAdapterDetectNoJSSignals(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# no js\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf(testDetectErrFmt, err)
	}
	if ok {
		t.Fatalf("expected detect=false when no JS/TS signals exist")
	}
}

func TestAdapterAnalyseRiskCues(t *testing.T) {
	repo := t.TempDir()
	source := "import { run } from \"risky\"\nrun()\n"
	if err := os.WriteFile(filepath.Join(repo, testIndexJS), []byte(source), 0o644); err != nil {
		t.Fatalf(testWriteSourceErrFmt, err)
	}

	riskyRoot := filepath.Join(repo, "node_modules", "risky")
	if err := os.MkdirAll(riskyRoot, 0o755); err != nil {
		t.Fatalf("mkdir risky: %v", err)
	}
	riskyPkg := "{\n  \"main\": \"index.js\",\n  \"gypfile\": true,\n  \"dependencies\": {\"deep-a\":\"1.0.0\"}\n}\n"
	if err := os.WriteFile(filepath.Join(riskyRoot, testPackageJSONName), []byte(riskyPkg), 0o644); err != nil {
		t.Fatalf("write risky package: %v", err)
	}
	riskyEntry := "const target = process.env.DEP_NAME\nmodule.exports = require(target)\nexports.run = () => 1\n"
	if err := os.WriteFile(filepath.Join(riskyRoot, testIndexJS), []byte(riskyEntry), 0o644); err != nil {
		t.Fatalf("write risky entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(riskyRoot, "binding.gyp"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}

	mustWritePackage(t, filepath.Join(repo, "node_modules", "deep-a"), "{\n  \"name\":\"deep-a\",\n  \"dependencies\": {\"deep-b\":\"1.0.0\"}\n}\n")
	mustWritePackage(t, filepath.Join(repo, "node_modules", "deep-b"), "{\n  \"name\":\"deep-b\",\n  \"dependencies\": {\"deep-c\":\"1.0.0\"}\n}\n")
	mustWritePackage(t, filepath.Join(repo, "node_modules", "deep-c"), "{\n  \"name\":\"deep-c\"\n}\n")

	report, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "risky",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(report.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDepFmt, len(report.Dependencies))
	}

	codes := make([]string, 0, len(report.Dependencies[0].RiskCues))
	for _, cue := range report.Dependencies[0].RiskCues {
		codes = append(codes, cue.Code)
	}
	for _, expected := range []string{"dynamic-loader", "native-module", "deep-transitive-graph"} {
		if !slices.Contains(codes, expected) {
			t.Fatalf("expected risk cue %q, got %#v", expected, codes)
		}
	}
}

func TestAdapterAnalyseRecommendations(t *testing.T) {
	repo := t.TempDir()
	source := "import { map } from \"lodash\"\nmap([1], (x) => x)\n"
	if err := os.WriteFile(filepath.Join(repo, testIndexJS), []byte(source), 0o644); err != nil {
		t.Fatalf(testWriteSourceErrFmt, err)
	}

	lodashRoot := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(lodashRoot, 0o755); err != nil {
		t.Fatalf("mkdir lodash: %v", err)
	}
	pkg := testPackageJSONMain
	if err := os.WriteFile(filepath.Join(lodashRoot, testPackageJSONName), []byte(pkg), 0o644); err != nil {
		t.Fatalf("write package: %v", err)
	}
	entry := strings.Join([]string{
		"export function map() {}",
		"export function filter() {}",
		"export function reduce() {}",
		"export function chunk() {}",
		"export function uniq() {}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(lodashRoot, testIndexJS), []byte(entry), 0o644); err != nil {
		t.Fatalf(testWriteEntrypointFmt, err)
	}

	report, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	recs := report.Dependencies[0].Recommendations
	codes := make([]string, 0, len(recs))
	for _, rec := range recs {
		codes = append(codes, rec.Code)
	}
	if !slices.Contains(codes, "prefer-subpath-imports") {
		t.Fatalf("expected subpath recommendation, got %#v", codes)
	}
	if !slices.Contains(codes, "consider-replacement") {
		t.Fatalf("expected replacement recommendation, got %#v", codes)
	}
}

func TestAdapterAnalyseRecommendationsHonoursThreshold(t *testing.T) {
	repo := t.TempDir()
	source := "import { map } from \"lodash\"\nmap([1], (x) => x)\n"
	if err := os.WriteFile(filepath.Join(repo, testIndexJS), []byte(source), 0o644); err != nil {
		t.Fatalf(testWriteSourceErrFmt, err)
	}

	lodashRoot := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(lodashRoot, 0o755); err != nil {
		t.Fatalf("mkdir lodash: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lodashRoot, testPackageJSONName), []byte(testPackageJSONMain), 0o644); err != nil {
		t.Fatalf("write package: %v", err)
	}
	entry := strings.Join([]string{
		"export function map() {}",
		"export function filter() {}",
		"export function reduce() {}",
		"export function chunk() {}",
		"export function uniq() {}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(lodashRoot, testIndexJS), []byte(entry), 0o644); err != nil {
		t.Fatalf(testWriteEntrypointFmt, err)
	}

	minUsagePercent := 10
	report, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:                          repo,
		Dependency:                        "lodash",
		MinUsagePercentForRecommendations: &minUsagePercent,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}

	codes := make([]string, 0, len(report.Dependencies[0].Recommendations))
	for _, rec := range report.Dependencies[0].Recommendations {
		codes = append(codes, rec.Code)
	}
	if slices.Contains(codes, "prefer-subpath-imports") {
		t.Fatalf("did not expect subpath recommendation when threshold is reduced, got %#v", codes)
	}
}

func TestDependencyFromModuleSkipsNodeBuiltins(t *testing.T) {
	// Test node: prefix
	if dep := dependencyFromModule("node:fs"); dep != "" {
		t.Fatalf("expected empty dependency for node:fs builtin, got %q", dep)
	}
	// Test bare built-in names
	if dep := dependencyFromModule("fs"); dep != "" {
		t.Fatalf("expected empty dependency for fs builtin, got %q", dep)
	}
	if dep := dependencyFromModule("path"); dep != "" {
		t.Fatalf("expected empty dependency for path builtin, got %q", dep)
	}
	if dep := dependencyFromModule("http"); dep != "" {
		t.Fatalf("expected empty dependency for http builtin, got %q", dep)
	}
	// Test npm packages still work
	if dep := dependencyFromModule("lodash/map"); dep != "lodash" {
		t.Fatalf("expected lodash dependency, got %q", dep)
	}
}

func TestAdapterMetadata(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "js-ts" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	if !slices.Contains(aliases, "js") || !slices.Contains(aliases, "typescript") {
		t.Fatalf("unexpected aliases: %#v", aliases)
	}
}

func TestAdapterAnalyseNoTargetWarning(t *testing.T) {
	repo := t.TempDir()
	report, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("expected warning when neither dependency nor topN is provided")
	}
}

func TestAdapterDetectWalkErrorOnFileRepoPath(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("not-dir"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected walk error for non-directory repo path")
	}
}

func TestJSAdapterHelperBranches(t *testing.T) {
	if !shouldSkipDetectDir("node_modules") || shouldSkipDetectDir("src") {
		t.Fatalf("unexpected shouldSkipDetectDir behavior")
	}
	if !isJSExtension(".ts") || isJSExtension(".md") {
		t.Fatalf("unexpected isJSExtension behavior")
	}
	if !matchesDependency("lodash/map", "lodash") || matchesDependency("react", "lodash") {
		t.Fatalf("unexpected matchesDependency behavior")
	}

	surface := ExportSurface{Names: map[string]struct{}{"a": {}, "b": {}}, IncludesWildcard: true}
	if got := totalExportCount(surface); got != 0 {
		t.Fatalf("expected wildcard total export count 0, got %d", got)
	}
	if got := exportUsedPercent(surface, map[string]struct{}{"a": {}}, 0); got != 0 {
		t.Fatalf("expected used percent 0 with unknown total, got %f", got)
	}
	if resolveMinUsageRecommendationThreshold(nil) <= 0 {
		t.Fatalf("expected default min usage threshold")
	}
}

func TestListDependenciesMissingAndBuiltinFiltering(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, testIndexJS), []byte(""), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	scan := ScanResult{
		Files: []FileScan{
			{
				Path: testIndexJS,
				Imports: []ImportBinding{
					{Module: "left-pad", ExportName: "default", LocalName: "leftPad", Kind: ImportDefault},
					{Module: "node:fs", ExportName: "default", LocalName: "fs", Kind: ImportDefault},
				},
			},
		},
	}
	deps, roots, warnings := listDependencies(repo, scan)
	if len(deps) != 0 {
		t.Fatalf("expected no existing dependencies, got %#v", deps)
	}
	if len(roots) != 0 {
		t.Fatalf("expected no dependency roots, got %#v", roots)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "dependency not found") {
		t.Fatalf("expected missing dependency warning, got %#v", warnings)
	}
}

func TestListDependenciesNestedWorkspaceNodeModules(t *testing.T) {
	repo := t.TempDir()
	appDir := filepath.Join(repo, "apps", "api")
	srcFile := filepath.Join(appDir, "src", testIndexJS)
	if err := os.MkdirAll(filepath.Dir(srcFile), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(srcFile, []byte(""), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := writeDependency(appDir, "express", testModuleExportsStub); err != nil {
		t.Fatalf("write express dependency: %v", err)
	}

	scan := ScanResult{
		Files: []FileScan{
			{
				Path: filepath.Join("apps", "api", "src", testIndexJS),
				Imports: []ImportBinding{
					{Module: "express", ExportName: "default", LocalName: "express", Kind: ImportDefault},
				},
			},
		},
	}
	deps, roots, warnings := listDependencies(repo, scan)
	if len(warnings) != 0 {
		t.Fatalf("expected no missing dependency warning, got %#v", warnings)
	}
	if len(deps) != 1 || deps[0] != "express" {
		t.Fatalf("expected express dependency, got %#v", deps)
	}
	if got := roots["express"]; got != filepath.Join(appDir, "node_modules", "express") {
		t.Fatalf("unexpected resolved dependency root: %q", got)
	}
}

func TestListDependenciesWarnsWhenDependencyHasMultipleRoots(t *testing.T) {
	repo := t.TempDir()
	apiDir := filepath.Join(repo, "apps", "api")
	webDir := filepath.Join(repo, "apps", "web")

	if err := writeDependency(apiDir, "express", testModuleExportsStub); err != nil {
		t.Fatalf("write api express dependency: %v", err)
	}
	if err := writeDependency(webDir, "express", testModuleExportsStub); err != nil {
		t.Fatalf("write web express dependency: %v", err)
	}

	scan := ScanResult{
		Files: []FileScan{
			{
				Path: filepath.Join("apps", "api", "src", testIndexJS),
				Imports: []ImportBinding{
					{Module: "express", ExportName: "default", LocalName: "express", Kind: ImportDefault},
				},
			},
			{
				Path: filepath.Join("apps", "web", "src", testIndexJS),
				Imports: []ImportBinding{
					{Module: "express", ExportName: "default", LocalName: "express", Kind: ImportDefault},
				},
			},
		},
	}
	_, _, warnings := listDependencies(repo, scan)
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "dependency resolves to multiple node_modules roots: express") {
		t.Fatalf("expected multi-root warning, got %#v", warnings)
	}
}

func writeDependency(repo string, name string, entrypoint string) error {
	depDir := filepath.Join(repo, "node_modules", name)
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		return err
	}
	packageJSON := testPackageJSONMain
	if err := os.WriteFile(filepath.Join(depDir, testPackageJSONName), []byte(packageJSON), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(depDir, testIndexJS), []byte(entrypoint), 0o644); err != nil {
		return err
	}
	return nil
}

func mustWritePackage(t *testing.T, root string, pkgJSON string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, testPackageJSONName), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("write %s package.json: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, testIndexJS), []byte(testModuleExportsStub), 0o644); err != nil {
		t.Fatalf("write %s index.js: %v", root, err)
	}
}
