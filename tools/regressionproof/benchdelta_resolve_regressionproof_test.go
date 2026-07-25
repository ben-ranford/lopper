package main

import "testing"

func TestRegressionProofBenchdeltaResolveRejectsSymlinkedOverlayAncestor(t *testing.T) {
	scenario := regressionProofScenario{
		baseFiles: benchdeltaProofBaseFiles(),
		headFiles: benchdeltaProofHeadFiles(),
	}
	runBenchdeltaRegressionProof(t, scenario, "fix(benchdelta): reject symlinked overlay escape", "./buggy::TestRejectsSymlinkedOverlayAncestorWithoutExternalMutation")
}

func TestRegressionProofBenchdeltaResolveRejectsSymlinkedManifestRoot(t *testing.T) {
	scenario := regressionProofScenario{
		baseFiles: benchdeltaManifestRootProofBaseFiles(),
		headFiles: benchdeltaManifestRootProofHeadFiles(),
	}
	runBenchdeltaRegressionProof(t, scenario, "fix(benchdelta): reject symlinked manifest root escape", "./buggy::TestRejectsSymlinkedManifestRootWithoutExternalMutation")
}

func TestRegressionProofBenchdeltaResolveRejectsSymlinkedManifestAncestorWithExistingDescendant(t *testing.T) {
	scenario := regressionProofScenario{
		baseFiles: benchdeltaManifestAncestorExistingProofBaseFiles(),
		headFiles: benchdeltaManifestAncestorExistingProofHeadFiles(),
	}
	runBenchdeltaRegressionProof(t, scenario, "fix(benchdelta): reject symlinked manifest ancestor with existing descendant", "./buggy::TestRejectsSymlinkedManifestAncestorWithExistingDescendantWithoutExternalMutation")
}

func TestRegressionProofBenchdeltaResolveRejectsSymlinkedManifestAncestorWithMissingDescendant(t *testing.T) {
	scenario := regressionProofScenario{
		baseFiles: benchdeltaManifestAncestorMissingProofBaseFiles(),
		headFiles: benchdeltaManifestAncestorMissingProofHeadFiles(),
	}
	runBenchdeltaRegressionProof(t, scenario, "fix(benchdelta): reject symlinked manifest ancestor with missing descendant", "./buggy::TestRejectsSymlinkedManifestAncestorWithMissingDescendantWithoutExternalMutation")
}

func runBenchdeltaRegressionProof(t *testing.T, scenario regressionProofScenario, title, regressionTest string) {
	t.Helper()

	repo := newRegressionProofRepo(t, scenario)
	body := "## Validation\n\nRegression-Test: " + regressionTest
	code, stderr := runRegressionProof(t, repo, regressionProofInvocation{
		title: title,
		body:  body,
	})
	if code != 0 {
		t.Fatalf("run returned %d, want 0; stderr=%s", code, stderr)
	}
}

func benchdeltaProofBaseFiles() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/regressionproof\n\ngo 1.26.0\n",
		"buggy/overlay.go": `package buggy

import (
	"os"
	"path/filepath"
)

func resolveOverlay(manifestRoot, externalRoot string) error {
	overlayPath := filepath.Join(manifestRoot, "nested", "escape", "overlay", "mutated.txt")
	return os.WriteFile(overlayPath, []byte("mutated"), 0o600)
}
`,
	}
}

func benchdeltaProofHeadFiles() map[string]string {
	files := benchdeltaProofBaseFiles()
	files["buggy/overlay.go"] = `package buggy

import (
	"fmt"
	"os"
	"path/filepath"
)

func resolveOverlay(manifestRoot, externalRoot string) error {
	overlayAncestor := filepath.Join(manifestRoot, "nested", "escape")
	info, err := os.Lstat(overlayAncestor)
	if err != nil {
		return fmt.Errorf("lstat overlay ancestor: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("overlay path contains symlink: %s", overlayAncestor)
	}
	overlayPath := filepath.Join(overlayAncestor, "overlay", "mutated.txt")
	return os.WriteFile(overlayPath, []byte("mutated"), 0o600)
}
`
	files["buggy/overlay_test.go"] = `package buggy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsSymlinkedOverlayAncestorWithoutExternalMutation(t *testing.T) {
	artifactDir := t.TempDir()
	externalRoot := filepath.Join(artifactDir, "external-root")
	if err := os.MkdirAll(filepath.Join(externalRoot, "overlay"), 0o755); err != nil {
		t.Fatalf("mkdir external overlay: %v", err)
	}
	staleOverlayPath := filepath.Join(externalRoot, "overlay", "stale.txt")
	if err := os.WriteFile(staleOverlayPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write stale overlay file: %v", err)
	}
	sentinelPath := filepath.Join(externalRoot, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	manifestRoot := filepath.Join(artifactDir, "manifest-root")
	if err := os.MkdirAll(filepath.Join(manifestRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir manifest nested dir: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(manifestRoot, "nested", "escape")); err != nil {
		t.Fatalf("create overlay ancestor symlink: %v", err)
	}

	err := resolveOverlay(manifestRoot, externalRoot)
	if err == nil || !strings.Contains(err.Error(), "overlay path contains symlink") {
		t.Fatalf("expected symlinked overlay ancestor to be rejected, got %v", err)
	}
	assertFileContent(t, staleOverlayPath, "keep me")
	assertFileContent(t, sentinelPath, "sentinel")
	if _, err := os.Stat(filepath.Join(externalRoot, "overlay", "mutated.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected external mutation to remain absent, got %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}
`
	return files
}

func benchdeltaManifestRootProofBaseFiles() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/regressionproof\n\ngo 1.26.0\n",
		"buggy/manifest.go": `package buggy

import (
	"os"
	"path/filepath"
)

func resolveManifest(outPath string) error {
	overlayPath := filepath.Join(filepath.Dir(outPath), "overlay", "mutated.txt")
	if err := os.WriteFile(overlayPath, []byte("mutated"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(outPath, []byte("manifest"), 0o600)
}
`,
	}
}

func benchdeltaManifestRootProofHeadFiles() map[string]string {
	files := benchdeltaManifestRootProofBaseFiles()
	files["buggy/manifest.go"] = `package buggy

import (
	"fmt"
	"os"
	"path/filepath"
)

func resolveManifest(outPath string) error {
	rootDir := filepath.Dir(outPath)
	info, err := os.Lstat(rootDir)
	if err != nil {
		return fmt.Errorf("lstat manifest root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root contains symlink: %s", rootDir)
	}
	overlayPath := filepath.Join(rootDir, "overlay", "mutated.txt")
	if err := os.WriteFile(overlayPath, []byte("mutated"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(outPath, []byte("manifest"), 0o600)
}
`
	files["buggy/manifest_test.go"] = `package buggy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsSymlinkedManifestRootWithoutExternalMutation(t *testing.T) {
	artifactDir := t.TempDir()
	externalRoot := filepath.Join(artifactDir, "external-root")
	if err := os.MkdirAll(filepath.Join(externalRoot, "overlay"), 0o755); err != nil {
		t.Fatalf("mkdir external overlay: %v", err)
	}
	staleOverlayPath := filepath.Join(externalRoot, "overlay", "stale.txt")
	if err := os.WriteFile(staleOverlayPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write stale overlay file: %v", err)
	}
	sentinelPath := filepath.Join(externalRoot, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	linkedRoot := filepath.Join(artifactDir, "linked-root")
	if err := os.Symlink(externalRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := resolveManifest(filepath.Join(linkedRoot, "definition.json"))
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlinked manifest root to be rejected, got %v", err)
	}
	assertFileContent(t, staleOverlayPath, "keep me")
	assertFileContent(t, sentinelPath, "sentinel")
	if _, err := os.Stat(filepath.Join(externalRoot, "definition.json")); !os.IsNotExist(err) {
		t.Fatalf("expected external manifest to remain absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalRoot, "overlay", "mutated.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected external overlay mutation to remain absent, got %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}
`
	return files
}

func benchdeltaManifestAncestorExistingProofBaseFiles() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/regressionproof\n\ngo 1.26.0\n",
		"buggy/manifest.go": `package buggy

import (
	"os"
	"path/filepath"
)

func resolveManifest(rootDir string) error {
	overlayPath := filepath.Join(rootDir, "overlay", "mutated.txt")
	if err := os.WriteFile(overlayPath, []byte("mutated"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(rootDir, "definition.json"), []byte("manifest"), 0o600)
}
`,
	}
}

func benchdeltaManifestAncestorExistingProofHeadFiles() map[string]string {
	files := benchdeltaManifestAncestorExistingProofBaseFiles()
	files["buggy/manifest.go"] = `package buggy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveManifest(rootDir string) error {
	if err := validateRootPathNoFollow(rootDir); err != nil {
		return err
	}
	overlayPath := filepath.Join(rootDir, "overlay", "mutated.txt")
	if err := os.WriteFile(overlayPath, []byte("mutated"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(rootDir, "definition.json"), []byte("manifest"), 0o600)
}

func validateRootPathNoFollow(rootDir string) error {
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolve root path: %w", err)
	}
	volumeRoot := filepath.VolumeName(rootAbs) + string(os.PathSeparator)
	currentPath := filepath.Clean(volumeRoot)

	info, err := os.Lstat(currentPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root contains symlink: %s", currentPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("root is not a directory: %s", currentPath)
	}

	rel, err := filepath.Rel(volumeRoot, rootAbs)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		info, err := os.Lstat(currentPath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("root contains symlink: %s", currentPath)
		}
		if !info.IsDir() {
			return fmt.Errorf("root is not a directory: %s", currentPath)
		}
	}
	return nil
}
`
	files["buggy/manifest_test.go"] = `package buggy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsSymlinkedManifestAncestorWithExistingDescendantWithoutExternalMutation(t *testing.T) {
	artifactDir := t.TempDir()
	externalRoot := filepath.Join(artifactDir, "external-root")
	existingRoot := filepath.Join(externalRoot, "existing")
	if err := os.MkdirAll(filepath.Join(existingRoot, "overlay"), 0o755); err != nil {
		t.Fatalf("mkdir external overlay: %v", err)
	}
	staleOverlayPath := filepath.Join(existingRoot, "overlay", "stale.txt")
	if err := os.WriteFile(staleOverlayPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write stale overlay file: %v", err)
	}
	sentinelPath := filepath.Join(externalRoot, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	manifestRoot := filepath.Join(artifactDir, "manifest-root")
	if err := os.MkdirAll(manifestRoot, 0o755); err != nil {
		t.Fatalf("mkdir manifest root: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(manifestRoot, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := resolveManifest(filepath.Join(manifestRoot, "escape", "existing"))
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlinked manifest ancestor to be rejected, got %v", err)
	}
	assertFileContent(t, staleOverlayPath, "keep me")
	assertFileContent(t, sentinelPath, "sentinel")
	if _, err := os.Stat(filepath.Join(existingRoot, "definition.json")); !os.IsNotExist(err) {
		t.Fatalf("expected external manifest to remain absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(existingRoot, "overlay", "mutated.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected external overlay mutation to remain absent, got %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}
`
	return files
}

func benchdeltaManifestAncestorMissingProofBaseFiles() map[string]string {
	files := benchdeltaManifestAncestorExistingProofBaseFiles()
	return files
}

func benchdeltaManifestAncestorMissingProofHeadFiles() map[string]string {
	files := benchdeltaManifestAncestorExistingProofBaseFiles()
	files["buggy/manifest.go"] = benchdeltaManifestAncestorExistingProofHeadFiles()["buggy/manifest.go"]
	files["buggy/manifest_test.go"] = `package buggy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsSymlinkedManifestAncestorWithMissingDescendantWithoutExternalMutation(t *testing.T) {
	artifactDir := t.TempDir()
	externalRoot := filepath.Join(artifactDir, "external-root")
	if err := os.MkdirAll(externalRoot, 0o755); err != nil {
		t.Fatalf("mkdir external root: %v", err)
	}
	sentinelPath := filepath.Join(externalRoot, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	manifestRoot := filepath.Join(artifactDir, "manifest-root")
	if err := os.MkdirAll(manifestRoot, 0o755); err != nil {
		t.Fatalf("mkdir manifest root: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(manifestRoot, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := resolveManifest(filepath.Join(manifestRoot, "escape", "missing", "nested"))
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlinked manifest ancestor to be rejected, got %v", err)
	}
	assertFileContent(t, sentinelPath, "sentinel")
	if _, err := os.Stat(filepath.Join(externalRoot, "missing", "nested", "definition.json")); !os.IsNotExist(err) {
		t.Fatalf("expected external manifest to remain absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalRoot, "missing", "nested", "overlay", "mutated.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected external overlay mutation to remain absent, got %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}
`
	return files
}
