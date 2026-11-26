package rust

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRustWorkspaceDiscoveryKeepsValidMembersAfterInvalidPattern(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "direct", cargoTomlName), "[package]\nname = \"direct\"")
	writeFile(t, filepath.Join(repo, "crates", "nested", "lib", cargoTomlName), "[package]\nname = \"lib\"")
	writeFile(t, filepath.Join(repo, "other", cargoTomlName), "[package]\nname = \"other\"")
	writeFile(t, filepath.Join(repo, "target", "lib", cargoTomlName), "[package]\nname = \"target\"")
	writeFile(t, filepath.Join(repo, cargoTomlName), "[workspace]\nmembers = ['[', 'direct', 'crates/**/lib', 'crates/*/lib']")

	discovery, err := discoverManifestData(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(repo, "crates", "nested", "lib", cargoTomlName),
		filepath.Join(repo, "direct", cargoTomlName),
	}
	if !stringSlicesEqual(discovery.ManifestPaths, want) {
		t.Fatalf("got manifests %v, want %v", discovery.ManifestPaths, want)
	}
	if len(discovery.Warnings) != 1 || !strings.Contains(discovery.Warnings[0], "invalid workspace member pattern") {
		t.Fatalf("expected one invalid pattern warning, got %v", discovery.Warnings)
	}
}
