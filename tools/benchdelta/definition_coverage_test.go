package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
