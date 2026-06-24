package golang

import (
	"context"
	"go/ast"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
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
	if len(dependencyWarnings("dep", true)) != 0 {
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
	assertGoModReplacementIgnoresLocalTarget(t)
	assertParseGoModStandardFile(t)
	assertParseGoModInlineRequireBlock(t)
	assertParseGoModCommentedInlineRequireBlock(t)
	assertNormalizeInlineGoModRequireLineWithComment(t)
	assertNormalizeInlineGoModRequireLineEmpty(t)
	assertParseGoModMalformedInput(t)
}

func assertGoModReplacementIgnoresLocalTarget(t *testing.T) {
	t.Helper()
	replaceSet := map[string]string{}
	addGoModReplacement("example.com/old => local/module", replaceSet)
	if len(replaceSet) != 0 {
		t.Fatalf("expected non-external replacement target to be ignored")
	}
}

func assertParseGoModStandardFile(t *testing.T) {
	t.Helper()
	assertParsedGoMod(t, "module example.com/root\n\nrequire github.com/acme/dep v1.2.3\nreplace example.com/old => ./local/module\n", parsedGoModExpectation{
		modulePath:         "example.com/root",
		dependencies:       []string{"github.com/acme/dep"},
		replacements:       map[string]string{},
		moduleMessage:      "expected module path from modfile parse",
		dependencyMessage:  "expected dependency from modfile parse",
		replacementMessage: "expected local replacement target to be ignored",
	})
}

func assertParseGoModInlineRequireBlock(t *testing.T) {
	t.Helper()
	inlineBlockGoModLines := []string{
		"module example.com/root",
		"require ( github.com/acme/dep v1.2.3 )",
		"require github.com/acme/other v1.4.0",
		"replace example.com/old => github.com/fork/old v1.2.4",
		"",
	}
	assertParsedGoMod(t, strings.Join(inlineBlockGoModLines, "\n"), parsedGoModExpectation{
		modulePath:         "example.com/root",
		dependencies:       []string{"github.com/acme/dep", "github.com/acme/other"},
		replacements:       map[string]string{"github.com/fork/old": "example.com/old"},
		moduleMessage:      "expected inline require block module path",
		dependencyMessage:  "expected inline require block dependencies to survive",
		replacementMessage: "expected inline require block to preserve later replace directives",
	})
}

func assertParseGoModCommentedInlineRequireBlock(t *testing.T) {
	t.Helper()
	commentedInlineBlockGoModLines := []string{
		"module example.com/root",
		"require ( github.com/acme/dep v1.2.3 ) // keep comment",
		"",
	}
	assertParsedGoMod(t, strings.Join(commentedInlineBlockGoModLines, "\n"), parsedGoModExpectation{
		modulePath:         "example.com/root",
		dependencies:       []string{"github.com/acme/dep"},
		replacements:       map[string]string{},
		moduleMessage:      "expected commented inline require block module path",
		dependencyMessage:  "expected commented inline require block dependency",
		replacementMessage: "expected no replacements from commented inline require block",
	})
}

func assertNormalizeInlineGoModRequireLineWithComment(t *testing.T) {
	t.Helper()
	normalizedLine, ok := normalizeInlineGoModRequireLine("require ( github.com/acme/dep v1.2.3 ) // keep comment")
	if !ok || normalizedLine != "require github.com/acme/dep v1.2.3 // keep comment" {
		t.Fatalf("expected inline require line normalization with comment, got line=%q ok=%v", normalizedLine, ok)
	}
}

func assertNormalizeInlineGoModRequireLineEmpty(t *testing.T) {
	t.Helper()
	emptyLine, emptyOK := normalizeInlineGoModRequireLine("require ( )")
	if emptyOK || emptyLine != "require ( )" {
		t.Fatalf("expected empty inline require line to stay unchanged, got line=%q ok=%v", emptyLine, emptyOK)
	}
}

func assertParseGoModMalformedInput(t *testing.T) {
	t.Helper()
	assertParsedGoMod(t, "module example.com/root\nrequire (\n", parsedGoModExpectation{
		modulePath:         "",
		dependencies:       nil,
		replacements:       map[string]string{},
		moduleMessage:      "expected malformed go.mod parse to return empty module path",
		dependencyMessage:  "expected malformed go.mod parse to preserve empty dependency slice",
		replacementMessage: "expected malformed go.mod parse to preserve empty replacements",
	})
}

type parsedGoModExpectation struct {
	modulePath         string
	dependencies       []string
	replacements       map[string]string
	moduleMessage      string
	dependencyMessage  string
	replacementMessage string
}

func assertParsedGoMod(t *testing.T, goMod string, want parsedGoModExpectation) {
	t.Helper()
	modulePath, dependencies, replacements := parseGoMod([]byte(goMod))
	if modulePath != want.modulePath {
		t.Fatalf("%s, got %q", want.moduleMessage, modulePath)
	}
	if !slices.Equal(dependencies, want.dependencies) {
		t.Fatalf("%s, got %#v", want.dependencyMessage, dependencies)
	}
	if !maps.Equal(replacements, want.replacements) {
		t.Fatalf("%s, got %#v", want.replacementMessage, replacements)
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
	resolvedRepoPath, ok := resolveRepoBoundedPath(repo, "./svc")
	if !ok || resolvedRepoPath != filepath.Join(repo, "svc") {
		t.Fatalf("expected in-repo relative path to resolve, got path=%q ok=%v", resolvedRepoPath, ok)
	}
	if _, ok := resolveRepoBoundedPath(repo, "../outside"); ok {
		t.Fatalf("expected resolveRepoBoundedPath to reject escaping path")
	}
}
