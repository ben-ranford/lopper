package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchGateFingerprintsHelperTestFiles(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"bench_test.go": `package benchpkg

import "testing"

var sink int

func BenchmarkValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink += helperValue()
	}
}
`,
		"helper_test.go": `package benchpkg

import "testing"

var helperInput = 1

func helperValue() int {
	return helperInput
}

func TestHelper(t *testing.T) {}
`,
	})
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"helper_test.go": `package benchpkg

import "testing"

var helperInput = 2

func helperValue() int {
	return helperInput
}

func TestHelper(t *testing.T) {}
`,
	})
	fixture.commit("head")

	output, exitCode := fixture.runBenchGate()
	if exitCode != 2 {
		t.Fatalf("bench gate exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "does not match the resolved head harness fingerprint") {
		t.Fatalf("expected helper file fingerprint mismatch, got:\n%s", output)
	}
}

func TestBenchGateFingerprintsTestMainHarnessFiles(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(map[string]string{
		"main_test.go": testMainHarnessFile("base"),
	}))
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"main_test.go": testMainHarnessFile("head"),
	})
	fixture.commit("head")

	assertBenchGateHarnessMismatch(t, fixture)
}

func TestBenchGateFingerprintsPackageLevelInitializedVars(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(map[string]string{
		"setup_test.go": initializedVarHarnessFile("base"),
	}))
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"setup_test.go": initializedVarHarnessFile("head"),
	})
	fixture.commit("head")

	assertBenchGateHarnessMismatch(t, fixture)
}

func TestBenchGateFingerprintsImportInitSetup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		baseFile string
		headFile string
	}{
		{
			name:     "blank import",
			baseFile: importInitSetupTestFile(`import _ "github.com/ben-ranford/lopper/benchpkg/testsetup"`, ""),
			headFile: importInitSetupTestFile(`import _ "github.com/ben-ranford/lopper/benchpkg/testsetuphead"`, ""),
		},
		{
			name: "named import",
			baseFile: importInitSetupTestFile(`import (
	"testing"

	"github.com/ben-ranford/lopper/benchpkg/testsetup"
)`, `func TestOnly(t *testing.T) {
	testsetup.Configure()
}`),
			headFile: importInitSetupTestFile(`import (
	"testing"

	"github.com/ben-ranford/lopper/benchpkg/testsetuphead"
)`, `func TestOnly(t *testing.T) {
	testsetuphead.Configure()
}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			fixture.writeBenchmarkPackage("benchpkg", importInitSetupPackageFiles(tc.baseFile))
			fixture.commit("base")
			fixture.writeBenchmarkPackage("benchpkg", map[string]string{"setup_test.go": tc.headFile})
			fixture.commit("head")

			assertBenchGateHarnessMismatch(t, fixture)
		})
	}
}

// TestBenchGateFingerprintsDotlessExternalModuleImport proves a named import
// of an external module with a dotless, slash-free path (valid via a local
// replace directive, as tested here) still counts as able to affect the
// benchmark. The benchmark and an ordinary Test-prefixed function compile
// into the same test binary, so that import's init() runs regardless of
// whether the import path looks like a typical hosted module path.
func TestBenchGateFingerprintsDotlessExternalModuleImport(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	// writeRepositoryHarness already copied the real go.mod; append this
	// test's require/replace to it rather than starting from scratch.
	fixtureGoMod, err := os.ReadFile(filepath.Join(fixture.root, "go.mod"))
	if err != nil {
		t.Fatalf("read fixture go.mod: %v", err)
	}
	fixture.writeFile("go.mod", string(fixtureGoMod)+"\nrequire foo v0.0.0\n\nreplace foo => ./foovendor\n")
	fixture.writeFile("foovendor/go.mod", "module foo\n\ngo 1.27.0\n")
	fixture.writeFile("foovendor/foo.go", "package foo\n\nfunc init() {}\n\nfunc Configure() {}\n")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(map[string]string{
		"setup_test.go": `package benchpkg

import (
	"testing"

	"foo"
)

func TestOnly(t *testing.T) {
	foo.Configure()
}
`,
	}))
	fixture.commit("base")

	fixture.writeBenchmarkPackage("benchpkg", map[string]string{"setup_test.go": `package benchpkg

import "testing"

func TestOnly(t *testing.T) {}
`})
	fixture.commit("head")

	assertBenchGateHarnessMismatch(t, fixture)
}

func importInitSetupPackageFiles(setupTestFile string) map[string]string {
	return benchmarkHarnessPackageFiles(map[string]string{
		"setup_test.go": setupTestFile,
		"testsetup/setup.go": `package testsetup

func init() {}

func Configure() {}
`,
		"testsetuphead/setup.go": `package testsetuphead

func init() {}

func Configure() {}
`,
	})
}

func importInitSetupTestFile(importDecl, body string) string {
	if body == "" {
		return "package benchpkg\n\n" + importDecl + "\n"
	}
	return "package benchpkg\n\n" + importDecl + "\n\n" + body + "\n"
}

func TestBenchGateFingerprintsInterfaceDispatchedMethods(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"bench_test.go": `package benchpkg

import (
	"fmt"
	"testing"
)

type benchValue struct{}

var sink string

func BenchmarkValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = fmt.Sprint(benchValue{})
	}
}
`,
		"stringer_test.go": `package benchpkg

func (benchValue) String() string {
	return "base"
}
`,
	})
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"stringer_test.go": `package benchpkg

func (benchValue) String() string {
	return "head"
}
`,
	})
	fixture.commit("head")

	assertBenchGateHarnessMismatch(t, fixture)
}

func benchmarkHarnessPackageFiles(extra map[string]string) map[string]string {
	files := map[string]string{
		"bench_test.go": `package benchpkg

import "testing"

func BenchmarkValue(b *testing.B) {}
`,
	}
	for name, content := range extra {
		files[name] = content
	}
	return files
}

func testMainHarnessFile(value string) string {
	return `package benchpkg

import (
	"os"
	"testing"
)

var setupValue = "` + value + `"

func TestMain(m *testing.M) {
	if setupValue == "" {
		os.Exit(1)
	}
	os.Exit(m.Run())
}
`
}

func initializedVarHarnessFile(value string) string {
	return `package benchpkg

var setupValue = "` + value + `"
`
}

func TestBenchGateFingerprintsBenchmarkEmbedsWithGoSemantics(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files       map[string]string
		changedFile string
	}{
		{
			name: "recursive directory contents",
			files: map[string]string{
				"bench_test.go": `package benchpkg

import (
	"embed"
	"testing"
)

//go:embed testdata
var fixtures embed.FS

func BenchmarkValue(b *testing.B) {
	_, _ = fixtures.ReadFile("testdata/nested/input.txt")
}
`,
				"testdata/nested/input.txt": "base\n",
			},
			changedFile: "testdata/nested/input.txt",
		},
		{
			name: "quoted filename with spaces",
			files: map[string]string{
				"bench_test.go": `package benchpkg

import (
	_ "embed"
	"testing"
)

//go:embed "test data/input.txt"
var input string

func BenchmarkValue(b *testing.B) {
	_ = len(input)
}
`,
				"test data/input.txt": "base\n",
			},
			changedFile: "test data/input.txt",
		},
		{
			name: "quoted filename with Go escape",
			files: map[string]string{
				"bench_test.go": `package benchpkg

import (
	_ "embed"
	"testing"
)

//go:embed "test\x20data/input.txt"
var input string

func BenchmarkValue(b *testing.B) {
	_ = len(input)
}
`,
				"test data/input.txt": "base\n",
			},
			changedFile: "test data/input.txt",
		},
		{
			name: "all directory includes hidden fixtures",
			files: map[string]string{
				"bench_test.go": `package benchpkg

import (
	"embed"
	"testing"
)

//go:embed all:testdata
var fixtures embed.FS

func BenchmarkValue(b *testing.B) {
	_, _ = fixtures.ReadFile("testdata/.fixture")
}
`,
				"testdata/.fixture": "base\n",
			},
			changedFile: "testdata/.fixture",
		},
		{
			name: "glob includes dotfiles",
			files: map[string]string{
				"bench_test.go": `package benchpkg

import (
	"embed"
	"testing"
)

//go:embed testdata/*
var fixtures embed.FS

func BenchmarkValue(b *testing.B) {
	_, _ = fixtures.ReadFile("testdata/.fixture")
}
`,
				"testdata/.fixture": "base\n",
			},
			changedFile: "testdata/.fixture",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			fixture.writeBenchmarkPackage("benchpkg", tc.files)
			fixture.commit("base")
			fixture.writeBenchmarkPackage("benchpkg", map[string]string{tc.changedFile: "head\n"})
			fixture.commit("head")

			assertBenchGateHarnessMismatch(t, fixture)
		})
	}
}

func TestBenchGateIgnoresOrdinaryTestOnlyEmbedFixtures(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"bench_test.go": `package benchpkg

import "testing"

func BenchmarkValue(b *testing.B) {}
`,
		"ordinary_test.go": `package benchpkg

import (
	_ "embed"
	"testing"
)

//go:embed testdata/ordinary.txt
var ordinaryInput string

func TestOrdinary(t *testing.T) {
	if ordinaryInput == "" {
		t.Fatal("missing ordinary fixture")
	}
}
`,
		"testdata/ordinary.txt": "base\n",
	})
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{"testdata/ordinary.txt": "head\n"})
	fixture.commit("head")

	output, exitCode := fixture.runBenchGate()
	if exitCode != 0 {
		t.Fatalf("bench gate exit code = %d, want 0\n%s", exitCode, output)
	}
}

func TestBenchGateIgnoresPackageCommentAndStringBenchmarkText(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchmarkpkg")
	fixture.writeBenchmarkPackage("benchmarkpkg", map[string]string{
		"bench_test.go": `package benchmarkpkg

import "testing"

var sink []byte

func BenchmarkValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = make([]byte, 1)
	}
}
`,
		"noise_test.go": `package benchmarkpkg

import "testing"

// BenchmarkComment is not a declaration.
func TestNoise(t *testing.T) {
	_ = "benchmarkLiteralBase"
}
`,
	})
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchmarkpkg", map[string]string{
		"noise_test.go": `package benchmarkpkg

import "testing"

// BenchmarkComment changed, but still is not a declaration.
func TestNoise(t *testing.T) {
	_ = "benchmarkLiteralHead"
}
`,
	})
	fixture.commit("head")

	output, exitCode := fixture.runBenchGate()
	if exitCode != 0 {
		t.Fatalf("bench gate exit code = %d, want 0\n%s", exitCode, output)
	}
}

func assertBenchGateHarnessMismatch(t *testing.T, fixture benchGateFixture) {
	t.Helper()

	output, exitCode := fixture.runBenchGate()
	if exitCode != 2 {
		t.Fatalf("bench gate exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "does not match the resolved head harness fingerprint") {
		t.Fatalf("expected harness fingerprint mismatch, got:\n%s", output)
	}
}

type benchGateFixture struct {
	t       *testing.T
	root    string
	pkgPath string
	goBin   string
}

func newBenchGateFixture(t *testing.T, packageName string) benchGateFixture {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("resolve go: %v", err)
	}

	root := t.TempDir()
	fixture := benchGateFixture{
		t:       t,
		root:    root,
		pkgPath: "./" + packageName,
		goBin:   goBin,
	}
	fixture.writeRepositoryHarness()
	fixture.git("init")
	fixture.git("config", "user.name", "Bench Gate Test")
	fixture.git("config", "user.email", "bench-gate-test@example.invalid")
	return fixture
}

func (f *benchGateFixture) writeRepositoryHarness() {
	// Copy the real go.mod/go.sum rather than writing a minimal one: the
	// copied internal/safeio sources below carry their own real transitive
	// dependencies (e.g. golang.org/x/sys on Linux), which a from-scratch
	// go.mod wouldn't declare.
	f.copyFile("go.mod")
	f.copyFile("go.sum")
	f.copyFile("scripts/bench-gate.sh")
	f.copyFile("tools/benchdelta/main.go")

	safeioFiles, err := filepath.Glob(repoPath(f.t, "internal/safeio/*.go"))
	if err != nil {
		f.t.Fatalf("glob safeio files: %v", err)
	}
	for _, path := range safeioFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f.copyFile(filepath.ToSlash(strings.TrimPrefix(path, repoPath(f.t, "")+string(os.PathSeparator))))
	}
}

func (f *benchGateFixture) writeBenchmarkPackage(packageName string, files map[string]string) {
	for name, content := range files {
		f.writeFile(filepath.Join(packageName, name), content)
	}
}

func (f *benchGateFixture) runBenchGate() (string, int) {
	f.t.Helper()

	cmd := exec.Command("sh", "./scripts/bench-gate.sh")
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(),
		"GO=go",
		"GO_BIN="+f.goBin,
		"GO_TOOLCHAIN=local",
		"GO_TEST_LDFLAGS=",
		"MEMORY_BENCH_BASE=HEAD~1",
		"MEMORY_BENCH_PACKAGES="+f.pkgPath,
		"BENCH_COUNT=1",
		"BENCH_TIME=1x",
		"BENCH_BASE_OUTPUT=.artifacts/bench-base.out",
		"BENCH_HEAD_OUTPUT=.artifacts/bench-head.out",
		"MEMORY_BENCH_SUMMARY=.artifacts/memory-bench-summary.md",
		"MEMORY_BENCH_STATUS=.artifacts/memory-bench-status.txt",
		"MEMORY_BENCH_ENFORCE=1",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		f.t.Fatalf("run bench gate: %v\n%s", err, output)
	}
	return string(output), exitErr.ExitCode()
}

func (f *benchGateFixture) commit(message string) {
	f.t.Helper()

	f.git("add", ".")
	f.git("commit", "-m", message)
}

func (f *benchGateFixture) git(args ...string) string {
	f.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	output, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (f *benchGateFixture) copyFile(relPath string) {
	f.t.Helper()

	content, err := os.ReadFile(repoPath(f.t, relPath))
	if err != nil {
		f.t.Fatalf("read %s: %v", relPath, err)
	}
	f.writeFile(relPath, string(content))
}

func (f *benchGateFixture) writeFile(relPath, content string) {
	f.t.Helper()

	path := filepath.Join(f.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", relPath, err)
	}
}
