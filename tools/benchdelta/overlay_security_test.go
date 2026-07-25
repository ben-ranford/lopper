package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withBenchdeltaFSHooks(t *testing.T, fn func()) {
	t.Helper()

	originalReadFile := readFile
	originalReadDir := readDir
	originalMarshalIndent := marshalIndent
	originalAbsPath := absPath
	originalRelPath := relPath
	originalEvalSymlinks := evalSymlinks
	originalOpenOSRoot := openOSRoot
	originalSameFile := sameFile
	originalRootLstat := rootLstat
	originalRootOpenRoot := rootOpenRoot
	originalRootOpen := rootOpen
	originalRootMkdir := rootMkdir
	originalRootRename := rootRename
	originalRootRemove := rootRemove
	originalCloseOSRoot := closeOSRoot
	originalResolveStageWriteSeam := resolveStageWriteSeam
	originalResolvePromoteSeam := resolvePromoteSeam
	originalApplyStageWriteSeam := applyStageWriteSeam
	originalApplyPromoteSeam := applyPromoteSeam

	t.Cleanup(func() {
		readFile = originalReadFile
		readDir = originalReadDir
		marshalIndent = originalMarshalIndent
		absPath = originalAbsPath
		relPath = originalRelPath
		evalSymlinks = originalEvalSymlinks
		openOSRoot = originalOpenOSRoot
		sameFile = originalSameFile
		rootLstat = originalRootLstat
		rootOpenRoot = originalRootOpenRoot
		rootOpen = originalRootOpen
		rootMkdir = originalRootMkdir
		rootRename = originalRootRename
		rootRemove = originalRootRemove
		closeOSRoot = originalCloseOSRoot
		resolveStageWriteSeam = originalResolveStageWriteSeam
		resolvePromoteSeam = originalResolvePromoteSeam
		applyStageWriteSeam = originalApplyStageWriteSeam
		applyPromoteSeam = originalApplyPromoteSeam
	})

	fn()
}

const (
	absoluteHarnessDefinitionTemplate = `{
  "version": %d,
  "resolved_from": "deadbeef",
  "package_targets": ["./benchpkg"],
  "benchmarks": [{"package_target":"./benchpkg","name":"BenchmarkHeadOnly"}],
  "bench_pattern": "^(BenchmarkHeadOnly)$",
  "run_pattern": "^$",
  "count": 1,
  "benchtime": "1x",
  "benchmem": true,
  "overlay_dir": "overlay",
  "harness_files": [{"path":"/tmp/head_benchmark_test.go","sha256":"abc","overlay_path":"benchpkg/head_benchmark_test.go"}]
}`
	overlayTraversalDefinitionTemplate = `{
  "version": %d,
  "resolved_from": "deadbeef",
  "package_targets": ["./benchpkg"],
  "benchmarks": [{"package_target":"./benchpkg","name":"BenchmarkHeadOnly"}],
  "bench_pattern": "^(BenchmarkHeadOnly)$",
  "run_pattern": "^$",
  "count": 1,
  "benchtime": "1x",
  "benchmem": true,
  "overlay_dir": "overlay",
  "harness_files": [{"path":"benchpkg/head_benchmark_test.go","sha256":"abc","overlay_path":"../escape.go"}]
}`
)

func TestReadBenchmarkDefinitionRejectsAbsoluteHarnessPath(t *testing.T) {
	definitionPath := filepath.Join(t.TempDir(), "definition.json")
	content := fmt.Sprintf(absoluteHarnessDefinitionTemplate, definitionVersion)
	writeDefinitionJSON(t, definitionPath, content)

	if _, _, err := readBenchmarkDefinition(definitionPath); err == nil || !strings.Contains(err.Error(), "benchmark harness path must be relative") {
		t.Fatalf("expected absolute harness path rejection, got %v", err)
	}
}

func TestReadBenchmarkDefinitionRejectsOverlayPathTraversal(t *testing.T) {
	definitionPath := filepath.Join(t.TempDir(), "definition.json")
	content := fmt.Sprintf(overlayTraversalDefinitionTemplate, definitionVersion)
	writeDefinitionJSON(t, definitionPath, content)

	if _, _, err := readBenchmarkDefinition(definitionPath); err == nil || !strings.Contains(err.Error(), "benchmark harness overlay path escapes its root") {
		t.Fatalf("expected overlay traversal rejection, got %v", err)
	}
}

func TestReadBenchmarkDefinitionRejectsDuplicateHarnessPaths(t *testing.T) {
	tests := []struct {
		name         string
		harnessFiles []benchmarkHarnessFile
		wantError    string
	}{
		{
			name: "target path",
			harnessFiles: []benchmarkHarnessFile{
				{Path: "benchpkg/head_benchmark_test.go", SHA256: "first", OverlayPath: "source/first.go"},
				{Path: "benchpkg/./head_benchmark_test.go", SHA256: "second", OverlayPath: "source/second.go"},
			},
			wantError: `duplicate benchmark harness path "benchpkg/head_benchmark_test.go"`,
		},
		{
			name: "overlay path",
			harnessFiles: []benchmarkHarnessFile{
				{Path: "benchpkg/first_test.go", SHA256: "first", OverlayPath: "source/head_benchmark_test.go"},
				{Path: "benchpkg/second_test.go", SHA256: "second", OverlayPath: "source/./head_benchmark_test.go"},
			},
			wantError: `duplicate benchmark harness overlay path "source/head_benchmark_test.go"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := benchmarkDefinition{
				Version:        definitionVersion,
				ResolvedFrom:   "deadbeef",
				PackageTargets: []string{"./benchpkg"},
				Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
				BenchPattern:   "^(BenchmarkHeadOnly)$",
				RunPattern:     "^$",
				Count:          1,
				Benchtime:      "1x",
				BenchMem:       true,
				HarnessFiles:   test.harnessFiles,
				OverlayDir:     "overlay",
			}
			content, err := json.Marshal(definition)
			if err != nil {
				t.Fatalf("marshal benchmark definition: %v", err)
			}
			definitionPath := filepath.Join(t.TempDir(), "definition.json")
			writeDefinitionJSON(t, definitionPath, string(content))

			if _, _, err := readBenchmarkDefinition(definitionPath); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected %s rejection, got %v", test.name, err)
			}
		})
	}
}

func TestRunResolveCommandPersistsManifestRelativeOverlayDir(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go":             "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) { _ = SharedValue() }\n",
		},
		commit: true,
	})
	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "manifests", "definition.json")
	overlayDir := filepath.Join(artifactDir, "manifests", "nested", "custom-overlay")

	if err := runResolveCommand([]string{
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", definitionPath,
		"-overlay-dir", overlayDir,
	}); err != nil {
		t.Fatalf("runResolveCommand returned error: %v", err)
	}

	definition, _, err := readBenchmarkDefinition(definitionPath)
	if err != nil {
		t.Fatalf("readBenchmarkDefinition returned error: %v", err)
	}
	if definition.OverlayDir != "nested/custom-overlay" {
		t.Fatalf("overlay dir = %q, want %q", definition.OverlayDir, "nested/custom-overlay")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(definitionPath), "nested", "custom-overlay", "benchpkg", "head_benchmark_test.go")); err != nil {
		t.Fatalf("expected nested overlay file to exist: %v", err)
	}
}

func TestRunResolveCommandRejectsOverlayDirOutsideManifestRoot(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
		},
		commit: true,
	})
	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "manifests", "definition.json")
	overlayDir := filepath.Join(artifactDir, "outside-overlay")

	err := runResolveCommand([]string{
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", definitionPath,
		"-overlay-dir", overlayDir,
	})
	if err == nil || !strings.Contains(err.Error(), "overlay directory escapes its root") {
		t.Fatalf("expected out-of-root overlay rejection, got %v", err)
	}
}

func TestMainResolveRejectsSymlinkedOverlayAncestorWithoutMutatingExternalDir(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go":             "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) { _ = SharedValue() }\n",
		},
		commit: true,
	})

	artifactDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize artifact dir: %v", err)
	}
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

	root := filepath.Join(artifactDir, "manifest-root")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir manifest nested dir: %v", err)
	}
	overlayAncestor := filepath.Join(root, "nested", "escape")
	if err := os.Symlink(externalRoot, overlayAncestor); err != nil {
		t.Fatalf("create overlay ancestor symlink: %v", err)
	}

	args := []string{
		"resolve",
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(root, "definition.json"),
		"-overlay-dir", filepath.Join(overlayAncestor, "overlay"),
	}
	output, exitCode := runBenchdeltaHelper(t, "TestMainResolveRejectsSymlinkedOverlayAncestorWithoutMutatingExternalDir", args...)
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "clear benchmark overlay")
	assertBenchdeltaHelperOutput(t, output, "overlay path contains symlink")
	assertFileContainsBytes(t, staleOverlayPath, []byte("keep me"))
	assertFileContainsBytes(t, sentinelPath, []byte("sentinel"))
	if _, err := os.Stat(filepath.Join(externalRoot, "benchpkg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external overlay content to remain absent, got %v", err)
	}
}

func TestMainResolveRejectsSymlinkedManifestRootWithoutMutatingExternalDir(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go":             "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) { _ = SharedValue() }\n",
		},
		commit: true,
	})

	artifactDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize artifact dir: %v", err)
	}
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
		t.Fatalf("create manifest root symlink: %v", err)
	}

	args := []string{
		"resolve",
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(linkedRoot, "definition.json"),
		"-overlay-dir", filepath.Join(linkedRoot, "overlay"),
	}
	output, exitCode := runBenchdeltaHelper(t, "TestMainResolveRejectsSymlinkedManifestRootWithoutMutatingExternalDir", args...)
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "open benchmark manifest root")
	assertBenchdeltaHelperOutput(t, output, "root contains symlink")
	assertFileContainsBytes(t, staleOverlayPath, []byte("keep me"))
	assertFileContainsBytes(t, sentinelPath, []byte("sentinel"))
	if _, err := os.Stat(filepath.Join(externalRoot, "definition.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external manifest to remain absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalRoot, "overlay", "benchpkg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external overlay content to remain absent, got %v", err)
	}
}

func TestMainResolveRejectsSymlinkedManifestRootAncestorWithExistingDescendantWithoutMutatingExternalDir(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go":             "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) { _ = SharedValue() }\n",
		},
		commit: true,
	})

	artifactDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize artifact dir: %v", err)
	}
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

	root := filepath.Join(artifactDir, "manifest-root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir manifest root: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create manifest ancestor symlink: %v", err)
	}

	args := []string{
		"resolve",
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(root, "escape", "existing", "definition.json"),
		"-overlay-dir", filepath.Join(root, "escape", "existing", "overlay"),
	}
	output, exitCode := runBenchdeltaHelper(t, "TestMainResolveRejectsSymlinkedManifestRootAncestorWithExistingDescendantWithoutMutatingExternalDir", args...)
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "open benchmark manifest root")
	assertBenchdeltaHelperOutput(t, output, "root contains symlink")
	assertFileContainsBytes(t, staleOverlayPath, []byte("keep me"))
	assertFileContainsBytes(t, sentinelPath, []byte("sentinel"))
	if _, err := os.Stat(filepath.Join(existingRoot, "definition.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external manifest to remain absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(existingRoot, "overlay", "benchpkg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external overlay content to remain absent, got %v", err)
	}
}

func TestMainResolveRejectsSymlinkedManifestRootAncestorWithMissingDescendantWithoutMutatingExternalDir(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go":             "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) { _ = SharedValue() }\n",
		},
		commit: true,
	})

	artifactDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize artifact dir: %v", err)
	}
	externalRoot := filepath.Join(artifactDir, "external-root")
	if err := os.MkdirAll(externalRoot, 0o755); err != nil {
		t.Fatalf("mkdir external root: %v", err)
	}
	sentinelPath := filepath.Join(externalRoot, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("write sentinel file: %v", err)
	}

	root := filepath.Join(artifactDir, "manifest-root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir manifest root: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create manifest ancestor symlink: %v", err)
	}

	args := []string{
		"resolve",
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(root, "escape", "missing", "nested", "definition.json"),
		"-overlay-dir", filepath.Join(root, "escape", "missing", "nested", "overlay"),
	}
	output, exitCode := runBenchdeltaHelper(t, "TestMainResolveRejectsSymlinkedManifestRootAncestorWithMissingDescendantWithoutMutatingExternalDir", args...)
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "open benchmark manifest root")
	assertBenchdeltaHelperOutput(t, output, "root contains symlink")
	assertFileContainsBytes(t, sentinelPath, []byte("sentinel"))
	if _, err := os.Stat(filepath.Join(externalRoot, "missing", "nested", "definition.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external manifest to remain absent, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalRoot, "missing", "nested", "overlay")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected external overlay content to remain absent, got %v", err)
	}
}

func TestMainResolveSupportsTrustedMacOSAliasPath(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go":             "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) { _ = SharedValue() }\n",
		},
		commit: true,
	})

	artifactDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize artifact dir: %v", err)
	}
	aliasDir, ok := trustedMacOSAliasPath(artifactDir)
	if !ok {
		t.Skip("trusted macOS alias path unavailable")
	}
	if err := os.MkdirAll(aliasDir, 0o755); err != nil {
		t.Fatalf("mkdir alias artifact dir: %v", err)
	}

	definitionPath := filepath.Join(aliasDir, "definition.json")
	overlayDir := filepath.Join(aliasDir, "overlay")
	output, exitCode := runBenchdeltaHelper(t, "TestMainResolveSupportsTrustedMacOSAliasPath", "resolve", "-repo", headRepo, "-package", "./benchpkg", "-count", "1", "-benchtime", "1x", "-out", definitionPath, "-overlay-dir", overlayDir)
	assertBenchdeltaHelperExit(t, output, exitCode, 0)
	assertBenchdeltaHelperOutput(t, output, "lopper-bench-definition:")

	if _, err := os.Stat(filepath.Join(artifactDir, "definition.json")); err != nil {
		t.Fatalf("expected canonical manifest path to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "overlay", "benchpkg", "head_benchmark_test.go")); err != nil {
		t.Fatalf("expected canonical overlay path to exist: %v", err)
	}
}

func TestResolveManifestLayoutNormalizesManifestRelativeOverlayDir(t *testing.T) {
	root := t.TempDir()
	definitionPath := filepath.Join(root, "artifacts", "definition.json")
	overlayPath := filepath.Join(root, "artifacts", "nested", ".", "overlay")
	definitionDir, definitionRel, overlayRel, overlayRoot, err := resolveManifestLayout(definitionPath, overlayPath)
	if err != nil {
		t.Fatalf("resolveManifestLayout returned error: %v", err)
	}
	if definitionDir != filepath.Join(root, "artifacts") {
		t.Fatalf("definition dir = %q", definitionDir)
	}
	if definitionRel != "definition.json" {
		t.Fatalf("definition rel = %q", definitionRel)
	}
	if overlayRel != "nested/overlay" {
		t.Fatalf("overlay rel = %q", overlayRel)
	}
	if overlayRoot != filepath.Join(root, "artifacts", "nested", "overlay") {
		t.Fatalf("overlay root = %q", overlayRoot)
	}
}

func TestResolveManifestLayoutRejectsOverlayDirAtManifestRoot(t *testing.T) {
	root := t.TempDir()
	_, _, _, _, err := resolveManifestLayout(filepath.Join(root, "artifacts", "definition.json"), filepath.Join(root, "artifacts"))
	if err == nil || !strings.Contains(err.Error(), "overlay directory must not resolve to the root directory") {
		t.Fatalf("expected manifest-root overlay rejection, got %v", err)
	}
}

func TestNormalizeManifestRelativePathCases(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if _, err := normalizeManifestRelativePath("overlay directory", " "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("expected empty path rejection, got %v", err)
		}
	})

	t.Run("normalized", func(t *testing.T) {
		got, err := normalizeManifestRelativePath("overlay directory", "./nested/../overlay/file.go")
		if err != nil {
			t.Fatalf("normalizeManifestRelativePath returned error: %v", err)
		}
		if got != "overlay/file.go" {
			t.Fatalf("normalized path = %q", got)
		}
	})
}

func TestPackageTargetDirReturnsNormalizedDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	got, err := packageTargetDir(repoRoot, "./benchpkg")
	if err != nil {
		t.Fatalf("packageTargetDir returned error: %v", err)
	}
	if got != filepath.Join(repoRoot, "benchpkg") {
		t.Fatalf("package dir = %q", got)
	}
}

func TestNormalizeDefinitionPathsRejectsInvalidOverlayDir(t *testing.T) {
	definition := benchmarkDefinition{
		OverlayDir: "/tmp/overlay",
		HarnessFiles: []benchmarkHarnessFile{{
			Path:        "benchpkg/head_benchmark_test.go",
			OverlayPath: "benchpkg/head_benchmark_test.go",
		}},
	}
	if err := normalizeDefinitionPaths(&definition); err == nil || !strings.Contains(err.Error(), "overlay directory must be relative") {
		t.Fatalf("expected invalid overlay dir rejection, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionRejectsInvalidHarnessPath(t *testing.T) {
	parent := canonicalTempDir(t)
	definition := benchmarkDefinition{
		Version:    definitionVersion,
		OverlayDir: "overlay",
		HarnessFiles: []benchmarkHarnessFile{{
			Path:        "/tmp/escape.go",
			OverlayPath: "benchpkg/head_benchmark_test.go",
		}},
	}
	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), definition, nil)
	if err == nil || !strings.Contains(err.Error(), "benchmark harness path must be relative") {
		t.Fatalf("expected invalid harness path rejection, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsOpenRootError(t *testing.T) {
	parent := canonicalTempDir(t)
	originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
	openCanonicalWriteRoot = func(string) (confinedWriteRoot, error) {
		return nil, errors.New("open root failed")
	}
	defer func() { openCanonicalWriteRoot = originalOpenCanonicalWriteRoot }()

	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || !strings.Contains(err.Error(), "open benchmark manifest root: open root failed") {
		t.Fatalf("expected open root error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsCanonicalizeRootError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		evalSymlinks = func(string) (string, error) {
			return "", errors.New("canonicalize failed")
		}

		parent := canonicalTempDir(t)
		err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
		if err == nil || !strings.Contains(err.Error(), "canonicalize benchmark manifest root: canonicalize failed") {
			t.Fatalf("expected canonicalize root error, got %v", err)
		}
	})
}

func TestWriteBenchmarkDefinitionReturnsManifestRootOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		openOSRoot = func(string) (*os.Root, error) {
			return nil, errors.New("open manifest root failed")
		}

		parent := canonicalTempDir(t)
		err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
		if err == nil || !strings.Contains(err.Error(), "open benchmark manifest root: open manifest root failed") {
			t.Fatalf("expected manifest root open error, got %v", err)
		}
	})
}

func TestWriteBenchmarkDefinitionReturnsOverlayWriteRootError(t *testing.T) {
	parent := canonicalTempDir(t)
	originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
	openCanonicalWriteRoot = func(string) (confinedWriteRoot, error) {
		return &fakeConfinedWriteRoot{failOnWrite: 1, err: errors.New("overlay write failed")}, nil
	}
	defer func() { openCanonicalWriteRoot = originalOpenCanonicalWriteRoot }()

	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, map[string][]byte{"benchpkg/head_benchmark_test.go": []byte("package benchpkg\n")})
	if err == nil || !strings.Contains(err.Error(), "write overlay file benchpkg/head_benchmark_test.go: overlay write failed") {
		t.Fatalf("expected overlay write error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsCloseError(t *testing.T) {
	parent := canonicalTempDir(t)
	originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
	openCanonicalWriteRoot = func(string) (confinedWriteRoot, error) {
		return &fakeConfinedWriteRoot{closeErr: errors.New("close failed")}, nil
	}
	defer func() { openCanonicalWriteRoot = originalOpenCanonicalWriteRoot }()

	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsManifestRootCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		closeCalls := 0
		closeOSRoot = func(root *os.Root) error {
			closeCalls++
			if closeCalls == 1 {
				return errors.New("close manifest root failed")
			}
			return root.Close()
		}

		parent := canonicalTempDir(t)
		err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
		if err == nil || !strings.Contains(err.Error(), "close manifest root failed") {
			t.Fatalf("expected manifest root close error, got %v", err)
		}
	})
}

func TestWriteBenchmarkDefinitionReturnsCreateDirectoryError(t *testing.T) {
	parent := canonicalTempDir(t)
	blocker := filepath.Join(parent, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	err := writeBenchmarkDefinition(filepath.Join(blocker, "definition.json"), filepath.Join(blocker, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || !strings.Contains(err.Error(), "root is not a directory") {
		t.Fatalf("expected create definition dir error, got %v", err)
	}
}

func TestApplyBenchmarkOverlayRejectsSymlinkTargetEscape(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo package dir: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	targetPath := filepath.Join(repo, "benchpkg", "head_benchmark_test.go")
	if err := os.Symlink(outsidePath, targetPath); err != nil {
		t.Fatalf("create target symlink: %v", err)
	}

	err := applyBenchmarkOverlay(repo, definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("expected symlink target rejection, got %v", err)
	}
	assertFileContainsBytes(t, outsidePath, []byte("outside"))
}

func TestApplyBenchmarkOverlayRejectsSymlinkAncestorEscape(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	repo := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(repo, "benchpkg")); err != nil {
		t.Fatalf("create ancestor symlink: %v", err)
	}

	err := applyBenchmarkOverlay(repo, definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if entries, err := os.ReadDir(outsideDir); err != nil {
		t.Fatalf("read outside dir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("expected outside dir to remain untouched, found %d entries", len(entries))
	}
}

func TestRunResolveCommandRejectsSymlinkedPackageDirWithoutOverlayMutation(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	initGitRepo(t, repo)
	externalRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(externalRoot, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir external package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalRoot, "benchpkg", "head_benchmark_test.go"), []byte("package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkExternal(b *testing.B) {}\n"), 0o600); err != nil {
		t.Fatalf("write external harness: %v", err)
	}
	if err := os.Symlink(filepath.Join(externalRoot, "benchpkg"), filepath.Join(repo, "benchpkg")); err != nil {
		t.Fatalf("create package dir symlink: %v", err)
	}

	artifactDir := canonicalTempDir(t)
	overlayDir := filepath.Join(artifactDir, "overlay")
	staleOverlayPath := filepath.Join(overlayDir, "stale.txt")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	if err := os.WriteFile(staleOverlayPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write stale overlay file: %v", err)
	}

	err := runResolveCommand([]string{
		"-repo", repo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(artifactDir, "definition.json"),
		"-overlay-dir", overlayDir,
	})
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlinked package dir rejection, got %v", err)
	}
	assertFileContainsBytes(t, staleOverlayPath, []byte("keep me"))
	if _, err := os.Stat(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go")); !os.IsNotExist(err) {
		t.Fatalf("expected overlay harness to remain absent, got %v", err)
	}
}

func TestRunResolveCommandRejectsSymlinkedPackageAncestorWithoutOverlayMutation(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "pkgroot"), 0o755); err != nil {
		t.Fatalf("mkdir pkgroot: %v", err)
	}
	initGitRepo(t, repo)
	externalRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(externalRoot, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir external package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalRoot, "benchpkg", "head_benchmark_test.go"), []byte("package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkExternal(b *testing.B) {}\n"), 0o600); err != nil {
		t.Fatalf("write external harness: %v", err)
	}
	if err := os.Symlink(externalRoot, filepath.Join(repo, "pkgroot", "escape")); err != nil {
		t.Fatalf("create package ancestor symlink: %v", err)
	}

	artifactDir := canonicalTempDir(t)
	overlayDir := filepath.Join(artifactDir, "overlay")
	staleOverlayPath := filepath.Join(overlayDir, "stale.txt")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	if err := os.WriteFile(staleOverlayPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write stale overlay file: %v", err)
	}

	err := runResolveCommand([]string{
		"-repo", repo,
		"-package", "./pkgroot/escape/benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(artifactDir, "definition.json"),
		"-overlay-dir", overlayDir,
	})
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlinked package ancestor rejection, got %v", err)
	}
	assertFileContainsBytes(t, staleOverlayPath, []byte("keep me"))
	if _, err := os.Stat(filepath.Join(overlayDir, "pkgroot", "escape", "benchpkg", "head_benchmark_test.go")); !os.IsNotExist(err) {
		t.Fatalf("expected overlay harness to remain absent, got %v", err)
	}
}

func TestRunResolveCommandRejectsSymlinkedHarnessWithoutOverlayMutation(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	externalHarness := filepath.Join(t.TempDir(), "external_benchmark_test.go")
	if err := os.WriteFile(externalHarness, []byte("package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkExternal(b *testing.B) {}\n"), 0o600); err != nil {
		t.Fatalf("write external harness: %v", err)
	}
	if err := os.Symlink(externalHarness, filepath.Join(repo, "benchpkg", "head_benchmark_test.go")); err != nil {
		t.Fatalf("create harness symlink: %v", err)
	}
	initGitRepo(t, repo)

	artifactDir := canonicalTempDir(t)
	overlayDir := filepath.Join(artifactDir, "overlay")
	staleOverlayPath := filepath.Join(overlayDir, "stale.txt")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	if err := os.WriteFile(staleOverlayPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write stale overlay file: %v", err)
	}

	err := runResolveCommand([]string{
		"-repo", repo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(artifactDir, "definition.json"),
		"-overlay-dir", overlayDir,
	})
	if err == nil || !strings.Contains(err.Error(), "benchmark harness path is a symlink") {
		t.Fatalf("expected symlinked harness rejection, got %v", err)
	}
	assertFileContainsBytes(t, staleOverlayPath, []byte("keep me"))
	if _, err := os.Stat(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go")); !os.IsNotExist(err) {
		t.Fatalf("expected overlay harness to remain absent, got %v", err)
	}
	assertFileContainsBytes(t, externalHarness, []byte("package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkExternal(b *testing.B) {}\n"))
}

func TestApplyBenchmarkOverlayLateFailureDoesNotMutateEarlierTargets(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	overlayDir := filepath.Join(artifactDir, "overlay")
	if err := os.MkdirAll(filepath.Join(overlayDir, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	alphaOverlay := []byte("package benchpkg\n\nfunc alphaOverlay() string { return \"alpha\" }\n")
	betaOverlay := []byte("package benchpkg\n\nfunc betaOverlay() string { return \"beta\" }\n")
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "alpha_test.go"), alphaOverlay, 0o600); err != nil {
		t.Fatalf("write alpha overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "beta_test.go"), betaOverlay, 0o600); err != nil {
		t.Fatalf("write beta overlay: %v", err)
	}

	definitionPath := filepath.Join(artifactDir, "definition.json")
	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   "deadbeef",
		PackageTargets: []string{"./benchpkg"},
		Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
		BenchPattern:   "^(BenchmarkHeadOnly)$",
		RunPattern:     "^$",
		Count:          1,
		Benchtime:      "1x",
		BenchMem:       true,
		OverlayDir:     "overlay",
		HarnessFiles: []benchmarkHarnessFile{
			{Path: "benchpkg/alpha_test.go", SHA256: bytesDigest(alphaOverlay), OverlayPath: "benchpkg/alpha_test.go"},
			{Path: "benchpkg/beta_test.go", SHA256: bytesDigest([]byte("different")), OverlayPath: "benchpkg/beta_test.go"},
		},
	}
	content, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	writeDefinitionJSON(t, definitionPath, string(content))

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo package dir: %v", err)
	}
	alphaTarget := filepath.Join(repo, "benchpkg", "alpha_test.go")
	betaTarget := filepath.Join(repo, "benchpkg", "beta_test.go")
	if err := os.WriteFile(alphaTarget, []byte("original alpha"), 0o600); err != nil {
		t.Fatalf("write alpha target: %v", err)
	}
	if err := os.WriteFile(betaTarget, []byte("original beta"), 0o600); err != nil {
		t.Fatalf("write beta target: %v", err)
	}

	err = applyBenchmarkOverlay(repo, definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "overlay file benchpkg/beta_test.go digest mismatch") {
		t.Fatalf("expected late digest mismatch, got %v", err)
	}
	assertFileContainsBytes(t, alphaTarget, []byte("original alpha"))
	assertFileContainsBytes(t, betaTarget, []byte("original beta"))
}

func TestWriteBenchmarkDefinitionStageWriteFailureRestoresPriorSet(t *testing.T) {
	parent := canonicalTempDir(t)
	definitionPath := filepath.Join(parent, "definition.json")
	overlayDir := filepath.Join(parent, "overlay")
	if err := os.MkdirAll(filepath.Join(overlayDir, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	originalManifest := []byte("{\"stale\":true}\n")
	originalOverlay := []byte("package benchpkg\n\nfunc stale() string { return \"old\" }\n")
	staleOverlay := []byte("keep me")
	if err := os.WriteFile(definitionPath, originalManifest, 0o640); err != nil {
		t.Fatalf("write original manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), originalOverlay, 0o644); err != nil {
		t.Fatalf("write original overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "stale.txt"), staleOverlay, 0o600); err != nil {
		t.Fatalf("write stale overlay: %v", err)
	}

	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   "deadbeef",
		PackageTargets: []string{"./benchpkg"},
		Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
		BenchPattern:   "^(BenchmarkHeadOnly)$",
		RunPattern:     "^$",
		Count:          1,
		Benchtime:      "1x",
		BenchMem:       true,
		OverlayDir:     "overlay",
		HarnessFiles: []benchmarkHarnessFile{{
			Path:        "benchpkg/head_benchmark_test.go",
			SHA256:      bytesDigest([]byte("package benchpkg\n")),
			OverlayPath: "benchpkg/head_benchmark_test.go",
		}},
	}

	withBenchdeltaFSHooks(t, func() {
		writes := 0
		resolveStageWriteSeam = func(string) error {
			writes++
			if writes == 2 {
				return errors.New("late resolve stage write failed")
			}
			return nil
		}
		err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, map[string][]byte{
			"benchpkg/head_benchmark_test.go": []byte("package benchpkg\n"),
		})
		if err == nil || !strings.Contains(err.Error(), "write benchmark definition: late resolve stage write failed") {
			t.Fatalf("expected late stage write error, got %v", err)
		}
	})

	assertFileContainsBytes(t, definitionPath, originalManifest)
	assertFileMode(t, definitionPath, 0o640)
	assertFileContainsBytes(t, filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), originalOverlay)
	assertFileMode(t, filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), 0o644)
	assertFileContainsBytes(t, filepath.Join(overlayDir, "stale.txt"), staleOverlay)
	assertNoBenchdeltaTemps(t, parent, ".benchdelta-resolve-*")
}

func TestWriteBenchmarkDefinitionPromotionFailureRestoresPriorSet(t *testing.T) {
	parent := canonicalTempDir(t)
	definitionPath := filepath.Join(parent, "definition.json")
	overlayDir := filepath.Join(parent, "overlay")
	if err := os.MkdirAll(filepath.Join(overlayDir, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	originalManifest := []byte("{\"stale\":true}\n")
	originalOverlay := []byte("package benchpkg\n\nfunc stale() string { return \"old\" }\n")
	staleOverlay := []byte("keep me")
	if err := os.WriteFile(definitionPath, originalManifest, 0o640); err != nil {
		t.Fatalf("write original manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), originalOverlay, 0o644); err != nil {
		t.Fatalf("write original overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "stale.txt"), staleOverlay, 0o600); err != nil {
		t.Fatalf("write stale overlay: %v", err)
	}

	newOverlay := []byte("package benchpkg\n\nfunc fresh() string { return \"new\" }\n")
	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   "deadbeef",
		PackageTargets: []string{"./benchpkg"},
		Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
		BenchPattern:   "^(BenchmarkHeadOnly)$",
		RunPattern:     "^$",
		Count:          1,
		Benchtime:      "1x",
		BenchMem:       true,
		OverlayDir:     "overlay",
		HarnessFiles: []benchmarkHarnessFile{{
			Path:        "benchpkg/head_benchmark_test.go",
			SHA256:      bytesDigest(newOverlay),
			OverlayPath: "benchpkg/head_benchmark_test.go",
		}},
	}

	withBenchdeltaFSHooks(t, func() {
		resolvePromoteSeam = func(step int, livePath string) error {
			if step == 1 && livePath == "overlay" {
				return errors.New("resolve promote failed")
			}
			return nil
		}
		err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, map[string][]byte{
			"benchpkg/head_benchmark_test.go": newOverlay,
		})
		if err == nil || !strings.Contains(err.Error(), "clear benchmark overlay: resolve promote failed") {
			t.Fatalf("expected promotion error, got %v", err)
		}
	})

	assertFileContainsBytes(t, definitionPath, originalManifest)
	assertFileMode(t, definitionPath, 0o640)
	assertFileContainsBytes(t, filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), originalOverlay)
	assertFileMode(t, filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), 0o644)
	assertFileContainsBytes(t, filepath.Join(overlayDir, "stale.txt"), staleOverlay)
	assertNoBenchdeltaTemps(t, parent, ".benchdelta-resolve-*")
}

func TestApplyBenchmarkOverlayStageWriteFailureRestoresTargetsAndRemovesTemps(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	overlayDir := filepath.Join(artifactDir, "overlay")
	if err := os.MkdirAll(filepath.Join(overlayDir, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	alphaOverlay := []byte("package benchpkg\n\nfunc alphaOverlay() string { return \"alpha\" }\n")
	gammaOverlay := []byte("package benchpkg\n\nfunc gammaOverlay() string { return \"gamma\" }\n")
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "alpha_test.go"), alphaOverlay, 0o600); err != nil {
		t.Fatalf("write alpha overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "gamma_test.go"), gammaOverlay, 0o600); err != nil {
		t.Fatalf("write gamma overlay: %v", err)
	}
	definitionPath := filepath.Join(artifactDir, "definition.json")
	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   "deadbeef",
		PackageTargets: []string{"./benchpkg"},
		Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
		BenchPattern:   "^(BenchmarkHeadOnly)$",
		RunPattern:     "^$",
		Count:          1,
		Benchtime:      "1x",
		BenchMem:       true,
		OverlayDir:     "overlay",
		HarnessFiles: []benchmarkHarnessFile{
			{Path: "benchpkg/alpha_test.go", SHA256: bytesDigest(alphaOverlay), OverlayPath: "benchpkg/alpha_test.go"},
			{Path: "benchpkg/gamma_test.go", SHA256: bytesDigest(gammaOverlay), OverlayPath: "benchpkg/gamma_test.go"},
		},
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo package dir: %v", err)
	}
	alphaTarget := filepath.Join(repo, "benchpkg", "alpha_test.go")
	gammaTarget := filepath.Join(repo, "benchpkg", "gamma_test.go")
	if err := os.WriteFile(alphaTarget, []byte("original alpha"), 0o644); err != nil {
		t.Fatalf("write alpha target: %v", err)
	}

	withBenchdeltaFSHooks(t, func() {
		writes := 0
		applyStageWriteSeam = func(string) error {
			writes++
			if writes == 2 {
				return errors.New("late apply stage write failed")
			}
			return nil
		}
		err := applyBenchmarkOverlay(repo, definitionPath, definition)
		if err == nil || !strings.Contains(err.Error(), "write benchmark harness benchpkg/gamma_test.go: late apply stage write failed") {
			t.Fatalf("expected stage write failure, got %v", err)
		}
	})

	assertFileContainsBytes(t, alphaTarget, []byte("original alpha"))
	assertFileMode(t, alphaTarget, 0o644)
	if _, err := os.Stat(gammaTarget); !os.IsNotExist(err) {
		t.Fatalf("expected gamma target to remain absent, got %v", err)
	}
	assertNoBenchdeltaTemps(t, repo, ".benchdelta-apply-*")
}

func TestApplyBenchmarkOverlayPromotionFailureRestoresTargetsAndRemovesNewFiles(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	overlayDir := filepath.Join(artifactDir, "overlay")
	if err := os.MkdirAll(filepath.Join(overlayDir, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	alphaOverlay := []byte("package benchpkg\n\nfunc alphaOverlay() string { return \"alpha\" }\n")
	gammaOverlay := []byte("package benchpkg\n\nfunc gammaOverlay() string { return \"gamma\" }\n")
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "alpha_test.go"), alphaOverlay, 0o600); err != nil {
		t.Fatalf("write alpha overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "gamma_test.go"), gammaOverlay, 0o600); err != nil {
		t.Fatalf("write gamma overlay: %v", err)
	}
	definitionPath := filepath.Join(artifactDir, "definition.json")
	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   "deadbeef",
		PackageTargets: []string{"./benchpkg"},
		Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
		BenchPattern:   "^(BenchmarkHeadOnly)$",
		RunPattern:     "^$",
		Count:          1,
		Benchtime:      "1x",
		BenchMem:       true,
		OverlayDir:     "overlay",
		HarnessFiles: []benchmarkHarnessFile{
			{Path: "benchpkg/alpha_test.go", SHA256: bytesDigest(alphaOverlay), OverlayPath: "benchpkg/alpha_test.go"},
			{Path: "benchpkg/gamma_test.go", SHA256: bytesDigest(gammaOverlay), OverlayPath: "benchpkg/gamma_test.go"},
		},
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo package dir: %v", err)
	}
	alphaTarget := filepath.Join(repo, "benchpkg", "alpha_test.go")
	gammaTarget := filepath.Join(repo, "benchpkg", "gamma_test.go")
	if err := os.WriteFile(alphaTarget, []byte("original alpha"), 0o644); err != nil {
		t.Fatalf("write alpha target: %v", err)
	}

	withBenchdeltaFSHooks(t, func() {
		applyPromoteSeam = func(step int, livePath string) error {
			if step == 2 && livePath == "benchpkg/gamma_test.go" {
				return errors.New("apply promote failed")
			}
			return nil
		}
		err := applyBenchmarkOverlay(repo, definitionPath, definition)
		if err == nil || !strings.Contains(err.Error(), "write benchmark harness benchpkg/gamma_test.go: apply promote failed") {
			t.Fatalf("expected promotion failure, got %v", err)
		}
	})

	assertFileContainsBytes(t, alphaTarget, []byte("original alpha"))
	assertFileMode(t, alphaTarget, 0o644)
	if _, err := os.Stat(gammaTarget); !os.IsNotExist(err) {
		t.Fatalf("expected gamma target to be removed after rollback, got %v", err)
	}
	assertNoBenchdeltaTemps(t, repo, ".benchdelta-apply-*")
}

func TestApplyBenchmarkOverlayReturnsOpenRootError(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
	openCanonicalWriteRoot = func(string) (confinedWriteRoot, error) {
		return nil, errors.New("open root failed")
	}
	defer func() { openCanonicalWriteRoot = originalOpenCanonicalWriteRoot }()

	err := applyBenchmarkOverlay(t.TempDir(), definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "open benchmark repo root: open root failed") {
		t.Fatalf("expected open repo root error, got %v", err)
	}
}

func TestApplyBenchmarkOverlayReturnsRepoPathResolutionError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absCalls := 0
		absPath = func(path string) (string, error) {
			absCalls++
			if absCalls == 1 {
				return "", errors.New("resolve repo failed")
			}
			return filepath.Abs(path)
		}

		err := applyBenchmarkOverlay(".", "definition.json", benchmarkDefinition{
			OverlayDir: "overlay",
			HarnessFiles: []benchmarkHarnessFile{{
				Path:        "benchpkg/head_benchmark_test.go",
				SHA256:      "abc",
				OverlayPath: "benchpkg/head_benchmark_test.go",
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "resolve repo path: resolve repo failed") {
			t.Fatalf("expected repo path resolution error, got %v", err)
		}
	})
}

func TestApplyBenchmarkOverlayReturnsDefinitionDirectoryResolutionError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repo := t.TempDir()
		absCalls := 0
		absPath = func(path string) (string, error) {
			absCalls++
			if absCalls == 2 {
				return "", errors.New("resolve definition dir failed")
			}
			return filepath.Abs(path)
		}

		err := applyBenchmarkOverlay(repo, "definition.json", benchmarkDefinition{
			OverlayDir: "overlay",
			HarnessFiles: []benchmarkHarnessFile{{
				Path:        "benchpkg/head_benchmark_test.go",
				SHA256:      "abc",
				OverlayPath: "benchpkg/head_benchmark_test.go",
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "resolve benchmark definition directory: resolve definition dir failed") {
			t.Fatalf("expected definition directory resolution error, got %v", err)
		}
	})
}

func TestApplyBenchmarkOverlayRejectsInvalidHarnessPath(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	definition.HarnessFiles[0].Path = "/tmp/escape.go"

	err := applyBenchmarkOverlay(t.TempDir(), definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "benchmark harness path must be relative") {
		t.Fatalf("expected invalid harness path rejection, got %v", err)
	}
}

func TestApplyBenchmarkOverlayReturnsRepoPathResolutionErrorWhenWorkingDirRemoved(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	withRemovedWorkingDir(t, func() {
		err := applyBenchmarkOverlay(".", definitionPath, definition)
		if err == nil || !strings.Contains(err.Error(), "open benchmark repo root") {
			t.Fatalf("expected repo root open error, got %v", err)
		}
	})
}

func TestApplyBenchmarkOverlayReturnsCloseError(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
	openCanonicalWriteRoot = func(string) (confinedWriteRoot, error) {
		return &fakeConfinedWriteRoot{closeErr: errors.New("close failed")}, nil
	}
	defer func() { openCanonicalWriteRoot = originalOpenCanonicalWriteRoot }()

	err := applyBenchmarkOverlay(t.TempDir(), definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestRunResolveCommandReturnsReadWrittenDefinitionErrorWhenWorkingDirRemoved(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
		},
		commit: true,
	})
	withRemovedWorkingDir(t, func() {
		err := runResolveCommand([]string{
			"-repo", headRepo,
			"-package", "./benchpkg",
			"-count", "1",
			"-benchtime", "1x",
			"-out", "definition.json",
		})
		if err == nil || !strings.Contains(err.Error(), "read written benchmark definition") {
			t.Fatalf("expected manifest readback error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsRootForCanonicalDirectory(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temp root: %v", err)
	}
	root, err := openManifestRootNoFollow(rootDir)
	if err != nil {
		t.Fatalf("openManifestRootNoFollow returned error: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func TestResolveManifestLayoutReturnsDefinitionPathResolutionError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absCalls := 0
		absPath = func(path string) (string, error) {
			absCalls++
			if absCalls == 1 {
				return "", errors.New("resolve definition path failed")
			}
			return filepath.Abs(path)
		}

		_, _, _, _, err := resolveManifestLayout("definition.json", "overlay")
		if err == nil || !strings.Contains(err.Error(), "resolve benchmark definition path: resolve definition path failed") {
			t.Fatalf("expected definition path resolution error, got %v", err)
		}
	})
}

func TestResolveManifestLayoutReturnsOverlayPathResolutionError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absCalls := 0
		absPath = func(path string) (string, error) {
			absCalls++
			if absCalls == 2 {
				return "", errors.New("resolve overlay path failed")
			}
			return filepath.Abs(path)
		}

		_, _, _, _, err := resolveManifestLayout(filepath.Join(t.TempDir(), "definition.json"), "overlay")
		if err == nil || !strings.Contains(err.Error(), "resolve benchmark overlay path: resolve overlay path failed") {
			t.Fatalf("expected overlay path resolution error, got %v", err)
		}
	})
}

func TestResolveManifestLayoutReturnsOverlayRelativeError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		relPath = func(string, string) (string, error) {
			return "", errors.New("relative path failed")
		}

		_, _, _, _, err := resolveManifestLayout(filepath.Join(t.TempDir(), "definition.json"), filepath.Join(t.TempDir(), "overlay"))
		if err == nil || !strings.Contains(err.Error(), "resolve benchmark overlay path: relative path failed") {
			t.Fatalf("expected overlay relative-path error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsRootPathResolutionError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return "", errors.New("resolve root failed")
		}

		root, err := openManifestRootNoFollow("manifest-root")
		if root != nil {
			t.Fatal("expected nil root on path resolution failure")
		}
		if err == nil || !strings.Contains(err.Error(), "resolve root path: resolve root failed") {
			t.Fatalf("expected root path resolution error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsRelativePathError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		relPath = func(string, string) (string, error) {
			return "", errors.New("relative root failed")
		}

		root, err := openManifestRootNoFollow(filepath.Join(t.TempDir(), "manifest-root"))
		if root != nil {
			t.Fatal("expected nil root on relative-path failure")
		}
		if err == nil || !strings.Contains(err.Error(), "relative root failed") {
			t.Fatalf("expected relative-path error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		openOSRoot = func(string) (*os.Root, error) {
			return nil, errors.New("open volume root failed")
		}

		root, err := openManifestRootNoFollow(filepath.Join(t.TempDir(), "manifest-root"))
		if root != nil {
			t.Fatal("expected nil root on open failure")
		}
		if err == nil || !strings.Contains(err.Error(), "open volume root failed") {
			t.Fatalf("expected volume-root open error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowSkipsCurrentDirectoryComponent(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		actualRelPath := relPath
		relPath = func(base, target string) (string, error) {
			rel, err := actualRelPath(base, target)
			if err != nil {
				return "", err
			}
			return filepath.Join(".", rel), nil
		}

		rootDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("canonicalize temp root: %v", err)
		}
		root, err := openManifestRootNoFollow(filepath.Join(rootDir, "nested"))
		if err == nil {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}
		if err == nil || !strings.Contains(err.Error(), "no such file or directory") {
			t.Fatalf("expected nested lookup after dot-prefix path, got root=%v err=%v", root, err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsCloseErrorAfterOpeningChild(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("canonicalize temp root: %v", err)
		}
		childDir := filepath.Join(rootDir, "nested")
		if err := os.MkdirAll(childDir, 0o755); err != nil {
			t.Fatalf("mkdir child dir: %v", err)
		}
		closeCalls := 0
		closeOSRoot = func(root *os.Root) error {
			closeCalls++
			if closeCalls == 1 {
				return errors.New("close parent failed")
			}
			return root.Close()
		}

		root, err := openManifestRootNoFollow(childDir)
		if root != nil {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}
		if err == nil || !strings.Contains(err.Error(), "close parent failed") {
			t.Fatalf("expected parent close error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowRejectsSymlinkedPathComponent(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	parent := t.TempDir()
	target := t.TempDir()
	symlinkPath := filepath.Join(parent, "escape")
	if err := os.Symlink(target, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	root, err := openManifestRootNoFollow(filepath.Join(symlinkPath, "child"))
	if root != nil {
		t.Fatal("expected nil root when symlinked path is rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink path rejection, got %v", err)
	}
}

func TestOpenRootChildNoFollowReturnsLstatError(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	child, err := openRootChildNoFollow(root, "missing", "/tmp/missing")
	if child != nil {
		t.Fatal("expected nil child for missing path")
	}
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

func trustedMacOSAliasPath(path string) (string, bool) {
	for _, prefix := range []string{"/private/tmp/", "/private/var/"} {
		if strings.HasPrefix(path, prefix) {
			return strings.TrimPrefix(path, "/private"), true
		}
	}
	if path == "/private/tmp" || path == "/private/var" {
		return strings.TrimPrefix(path, "/private"), true
	}
	return "", false
}

func TestOpenRootChildNoFollowRejectsSymlink(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	rootDir := t.TempDir()
	target := filepath.Join(rootDir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(rootDir, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	child, err := openRootChildNoFollow(root, "escape", filepath.Join(rootDir, "escape"))
	if child != nil {
		t.Fatal("expected nil child for symlink")
	}
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestOpenRootChildNoFollowRejectsFile(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	child, err := openRootChildNoFollow(root, "file.txt", filepath.Join(rootDir, "file.txt"))
	if child != nil {
		t.Fatal("expected nil child for regular file")
	}
	if err == nil || !strings.Contains(err.Error(), "root is not a directory") {
		t.Fatalf("expected non-directory rejection, got %v", err)
	}
}

func TestOpenRootChildNoFollowReturnsOpenRootError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		rootOpenRoot = func(*os.Root, string) (*os.Root, error) {
			return nil, errors.New("open child failed")
		}

		child, err := openRootChildNoFollow(root, "nested", filepath.Join(rootDir, "nested"))
		if child != nil {
			t.Fatal("expected nil child on OpenRoot failure")
		}
		if err == nil || !strings.Contains(err.Error(), "open child failed") {
			t.Fatalf("expected OpenRoot failure, got %v", err)
		}
	})
}

func TestOpenRootChildNoFollowReturnsOpenedInfoError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		realRootLstat := rootLstat
		callCount := 0
		rootLstat = func(root *os.Root, name string) (os.FileInfo, error) {
			callCount++
			if callCount == 2 && name == "." {
				return nil, errors.New("opened lstat failed")
			}
			return realRootLstat(root, name)
		}

		child, err := openRootChildNoFollow(root, "nested", filepath.Join(rootDir, "nested"))
		if child != nil {
			if closeErr := child.Close(); closeErr != nil {
				t.Fatalf("close child root: %v", closeErr)
			}
			t.Fatal("expected nil child on opened-info failure")
		}
		if err == nil || !strings.Contains(err.Error(), "opened lstat failed") {
			t.Fatalf("expected opened-info error, got %v", err)
		}
	})
}

func TestOpenRootChildNoFollowDetectsExternalMutation(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		sameFile = func(os.FileInfo, os.FileInfo) bool { return false }

		child, err := openRootChildNoFollow(root, "nested", filepath.Join(rootDir, "nested"))
		if child != nil {
			if closeErr := child.Close(); closeErr != nil {
				t.Fatalf("close child root: %v", closeErr)
			}
			t.Fatal("expected nil child on SameFile mismatch")
		}
		if err == nil || !strings.Contains(err.Error(), "root changed while opening") {
			t.Fatalf("expected SameFile mismatch error, got %v", err)
		}
	})
}

func TestCloseOSRootWithErrorReturnsOriginalError(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	originalErr := errors.New("boom")
	if got := closeOSRootWithError(root, originalErr); !errors.Is(got, originalErr) {
		t.Fatalf("expected original error to be returned, got %v", got)
	}
}

func TestCloseOSRootWithErrorJoinsCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Fatalf("close root: %v", err)
			}
		})
		closeOSRoot = func(*os.Root) error {
			return errors.New("close root failed")
		}

		got := closeOSRootWithError(root, errors.New("boom"))
		if got == nil || !strings.Contains(got.Error(), "boom") || !strings.Contains(got.Error(), "close root failed") {
			t.Fatalf("expected joined close error, got %v", got)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsNilForEmptyParts(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := removeManifestSubtreeParts(root, nil, "overlay"); err != nil {
		t.Fatalf("expected empty parts to be ignored, got %v", err)
	}
}

func TestRemoveManifestSubtreePartsReturnsRootError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		rootLstat = func(*os.Root, string) (os.FileInfo, error) {
			return nil, errors.New("lstat root failed")
		}

		if err := removeManifestSubtreeParts(root, []string{"overlay"}, "overlay"); err == nil || !strings.Contains(err.Error(), "lstat root failed") {
			t.Fatalf("expected root lstat error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsRejectsNestedSymlink(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	target := filepath.Join(rootDir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(rootDir, "overlay", "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	err = removeManifestSubtreeParts(root, []string{"overlay", "escape", "child"}, "overlay/escape/child")
	if err == nil || !strings.Contains(err.Error(), "overlay path contains symlink") {
		t.Fatalf("expected nested symlink rejection, got %v", err)
	}
}

func TestRemoveManifestSubtreePartsRejectsNestedFileComponent(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "overlay"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write overlay file: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	err = removeManifestSubtreeParts(root, []string{"overlay", "child"}, "overlay/child")
	if err == nil || !strings.Contains(err.Error(), "overlay path component is not a directory") {
		t.Fatalf("expected nested file rejection, got %v", err)
	}
}

func TestRemoveManifestSubtreePartsRemovesLeafFile(t *testing.T) {
	rootDir := t.TempDir()
	leafPath := filepath.Join(rootDir, "overlay-file")
	if err := os.WriteFile(leafPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write leaf file: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := removeManifestSubtreeParts(root, []string{"overlay-file"}, "overlay-file"); err != nil {
		t.Fatalf("removeManifestSubtreeParts returned error: %v", err)
	}
	if _, err := os.Stat(leafPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected leaf file to be removed, got %v", err)
	}
}

func TestRemoveManifestSubtreePartsReturnsLeafRemoveError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		leafPath := filepath.Join(rootDir, "overlay-file")
		if err := os.WriteFile(leafPath, []byte("x"), 0o600); err != nil {
			t.Fatalf("write leaf file: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		rootRemove = func(*os.Root, string) error {
			return errors.New("remove leaf failed")
		}

		err = removeManifestSubtreeParts(root, []string{"overlay-file"}, "overlay-file")
		if err == nil || !strings.Contains(err.Error(), "remove leaf failed") {
			t.Fatalf("expected leaf remove error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsRemovesDirectoryTree(t *testing.T) {
	rootDir := t.TempDir()
	nestedFile := filepath.Join(rootDir, "overlay", "benchpkg", "head_benchmark_test.go")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o755); err != nil {
		t.Fatalf("mkdir nested directory: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("package benchpkg\n"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := removeManifestSubtreeParts(root, []string{"overlay"}, "overlay"); err != nil {
		t.Fatalf("removeManifestSubtreeParts returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "overlay")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected overlay directory to be removed, got %v", err)
	}
}

func TestRemoveManifestSubtreePartsReturnsNestedOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootDir, "overlay", "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		rootOpenRoot = func(*os.Root, string) (*os.Root, error) {
			return nil, errors.New("open nested failed")
		}

		err = removeManifestSubtreeParts(root, []string{"overlay", "nested"}, "overlay/nested")
		if err == nil || !strings.Contains(err.Error(), "open nested failed") {
			t.Fatalf("expected nested open error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsDirectoryOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		rootOpenRoot = func(*os.Root, string) (*os.Root, error) {
			return nil, errors.New("open overlay failed")
		}

		err = removeManifestSubtreeParts(root, []string{"overlay"}, "overlay")
		if err == nil || !strings.Contains(err.Error(), "open overlay failed") {
			t.Fatalf("expected directory open error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsDirectoryReadError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		tmpFile := filepath.Join(t.TempDir(), "file.txt")
		if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		rootOpen = func(*os.Root, string) (*os.File, error) {
			return os.Open(tmpFile)
		}

		err = removeManifestSubtreeParts(root, []string{"overlay"}, "overlay")
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("expected directory read error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsRecursiveChildError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		nestedDir := filepath.Join(rootDir, "overlay", "child")
		if err := os.MkdirAll(nestedDir, 0o755); err != nil {
			t.Fatalf("mkdir nested child: %v", err)
		}
		if err := os.WriteFile(filepath.Join(nestedDir, "leaf.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write leaf: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		realRootRemove := rootRemove
		rootRemove = func(root *os.Root, name string) error {
			if name == "leaf.txt" {
				return errors.New("remove nested leaf failed")
			}
			return realRootRemove(root, name)
		}

		err = removeManifestSubtreeParts(root, []string{"overlay"}, "overlay")
		if err == nil || !strings.Contains(err.Error(), "remove nested leaf failed") {
			t.Fatalf("expected recursive child error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsChildCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		closeCalls := 0
		closeOSRoot = func(root *os.Root) error {
			closeCalls++
			if closeCalls == 1 {
				return errors.New("close child failed")
			}
			return root.Close()
		}

		err = removeManifestSubtreeParts(root, []string{"overlay"}, "overlay")
		if err == nil || !strings.Contains(err.Error(), "close child failed") {
			t.Fatalf("expected child close error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsRootRemoveErrorAfterDirectoryCleanup(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay: %v", err)
		}
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		realRootRemove := rootRemove
		rootRemove = func(root *os.Root, name string) error {
			if name == "overlay" {
				return errors.New("remove overlay failed")
			}
			return realRootRemove(root, name)
		}

		err = removeManifestSubtreeParts(root, []string{"overlay"}, "overlay")
		if err == nil || !strings.Contains(err.Error(), "remove overlay failed") {
			t.Fatalf("expected root remove error, got %v", err)
		}
	})
}

func writeDefinitionJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write definition json: %v", err)
	}
}

func writeOverlayDefinitionFixture(t *testing.T, artifactDir string) (string, benchmarkDefinition) {
	t.Helper()
	content := []byte("package benchpkg\n")
	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   "deadbeef",
		PackageTargets: []string{"./benchpkg"},
		Benchmarks:     []resolvedBenchmark{{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"}},
		BenchPattern:   "^(BenchmarkHeadOnly)$",
		RunPattern:     "^$",
		Count:          1,
		Benchtime:      "1x",
		BenchMem:       true,
		HarnessFiles: []benchmarkHarnessFile{{
			Path:        "benchpkg/head_benchmark_test.go",
			SHA256:      bytesDigest(content),
			OverlayPath: "benchpkg/head_benchmark_test.go",
		}},
		OverlayDir: "overlay",
	}
	overlayDir := filepath.Join(artifactDir, "overlay")
	definitionPath := filepath.Join(artifactDir, "definition.json")
	if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, map[string][]byte{
		"benchpkg/head_benchmark_test.go": content,
	}); err != nil {
		t.Fatalf("write overlay definition fixture: %v", err)
	}
	return definitionPath, definition
}

func supportsSymlink(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write symlink target probe: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		return false
	}
	return true
}

func withRemovedWorkingDir(t *testing.T, fn func()) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove temp dir: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore working directory: %v", chdirErr)
		}
	}()
	fn()
}

type fakeConfinedWriteRoot struct {
	failOnWrite int
	writes      int
	err         error
	closeErr    error
}

func (f *fakeConfinedWriteRoot) WriteFileCreatingParents(string, []byte, os.FileMode, os.FileMode) error {
	f.writes++
	if f.writes == f.failOnWrite {
		return f.err
	}
	return nil
}

func (f *fakeConfinedWriteRoot) Close() error {
	return f.closeErr
}

func assertFileContainsBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("file %s mode = %o, want %o", path, got, want)
	}
}

func assertNoBenchdeltaTemps(t *testing.T, root, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, pattern))
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no leftover benchdelta temps matching %q, found %v", pattern, matches)
	}
}
