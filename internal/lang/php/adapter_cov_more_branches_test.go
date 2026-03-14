package php

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestPHPAdditionalBranches(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	writeFile(t, repoFile, "x")
	if err := applyPHPRootSignals(repoFile, &language.Detection{}, map[string]struct{}{}); err == nil {
		t.Fatalf("expected root signal stat error for non-directory repo path")
	}
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail on invalid repo path")
	}

	reports, warnings := buildTopPHPDependencies(3, scanResult{}, 50, report.DefaultRemovalCandidateWeights())
	if len(reports) != 0 || len(warnings) == 0 {
		t.Fatalf("expected top-N request without dependency data to warn, reports=%#v warnings=%#v", reports, warnings)
	}
	if hasRiskCue(nil, "missing") {
		t.Fatalf("did not expect missing risk cue in nil list")
	}
	if err := contextErr(nil); err != nil {
		t.Fatalf("expected nil context error, got %v", err)
	}
	weights := resolveRemovalCandidateWeights(&report.RemovalCandidateWeights{Usage: -1, Impact: 2, Confidence: 3})
	if weights.Usage < 0 || weights.Impact > 1 || weights.Confidence > 1 {
		t.Fatalf("expected removal candidate weights to normalize, got %#v", weights)
	}

	resolver := dependencyResolver{
		namespaceToDep: map[string]string{
			"Vendor":     "vendor/root",
			`Vendor\Pkg`: helpersVendorPkgDependency,
		},
	}
	if dependency := resolver.resolveWithPSR4(`Vendor\Pkg\Client`); dependency != helpersVendorPkgDependency {
		t.Fatalf("expected longest PSR-4 prefix match, got %q", dependency)
	}
	if dependency, resolved := resolver.dependencyFromModule(`Missing`); dependency != "" || !resolved {
		t.Fatalf("expected heuristic miss to report resolved-without-dependency, got dependency=%q resolved=%v", dependency, resolved)
	}
	if dependency, resolved := resolver.dependencyFromModule(""); dependency != "" || resolved {
		t.Fatalf("expected blank module to resolve empty/false, got dependency=%q resolved=%v", dependency, resolved)
	}
	if _, _, ok, unresolved := parseUsePart("", "", "x.php", 1, dependencyResolver{}); ok || unresolved {
		t.Fatalf("expected blank use-part parse to fail without unresolved attribution")
	}
	if _, _, _, ok := parseNamespaceReferenceMetadata("ignored", []int{0}); ok {
		t.Fatalf("expected malformed match metadata to fail")
	}
	if _, _, _, ok := parseNamespaceReferenceMetadata(`\`, []int{0, 1}); ok {
		t.Fatalf("expected empty namespace metadata to fail")
	}
	if _, _, _, ok := parseGroupedUseStatement(`Vendor\Pkg\Client`, "x.php", 1, dependencyResolver{}); ok {
		t.Fatalf("expected non-grouped use statement parse to fail")
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "vendor"), 0o755); err != nil {
		t.Fatalf("mkdir vendor dir: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	var vendorEntry os.DirEntry
	for _, entry := range entries {
		if entry.Name() == "vendor" {
			vendorEntry = entry
			break
		}
	}
	if vendorEntry == nil {
		t.Fatalf("expected vendor dir entry")
	}
	if err := walkPHPDetectionEntry(filepath.Join(repo, "vendor"), vendorEntry, map[string]struct{}{}, &language.Detection{}, new(int), maxDetectFiles); err != filepath.SkipDir {
		t.Fatalf("expected vendor directory to be skipped, got %v", err)
	}
	if err := scanDirEntry(repo, filepath.Join(repo, "vendor"), vendorEntry, &scanState{}); err != filepath.SkipDir {
		t.Fatalf("expected scanDirEntry to skip vendor directory, got %v", err)
	}

	visited := maxDetectFiles
	phpFile := filepath.Join(repo, "Index.php")
	writeFile(t, phpFile, "<?php\n")
	fileEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir for php file: %v", err)
	}
	var phpEntry os.DirEntry
	for _, entry := range fileEntries {
		if entry.Name() == "Index.php" {
			phpEntry = entry
			break
		}
	}
	if phpEntry == nil {
		t.Fatalf("expected php file entry")
	}
	if err := walkPHPDetectionEntry(phpFile, phpEntry, map[string]struct{}{}, &language.Detection{}, &visited, maxDetectFiles); err != filepath.SkipAll {
		t.Fatalf("expected detection walk to stop after max files, got %v", err)
	}

	localResolver := dependencyResolver{localNamespace: map[string]struct{}{"App": {}}}
	if binding, unresolved, ok := parseNamespaceReference(`App\Local`, []int{0, len(`App\Local`)}, "file.php", localResolver, map[string]struct{}{}); ok || unresolved != 0 || binding != (importBinding{}) {
		t.Fatalf("expected local namespace reference to be skipped without unresolved count, binding=%#v unresolved=%d ok=%v", binding, unresolved, ok)
	}
	if binding, unresolved, ok := parseNamespaceReference("ignored", []int{0}, "file.php", dependencyResolver{}, map[string]struct{}{}); ok || unresolved != 0 || binding != (importBinding{}) {
		t.Fatalf("expected malformed namespace reference to be skipped, binding=%#v unresolved=%d ok=%v", binding, unresolved, ok)
	}

	removedPath := filepath.Join(repo, "Removed.php")
	writeFile(t, removedPath, "<?php\n")
	entries, err = os.ReadDir(repo)
	if err != nil {
		t.Fatalf("re-read repo dir for removed file: %v", err)
	}
	var removedEntry os.DirEntry
	for _, entry := range entries {
		if entry.Name() == "Removed.php" {
			removedEntry = entry
			break
		}
	}
	if removedEntry == nil {
		t.Fatalf("expected Removed.php dir entry")
	}
	if err := os.Remove(removedPath); err != nil {
		t.Fatalf("remove php source: %v", err)
	}
	if err := scanFileEntry(repo, removedPath, dependencyResolver{}, &scanResult{}, &scanState{}); err == nil {
		t.Fatalf("expected removed PHP source file to fail scan")
	}

	binding := newImportBinding("file.php", 3, "vendor/pkg", `Vendor\Pkg\Client`, "ClientAlias", "", false)
	if binding.Name != "ClientAlias" || binding.Location.Column != 1 {
		t.Fatalf("expected new import binding to fall back to local name and fixed column, got %#v", binding)
	}
}
