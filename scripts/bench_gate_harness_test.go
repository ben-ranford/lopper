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

// TestBenchGateFingerprintsCompilerDirectiveOnSelectedDeclaration proves
// that adding a compiler directive comment (e.g. //go:noinline) to a
// benchmark-reachable helper changes the fingerprint even though the
// declaration's own text is unchanged. A directive comment attaches as the
// declaration's Doc comment, positioned before decl.Pos(); hashing only
// [decl.Pos(), decl.End()) would miss it even though it can change
// inlining, escape analysis, and therefore the benchmark's own allocations.
func TestBenchGateFingerprintsCompilerDirectiveOnSelectedDeclaration(t *testing.T) {
	assertBenchGateFingerprintsBaseThenHeadFiles(t,
		map[string]string{
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

func helperValue() int {
	return 1
}
`,
		},
		map[string]string{
			"helper_test.go": `package benchpkg

//go:noinline
func helperValue() int {
	return 1
}
`,
		},
	)
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

// TestBenchGateFingerprintsStdlibRegistrationSideEffectImport proves that a
// named import of a standard-library package with a known cross-package
// registration side effect (image/png registers a codec via init(), used by
// any image.Decode caller in the same test binary) still counts as able to
// affect the benchmark, even though it appears only in an ordinary test
// function unrelated to the benchmark itself. Standard-library imports are
// otherwise excluded (see importCanAffectBenchmark) to avoid making nearly
// every test file a root via universally-imported packages like "testing".
func TestBenchGateFingerprintsStdlibRegistrationSideEffectImport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		headFile string
	}{
		{
			name: "image/png",
			headFile: `package benchpkg

import (
	"bytes"
	"image/png"
	"testing"
)

func TestOnly(t *testing.T) {
	var buf bytes.Buffer
	_ = png.Encode
	_ = buf
}
`,
		},
		{
			name: "crypto/sha3",
			headFile: `package benchpkg

import (
	"crypto/sha3"
	"testing"
)

func TestOnly(t *testing.T) {
	_ = sha3.New256
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(map[string]string{
				"setup_test.go": `package benchpkg

import "testing"

func TestOnly(t *testing.T) {}
`,
			}))
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
	assertBenchGateFingerprintsExternalModuleImport(t, "require foo v0.0.0\n\nreplace foo => ./foovendor\n")
}

// TestBenchGateFingerprintsQuotedGoModRequireImport proves a named import
// of a module declared with go.mod's quoted-token directive form (require
// "example.com/dep" v1.0.0, valid per Go's own modfile syntax) still counts
// as able to affect the benchmark. Without unquoting, the required path
// parsed from go.mod would never match the unquoted path an *ast.ImportSpec
// carries, so the import would be missed regardless of tracking it by
// module membership.
func TestBenchGateFingerprintsQuotedGoModRequireImport(t *testing.T) {
	assertBenchGateFingerprintsExternalModuleImport(t, "require \"foo\" v0.0.0\n\nreplace \"foo\" => ./foovendor\n")
}

// assertBenchGateFingerprintsExternalModuleImport is the shared fixture for
// both external-module-import tests above: a benchmark package where an
// ordinary test's only distinguishing feature is a named import of a local
// "foo" module declared by goModRequireReplace, removed between base and
// head. writeRepositoryHarness already copied the real go.mod; the
// directive is appended to it rather than starting from scratch.
func assertBenchGateFingerprintsExternalModuleImport(t *testing.T, goModRequireReplace string) {
	t.Helper()

	fixture := newBenchGateFixture(t, "benchpkg")
	fixtureGoMod, err := os.ReadFile(filepath.Join(fixture.root, "go.mod"))
	if err != nil {
		t.Fatalf("read fixture go.mod: %v", err)
	}
	fixture.writeFile("go.mod", string(fixtureGoMod)+"\n"+goModRequireReplace)
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
	assertBenchGateFingerprintsBaseThenHeadFiles(t,
		map[string]string{
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
		},
		map[string]string{
			"stringer_test.go": `package benchpkg

func (benchValue) String() string {
	return "head"
}
`,
		},
	)
}

// assertBenchGateFingerprintsBaseThenHeadFiles is the shared fixture for
// tests that write a base commit's files, then overwrite a subset with a
// head commit's files, and assert the harness fingerprint mismatches.
func assertBenchGateFingerprintsBaseThenHeadFiles(t *testing.T, baseFiles, headFiles map[string]string) {
	t.Helper()

	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", baseFiles)
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", headFiles)
	fixture.commit("head")

	assertBenchGateHarnessMismatch(t, fixture)
}

// TestBenchGateFingerprintsSwitchedImportBehindRetainedAlias proves that
// switching an import's target while keeping its local alias -- e.g.
// `import rng "math/rand"` to `import rng "crypto/rand"` -- still counts,
// even though the benchmark's own declaration text (which only mentions
// rng.Read) is unchanged. Without indexing imports by their explicit local
// aliases, that import decl was never reachable from the benchmark that
// uses it, so the fingerprint stayed equal despite the benchmark now
// executing different code.
func TestBenchGateFingerprintsSwitchedImportBehindRetainedAlias(t *testing.T) {
	assertBenchGateFingerprintsBaseThenHeadFiles(t,
		map[string]string{
			"bench_test.go": `package benchpkg

import (
	rng "math/rand"
	"testing"
)

func BenchmarkValue(b *testing.B) {
	buf := make([]byte, 16)
	for i := 0; i < b.N; i++ {
		rng.Read(buf)
	}
}
`,
		},
		map[string]string{
			"bench_test.go": `package benchpkg

import (
	rng "crypto/rand"
	"testing"
)

func BenchmarkValue(b *testing.B) {
	buf := make([]byte, 16)
	for i := 0; i < b.N; i++ {
		rng.Read(buf)
	}
}
`,
		},
	)
}

// TestBenchGateIgnoresMethodOnUnreachableReceiverType proves that a method
// declared on a type used only by an ordinary test -- never constructed or
// otherwise referenced from any benchmark -- no longer forces the file into
// the harness fingerprint. Methods were previously always roots regardless
// of receiver reachability (to support implicit interface dispatch, see
// TestBenchGateFingerprintsInterfaceDispatchedMethods above); scoping that
// to reachable receiver types keeps that support while no longer
// invalidating benchmarks on unrelated ordinary-test method changes.
func TestBenchGateIgnoresMethodOnUnreachableReceiverType(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(map[string]string{
		"helper_test.go": `package benchpkg

import "testing"

type unrelatedHelper struct{}

func (unrelatedHelper) doThing() int {
	return 1
}

func TestUnrelated(t *testing.T) {
	h := unrelatedHelper{}
	if h.doThing() != 1 {
		t.Fatal("nope")
	}
}
`,
	}))
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"helper_test.go": `package benchpkg

import "testing"

type unrelatedHelper struct{}

func (unrelatedHelper) doThing() int {
	return 2
}

func TestUnrelated(t *testing.T) {
	h := unrelatedHelper{}
	if h.doThing() != 2 {
		t.Fatal("nope")
	}
}
`,
	})
	fixture.commit("head")

	output, exitCode := fixture.runBenchGate()
	if exitCode != 0 {
		t.Fatalf("bench gate exit code = %d, want 0\n%s", exitCode, output)
	}
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
	assertBenchGateIgnoresChangedOrdinaryFixture(t, map[string]string{
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
}

// TestBenchGateIgnoresOrdinaryTestOnlyEmbedSharingVarGroupWithBenchmark
// proves go:embed comment scoping works at the individual spec, not the
// whole declaration: a benchmark referencing only one embed var in a
// grouped var(...) block must not pull in a sibling var's embed fixture in
// the same group, even though selecting the benchmark's own var marks the
// whole surrounding *ast.GenDecl as reached.
func TestBenchGateIgnoresOrdinaryTestOnlyEmbedSharingVarGroupWithBenchmark(t *testing.T) {
	assertBenchGateIgnoresChangedOrdinaryFixture(t, map[string]string{
		"bench_test.go": `package benchpkg

import (
	_ "embed"
	"testing"
)

var (
	//go:embed testdata/bench.txt
	benchFixture string
	//go:embed testdata/ordinary.txt
	ordinaryFixture string
)

func BenchmarkValue(b *testing.B) {
	_ = len(benchFixture)
}

func TestOrdinary(t *testing.T) {
	if ordinaryFixture == "" {
		t.Fatal("missing ordinary fixture")
	}
}
`,
		"testdata/bench.txt":    "bench\n",
		"testdata/ordinary.txt": "base\n",
	})
}

// assertBenchGateIgnoresChangedOrdinaryFixture commits baseFiles, changes
// only testdata/ordinary.txt for head, and asserts the harness fingerprint
// still matches (exit 0): the benchmark package's shape is unrelated to
// that ordinary-test-only fixture, however it's referenced.
func assertBenchGateIgnoresChangedOrdinaryFixture(t *testing.T, baseFiles map[string]string) {
	t.Helper()

	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", baseFiles)
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

// TestBenchGateIgnoresOrdinaryTestBodyChangeSharingFileWithBenchmark proves
// that when a benchmark and an unrelated ordinary test share one _test.go
// file, a change confined to the ordinary test's body doesn't invalidate
// the fingerprint. The fingerprint hashes only the selected declarations'
// source text, not the whole file, so TestNoise's own body -- never
// selected, since the benchmark never references it -- can't affect it.
func TestBenchGateIgnoresOrdinaryTestBodyChangeSharingFileWithBenchmark(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"bench_test.go": `package benchpkg

import "testing"

func BenchmarkValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
	}
}

func TestNoise(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("base assertion")
	}
}
`,
	})
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"bench_test.go": `package benchpkg

import "testing"

func BenchmarkValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
	}
}

func TestNoise(t *testing.T) {
	if 2+2 != 4 {
		t.Fatal("head assertion, totally different math")
	}
}
`,
	})
	fixture.commit("head")

	output, exitCode := fixture.runBenchGate()
	if exitCode != 0 {
		t.Fatalf("bench gate exit code = %d, want 0\n%s", exitCode, output)
	}
}

// TestBenchGateRoutesSelectorBuildFailureThroughInvalidGate proves that a
// failure building the embedded harness selector (e.g. GOCACHE or the
// temporary filesystem becomes unavailable) is routed through
// fail_invalid_memory_gate rather than letting the script's top-level set -e
// exit raw on go build's own status. A raw exit would skip writing the
// invalid-status artifact and leave a stale summary/status from a previous
// run in place.
func TestBenchGateRoutesSelectorBuildFailureThroughInvalidGate(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(nil))
	fixture.commit("base")
	fixture.writeFile("README.md", "head commit marker\n")
	fixture.commit("head")

	realGoBin := fixture.goBin
	fakeGoPath := filepath.Join(t.TempDir(), "go")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"build\" ]; then\n" +
		"\tfor arg in \"$@\"; do\n" +
		"\t\tcase \"$arg\" in\n" +
		"\t\t*benchharness.go) exit 1 ;;\n" +
		"\t\tesac\n" +
		"\tdone\n" +
		"fi\n" +
		"exec \"" + realGoBin + "\" \"$@\"\n"
	if err := os.WriteFile(fakeGoPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake go wrapper: %v", err)
	}
	fixture.goBin = fakeGoPath

	output, exitCode := fixture.runBenchGate()
	if exitCode != 2 {
		t.Fatalf("bench gate exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Memory benchmark gate invalid") {
		t.Fatalf("expected invalid-gate diagnostic, got:\n%s", output)
	}
	status, err := os.ReadFile(filepath.Join(fixture.root, ".artifacts", "memory-bench-status.txt"))
	if err != nil {
		t.Fatalf("read memory bench status: %v", err)
	}
	if got := strings.TrimSpace(string(status)); got != "2" {
		t.Fatalf("expected memory bench status 2, got %q", got)
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
