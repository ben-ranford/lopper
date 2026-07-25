package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temp dir: %v", err)
	}
	return dir
}

func TestRunCompareCommandReturnsFlagParseError(t *testing.T) {
	if err := runCompareCommand([]string{"-unknown"}); err == nil {
		t.Fatal("expected flag parse error")
	}
}

func TestRunCompareCommandReturnsHeadParseError(t *testing.T) {
	dir, basePath, _ := writeMatchingBenchmarkFixtures(t)

	err := runCompareCommand([]string{
		"-base", basePath,
		"-head", filepath.Join(dir, "missing.txt"),
	})
	if err == nil || !strings.Contains(err.Error(), "parse head benchmarks") {
		t.Fatalf("expected head benchmark parse error, got %v", err)
	}
}

func TestRunCompareCommandReturnsNilForMatchingBenchmarks(t *testing.T) {
	_, basePath, headPath := writeMatchingBenchmarkFixtures(t)

	if err := runCompareCommand([]string{"-base", basePath, "-head", headPath}); err != nil {
		t.Fatalf("runCompareCommand returned error: %v", err)
	}
}

func TestParseBenchmarkFileSkipsInvalidBenchmarkLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.txt")
	lines := []string{
		"goos: darwin",
		"pkg: example.com/benchrepo/benchpkg",
		"BenchmarkBroken-8 1000 25000 ns/op",
		"BenchmarkValid-8 1000 25000 ns/op 128 B/op 3 allocs/op",
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write benchmark file: %v", err)
	}

	data, err := parseBenchmarkFile(path)
	if err != nil {
		t.Fatalf("parseBenchmarkFile returned error: %v", err)
	}
	if len(data.data) != 1 {
		t.Fatalf("benchmark entries = %#v, want 1", data.data)
	}
}

func TestParseBenchmarkFileReturnsScannerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.txt")
	content := "pkg: example.com/benchrepo/benchpkg\n" + strings.Repeat("a", 70*1024) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write oversized benchmark file: %v", err)
	}

	if _, err := parseBenchmarkFile(path); err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("expected scanner error, got %v", err)
	}
}

func TestRunResolveCommandDefaultsOverlayDir(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
		},
		commit: true,
	})
	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "artifacts", "definition.json")

	if err := runResolveCommand([]string{
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", definitionPath,
	}); err != nil {
		t.Fatalf("runResolveCommand returned error: %v", err)
	}

	definition, _, err := readBenchmarkDefinition(definitionPath)
	if err != nil {
		t.Fatalf("readBenchmarkDefinition returned error: %v", err)
	}
	if definition.OverlayDir != defaultOverlayDir {
		t.Fatalf("overlay dir = %q, want %q", definition.OverlayDir, defaultOverlayDir)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(definitionPath), defaultOverlayDir, "benchpkg", "head_benchmark_test.go")); err != nil {
		t.Fatalf("expected default overlay file to exist: %v", err)
	}
}

func TestRunResolveCommandWrapsResolutionError(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
		},
	})

	err := runResolveCommand([]string{
		"-repo", repo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(t.TempDir(), "definition.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "resolve benchmark definition: resolve HEAD commit") {
		t.Fatalf("expected wrapped resolution error, got %v", err)
	}
}

func TestRunResolveCommandReturnsWriteError(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
		},
		commit: true,
	})
	blocker := filepath.Join(canonicalTempDir(t), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := runResolveCommand([]string{
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", filepath.Join(blocker, "definition.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "root is not a directory") {
		t.Fatalf("expected definition write error, got %v", err)
	}
}

func TestRunBenchmarkCommandReturnsApplyError(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
		},
		commit: true,
	})
	definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolveBenchmarkDefinition returned error: %v", err)
	}

	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "definition.json")
	overlayDir := filepath.Join(artifactDir, "overlay")
	definition.OverlayDir = filepath.Base(overlayDir)
	if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
		t.Fatalf("writeBenchmarkDefinition returned error: %v", err)
	}
	if err := os.Remove(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go")); err != nil {
		t.Fatalf("remove overlay file: %v", err)
	}

	err = runBenchmarkCommand([]string{
		"-repo", t.TempDir(),
		"-definition", definitionPath,
	})
	if err == nil || !strings.Contains(err.Error(), "apply benchmark definition: read overlay file") {
		t.Fatalf("expected wrapped apply error, got %v", err)
	}
}

func TestMainReturnsAfterResolveCommand(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
		},
		commit: true,
	})
	definitionPath := filepath.Join(canonicalTempDir(t), "definition.json")
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{
		oldArgs[0],
		"resolve",
		"-repo", headRepo,
		"-package", "./benchpkg",
		"-count", "1",
		"-benchtime", "1x",
		"-out", definitionPath,
	}

	main()

	if _, err := os.Stat(definitionPath); err != nil {
		t.Fatalf("expected definition output from main resolve path: %v", err)
	}
}

func TestMainReturnsAfterRunCommand(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
		},
		commit: true,
	})
	definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolveBenchmarkDefinition returned error: %v", err)
	}
	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "definition.json")
	overlayDir := filepath.Join(artifactDir, "overlay")
	definition.OverlayDir = filepath.Base(overlayDir)
	if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
		t.Fatalf("writeBenchmarkDefinition returned error: %v", err)
	}

	baseRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
		},
	})
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{
		oldArgs[0],
		"run",
		"-repo", baseRepo,
		"-definition", definitionPath,
	}

	main()
}

func TestResolveBenchmarkDefinitionDeduplicatesHarnessFiles(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkZulu(b *testing.B) {}
func BenchmarkAlpha(b *testing.B) {}
`,
		},
		commit: true,
	})

	definition, overlayFiles, err := resolveBenchmarkDefinition(repo, []string{"./benchpkg", "./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolveBenchmarkDefinition returned error: %v", err)
	}

	if len(definition.HarnessFiles) != 1 {
		t.Fatalf("harness files = %#v, want 1", definition.HarnessFiles)
	}
	if len(overlayFiles) != 1 {
		t.Fatalf("overlay files = %#v, want 1", overlayFiles)
	}
	names := make([]string, 0, len(definition.Benchmarks))
	for _, benchmark := range definition.Benchmarks {
		names = append(names, benchmark.Name)
	}
	if want := []string{"BenchmarkAlpha", "BenchmarkAlpha", "BenchmarkZulu", "BenchmarkZulu"}; !slices.Equal(names, want) {
		t.Fatalf("benchmark names = %#v, want %#v", names, want)
	}
	if definition.BenchPattern != "^(BenchmarkAlpha|BenchmarkZulu)$" {
		t.Fatalf("bench pattern = %q", definition.BenchPattern)
	}
}

func TestResolvePackageBenchmarksIncludesHelperOnlyAndTestMainFiles(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/bench_test.go": `package benchpkg

import "testing"

func BenchmarkValid(b *testing.B) {}
`,
			"benchpkg/goleak_test.go": `package benchpkg

import "testing"

func TestMain(m *testing.M) {
	testing.Init()
	m.Run()
}
`,
			"benchpkg/helpers_test.go": `package benchpkg

func benchmarkHelperName() string { return "helper" }
`,
		},
	})

	harnesses, benchmarks, err := resolvePackageBenchmarks(repo, "./benchpkg")
	if err != nil {
		t.Fatalf("resolvePackageBenchmarks returned error: %v", err)
	}

	wantHarnessPaths := []string{
		"benchpkg/bench_test.go",
		"benchpkg/goleak_test.go",
		"benchpkg/helpers_test.go",
	}
	gotHarnessPaths := make([]string, 0, len(harnesses))
	for _, harness := range harnesses {
		gotHarnessPaths = append(gotHarnessPaths, harness.Path)
	}
	if !slices.Equal(gotHarnessPaths, wantHarnessPaths) {
		t.Fatalf("harness paths = %#v, want %#v", gotHarnessPaths, wantHarnessPaths)
	}
	if want := []string{"BenchmarkValid"}; !slices.Equal(benchmarks, want) {
		t.Fatalf("benchmarks = %#v, want %#v", benchmarks, want)
	}
}

func TestResolvePackageBenchmarksRejectsRootTarget(t *testing.T) {
	if _, _, err := resolvePackageBenchmarks(t.TempDir(), "."); err == nil || !strings.Contains(err.Error(), "repo root") {
		t.Fatalf("expected repo root rejection, got %v", err)
	}
}

func TestBenchmarkFunctionsInFileSkipsStarIdentParameter(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkStarIdent(b *testing) {}
func BenchmarkValid(b *testing.B) {}
`,
		},
	})

	names, err := benchmarkFunctionsInFile(filepath.Join(repo, "benchpkg", "head_benchmark_test.go"))
	if err != nil {
		t.Fatalf("benchmarkFunctionsInFile returned error: %v", err)
	}
	if want := []string{"BenchmarkValid"}; !slices.Equal(names, want) {
		t.Fatalf("benchmark names = %#v, want %#v", names, want)
	}
}

func TestWriteBenchmarkDefinitionReturnsOverlayCreateError(t *testing.T) {
	parent := canonicalTempDir(t)
	blocker := filepath.Join(parent, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(blocker, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || (!strings.Contains(err.Error(), "clear benchmark overlay") && !strings.Contains(err.Error(), "create benchmark overlay")) {
		t.Fatalf("expected overlay setup error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsEmptyOverlayDirError(t *testing.T) {
	err := writeBenchmarkDefinition(filepath.Join(canonicalTempDir(t), "definition.json"), "", benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || !strings.Contains(err.Error(), "overlay directory escapes its root") {
		t.Fatalf("expected empty overlay dir error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsOverlaySubdirCreateError(t *testing.T) {
	parent := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(parent, "blocked"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, map[string][]byte{"../blocked/child.go": []byte("package benchpkg\n")})
	if err == nil || !strings.Contains(err.Error(), "overlay file path escapes its root") {
		t.Fatalf("expected overlay subdir create error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsOverlayWriteError(t *testing.T) {
	parent := canonicalTempDir(t)
	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, map[string][]byte{".": []byte("package benchpkg\n")})
	if err == nil || !strings.Contains(err.Error(), "overlay file path must not resolve to the root directory") {
		t.Fatalf("expected overlay write error, got %v", err)
	}
}

func TestWriteBenchmarkDefinitionReturnsManifestWriteError(t *testing.T) {
	parent := canonicalTempDir(t)
	outPath := filepath.Join(parent, "definition.json")
	if err := os.MkdirAll(outPath, 0o755); err != nil {
		t.Fatalf("mkdir out path: %v", err)
	}

	err := writeBenchmarkDefinition(outPath, filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || !strings.Contains(err.Error(), "write benchmark definition") {
		t.Fatalf("expected manifest write error, got %v", err)
	}
}

func TestBenchmarkFunctionsInFileReturnsFileTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized_benchmark_test.go")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxBenchmarkHarnessBytes+1)), 0o600); err != nil {
		t.Fatalf("write oversized benchmark file: %v", err)
	}

	_, err := benchmarkFunctionsInFile(path)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestOpenPackageTargetRootNoFollowReturnsRelativePathError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		repoRootHandle, err := os.OpenRoot(repoRoot)
		if err != nil {
			t.Fatalf("open repo root: %v", err)
		}
		defer func() {
			if closeErr := repoRootHandle.Close(); closeErr != nil {
				t.Fatalf("close repo root: %v", closeErr)
			}
		}()

		relPath = func(string, string) (string, error) {
			return "", errors.New("relative package path failed")
		}

		_, _, packageRoot, err := openPackageTargetRootNoFollow(repoRoot, repoRootHandle, "./benchpkg")
		if packageRoot != nil {
			t.Fatal("expected nil package root on relative-path failure")
		}
		if err == nil || !strings.Contains(err.Error(), "relative package path failed") {
			t.Fatalf("expected relative-path error, got %v", err)
		}
	})
}

func TestOpenPackageTargetRootNoFollowReturnsOwnedRootCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repoRoot, "pkgroot", "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir package root: %v", err)
		}
		repoRootHandle, err := os.OpenRoot(repoRoot)
		if err != nil {
			t.Fatalf("open repo root: %v", err)
		}
		defer func() {
			if closeErr := repoRootHandle.Close(); closeErr != nil {
				t.Fatalf("close repo root: %v", closeErr)
			}
		}()

		closeCalls := 0
		closeOSRoot = func(root *os.Root) error {
			closeCalls++
			if closeCalls == 1 {
				return errors.New("close owned root failed")
			}
			return root.Close()
		}

		_, _, packageRoot, err := openPackageTargetRootNoFollow(repoRoot, repoRootHandle, "./pkgroot/benchpkg")
		if packageRoot != nil {
			t.Fatal("expected nil package root on owned-root close failure")
		}
		if err == nil || !strings.Contains(err.Error(), "close owned root failed") {
			t.Fatalf("expected owned-root close error, got %v", err)
		}
	})
}

func TestReadRootDirReturnsOpenError(t *testing.T) {
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

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return nil, errors.New("open dir failed")
		}

		if _, err := readRootDir(root); err == nil || !strings.Contains(err.Error(), "open dir failed") {
			t.Fatalf("expected readRootDir open error, got %v", err)
		}
	})
}

func TestReadCapturedHarnessBytesReturnsTotalLimitError(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "bench_test.go"), []byte("package benchpkg\n"), 0o600); err != nil {
		t.Fatalf("write harness file: %v", err)
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

	remainingBytes := int64(4)
	_, err = readCapturedHarnessBytes(root, "bench_test.go", &remainingBytes)
	if err == nil || !strings.Contains(err.Error(), "captured benchmark harness bytes exceed total limit") {
		t.Fatalf("expected total-limit error, got %v", err)
	}
}

func TestReadRootFileLimitReturnsOpenError(t *testing.T) {
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

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return nil, errors.New("open file failed")
		}

		if _, err := readRootFileLimit(root, "bench_test.go", 16); err == nil || !strings.Contains(err.Error(), "open file failed") {
			t.Fatalf("expected root open error, got %v", err)
		}
	})
}

func TestReadRootFileLimitReturnsFileTooLargeFromStat(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "bench_test.go"), []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatalf("write harness file: %v", err)
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

	_, err = readRootFileLimit(root, "bench_test.go", 8)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestCreateBenchmarkTempDirReturnsError(t *testing.T) {
	if _, err := createBenchmarkTempDir(filepath.Join(t.TempDir(), "missing"), ".benchdelta-test-"); err == nil {
		t.Fatal("expected temp-dir creation error")
	}
}

func TestCaptureBenchmarkTargetSnapshotReturnsMissingSnapshot(t *testing.T) {
	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	snapshot, err := captureBenchmarkTargetSnapshot(root, filepath.Join(rootDir, "benchpkg", "bench_test.go"), "benchpkg/bench_test.go")
	if err != nil {
		t.Fatalf("captureBenchmarkTargetSnapshot returned error: %v", err)
	}
	if snapshot.existed {
		t.Fatalf("snapshot.existed = %v, want false", snapshot.existed)
	}
	if snapshot.mode != 0o600 {
		t.Fatalf("snapshot.mode = %o, want %o", snapshot.mode, 0o600)
	}
}

func TestCaptureBenchmarkTargetSnapshotRejectsNonRegularTarget(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir package dir: %v", err)
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

	_, err = captureBenchmarkTargetSnapshot(root, filepath.Join(rootDir, "benchpkg"), "benchpkg")
	if err == nil || !strings.Contains(err.Error(), "target path is not a regular file") {
		t.Fatalf("expected non-regular target rejection, got %v", err)
	}
}

func TestCaptureBenchmarkTargetSnapshotReturnsOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(rootDir, "bench_test.go"), []byte("package benchpkg\n"), 0o600); err != nil {
			t.Fatalf("write harness file: %v", err)
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

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return nil, errors.New("open snapshot target failed")
		}

		_, err = captureBenchmarkTargetSnapshot(root, filepath.Join(rootDir, "bench_test.go"), "bench_test.go")
		if err == nil || !strings.Contains(err.Error(), "open snapshot target failed") {
			t.Fatalf("expected snapshot open error, got %v", err)
		}
	})
}

func TestPromoteBenchmarkSetReturnsLivePathLookupError(t *testing.T) {
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
			return nil, errors.New("live path lookup failed")
		}

		promotions := []benchmarkPromotedPath{{
			liveRel:    "overlay",
			stagedRel:  "stage/overlay",
			backupRel:  "backup/overlay",
			errorLabel: "clear benchmark overlay",
		}}
		err = promoteBenchmarkSet(root, promotions, nil)
		if err == nil || !strings.Contains(err.Error(), "clear benchmark overlay: live path lookup failed") {
			t.Fatalf("expected live-path lookup error, got %v", err)
		}
	})
}

func TestBenchmarkRollbackCleanupJoinsRollbackError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootDir, "live"), 0o755); err != nil {
			t.Fatalf("mkdir live dir: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(rootDir, "backup"), 0o755); err != nil {
			t.Fatalf("mkdir backup dir: %v", err)
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

		rootRename = func(*os.Root, string, string) error {
			return errors.New("rollback rename failed")
		}

		promotions := []benchmarkPromotedPath{{
			liveRel:      "live/current",
			discardRel:   "discard/current",
			backupRel:    "backup/current",
			promoted:     true,
			backupExists: true,
		}}
		err = benchmarkRollbackCleanup(root, promotions, 0, errors.New("promote failed"))
		if err == nil || !strings.Contains(err.Error(), "promote failed") || !strings.Contains(err.Error(), "rollback rename failed") {
			t.Fatalf("expected joined rollback cleanup error, got %v", err)
		}
	})
}

func TestCleanupBenchmarkSetRootsSkipsEmptyPathAndJoinsErrors(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay dir: %v", err)
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
			return errors.New("cleanup remove failed")
		}

		err = cleanupBenchmarkSetRoots(root, "", "overlay")
		if err == nil || !strings.Contains(err.Error(), "cleanup remove failed") {
			t.Fatalf("expected cleanup error, got %v", err)
		}
	})
}

func TestValidateManifestRootPathNoFollowReturnsMissingDescendantNil(t *testing.T) {
	rootDir := t.TempDir()
	if err := validateManifestRootPathNoFollow(filepath.Join(rootDir, "missing", "child")); err != nil {
		t.Fatalf("expected missing descendant to be allowed, got %v", err)
	}
}

func TestValidateManifestRootPathNoFollowRejectsNonDirectoryDescendant(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file descendant: %v", err)
	}

	err := validateManifestRootPathNoFollow(filepath.Join(rootDir, "file", "child"))
	if err == nil || !strings.Contains(err.Error(), "root is not a directory") {
		t.Fatalf("expected non-directory descendant rejection, got %v", err)
	}
}

func TestCanonicalizeTrustedManifestRootReturnsAliasRoot(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) { return "/tmp", nil }
		evalSymlinks = func(string) (string, error) { return "/private/tmp", nil }
		relPath = func(string, string) (string, error) { return ".", nil }

		got, err := canonicalizeTrustedManifestRoot("ignored")
		if err != nil {
			t.Fatalf("canonicalizeTrustedManifestRoot returned error: %v", err)
		}
		if got != "/private/tmp" {
			t.Fatalf("canonicalized root = %q, want %q", got, "/private/tmp")
		}
	})
}

func TestCanonicalizeTrustedManifestRootReturnsAliasEvalError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) { return "/tmp/work", nil }
		evalSymlinks = func(string) (string, error) { return "", errors.New("alias eval failed") }

		if _, err := canonicalizeTrustedManifestRoot("ignored"); err == nil || !strings.Contains(err.Error(), "alias eval failed") {
			t.Fatalf("expected alias eval error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsVolumeRoot(t *testing.T) {
	root, err := openManifestRootNoFollow(string(os.PathSeparator))
	if err != nil {
		t.Fatalf("openManifestRootNoFollow returned error: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func TestValidateManifestRootPathNoFollowReturnsRelativePathError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		relPath = func(string, string) (string, error) {
			return "", errors.New("manifest relative failed")
		}

		err := validateManifestRootPathNoFollow(filepath.Join(rootDir, "nested"))
		if err == nil || !strings.Contains(err.Error(), "manifest relative failed") {
			t.Fatalf("expected relative-path error, got %v", err)
		}
	})
}

func TestValidateManifestRootPathNoFollowReturnsCanonicalizeError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return "", errors.New("manifest resolve failed")
		}

		err := validateManifestRootPathNoFollow("ignored")
		if err == nil || !strings.Contains(err.Error(), "resolve root path: manifest resolve failed") {
			t.Fatalf("expected canonicalize error, got %v", err)
		}
	})
}

func TestReadRootFileLimitReturnsReadError(t *testing.T) {
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

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return os.Open(t.TempDir())
		}

		if _, err := readRootFileLimit(root, "bench_test.go", 16); err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Fatalf("expected read error, got %v", err)
		}
	})
}

func TestExistingRegularFilePermRejectsSymlink(t *testing.T) {
	if !supportsSymlink(t) {
		t.Skip("symlinks unavailable")
	}

	rootDir := t.TempDir()
	target := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	link := filepath.Join(rootDir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if _, err := existingRegularFilePerm(link, 0o600); err == nil || !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestPromoteBenchmarkSetReturnsBackupRenameError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay dir: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(rootDir, "backup"), 0o755); err != nil {
			t.Fatalf("mkdir backup dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rootDir, "overlay", "live.txt"), []byte("live"), 0o600); err != nil {
			t.Fatalf("write live file: %v", err)
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

		realRootRename := rootRename
		rootRename = func(root *os.Root, oldName, newName string) error {
			if oldName == filepath.Clean("overlay/live.txt") && newName == filepath.Clean("backup/live.txt") {
				return errors.New("backup rename failed")
			}
			return realRootRename(root, oldName, newName)
		}

		promotions := []benchmarkPromotedPath{{
			liveRel:    "overlay/live.txt",
			stagedRel:  "stage/live.txt",
			backupRel:  "backup/live.txt",
			errorLabel: "write benchmark harness overlay/live.txt",
		}}
		err = promoteBenchmarkSet(root, promotions, nil)
		if err == nil || !strings.Contains(err.Error(), "backup rename failed") {
			t.Fatalf("expected backup rename error, got %v", err)
		}
	})
}

func TestResolveBenchmarkDefinitionReturnsRepoPathError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return "", errors.New("resolve repo failed")
		}

		if _, _, err := resolveBenchmarkDefinition(".", []string{"./benchpkg"}, 1, "1x"); err == nil || !strings.Contains(err.Error(), "resolve repo path: resolve repo failed") {
			t.Fatalf("expected repo path error, got %v", err)
		}
	})
}

func TestResolveBenchmarkDefinitionReturnsOpenRepoRootError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
			},
			commit: true,
		})
		openOSRoot = func(string) (*os.Root, error) {
			return nil, errors.New("open repo root failed")
		}

		if _, _, err := resolveBenchmarkDefinition(repo, []string{"./benchpkg"}, 1, "1x"); err == nil || !strings.Contains(err.Error(), "open repo root: open repo root failed") {
			t.Fatalf("expected repo root open error, got %v", err)
		}
	})
}

func TestResolvePackageBenchmarksReturnsOpenRepoRootError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		openOSRoot = func(string) (*os.Root, error) {
			return nil, errors.New("open package repo root failed")
		}

		if _, _, err := resolvePackageBenchmarks(t.TempDir(), "./benchpkg"); err == nil || !strings.Contains(err.Error(), "open repo root: open package repo root failed") {
			t.Fatalf("expected resolvePackageBenchmarks open error, got %v", err)
		}
	})
}

func TestResolvePackageBenchmarksReturnsCloseError(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
		},
	})

	withBenchdeltaFSHooks(t, func() {
		closeCalls := 0
		closeOSRoot = func(root *os.Root) error {
			closeCalls++
			if closeCalls == 1 {
				return errors.New("close package root failed")
			}
			return root.Close()
		}

		if _, _, err := resolvePackageBenchmarks(repo, "./benchpkg"); err == nil || !strings.Contains(err.Error(), "close package root failed") {
			t.Fatalf("expected close error, got %v", err)
		}
	})
}

func TestResolvePackageBenchmarksWithinRootRejectsNonRegularHarness(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "benchpkg", "dir_test.go"), 0o755); err != nil {
		t.Fatalf("mkdir invalid harness dir: %v", err)
	}
	repoRootHandle, err := os.OpenRoot(repoRoot)
	if err != nil {
		t.Fatalf("open repo root: %v", err)
	}
	defer func() {
		if closeErr := repoRootHandle.Close(); closeErr != nil {
			t.Fatalf("close repo root: %v", closeErr)
		}
	}()

	remainingBytes := int64(maxBenchmarkHarnessTotal)
	if _, _, _, err := resolvePackageBenchmarksWithinRoot(repoRoot, repoRootHandle, "./benchpkg", &remainingBytes); err == nil || !strings.Contains(err.Error(), "benchmark harness path is not a regular file") {
		t.Fatalf("expected non-regular harness rejection, got %v", err)
	}
}

func TestResolvePackageBenchmarksWithinRootReturnsReadHarnessError(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir package dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "benchpkg", "head_benchmark_test.go"), []byte("package benchpkg\n"), 0o600); err != nil {
		t.Fatalf("write harness file: %v", err)
	}
	repoRootHandle, err := os.OpenRoot(repoRoot)
	if err != nil {
		t.Fatalf("open repo root: %v", err)
	}
	defer func() {
		if closeErr := repoRootHandle.Close(); closeErr != nil {
			t.Fatalf("close repo root: %v", closeErr)
		}
	}()

	remainingBytes := int64(1)
	if _, _, _, err := resolvePackageBenchmarksWithinRoot(repoRoot, repoRootHandle, "./benchpkg", &remainingBytes); err == nil || !strings.Contains(err.Error(), "read benchmark harness") {
		t.Fatalf("expected read harness error, got %v", err)
	}
}

func TestValidateBenchmarkHarnessTargetsRejectsNonTestHarnessPath(t *testing.T) {
	err := validateBenchmarkHarnessTargets(t.TempDir(), benchmarkDefinition{
		PackageTargets: []string{"./benchpkg"},
		HarnessFiles: []benchmarkHarnessFile{{
			Path: "benchpkg/helper.go",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "must reference a package _test.go file") {
		t.Fatalf("expected non-test harness rejection, got %v", err)
	}
}

func TestValidateBenchmarkHarnessTargetsRejectsHarnessOutsideTargets(t *testing.T) {
	err := validateBenchmarkHarnessTargets(t.TempDir(), benchmarkDefinition{
		PackageTargets: []string{"./benchpkg"},
		HarnessFiles: []benchmarkHarnessFile{{
			Path: "otherpkg/head_benchmark_test.go",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside benchmark package targets") {
		t.Fatalf("expected outside-target rejection, got %v", err)
	}
}

func TestValidateBenchmarkHarnessTargetsReturnsPackageReadError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repoRoot, "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir package dir: %v", err)
		}
		readDir = func(string) ([]os.DirEntry, error) {
			return nil, errors.New("read package failed")
		}

		err := validateBenchmarkHarnessTargets(repoRoot, benchmarkDefinition{
			PackageTargets: []string{"./benchpkg"},
			HarnessFiles: []benchmarkHarnessFile{{
				Path: "benchpkg/head_benchmark_test.go",
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "read package ./benchpkg: read package failed") {
			t.Fatalf("expected package read error, got %v", err)
		}
	})
}

func TestValidateBenchmarkHarnessTargetsRejectsPackageDirectoryFile(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "benchpkg"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write package file: %v", err)
	}

	err := validateBenchmarkHarnessTargets(repoRoot, benchmarkDefinition{
		PackageTargets: []string{"./benchpkg"},
		HarnessFiles: []benchmarkHarnessFile{{
			Path: "benchpkg/head_benchmark_test.go",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "package directory is not a directory") {
		t.Fatalf("expected package-directory file rejection, got %v", err)
	}
}

func TestGitStatusPorcelainV2HasPathsReturnsFalseForEmptyPaths(t *testing.T) {
	dirty, err := gitStatusPorcelainV2HasPaths(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("gitStatusPorcelainV2HasPaths returned error: %v", err)
	}
	if dirty {
		t.Fatal("expected empty path list to be clean")
	}
}

func TestGitStatusPorcelainV2HasPathsReturnsErrorForInvalidRepo(t *testing.T) {
	if _, err := gitStatusPorcelainV2HasPaths(t.TempDir(), []string{"benchpkg/head_benchmark_test.go"}); err == nil {
		t.Fatal("expected git status error for non-repo path")
	}
}

func TestGitStatusPorcelainV2HasPathsDetectsDirtyHarness(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
		},
		commit: true,
	})
	if err := os.WriteFile(filepath.Join(repo, "benchpkg", "head_benchmark_test.go"), []byte("package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkDirty(b *testing.B) {}\n"), 0o600); err != nil {
		t.Fatalf("rewrite harness file: %v", err)
	}

	dirty, err := gitStatusPorcelainV2HasPaths(repo, []string{"benchpkg/head_benchmark_test.go"})
	if err != nil {
		t.Fatalf("gitStatusPorcelainV2HasPaths returned error: %v", err)
	}
	if !dirty {
		t.Fatal("expected dirty harness to be reported")
	}
}

func TestOpenCanonicalWriteRootReturnsCanonicalizeError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
		openCanonicalWriteRoot = originalOpenCanonicalWriteRoot
		evalSymlinks = func(string) (string, error) {
			return "", errors.New("canonicalize write root failed")
		}

		root, err := openCanonicalWriteRoot(t.TempDir())
		if root != nil {
			t.Fatal("expected nil write root on canonicalize failure")
		}
		if err == nil || !strings.Contains(err.Error(), "canonicalize root: canonicalize write root failed") {
			t.Fatalf("expected canonicalize error, got %v", err)
		}
	})
}

func TestOpenPackageTargetRootNoFollowRejectsRootRelativePath(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		repoRootHandle, err := os.OpenRoot(repoRoot)
		if err != nil {
			t.Fatalf("open repo root: %v", err)
		}
		defer func() {
			if closeErr := repoRootHandle.Close(); closeErr != nil {
				t.Fatalf("close repo root: %v", closeErr)
			}
		}()

		relPath = func(string, string) (string, error) { return ".", nil }

		if _, _, packageRoot, err := openPackageTargetRootNoFollow(repoRoot, repoRootHandle, "./benchpkg"); err == nil || !strings.Contains(err.Error(), "must not resolve to repo root") || packageRoot != nil {
			t.Fatalf("expected repo-root rejection, got root=%v err=%v", packageRoot, err)
		}
	})
}

func TestOpenPackageTargetRootNoFollowSkipsCurrentDirectoryPathPart(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repoRoot, "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir package dir: %v", err)
		}
		repoRootHandle, err := os.OpenRoot(repoRoot)
		if err != nil {
			t.Fatalf("open repo root: %v", err)
		}
		defer func() {
			if closeErr := repoRootHandle.Close(); closeErr != nil {
				t.Fatalf("close repo root: %v", closeErr)
			}
		}()

		relPath = func(string, string) (string, error) { return filepath.Join(".", "benchpkg"), nil }

		packageDir, packageRel, packageRoot, err := openPackageTargetRootNoFollow(repoRoot, repoRootHandle, "./benchpkg")
		if err != nil {
			t.Fatalf("openPackageTargetRootNoFollow returned error: %v", err)
		}
		if packageDir != filepath.Join(repoRoot, "benchpkg") || packageRel != filepath.Join(".", "benchpkg") {
			t.Fatalf("unexpected package resolution: dir=%q rel=%q", packageDir, packageRel)
		}
		if err := packageRoot.Close(); err != nil {
			t.Fatalf("close package root: %v", err)
		}
	})
}

func TestReadCapturedHarnessBytesReturnsReadError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		root, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		defer func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}()

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return nil, errors.New("open captured harness failed")
		}

		remainingBytes := int64(maxBenchmarkHarnessTotal)
		if _, err := readCapturedHarnessBytes(root, "bench_test.go", &remainingBytes); err == nil || !strings.Contains(err.Error(), "open captured harness failed") {
			t.Fatalf("expected readCapturedHarnessBytes error, got %v", err)
		}
	})
}

func TestReadRootFileLimitReturnsFileTooLargeFromReader(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	withBenchdeltaFSHooks(t, func() {
		rootOpen = func(*os.Root, string) (*os.File, error) {
			return os.Open("/dev/zero")
		}

		if _, err := readRootFileLimit(root, "bench_test.go", 8); !errors.Is(err, safeio.ErrFileTooLarge) {
			t.Fatalf("expected ErrFileTooLarge from reader limit, got %v", err)
		}
	})
}

func TestCaptureBenchmarkTargetSnapshotReturnsRootStatError(t *testing.T) {
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
			return nil, errors.New("snapshot lstat failed")
		}

		if _, err := captureBenchmarkTargetSnapshot(root, "/tmp/bench_test.go", "bench_test.go"); err == nil || !strings.Contains(err.Error(), "snapshot lstat failed") {
			t.Fatalf("expected snapshot lstat error, got %v", err)
		}
	})
}

func TestCaptureBenchmarkTargetSnapshotReturnsJoinedReadAndCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(rootDir, "bench_test.go"), []byte("package benchpkg\n"), 0o600); err != nil {
			t.Fatalf("write harness file: %v", err)
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

		rootOpen = func(*os.Root, string) (*os.File, error) {
			file, err := os.Open(filepath.Join(rootDir, "bench_test.go"))
			if err != nil {
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			return file, nil
		}

		if _, err := captureBenchmarkTargetSnapshot(root, filepath.Join(rootDir, "bench_test.go"), "bench_test.go"); err == nil || !strings.Contains(err.Error(), "file already closed") {
			t.Fatalf("expected joined read/close error, got %v", err)
		}
	})
}

func TestEnsureRootPathNoFollowReturnsNilForCurrentDirectory(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := ensureRootPathNoFollow(root, ".", true, 0o755); err != nil {
		t.Fatalf("ensureRootPathNoFollow returned error: %v", err)
	}
}

func TestEnsureRootPathNoFollowReturnsLstatErrorWhenCreateDisabled(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := ensureRootPathNoFollow(root, "missing/child", false, 0o755); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

func TestEnsureRootPathNoFollowReturnsCreateError(t *testing.T) {
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

		rootMkdir = func(*os.Root, string, os.FileMode) error {
			return errors.New("mkdir parent failed")
		}

		if err := ensureRootPathNoFollow(root, "nested", true, 0o755); err == nil || !strings.Contains(err.Error(), "mkdir parent failed") {
			t.Fatalf("expected mkdir error, got %v", err)
		}
	})
}

func TestEnsureRootPathNoFollowRejectsNonDirectoryParent(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
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

	if err := ensureRootPathNoFollow(root, "file/child", false, 0o755); err == nil || !strings.Contains(err.Error(), "output parent is not a directory") {
		t.Fatalf("expected non-directory parent error, got %v", err)
	}
}

func TestEnsureRootPathNoFollowReturnsOpenRootError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested dir: %v", err)
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
			return nil, errors.New("open ensured root failed")
		}

		if err := ensureRootPathNoFollow(root, "nested", false, 0o755); err == nil || !strings.Contains(err.Error(), "open ensured root failed") {
			t.Fatalf("expected open root error, got %v", err)
		}
	})
}

func TestEnsureRootPathNoFollowReturnsOwnedCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootDir, "nested", "child"), 0o755); err != nil {
			t.Fatalf("mkdir nested child: %v", err)
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
				return errors.New("close ensured root failed")
			}
			return root.Close()
		}

		if err := ensureRootPathNoFollow(root, "nested/child", false, 0o755); err == nil || !strings.Contains(err.Error(), "close ensured root failed") {
			t.Fatalf("expected owned close error, got %v", err)
		}
	})
}

func TestEnsureRootPathNoFollowReturnsFinalCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir nested dir: %v", err)
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

		closeOSRoot = func(*os.Root) error {
			return errors.New("final ensured close failed")
		}

		if err := ensureRootPathNoFollow(root, "nested", false, 0o755); err == nil || !strings.Contains(err.Error(), "final ensured close failed") {
			t.Fatalf("expected final close error, got %v", err)
		}
	})
}

func TestRenameWithinRootReturnsEnsureError(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write path blocker: %v", err)
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

	if err := renameWithinRoot(root, "from", filepath.Join("file", "child")); err == nil || !strings.Contains(err.Error(), "output parent is not a directory") {
		t.Fatalf("expected ensure-path error, got %v", err)
	}
}

func TestValidateManifestSubtreePathPartsReturnsNilForEmptyParts(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := validateManifestSubtreePathParts(root, nil, "overlay"); err != nil {
		t.Fatalf("expected empty path parts to be ignored, got %v", err)
	}
}

func TestValidateManifestSubtreePathPartsReturnsRootError(t *testing.T) {
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
			return nil, errors.New("validate subtree lstat failed")
		}

		if err := validateManifestSubtreePathParts(root, []string{"overlay"}, "overlay"); err == nil || !strings.Contains(err.Error(), "validate subtree lstat failed") {
			t.Fatalf("expected subtree lstat error, got %v", err)
		}
	})
}

func TestValidateManifestSubtreePathPartsReturnsOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay dir: %v", err)
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
			return nil, errors.New("validate subtree open failed")
		}

		if err := validateManifestSubtreePathParts(root, []string{"overlay", "child"}, "overlay/child"); err == nil || !strings.Contains(err.Error(), "validate subtree open failed") {
			t.Fatalf("expected subtree open error, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsIgnoresMissingLeafOnRemove(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(rootDir, "overlay-file"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write overlay leaf: %v", err)
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
			return os.ErrNotExist
		}

		if err := removeManifestSubtreeParts(root, []string{"overlay-file"}, "overlay-file"); err != nil {
			t.Fatalf("expected missing leaf remove to be ignored, got %v", err)
		}
	})
}

func TestRemoveManifestSubtreePartsReturnsDirectoryOpenErrorWithCloseError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay dir: %v", err)
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

		tmpFile := filepath.Join(rootDir, "tmpfile")
		if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		rootOpen = func(*os.Root, string) (*os.File, error) {
			file, err := os.Open(tmpFile)
			if err != nil {
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			return file, nil
		}

		if err := removeManifestSubtreeParts(root, []string{"overlay"}, "overlay"); err == nil || !strings.Contains(err.Error(), "file already closed") {
			t.Fatalf("expected closed-file directory read error, got %v", err)
		}
	})
}

func TestValidateBenchmarkHarnessTargetsReturnsPackageTargetError(t *testing.T) {
	err := validateBenchmarkHarnessTargets(t.TempDir(), benchmarkDefinition{
		PackageTargets: []string{"."},
		HarnessFiles: []benchmarkHarnessFile{{
			Path: "benchpkg/head_benchmark_test.go",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "must not resolve to repo root") {
		t.Fatalf("expected package target error, got %v", err)
	}
}

func TestResolvePackageBenchmarksWithinRootReturnsReadPackageError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(repoRoot, "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir package dir: %v", err)
		}
		repoRootHandle, err := os.OpenRoot(repoRoot)
		if err != nil {
			t.Fatalf("open repo root: %v", err)
		}
		defer func() {
			if closeErr := repoRootHandle.Close(); closeErr != nil {
				t.Fatalf("close repo root: %v", closeErr)
			}
		}()

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return nil, errors.New("open package dir failed")
		}

		remainingBytes := int64(maxBenchmarkHarnessTotal)
		if _, _, _, err := resolvePackageBenchmarksWithinRoot(repoRoot, repoRootHandle, "./benchpkg", &remainingBytes); err == nil || !strings.Contains(err.Error(), "read package ./benchpkg: open package dir failed") {
			t.Fatalf("expected read package error, got %v", err)
		}
	})
}

func TestResolvePackageBenchmarksWithinRootReturnsHarnessStatError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		repoRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/benchrepo\n\ngo 1.26.0\n"), 0o600); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(repoRoot, "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir package dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "benchpkg", "head_benchmark_test.go"), []byte("package benchpkg\n"), 0o600); err != nil {
			t.Fatalf("write harness file: %v", err)
		}
		repoRootHandle, err := os.OpenRoot(repoRoot)
		if err != nil {
			t.Fatalf("open repo root: %v", err)
		}
		defer func() {
			if closeErr := repoRootHandle.Close(); closeErr != nil {
				t.Fatalf("close repo root: %v", closeErr)
			}
		}()

		realRootLstat := rootLstat
		rootLstat = func(root *os.Root, name string) (os.FileInfo, error) {
			if name == "head_benchmark_test.go" {
				return nil, errors.New("harness lstat failed")
			}
			return realRootLstat(root, name)
		}

		remainingBytes := int64(maxBenchmarkHarnessTotal)
		if _, _, _, err := resolvePackageBenchmarksWithinRoot(repoRoot, repoRootHandle, "./benchpkg", &remainingBytes); err == nil || !strings.Contains(err.Error(), "read package ./benchpkg: harness lstat failed") {
			t.Fatalf("expected harness stat error, got %v", err)
		}
	})
}

func TestWriteBenchmarkDefinitionReturnsMarshalError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		marshalIndent = func(any, string, string) ([]byte, error) {
			return nil, errors.New("marshal manifest failed")
		}

		parent := canonicalTempDir(t)
		err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
		if err == nil || !strings.Contains(err.Error(), "marshal benchmark definition: marshal manifest failed") {
			t.Fatalf("expected marshal error, got %v", err)
		}
	})
}

func TestApplyBenchmarkOverlayReturnsManifestRootOpenError(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)

	withBenchdeltaFSHooks(t, func() {
		originalOpenCanonicalWriteRoot := openCanonicalWriteRoot
		openCanonicalWriteRoot = func(string) (confinedWriteRoot, error) {
			return &fakeConfinedWriteRoot{}, nil
		}
		defer func() { openCanonicalWriteRoot = originalOpenCanonicalWriteRoot }()
		openOSRoot = func(string) (*os.Root, error) {
			return nil, errors.New("open apply manifest root failed")
		}

		err := applyBenchmarkOverlay(t.TempDir(), definitionPath, definition)
		if err == nil || !strings.Contains(err.Error(), "open benchmark repo root: open apply manifest root failed") {
			t.Fatalf("expected repo manifest root open error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowReturnsRelativePathErrorDirect(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return filepath.Join(string(os.PathSeparator), "tmp"), nil
		}
		relPath = func(string, string) (string, error) {
			return "", errors.New("open root relative failed")
		}

		if _, err := openManifestRootNoFollow("ignored"); err == nil || !strings.Contains(err.Error(), "open root relative failed") {
			t.Fatalf("expected relative-path error, got %v", err)
		}
	})
}

func TestOpenManifestRootNoFollowSkipsCurrentDirectoryComponentOnExistingPath(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return filepath.Join(string(os.PathSeparator), "missing"), nil
		}
		relPath = func(base, target string) (string, error) {
			return "." + string(os.PathSeparator) + "missing", nil
		}

		root, err := openManifestRootNoFollow("ignored")
		if root != nil {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close root: %v", closeErr)
			}
		}
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing-path error after dot-prefix skip, got %v", err)
		}
	})
}

func TestValidateManifestRootPathNoFollowReturnsRelativePathErrorDirect(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return filepath.Join(string(os.PathSeparator), "tmp"), nil
		}
		relPath = func(string, string) (string, error) {
			return "", errors.New("validate root relative failed")
		}

		err := validateManifestRootPathNoFollow("ignored")
		if err == nil || !strings.Contains(err.Error(), "validate root relative failed") {
			t.Fatalf("expected relative-path error, got %v", err)
		}
	})
}

func TestValidateManifestRootPathNoFollowReturnsNilForVolumeRoot(t *testing.T) {
	if err := validateManifestRootPathNoFollow(string(os.PathSeparator)); err != nil {
		t.Fatalf("validateManifestRootPathNoFollow returned error: %v", err)
	}
}

func TestValidateManifestRootPathNoFollowSkipsCurrentDirectoryPart(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		absPath = func(string) (string, error) {
			return filepath.Join(string(os.PathSeparator), "missing"), nil
		}
		relPath = func(base, target string) (string, error) {
			return "." + string(os.PathSeparator) + "missing", nil
		}

		if err := validateManifestRootPathNoFollow("ignored"); err != nil {
			t.Fatalf("validateManifestRootPathNoFollow returned error: %v", err)
		}
	})
}

func TestEnsureRootPathNoFollowSkipsCurrentDirectoryPart(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
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

	if err := ensureRootPathNoFollow(root, filepath.Join(".", "nested"), false, 0o755); err != nil {
		t.Fatalf("ensureRootPathNoFollow returned error: %v", err)
	}
}

func TestEnsureRootPathNoFollowReturnsNilForSlashOnly(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := ensureRootPathNoFollow(root, string(os.PathSeparator), false, 0o755); err != nil {
		t.Fatalf("ensureRootPathNoFollow returned error: %v", err)
	}
}

func TestRemoveManifestSubtreePartsReturnsNilForMissingLeaf(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := removeManifestSubtreeParts(root, []string{"missing"}, "missing"); err != nil {
		t.Fatalf("expected missing leaf to be ignored, got %v", err)
	}
}

func TestOpenManifestRootNoFollowReturnsRelativePathErrorForNonAliasRoot(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := filepath.Join(t.TempDir(), "manifest-root")
		absPath = func(string) (string, error) {
			return rootDir, nil
		}
		relPath = func(base, target string) (string, error) {
			if base == string(os.PathSeparator) {
				return "", errors.New("non-alias relative failed")
			}
			return filepath.Rel(base, target)
		}

		if _, err := openManifestRootNoFollow("ignored"); err == nil || !strings.Contains(err.Error(), "non-alias relative failed") {
			t.Fatalf("expected non-alias relative-path error, got %v", err)
		}
	})
}

func TestValidateManifestRootPathNoFollowReturnsRelativePathErrorForNonAliasRoot(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := filepath.Join(t.TempDir(), "manifest-root")
		absPath = func(string) (string, error) {
			return rootDir, nil
		}
		relPath = func(base, target string) (string, error) {
			if base == string(os.PathSeparator) {
				return "", errors.New("validate non-alias relative failed")
			}
			return filepath.Rel(base, target)
		}

		err := validateManifestRootPathNoFollow("ignored")
		if err == nil || !strings.Contains(err.Error(), "validate non-alias relative failed") {
			t.Fatalf("expected non-alias relative-path error, got %v", err)
		}
	})
}

func TestWriteBenchmarkDefinitionReturnsStageRootCreateError(t *testing.T) {
	parent := canonicalTempDir(t)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod parent read-only: %v", err)
	}
	defer func() {
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Fatalf("restore parent perms: %v", err)
		}
	}()

	err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(parent, "overlay"), benchmarkDefinition{Version: definitionVersion}, nil)
	if err == nil || !strings.Contains(err.Error(), "create benchmark staging root") {
		t.Fatalf("expected stage-root creation error, got %v", err)
	}
}

func TestApplyBenchmarkOverlayReturnsStageRootCreateError(t *testing.T) {
	artifactDir := canonicalTempDir(t)
	definitionPath, definition := writeOverlayDefinitionFixture(t, artifactDir)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo package: %v", err)
	}
	if err := os.Chmod(repo, 0o500); err != nil {
		t.Fatalf("chmod repo read-only: %v", err)
	}
	defer func() {
		if err := os.Chmod(repo, 0o700); err != nil {
			t.Fatalf("restore repo perms: %v", err)
		}
	}()

	err := applyBenchmarkOverlay(repo, definitionPath, definition)
	if err == nil || !strings.Contains(err.Error(), "create benchmark staging root") {
		t.Fatalf("expected apply stage-root creation error, got %v", err)
	}
}

func TestRemoveManifestSubtreePartsReturnsRootOpenError(t *testing.T) {
	withBenchdeltaFSHooks(t, func() {
		rootDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(rootDir, "overlay"), 0o755); err != nil {
			t.Fatalf("mkdir overlay dir: %v", err)
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

		rootOpen = func(*os.Root, string) (*os.File, error) {
			return nil, errors.New("open overlay listing failed")
		}

		if err := removeManifestSubtreeParts(root, []string{"overlay"}, "overlay"); err == nil || !strings.Contains(err.Error(), "open overlay listing failed") {
			t.Fatalf("expected rootOpen error, got %v", err)
		}
	})
}
