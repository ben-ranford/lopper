package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseBenchmarkFileAndCompare(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.txt")
	headPath := filepath.Join(dir, "head.txt")

	baseLines := []string{
		"goos: darwin",
		"pkg: github.com/ben-ranford/lopper/internal/lang/shared",
		"BenchmarkCountUsage-8    1000   25000 ns/op   25632 B/op   375 allocs/op",
		"BenchmarkCountUsage-8    1000   25500 ns/op   25632 B/op   375 allocs/op",
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormatLargeTable-8    500   90000 ns/op   64000 B/op   120 allocs/op",
	}
	base := strings.Join(baseLines, "\n")
	headLines := []string{
		"goos: darwin",
		"pkg: github.com/ben-ranford/lopper/internal/lang/shared",
		"BenchmarkCountUsage-8    1000   25000 ns/op   30000 B/op   430 allocs/op",
		"BenchmarkCountUsage-8    1000   25500 ns/op   30000 B/op   430 allocs/op",
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormatLargeTable-8    500   90000 ns/op   64000 B/op   120 allocs/op",
		"pkg: github.com/ben-ranford/lopper/internal/notify",
		"BenchmarkNotifyDispatch-8    1000   12000 ns/op   1024 B/op   12 allocs/op",
	}
	head := strings.Join(headLines, "\n")

	if err := os.WriteFile(basePath, []byte(base), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	if err := os.WriteFile(headPath, []byte(head), 0o644); err != nil {
		t.Fatalf("write head: %v", err)
	}

	baseData, err := parseBenchmarkFile(basePath)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	headData, err := parseBenchmarkFile(headPath)
	if err != nil {
		t.Fatalf("parse head: %v", err)
	}

	summary, hasRegression := compareBenchmarks(baseData, headData, deltaThresholds{
		bytesPct:  15,
		allocsPct: 10,
	})
	if !hasRegression {
		t.Fatalf("expected regression from increased bytes/op and allocs/op")
	}
	if !strings.Contains(summary, "BenchmarkCountUsage") {
		t.Fatalf("expected matched benchmark in summary, got:\n%s", summary)
	}
	if !strings.Contains(summary, "New-only benchmarks") {
		t.Fatalf("expected new-only benchmarks note, got:\n%s", summary)
	}
	if !strings.Contains(summary, "regression") {
		t.Fatalf("expected regression status, got:\n%s", summary)
	}
}

func TestPercentDelta(t *testing.T) {
	if got, failed := percentDelta(0, 0, 10); got != "0.0%" || failed {
		t.Fatalf("expected zero delta, got %q failed=%t", got, failed)
	}
	if got, failed := percentDelta(0, 1, 10); got != "new non-zero" || !failed {
		t.Fatalf("expected new non-zero failure, got %q failed=%t", got, failed)
	}
	if got, failed := percentDelta(100, 105, 10); got != "+5.0%" || failed {
		t.Fatalf("expected +5.0%% without failure, got %q failed=%t", got, failed)
	}
}

func TestBenchmarkParsingAndFormattingBranches(t *testing.T) {
	name, sample, ok := parseBenchmarkLine("", "BenchmarkFresh-8 1000 25000 ns/op 128 B/op 3 allocs/op")
	if !ok || name != "unknown-package/BenchmarkFresh" {
		t.Fatalf("expected unknown-package fallback benchmark, got name=%q ok=%v", name, ok)
	}
	if len(sample.bytesPerOp) != 1 || len(sample.allocsPerOp) != 1 {
		t.Fatalf("expected bytes and allocs samples, got %#v", sample)
	}

	for _, line := range []string{
		"BenchmarkShort",
		"BenchmarkNoMetrics-8 1000 25000 ns/op",
		"BenchmarkBadValue-8 1000 nope B/op",
	} {
		if _, _, ok := parseBenchmarkLine("pkg", line); ok {
			t.Fatalf("expected parseBenchmarkLine(%q) to be ignored", line)
		}
	}

	if got := normalizeBenchmarkName("BenchmarkDash-foo"); got != "BenchmarkDash-foo" {
		t.Fatalf("expected non-numeric suffix to remain, got %q", got)
	}
	if got := normalizeBenchmarkName("BenchmarkNoDash"); got != "BenchmarkNoDash" {
		t.Fatalf("expected dashless name to remain, got %q", got)
	}
	if got := average(nil); got != 0 {
		t.Fatalf("expected zero average for empty slice, got %.1f", got)
	}
	if got := average([]float64{2, 4, 6}); got != 4 {
		t.Fatalf("expected average 4, got %.1f", got)
	}

	summary, hasRegression := compareBenchmarks(benchmarkData{}, benchmarkData{}, deltaThresholds{bytesPct: 15, allocsPct: 10})
	if hasRegression {
		t.Fatalf("expected empty comparison to pass, got summary %q", summary)
	}
	if !strings.Contains(summary, "No overlapping benchmark names were found") || !strings.Contains(summary, "Result: memory benchmark gate passed.") {
		t.Fatalf("expected empty comparison summary, got %q", summary)
	}
}

func TestMainExitCodesAndErrorPaths(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	dir, basePath, headPath := writeMatchingBenchmarkFixtures(t)

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOutput string
	}{
		{
			name:       "success",
			args:       []string{"-base", basePath, "-head", headPath},
			wantCode:   0,
			wantOutput: "Result: memory benchmark gate passed.",
		},
		{
			name:       "regression",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "regressed.txt")},
			wantCode:   1,
			wantOutput: "Result: memory benchmark regression detected.",
		},
		{
			name:       "missing args",
			args:       nil,
			wantCode:   2,
			wantOutput: "both -base and -head are required",
		},
		{
			name:       "missing base file",
			args:       []string{"-base", filepath.Join(dir, "missing.txt"), "-head", headPath},
			wantCode:   2,
			wantOutput: "parse base benchmarks",
		},
	}

	writeBenchmarkFixture(t, filepath.Join(dir, "regressed.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 130 B/op 2 allocs/op",
	})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, exitCode := runBenchdeltaHelper(t, "TestMainExitCodesAndErrorPaths", tc.args...)
			assertBenchdeltaHelperExit(t, output, exitCode, tc.wantCode)
			assertBenchdeltaHelperOutput(t, output, tc.wantOutput)
		})
	}
}

func TestMainSummaryWriteErrorExit(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	dir, basePath, headPath := writeMatchingBenchmarkFixtures(t)

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	output, exitCode := runBenchdeltaHelper(t, "TestMainSummaryWriteErrorExit", "-base", basePath, "-head", headPath, "-summary-out", filepath.Join(blocker, "summary.md"))
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "write summary")
}

func runBenchdeltaMainIfRequested(t *testing.T) bool {
	t.Helper()
	if os.Getenv("GO_WANT_BENCHDELTA_HELPER") != "1" {
		return false
	}

	argsIndex := slices.Index(os.Args, "--")
	if argsIndex < 0 {
		t.Fatal("missing helper args separator")
	}
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	cmdArgs := os.Args[argsIndex+1:]
	os.Args = append([]string{oldArgs[0]}, cmdArgs...)
	main()
	return true
}

func writeBenchmarkFixture(t *testing.T, path string, lines []string) {
	t.Helper()
	content := append([]string{"goos: darwin"}, lines...)
	if err := os.WriteFile(path, []byte(strings.Join(content, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write benchmark fixture: %v", err)
	}
}

func writeMatchingBenchmarkFixtures(t *testing.T) (string, string, string) {
	t.Helper()

	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.txt")
	headPath := filepath.Join(dir, "head.txt")
	lines := []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
	}
	writeBenchmarkFixture(t, basePath, lines)
	writeBenchmarkFixture(t, headPath, lines)
	return dir, basePath, headPath
}

func runBenchdeltaHelper(t *testing.T, testName string, args ...string) ([]byte, int) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run="+testName, "--")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GO_WANT_BENCHDELTA_HELPER=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GO_WANT_BENCHDELTA_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, 0
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected subprocess exit error or success, got %v\n%s", err, output)
	}
	return output, exitErr.ExitCode()
}

func assertBenchdeltaHelperExit(t *testing.T, output []byte, gotCode, wantCode int) {
	t.Helper()
	if gotCode != wantCode {
		t.Fatalf("exit code = %d, want %d\n%s", gotCode, wantCode, output)
	}
}

func assertBenchdeltaHelperOutput(t *testing.T, output []byte, want string) {
	t.Helper()
	if !strings.Contains(string(output), want) {
		t.Fatalf("expected output to contain %q, got %q", want, string(output))
	}
}
