//go:build !regressionproof

package rust

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestDiscoveryCapBoundary(t *testing.T) {
	repo := t.TempDir()
	for i := range maxManifestCount + 1 {
		writeFile(t, filepath.Join(repo, "crates", fmt.Sprintf("crate-%03d", i), cargoManifestFile), "[package]\nname = \"x\"\nversion = \"0.1.0\"\n")
	}
	paths, warnings, truncated, err := discoverManifestsByWalkWithStatus(repo)
	if err != nil {
		t.Fatalf("discover manifests cap: %v", err)
	}
	if !truncated || len(paths) != maxManifestCount || !strings.Contains(strings.Join(warnings, "\n"), "capped at 256 manifests") {
		t.Fatalf("expected manifest discovery to stop on the %dth manifest, got truncated=%v paths=%d warnings=%#v", maxManifestCount+1, truncated, len(paths), warnings)
	}

	exactRepo := t.TempDir()
	for i := range maxManifestCount {
		writeFile(t, filepath.Join(exactRepo, "crates", fmt.Sprintf("crate-%03d", i), cargoManifestFile), "[package]\nname = \"x\"\nversion = \"0.1.0\"\n")
	}
	paths, warnings, truncated, err = discoverManifestsByWalkWithStatus(exactRepo)
	if err != nil {
		t.Fatalf("discover exact manifest limit: %v", err)
	}
	if truncated || len(paths) != maxManifestCount || strings.Contains(strings.Join(warnings, "\n"), "capped at") {
		t.Fatalf("expected exact manifest limit without truncation, got truncated=%v paths=%d warnings=%#v", truncated, len(paths), warnings)
	}
}

func TestManifestDiscoveryCapCarriesPackageScopeCoverageGap(t *testing.T) {
	repo := t.TempDir()
	rootManifest := filepath.Join(repo, cargoTomlName)
	writeFile(t, rootManifest, `[package]
name = "root"
version = "0.1.0"

[dependencies]
rootdep = "1"

[workspace]
members = ["crates/*"]
`)
	for index := range maxManifestCount {
		writeFile(t, filepath.Join(repo, "crates", fmt.Sprintf("crate-%03d", index), cargoTomlName), "[package]\nname = \"member\"\nversion = \"0.1.0\"\n")
	}

	discovery, err := discoverManifestDataForScope(repo, "")
	if err != nil {
		t.Fatalf("discover package manifests: %v", err)
	}
	if len(discovery.ManifestPaths) != maxManifestCount+1 || len(discovery.CoverageGaps) != 1 || discovery.CoverageGaps[0].Code != "rust-manifest-discovery-truncated" {
		t.Fatalf("expected complete package manifests with one discovery gap, got paths=%d gaps=%#v", len(discovery.ManifestPaths), discovery.CoverageGaps)
	}
	if discovery.ParsedDependencies[rootManifest]["rootdep"].Canonical != "rootdep" {
		t.Fatalf("expected root dependencies to remain parsed under cap, got %#v", discovery.ParsedDependencies[rootManifest])
	}
}
