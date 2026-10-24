package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var goCompilerCommand = regexp.MustCompile(`(?:^|/)compile(?:\.exe)?(?:[ \t"]|$)`)

func hasGoCompilerInvocation(output string) bool {
	return goCompilerCommand.MatchString(strings.ReplaceAll(output, `\`, "/"))
}

// Selector source is emitted under a fresh temporary directory for every
// bench-gate invocation. -trimpath must keep that changing path out of the Go
// build cache key while still invalidating the cache when the source changes.
func TestBenchSelectorBuildCacheAcrossTemporaryRoots(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("resolve Go executable: %v", err)
	}
	script := readConfig(t, "scripts/bench-gate.sh")
	const sourceStart = `cat > "$benchmark_harness_selector_src" <<'GOEOF';` + "\n"
	start := strings.Index(script, sourceStart)
	if start < 0 {
		t.Fatal("bench-gate selector source heredoc is missing")
	}
	start += len(sourceStart)
	end := strings.Index(script[start:], "\nGOEOF")
	if end < 0 {
		t.Fatal("bench-gate selector source heredoc is unterminated")
	}
	selectorSource := script[start : start+end]
	if !strings.Contains(script, `build -trimpath -o "$benchmark_harness_selector_bin" "$benchmark_harness_selector_src"`) {
		t.Fatal("bench-gate selector build must use -trimpath")
	}

	initialDir := filepath.Join(t.TempDir(), "first")
	nextDir := filepath.Join(t.TempDir(), "second")
	for _, root := range []string{initialDir, nextDir} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	firstSource := filepath.Join(initialDir, "benchharness.go")
	secondSource := filepath.Join(nextDir, "benchharness.go")
	// Salt this test's selector source so a prior test or gate invocation cannot
	// populate the first build's cache entry before the reuse assertion.
	cacheSalt := filepath.Dir(initialDir)
	selectorSource += "\n// cache test salt: " + cacheSalt + "\n"
	if err := os.WriteFile(firstSource, []byte(selectorSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondSource, []byte(selectorSource), 0o600); err != nil {
		t.Fatal(err)
	}

	build := func(source, output string) string {
		t.Helper()
		cmd := exec.Command(goBin, "build", "-x", "-trimpath", "-o", output, source)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-buildvcs=false")
		combined, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go build %s failed: %v\n%s", source, err, combined)
		}
		return string(combined)
	}

	firstOutput := build(firstSource, filepath.Join(initialDir, "benchharness"))
	if !hasGoCompilerInvocation(firstOutput) {
		t.Fatalf("first selector build did not compile source; -x output:\n%s", firstOutput)
	}
	secondOutput := build(secondSource, filepath.Join(nextDir, "benchharness"))
	if hasGoCompilerInvocation(secondOutput) {
		t.Fatalf("identical selector source in another temporary root missed the build cache; -x output:\n%s", secondOutput)
	}
	if err := os.WriteFile(secondSource, []byte(strings.Replace(selectorSource, cacheSalt, cacheSalt+"-changed", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	changedOutput := build(secondSource, filepath.Join(nextDir, "benchharness-changed"))
	if !hasGoCompilerInvocation(changedOutput) {
		t.Fatalf("changed selector source reused stale compiled output; -x output:\n%s", changedOutput)
	}
}
