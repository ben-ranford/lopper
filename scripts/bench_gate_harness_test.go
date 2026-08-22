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

func TestBenchGateIgnoresPackageCommentAndStringBenchmarkText(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchmarkpkg")
	fixture.writeBenchmarkPackage("benchmarkpkg", map[string]string{
		"bench_test.go": `package benchmarkpkg

import "testing"

var sink int

func BenchmarkValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink++
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
	f.writeFile("go.mod", "module github.com/ben-ranford/lopper\n\ngo 1.27.0\n")
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
