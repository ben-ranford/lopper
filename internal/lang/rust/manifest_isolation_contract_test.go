package rust

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	toml "github.com/pelletier/go-toml/v2"
)

func TestMalformedWorkspaceFallbackPreservesUnselectedCrate(t *testing.T) {
	repo := t.TempDir()
	broken := filepath.Join(repo, "broken")
	selected := filepath.Join(broken, "selected")
	retained := filepath.Join(broken, "retained")
	writeFile(t, filepath.Join(repo, cargoTomlName), "[workspace]\nmembers = [\"broken/selected\"]\n")
	writeFile(t, filepath.Join(broken, cargoTomlName), "[package\nname = \"broken\"\n")
	writeFallbackCrate(t, selected, "selected_dep")
	writeFallbackCrate(t, retained, "retained_dep")

	discovery, err := discoverManifestDataForScope(repo, "", []string{selected})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(discovery.ManifestPaths, []string{filepath.Join(retained, cargoTomlName)}) {
		t.Fatalf("fallback must retain only the unselected crate: %v", discovery.ManifestPaths)
	}
	if !slices.Equal(discovery.WorkspaceManifestPaths, []string{filepath.Join(repo, cargoTomlName)}) {
		t.Fatalf("workspace context changed: %v", discovery.WorkspaceManifestPaths)
	}
	if !slices.Equal(discovery.SourceFallbackRoots, []string{broken}) || !slices.Equal(discovery.ExcludedSourceRoots, []string{broken}) {
		t.Fatalf("malformed root boundaries changed: fallback %v, excluded %v", discovery.SourceFallbackRoots, discovery.ExcludedSourceRoots)
	}
	assertMalformedFallbackEvidence(t, discovery.Warnings, discovery.CoverageGaps)
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10, IsolatedProjectRoots: []string{selected}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "retained-dep" || result.Dependencies[0].UsedExportsCount != 1 {
		t.Fatalf("parent must analyse the retained crate without absorbing the selected crate: %#v", result.Dependencies)
	}
	assertMalformedFallbackEvidence(t, result.Warnings, result.CoverageGaps)
}

func writeFallbackCrate(t *testing.T, root, dependency string) {
	t.Helper()
	writeFile(t, filepath.Join(root, cargoTomlName), "[package]\nname = \"crate\"\nversion = \"0.1.0\"\n[dependencies]\n"+dependency+" = \"1\"\n")
	writeFile(t, filepath.Join(root, "src", "lib.rs"), "use "+dependency+"::Thing;\npub fn example() { let _ = Thing; }\n")
}

func assertMalformedFallbackEvidence(t *testing.T, warnings []string, gaps []report.CoverageGap) {
	t.Helper()
	if !strings.Contains(strings.Join(warnings, "\n"), "skipped malformed Cargo manifest broken/Cargo.toml") {
		t.Fatalf("missing malformed manifest warning: %v", warnings)
	}
	if len(gaps) != 1 || gaps[0].Code != report.CoverageGapRustMalformedManifest || gaps[0].Path != "broken/Cargo.toml" || len(gaps[0].Evidence) != 1 {
		t.Fatalf("malformed manifest coverage evidence changed: %#v", gaps)
	}
	if !strings.Contains(gaps[0].Evidence[0], "skipped malformed Cargo manifest broken/Cargo.toml") {
		t.Fatalf("coverage gap lost its diagnostic: %#v", gaps)
	}
}

func TestCargoManifestParseErrorPreservesDecoderCause(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, cargoTomlName)
	writeFile(t, manifest, "[package\nname = \"broken\"\n")
	_, _, err := parseCargoManifest(manifest, repo)
	var decodeErr *toml.DecodeError
	if !isCargoManifestParseError(err) || !errors.As(err, &decodeErr) {
		t.Fatalf("malformed Cargo error must preserve its decoder cause: %v", err)
	}
	if !strings.Contains(err.Error(), "parse Cargo manifest Cargo.toml") || !errors.Is(err, decodeErr) {
		t.Fatalf("parse error lost manifest context or cause identity: %v", err)
	}
	_, _, err = parseCargoManifest(filepath.Join(repo, "missing", cargoTomlName), repo)
	if !errors.Is(err, fs.ErrNotExist) || isCargoManifestParseError(err) {
		t.Fatalf("missing manifest must retain operational error classification: %v", err)
	}
}
