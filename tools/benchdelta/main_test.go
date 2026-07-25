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
	headLines := []string{
		"goos: darwin",
		"pkg: github.com/ben-ranford/lopper/internal/lang/shared",
		"BenchmarkCountUsage-8    1000   25000 ns/op   30000 B/op   430 allocs/op",
		"BenchmarkCountUsage-8    1000   25500 ns/op   30000 B/op   430 allocs/op",
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormatLargeTable-8    500   90000 ns/op   64000 B/op   120 allocs/op",
	}
	writeBenchmarkFixture(t, basePath, baseLines[1:])
	writeBenchmarkFixture(t, headPath, headLines[1:])

	baseInput, err := parseBenchmarkFile(basePath)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	headInput, err := parseBenchmarkFile(headPath)
	if err != nil {
		t.Fatalf("parse head: %v", err)
	}

	summary, statusCode := compareBenchmarks(baseInput, headInput, deltaThresholds{
		bytesPct:  15,
		allocsPct: 10,
	})
	if statusCode != exitCodeRegression {
		t.Fatalf("expected regression status 1 from increased bytes/op and allocs/op, got %d\n%s", statusCode, summary)
	}
	if !strings.Contains(summary, "BenchmarkCountUsage") {
		t.Fatalf("expected matched benchmark in summary, got:\n%s", summary)
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
	name, sample, status, diagnostic := parseBenchmarkLine("", "BenchmarkFresh-8 1000 25000 ns/op 128 B/op 3 allocs/op")
	if status != benchmarkLineComplete || diagnostic != "" || name != "unknown-package/BenchmarkFresh" {
		t.Fatalf("expected unknown-package fallback benchmark, got name=%q status=%v diagnostic=%q", name, status, diagnostic)
	}
	if len(sample.bytesPerOp) != 1 || len(sample.allocsPerOp) != 1 {
		t.Fatalf("expected bytes and allocs samples, got %#v", sample)
	}

	for _, line := range []string{
		"BenchmarkShort",
		"BenchmarkNoMetrics-8 1000 25000 ns/op",
		"BenchmarkBadValue-8 1000 nope B/op",
	} {
		if _, _, status, _ := parseBenchmarkLine("pkg", line); status != benchmarkLineIgnored {
			t.Fatalf("expected parseBenchmarkLine(%q) to be ignored", line)
		}
	}

	for _, tc := range []struct {
		line           string
		wantDiagnostic string
	}{
		{
			line:           "BenchmarkBytesOnly-8 1000 25000 ns/op 128 B/op",
			wantDiagnostic: "`pkg/BenchmarkBytesOnly` missing allocs/op",
		},
		{
			line:           "BenchmarkAllocsOnly-8 1000 25000 ns/op 3 allocs/op",
			wantDiagnostic: "`pkg/BenchmarkAllocsOnly` missing B/op",
		},
	} {
		name, sample, status, diagnostic := parseBenchmarkLine("pkg", tc.line)
		if status != benchmarkLineIncomplete {
			t.Fatalf("parseBenchmarkLine(%q) status = %v, want incomplete", tc.line, status)
		}
		if name == "" || len(sample.bytesPerOp) != 0 || len(sample.allocsPerOp) != 0 {
			t.Fatalf("parseBenchmarkLine(%q) returned unexpected sample %#v for %q", tc.line, sample, name)
		}
		if diagnostic != tc.wantDiagnostic {
			t.Fatalf("parseBenchmarkLine(%q) diagnostic = %q, want %q", tc.line, diagnostic, tc.wantDiagnostic)
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
}

func TestIssue1403CompareRejectsMissingBenchmarks(t *testing.T) {
	tests := []struct {
		name           string
		base           benchmarkInput
		head           benchmarkInput
		wantStatusCode int
		wantContains   []string
	}{
		{
			name:           "both empty",
			base:           benchmarkInput{data: benchmarkData{}},
			head:           benchmarkInput{data: benchmarkData{}},
			wantStatusCode: exitCodeInvalid,
			wantContains: []string{
				"Comparison status: invalid",
				"Base benchmarks: none",
				"Head benchmarks: none",
				"No overlapping benchmark names were found between base and head.",
			},
		},
		{
			name: "head-only",
			base: benchmarkInput{data: benchmarkData{
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkShared": {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
			}},
			head: benchmarkInput{data: benchmarkData{
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkShared":   {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkHeadOnly": {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
			}},
			wantStatusCode: exitCodeInvalid,
			wantContains: []string{
				"Comparison status: invalid",
				"Head-only benchmarks (missing on base):",
				"`github.com/ben-ranford/lopper/pkg/bench/BenchmarkHeadOnly`",
			},
		},
		{
			name: "base-only",
			base: benchmarkInput{data: benchmarkData{
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkShared":   {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkBaseOnly": {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
			}},
			head: benchmarkInput{data: benchmarkData{
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkShared": {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
			}},
			wantStatusCode: exitCodeInvalid,
			wantContains: []string{
				"Comparison status: invalid",
				"Base-only benchmarks (missing on head):",
				"`github.com/ben-ranford/lopper/pkg/bench/BenchmarkBaseOnly`",
			},
		},
		{
			name: "zero overlap",
			base: benchmarkInput{data: benchmarkData{
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkBaseOnly": {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
			}},
			head: benchmarkInput{data: benchmarkData{
				"github.com/ben-ranford/lopper/pkg/bench/BenchmarkHeadOnly": {bytesPerOp: []float64{100}, allocsPerOp: []float64{1}},
			}},
			wantStatusCode: exitCodeInvalid,
			wantContains: []string{
				"Comparison status: invalid",
				"Base-only benchmarks (missing on head):",
				"Head-only benchmarks (missing on base):",
				"No overlapping benchmark names were found between base and head.",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary, statusCode := compareBenchmarks(tc.base, tc.head, deltaThresholds{bytesPct: 15, allocsPct: 10})
			if statusCode != tc.wantStatusCode {
				t.Fatalf("status code = %d, want %d\n%s", statusCode, tc.wantStatusCode, summary)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(summary, want) {
					t.Fatalf("expected summary to contain %q, got:\n%s", want, summary)
				}
			}
		})
	}
}

func TestIssue1403RejectsIncompleteBenchmarkSamples(t *testing.T) {
	t.Run("parse file excludes partial duplicates from aggregation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bench.txt")
		writeBenchmarkFixture(t, path, []string{
			"pkg: github.com/ben-ranford/lopper/internal/report",
			"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			"BenchmarkFormat-8 1000 100 ns/op 130 B/op",
			"BenchmarkAllocsOnly-8 1000 100 ns/op 4 allocs/op",
		})

		input, err := parseBenchmarkFile(path)
		if err != nil {
			t.Fatalf("parse benchmark file: %v", err)
		}
		sample := input.data["github.com/ben-ranford/lopper/internal/report/BenchmarkFormat"]
		if len(sample.bytesPerOp) != 1 || len(sample.allocsPerOp) != 1 {
			t.Fatalf("expected only complete sample to be aggregated, got %#v", sample)
		}
		if sample.bytesPerOp[0] != 100 || sample.allocsPerOp[0] != 1 {
			t.Fatalf("aggregated complete sample = %#v, want bytes=100 allocs=1", sample)
		}
		if got, want := input.incomplete, []string{
			"`github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing allocs/op",
			"`github.com/ben-ranford/lopper/internal/report/BenchmarkAllocsOnly` missing B/op",
		}; !slices.Equal(got, want) {
			t.Fatalf("incomplete diagnostics = %#v, want %#v", got, want)
		}
	})

	tests := []struct {
		name         string
		baseLines    []string
		headLines    []string
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "base bytes-only sample is incomplete",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			wantContains: []string{
				"Comparison status: incomplete",
				"Incomplete benchmark samples:",
				"base: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing allocs/op",
			},
		},
		{
			name: "head allocs-only sample is incomplete",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 1 allocs/op",
			},
			wantContains: []string{
				"Comparison status: incomplete",
				"head: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing B/op",
			},
		},
		{
			name: "base complete plus partial duplicate stays incomplete and averages only complete sample",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
				"BenchmarkFormat-8 1000 100 ns/op 130 B/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			wantContains: []string{
				"Comparison status: incomplete",
				"| `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` | 100.0 | 100.0 | +0.0% | 1.0 | 1.0 | +0.0% | ok |",
				"base: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing allocs/op",
			},
			wantOmit: []string{"115.0"},
		},
		{
			name: "head complete plus partial duplicate stays incomplete and averages only complete sample",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
				"BenchmarkFormat-8 1000 100 ns/op 4 allocs/op",
			},
			wantContains: []string{
				"Comparison status: incomplete",
				"| `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` | 100.0 | 100.0 | +0.0% | 1.0 | 1.0 | +0.0% | ok |",
				"head: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing B/op",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			basePath := filepath.Join(dir, "base.txt")
			headPath := filepath.Join(dir, "head.txt")
			writeBenchmarkFixture(t, basePath, tc.baseLines)
			writeBenchmarkFixture(t, headPath, tc.headLines)

			baseInput, err := parseBenchmarkFile(basePath)
			if err != nil {
				t.Fatalf("parse base: %v", err)
			}
			headInput, err := parseBenchmarkFile(headPath)
			if err != nil {
				t.Fatalf("parse head: %v", err)
			}

			summary, statusCode := compareBenchmarks(baseInput, headInput, deltaThresholds{bytesPct: 15, allocsPct: 10})
			if statusCode != exitCodeInvalid {
				t.Fatalf("status code = %d, want %d\n%s", statusCode, exitCodeInvalid, summary)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(summary, want) {
					t.Fatalf("expected summary to contain %q, got:\n%s", want, summary)
				}
			}
			for _, omit := range tc.wantOmit {
				if strings.Contains(summary, omit) {
					t.Fatalf("expected summary to omit %q, got:\n%s", omit, summary)
				}
			}
		})
	}
}

func TestParseBenchmarkFileBranches(t *testing.T) {
	t.Run("skips invalid benchmark lines in file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bench.txt")
		writeBenchmarkFixture(t, path, []string{
			"pkg: github.com/ben-ranford/lopper/internal/report",
			"BenchmarkIgnored-8 1000 100 ns/op",
			"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
		})

		input, err := parseBenchmarkFile(path)
		if err != nil {
			t.Fatalf("parse benchmark file: %v", err)
		}
		if len(input.data) != 1 {
			t.Fatalf("expected only one parsed benchmark, got %#v", input.data)
		}
		if len(input.incomplete) != 0 {
			t.Fatalf("expected no incomplete diagnostics, got %#v", input.incomplete)
		}
	})

	t.Run("scanner error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "too-large.txt")
		content := strings.Repeat("x", 70*1024) + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write oversized benchmark file: %v", err)
		}

		if _, err := parseBenchmarkFile(path); err == nil {
			t.Fatal("expected scanner error for oversized benchmark line")
		}
	})
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
			wantCode:   exitCodePassed,
			wantOutput: "Result: memory benchmark gate passed.",
		},
		{
			name:       "regression",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "regressed.txt")},
			wantCode:   exitCodeRegression,
			wantOutput: "Result: memory benchmark regression detected.",
		},
		{
			name:       "issue 1403 empty comparison invalid",
			args:       []string{"-base", filepath.Join(dir, "empty.txt"), "-head", filepath.Join(dir, "empty.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "Comparison status: invalid",
		},
		{
			name:       "issue 1403 head-only invalid",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "head-only.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "Head-only benchmarks (missing on base):",
		},
		{
			name:       "issue 1403 base-only invalid",
			args:       []string{"-base", filepath.Join(dir, "base-only.txt"), "-head", headPath},
			wantCode:   exitCodeInvalid,
			wantOutput: "Base-only benchmarks (missing on head):",
		},
		{
			name:       "issue 1403 zero overlap invalid",
			args:       []string{"-base", filepath.Join(dir, "zero-overlap-base.txt"), "-head", filepath.Join(dir, "zero-overlap-head.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "No overlapping benchmark names were found between base and head.",
		},
		{
			name:       "issue 1403 base bytes-only incomplete",
			args:       []string{"-base", filepath.Join(dir, "base-bytes-only.txt"), "-head", headPath},
			wantCode:   exitCodeInvalid,
			wantOutput: "Comparison status: incomplete",
		},
		{
			name:       "issue 1403 head allocs-only incomplete",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "head-allocs-only.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "Comparison status: incomplete",
		},
		{
			name:       "missing args",
			args:       nil,
			wantCode:   exitCodeInvalid,
			wantOutput: "both -base and -head are required",
		},
		{
			name:       "missing base file",
			args:       []string{"-base", filepath.Join(dir, "missing.txt"), "-head", headPath},
			wantCode:   exitCodeInvalid,
			wantOutput: "parse base benchmarks",
		},
		{
			name:       "missing head file",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "missing-head.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "parse head benchmarks",
		},
	}

	writeBenchmarkFixture(t, filepath.Join(dir, "regressed.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 130 B/op 2 allocs/op",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "empty.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "head-only.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
		"BenchmarkFormatHeadOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "base-only.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
		"BenchmarkFormatBaseOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "zero-overlap-base.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkBaseOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "zero-overlap-head.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkHeadOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "base-bytes-only.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 100 B/op",
	})
	writeBenchmarkFixture(t, filepath.Join(dir, "head-allocs-only.txt"), []string{
		"pkg: github.com/ben-ranford/lopper/internal/report",
		"BenchmarkFormat-8 1000 100 ns/op 1 allocs/op",
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
	assertBenchdeltaHelperExit(t, output, exitCode, exitCodeInvalid)
	assertBenchdeltaHelperOutput(t, output, "write summary")
}

func TestMainSummaryWriteSuccess(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	dir, basePath, headPath := writeMatchingBenchmarkFixtures(t)
	summaryPath := filepath.Join(dir, "summary.md")

	output, exitCode := runBenchdeltaHelper(t, "TestMainSummaryWriteSuccess", "-base", basePath, "-head", headPath, "-summary-out", summaryPath)
	assertBenchdeltaHelperExit(t, output, exitCode, exitCodePassed)
	assertBenchdeltaHelperOutput(t, output, "Result: memory benchmark gate passed.")

	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summaryBytes), "Result: memory benchmark gate passed.") {
		t.Fatalf("expected written summary to contain pass result, got:\n%s", summaryBytes)
	}
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
