package rust

import (
	"path/filepath"
	"testing"
)

func TestDiscoverManifestDataPreloadsRootManifestDependencies(t *testing.T) {
	repo := t.TempDir()
	rootManifest := filepath.Join(repo, cargoTomlName)
	writeFile(t, rootManifest, "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n[dependencies]\nserde = \"1\"\n")

	discovery, err := discoverManifestData(repo)
	if err != nil {
		t.Fatalf("discover manifest data: %v", err)
	}
	if len(discovery.ManifestPaths) != 1 || discovery.ManifestPaths[0] != rootManifest {
		t.Fatalf("expected only root manifest path, got %#v", discovery.ManifestPaths)
	}
	deps, ok := discovery.ParsedDependencies[rootManifest]
	if !ok {
		t.Fatalf("expected root manifest dependencies to be pre-parsed")
	}
	if _, ok := deps["serde"]; !ok {
		t.Fatalf("expected serde dependency in pre-parsed root dependencies, got %#v", deps)
	}
}

func TestExtractManifestDependenciesUsesPreParsedManifestData(t *testing.T) {
	repo := t.TempDir()
	missingManifest := filepath.Join(repo, cargoTomlName)

	discovery := manifestDiscoveryResult{
		ManifestPaths: []string{missingManifest},
		Warnings:      []string{"discovery warning"},
		ParsedDependencies: map[string]map[string]dependencyInfo{
			missingManifest: {
				"serde": {Canonical: "serde"},
			},
		},
	}

	lookup, renamed, warnings, err := extractManifestDependencies(repo, discovery)
	if err != nil {
		t.Fatalf("extract manifest dependencies: %v", err)
	}
	if len(renamed) != 0 {
		t.Fatalf("expected no renamed aliases, got %#v", renamed)
	}
	if _, ok := lookup["serde"]; !ok {
		t.Fatalf("expected serde from pre-parsed manifest data, got %#v", lookup)
	}
	if len(warnings) != 1 || warnings[0] != "discovery warning" {
		t.Fatalf("expected discovery warnings to be preserved, got %#v", warnings)
	}
}
