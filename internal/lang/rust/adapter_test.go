package rust

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	testCargoToml                = "Cargo.toml"
	testCargoSectionPackage      = "[package]"
	testCargoSectionDependencies = "[dependencies]"
	testRustLibRS                = "lib.rs"
	testRustMainRS               = "main.rs"
	expectedOneDependencyRowFmt  = "expected one dependency row, got %d"
)

func TestDetectWithConfidenceWorkspaceMembers(t *testing.T) {
	repo := t.TempDir()
	workspaceManifestLines := []string{
		"[workspace]",
		`members = ["crates/*"]`,
		"",
	}
	writeFile(t, filepath.Join(repo, testCargoToml), strings.Join(workspaceManifestLines, "\n"))
	alphaManifestLines := []string{
		testCargoSectionPackage,
		`name = "alpha"`,
		`version = "0.1.0"`,
		"",
	}
	writeFile(t, filepath.Join(repo, "crates", "alpha", testCargoToml), strings.Join(alphaManifestLines, "\n"))
	writeFile(t, filepath.Join(repo, "crates", "alpha", "src", testRustLibRS), "pub fn run() {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect rust: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected detection match")
	}
	memberRoot := filepath.Join(repo, "crates", "alpha")
	if !slices.Contains(detection.Roots, memberRoot) {
		t.Fatalf("expected member root in detection roots, got %#v", detection.Roots)
	}
	if slices.Contains(detection.Roots, repo) {
		t.Fatalf("did not expect pure workspace root in detection roots: %#v", detection.Roots)
	}
}

func TestAnalyseRenamedAndPathDependencyHandling(t *testing.T) {
	repo := t.TempDir()
	rootManifestLines := []string{
		testCargoSectionPackage,
		`name = "demo"`,
		`version = "0.1.0"`,
		"",
		testCargoSectionDependencies,
		`serde_json = { package = "serde-json", version = "1.0" }`,
		`local_dep = { path = "./crates/local_dep" }`,
		"",
	}
	writeFile(t, filepath.Join(repo, testCargoToml), strings.Join(rootManifestLines, "\n"))
	mainSourceLines := []string{
		"use serde_json::Value;",
		"use local_dep::helper;",
		"fn main() { let _v = Value::Null; helper(); }",
		"",
	}
	writeFile(t, filepath.Join(repo, "src", testRustMainRS), strings.Join(mainSourceLines, "\n"))
	localDepManifestLines := []string{
		testCargoSectionPackage,
		`name = "local-dep"`,
		`version = "0.1.0"`,
		"",
	}
	writeFile(t, filepath.Join(repo, "crates", "local_dep", testCargoToml), strings.Join(localDepManifestLines, "\n"))
	writeFile(t, filepath.Join(repo, "crates", "local_dep", "src", testRustLibRS), "pub fn helper() {}\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:                          repo,
		Dependency:                        "serde-json",
		TopN:                              0,
		MinUsagePercentForRecommendations: nil,
	})
	if err != nil {
		t.Fatalf("analyse rust: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyRowFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "rust" {
		t.Fatalf("expected rust language row, got %q", dep.Language)
	}
	if dep.Name != "serde-json" {
		t.Fatalf("expected canonical crate name serde-json, got %q", dep.Name)
	}
	if len(dep.UsedImports) == 0 {
		t.Fatalf("expected used imports for serde-json")
	}
	for _, imported := range dep.UsedImports {
		if strings.Contains(imported.Module, "local_dep") {
			t.Fatalf("did not expect path dependency imports in external dep report: %#v", dep.UsedImports)
		}
	}
}

func TestAnalyseTopWorkspaceDependencies(t *testing.T) {
	repo := t.TempDir()
	workspaceManifestLines := []string{
		"[workspace]",
		`members = ["crates/a", "crates/b"]`,
		"",
	}
	writeFile(t, filepath.Join(repo, testCargoToml), strings.Join(workspaceManifestLines, "\n"))

	crateAManifestLines := []string{
		testCargoSectionPackage,
		`name = "a"`,
		`version = "0.1.0"`,
		"",
		testCargoSectionDependencies,
		`anyhow = "1.0"`,
		"",
	}
	writeFile(t, filepath.Join(repo, "crates", "a", testCargoToml), strings.Join(crateAManifestLines, "\n"))
	writeFile(t, filepath.Join(repo, "crates", "a", "src", testRustLibRS), "use anyhow::Result;\npub fn run() -> Result<()> { Ok(()) }\n")

	crateBManifestLines := []string{
		testCargoSectionPackage,
		`name = "b"`,
		`version = "0.1.0"`,
		"",
		testCargoSectionDependencies,
		`anyhow = "1.0"`,
		"",
	}
	writeFile(t, filepath.Join(repo, "crates", "b", testCargoToml), strings.Join(crateBManifestLines, "\n"))
	writeFile(t, filepath.Join(repo, "crates", "b", "src", testRustLibRS), "use anyhow::Result;\npub fn run() -> Result<()> { Ok(()) }\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse top rust: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in top report")
	}
	if reportData.Dependencies[0].Name != "anyhow" {
		t.Fatalf("expected anyhow dependency, got %#v", reportData.Dependencies)
	}
	if reportData.Summary == nil {
		t.Fatalf("expected summary in report")
	}
}

func TestAnalyseWildcardAndNestedUseRegression(t *testing.T) {
	repo := t.TempDir()
	manifestLines := []string{
		testCargoSectionPackage,
		`name = "demo"`,
		`version = "0.1.0"`,
		"",
		testCargoSectionDependencies,
		`serde = "1.0"`,
		"",
	}
	writeFile(t, filepath.Join(repo, testCargoToml), strings.Join(manifestLines, "\n"))
	mainSourceLines := []string{
		"use serde::{de::DeserializeOwned, *};",
		"fn main() {}",
		"",
	}
	writeFile(t, filepath.Join(repo, "src", testRustMainRS), strings.Join(mainSourceLines, "\n"))

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse rust wildcard: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected rust dependencies")
	}
	dep := reportData.Dependencies[0]
	if dep.Name != "serde" {
		t.Fatalf("expected serde dependency, got %#v", reportData.Dependencies)
	}
	codes := make([]string, 0, len(dep.RiskCues))
	for _, cue := range dep.RiskCues {
		codes = append(codes, cue.Code)
	}
	if !slices.Contains(codes, "broad-imports") {
		t.Fatalf("expected broad-imports risk cue, got %#v", dep.RiskCues)
	}
}

func TestRustMinUsageThresholdControlsLowUsageRecommendation(t *testing.T) {
	repo := t.TempDir()
	manifestLines := []string{
		testCargoSectionPackage,
		`name = "demo"`,
		`version = "0.1.0"`,
		"",
		testCargoSectionDependencies,
		`serde = "1.0"`,
		"",
	}
	writeFile(t, filepath.Join(repo, testCargoToml), strings.Join(manifestLines, "\n"))
	mainSourceLines := []string{
		"use serde::{Deserialize, Serialize};",
		"#[derive(Serialize)]",
		"struct Person { id: u64 }",
		"fn main() {}",
		"",
	}
	writeFile(t, filepath.Join(repo, "src", testRustMainRS), strings.Join(mainSourceLines, "\n"))

	highThreshold := 80
	reportWithRec, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:                          repo,
		Dependency:                        "serde",
		MinUsagePercentForRecommendations: &highThreshold,
	})
	if err != nil {
		t.Fatalf("analyse rust high threshold: %v", err)
	}
	if len(reportWithRec.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyRowFmt, len(reportWithRec.Dependencies))
	}
	if !hasRecommendation(reportWithRec.Dependencies[0], "reduce-rust-surface-area") {
		t.Fatalf("expected low-usage recommendation with high threshold, got %#v", reportWithRec.Dependencies[0].Recommendations)
	}

	lowThreshold := 20
	reportWithoutRec, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:                          repo,
		Dependency:                        "serde",
		MinUsagePercentForRecommendations: &lowThreshold,
	})
	if err != nil {
		t.Fatalf("analyse rust low threshold: %v", err)
	}
	if len(reportWithoutRec.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyRowFmt, len(reportWithoutRec.Dependencies))
	}
	if hasRecommendation(reportWithoutRec.Dependencies[0], "reduce-rust-surface-area") {
		t.Fatalf("did not expect low-usage recommendation with low threshold, got %#v", reportWithoutRec.Dependencies[0].Recommendations)
	}
}

func hasRecommendation(dep report.DependencyReport, code string) bool {
	for _, rec := range dep.Recommendations {
		if rec.Code == code {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
