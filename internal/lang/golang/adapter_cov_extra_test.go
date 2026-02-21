package golang

import (
	"context"
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

const coverageDepPath = "github.com/x/y"

func TestGoAdapterCoverageHelpers(t *testing.T) {
	assertGoFinalizeDetection(t)
	assertGoScanWarnings(t)
	assertGoImportBindingHelpers(t)
	assertGoTagVersionHelpers(t)
	assertGoModHelpers(t)
	assertGoWorkAndPathHelpers(t)
}

func TestGoAdapterCoverageHelpersMore(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	adapter := NewAdapter()
	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || len(detection.Roots) == 0 {
		t.Fatalf("expected matched detection with root fallback, got %#v", detection)
	}

	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail for invalid repo path")
	}

	scan := scanResult{
		Files: []fileScan{{
			Imports: []importBinding{{Dependency: coverageDepPath, Module: coverageDepPath, Name: "*", Local: "y", Wildcard: true}},
			Usage:   map[string]int{"*": 1},
		}},
	}
	depReport, _ := buildDependencyReport(coverageDepPath, scan)
	if len(depReport.RiskCues) == 0 {
		t.Fatalf("expected wildcard import risk cue")
	}

	processGoModLine("replace (", &goModParseState{
		replaceSet: make(map[string]string),
	})
	if parseGoModReplaceBlockControl("replace (", &goModParseState{}) != true {
		t.Fatalf("expected replace block control to start block")
	}

	if got := inferDependency("example.com/x"); got != "example.com/x" {
		t.Fatalf("expected short external path inference, got %q", got)
	}

	if deps, warnings := buildRequestedGoDependencies(language.Request{TopN: 1}, scan); len(warnings) != 0 || len(deps) == 0 {
		t.Fatalf("expected top dependency reports, deps=%#v warnings=%#v", deps, warnings)
	}

	if _, err := loadGoModuleInfo("\x00"); err == nil {
		t.Fatalf("expected loadGoModuleInfo to fail on invalid path")
	}

	weights := report.RemovalCandidateWeights{Usage: 1, Impact: 0, Confidence: 0}
	if deps, warnings := buildTopGoDependencies(1, scan, weights); len(warnings) != 0 || len(deps) == 0 {
		t.Fatalf("expected buildTopGoDependencies to return reports, deps=%#v warnings=%#v", deps, warnings)
	}
}

func assertGoFinalizeDetection(t *testing.T) {
	t.Helper()
	detection := shared.FinalizeDetection("repo", language.Detection{Matched: true, Confidence: 1}, map[string]struct{}{})
	if detection.Confidence != 35 {
		t.Fatalf("expected confidence floor when matched, got %d", detection.Confidence)
	}
	if len(detection.Roots) == 0 || detection.Roots[0] != "repo" {
		t.Fatalf("expected fallback root assignment, got %#v", detection.Roots)
	}
	detection = shared.FinalizeDetection("repo", language.Detection{Matched: true, Confidence: 120}, map[string]struct{}{})
	if detection.Confidence != 95 {
		t.Fatalf("expected confidence cap, got %d", detection.Confidence)
	}
}

func assertGoScanWarnings(t *testing.T) {
	t.Helper()
	result := &scanResult{
		SkippedLargeFiles:       1,
		SkippedNestedModuleDirs: 1,
	}
	appendScanWarnings(result, moduleInfo{})
	if len(result.Warnings) == 0 {
		t.Fatalf("expected warnings from appendScanWarnings")
	}
}

func assertGoImportBindingHelpers(t *testing.T) {
	t.Helper()
	if dependencyWarnings("dep", true) != nil {
		t.Fatalf("expected no dependency warnings when imports exist")
	}
	name, local, wildcard := importBindingIdentity("example.com/x/y", &ast.Ident{Name: "   "})
	if wildcard || name != "y" || local != "y" {
		t.Fatalf("expected blank alias to fall back to inferred name, got name=%q local=%q wildcard=%v", name, local, wildcard)
	}
}

func assertGoTagVersionHelpers(t *testing.T) {
	t.Helper()
	if isActiveBuildTag("") {
		t.Fatalf("expected empty build tag to be inactive")
	}
	if isSupportedGoReleaseTag("not-a-go-tag") {
		t.Fatalf("expected non-go release tag to be unsupported")
	}
	if _, ok := goVersionMinor("go2.1"); ok {
		t.Fatalf("expected non-go1 version to be rejected")
	}
	if _, ok := leadingInt(""); ok {
		t.Fatalf("expected empty leadingInt input to fail")
	}
	if isVersionSuffix("v") {
		t.Fatalf("expected short version suffix to fail")
	}
	if isVersionSuffix("v1x") {
		t.Fatalf("expected non-numeric version suffix to fail")
	}
}

func assertGoModHelpers(t *testing.T) {
	t.Helper()
	if parseGoModBlockControl("x", "require (", nil) {
		t.Fatalf("expected nil inBlock pointer to return false")
	}
	replaceSet := map[string]string{}
	addGoModReplacement("example.com/old => local/module", replaceSet)
	if len(replaceSet) != 0 {
		t.Fatalf("expected non-external replacement target to be ignored")
	}
}

func assertGoWorkAndPathHelpers(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	goWork := "go 1.26\nuse ../outside\n"
	if err := os.WriteFile(filepath.Join(repo, goWorkName), []byte(goWork), 0o600); err != nil {
		t.Fatalf("write go.work: %v", err)
	}
	mods, err := loadGoWorkLocalModules(repo)
	if err != nil {
		t.Fatalf("loadGoWorkLocalModules: %v", err)
	}
	if len(mods) != 0 {
		t.Fatalf("expected out-of-repo go.work entries to be ignored")
	}

	if _, ok := resolveRepoBoundedPath(repo, ""); ok {
		t.Fatalf("expected empty resolveRepoBoundedPath input to fail")
	}
	if _, ok := resolveRepoBoundedPath(repo, "../outside"); ok {
		t.Fatalf("expected resolveRepoBoundedPath to reject escaping path")
	}
}
