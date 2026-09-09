package rust

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRustDetectionMalformedRootRetainsNestedSignals(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), "[workspace]\nmembers = [\"crates/*\"]\nbroken = [")
	writeFile(t, filepath.Join(repo, cargoLockName), "version = 3\n")
	writeFile(t, filepath.Join(repo, "crates", "working", cargoTomlName), "[package]\nname = \"working\"\nversion = \"0.1.0\"\n")
	writeFile(t, filepath.Join(repo, "crates", "working", "src", testRustLibRS), "pub fn working() {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect malformed root Cargo manifest: %v", err)
	}
	if len(detection.Roots) != 1 || !samePath(detection.Roots[0], repo) {
		t.Fatalf("expected one repository fallback without overlapping child roots, got %#v", detection)
	}
	if detection.Confidence <= 60 {
		t.Fatalf("expected nested manifest, lock, and source signals beyond malformed-root confidence, got %#v", detection)
	}
}

func TestRustDetectionMatchesSourceWithoutCargoMetadata(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", testRustLibRS), "pub fn source_only() {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect Rust source without Cargo metadata: %v", err)
	}
	if !detection.Matched || len(detection.Roots) != 1 || !samePath(detection.Roots[0], repo) {
		t.Fatalf("expected Rust source signal to select the repository root, got %#v", detection)
	}
}
