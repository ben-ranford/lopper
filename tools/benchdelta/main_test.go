package main

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	reportBenchmarkPkg = "github.com/ben-ranford/lopper/internal/report"
	benchBenchmarkPkg  = "github.com/ben-ranford/lopper/pkg/bench"
)

func TestParseBenchmarkFileAndCompare(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.txt")
	headPath := filepath.Join(dir, "head.txt")

	baseLines := []string{
		packageLine("github.com/ben-ranford/lopper/internal/lang/shared"),
		benchmarkFixtureLine("CountUsage", "1000", "25000", "ns/op", "25632", "B/op", "375", "allocs/op"),
		benchmarkFixtureLine("CountUsage", "1000", "25500", "ns/op", "25632", "B/op", "375", "allocs/op"),
		packageLine(reportBenchmarkPkg),
		benchmarkFixtureLine("FormatLargeTable", "500", "90000", "ns/op", "64000", "B/op", "120", "allocs/op"),
	}
	headLines := []string{
		packageLine("github.com/ben-ranford/lopper/internal/lang/shared"),
		benchmarkFixtureLine("CountUsage", "1000", "25000", "ns/op", "30000", "B/op", "430", "allocs/op"),
		benchmarkFixtureLine("CountUsage", "1000", "25500", "ns/op", "30000", "B/op", "430", "allocs/op"),
		packageLine(reportBenchmarkPkg),
		benchmarkFixtureLine("FormatLargeTable", "500", "90000", "ns/op", "64000", "B/op", "120", "allocs/op"),
	}
	writeBenchmarkFixture(t, basePath, baseLines)
	writeBenchmarkFixture(t, headPath, headLines)

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

func TestParseBenchmarkLineFallbackAndIgnoredBranches(t *testing.T) {
	name, sample, status, diagnostic := parseBenchmarkLine("", "BenchmarkFresh-8 1000 25000 ns/op 128 B/op 3 allocs/op")
	if status != benchmarkLineComplete || diagnostic != "" || name != "unknown-package/BenchmarkFresh" {
		t.Fatalf("expected unknown-package fallback benchmark, got name=%q status=%v diagnostic=%q", name, status, diagnostic)
	}
	if len(sample.bytesPerOp) != 1 || len(sample.allocsPerOp) != 1 {
		t.Fatalf("expected bytes and allocs samples, got %#v", sample)
	}

	for _, line := range []string{
		"BenchmarkShort",
		"BenchmarkBadValue-8 1000 nope B/op",
	} {
		if _, _, status, _ := parseBenchmarkLine("pkg", line); status != benchmarkLineIgnored {
			t.Fatalf("expected parseBenchmarkLine(%q) to be ignored", line)
		}
	}
}

func TestParseBenchmarkLineRejectsIncompleteMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		line           string
		wantDiagnostic string
	}{
		{
			name:           "bytes only",
			line:           benchmarkFixtureLine("BytesOnly", "1000", "25000", "ns/op", "128", "B/op"),
			wantDiagnostic: benchmarkMissingMetric("pkg", "BytesOnly", "allocs/op"),
		},
		{
			name:           "allocs only",
			line:           benchmarkFixtureLine("AllocsOnly", "1000", "25000", "ns/op", "3", "allocs/op"),
			wantDiagnostic: benchmarkMissingMetric("pkg", "AllocsOnly", "B/op"),
		},
		{
			name:           "ns only",
			line:           benchmarkFixtureLine("NsOnly", "1000", "25000", "ns/op"),
			wantDiagnostic: benchmarkMissingMetric("pkg", "NsOnly", "B/op and allocs/op"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
		})
	}
}

func TestParseBenchmarkLineRejectsNonFiniteMemoryMetrics(t *testing.T) {
	t.Parallel()

	spellings := acceptedNonFiniteSpellings(t)
	metrics := []nonFiniteMetricCase{
		{
			name:        "bytes",
			metric:      benchmarkMetricBytesPerOp,
			allocsValue: "3",
		},
		{
			name:       "allocs",
			metric:     benchmarkMetricAllocsPerOp,
			bytesValue: "128",
		},
	}

	for _, metric := range metrics {
		t.Run(metric.name, func(t *testing.T) {
			for _, spelling := range spellings {
				assertNonFiniteMetricRejected(t, metric, spelling)
			}
		})
	}
}

type nonFiniteMetricCase struct {
	name        string
	metric      string
	bytesValue  string
	allocsValue string
}

func TestAcceptedNonFiniteSpellings(t *testing.T) {
	t.Parallel()

	spellings := acceptedNonFiniteSpellings(t)
	wantCount := (1 << len("nan")) + 3*(1<<len("inf")) + 3*(1<<len("infinity"))
	if len(spellings) != wantCount {
		t.Fatalf("acceptedNonFiniteSpellings() returned %d spellings, want %d", len(spellings), wantCount)
	}

	unique := make(map[string]struct{}, len(spellings))
	for _, spelling := range spellings {
		if _, exists := unique[spelling]; exists {
			t.Fatalf("acceptedNonFiniteSpellings() returned duplicate %q", spelling)
		}
		unique[spelling] = struct{}{}
	}
	for _, representative := range []string{"nAn", "iNf", "+iNf", "-iNf", "iNfInItY", "+iNfInItY", "-iNfInItY"} {
		if !slices.Contains(spellings, representative) {
			t.Errorf("acceptedNonFiniteSpellings() omitted %q", representative)
		}
	}
	for _, signedNaN := range []string{"+nAn", "-nAn"} {
		if slices.Contains(spellings, signedNaN) {
			t.Errorf("acceptedNonFiniteSpellings() unexpectedly included %q", signedNaN)
		}
	}
}

func TestBenchmarkHelpers(t *testing.T) {
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
			base: benchmarkInput{data: benchmarkData{benchmarkKey(benchBenchmarkPkg, "Shared"): completeBenchmarkSample()}},
			head: benchmarkInput{data: benchmarkData{
				benchmarkKey(benchBenchmarkPkg, "Shared"):   completeBenchmarkSample(),
				benchmarkKey(benchBenchmarkPkg, "HeadOnly"): completeBenchmarkSample(),
			}},
			wantStatusCode: exitCodeInvalid,
			wantContains: []string{
				"Comparison status: invalid",
				"Head-only benchmarks (missing on base):",
				benchmarkRef(benchBenchmarkPkg, "HeadOnly"),
			},
		},
		{
			name: "base-only",
			base: benchmarkInput{data: benchmarkData{
				benchmarkKey(benchBenchmarkPkg, "Shared"):   completeBenchmarkSample(),
				benchmarkKey(benchBenchmarkPkg, "BaseOnly"): completeBenchmarkSample(),
			}},
			head:           benchmarkInput{data: benchmarkData{benchmarkKey(benchBenchmarkPkg, "Shared"): completeBenchmarkSample()}},
			wantStatusCode: exitCodeInvalid,
			wantContains: []string{
				"Comparison status: invalid",
				"Base-only benchmarks (missing on head):",
				benchmarkRef(benchBenchmarkPkg, "BaseOnly"),
			},
		},
		{
			name:           "zero overlap",
			base:           benchmarkInput{data: benchmarkData{benchmarkKey(benchBenchmarkPkg, "BaseOnly"): completeBenchmarkSample()}},
			head:           benchmarkInput{data: benchmarkData{benchmarkKey(benchBenchmarkPkg, "HeadOnly"): completeBenchmarkSample()}},
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

func TestParseBenchmarkFileExcludesPartialDuplicatesFromAggregation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.txt")
	writeBenchmarkFixture(t, path, reportFixture(completeBenchmark("Format"), bytesOnlyBenchmark("Format", "130"), allocsOnlyBenchmark("AllocsOnly", "4")))

	input, err := parseBenchmarkFile(path)
	if err != nil {
		t.Fatalf("parse benchmark file: %v", err)
	}
	sample := input.data[benchmarkKey(reportBenchmarkPkg, "Format")]
	if len(sample.bytesPerOp) != 1 || len(sample.allocsPerOp) != 1 {
		t.Fatalf("expected only complete sample to be aggregated, got %#v", sample)
	}
	if sample.bytesPerOp[0] != 100 || sample.allocsPerOp[0] != 1 {
		t.Fatalf("aggregated complete sample = %#v, want bytes=100 allocs=1", sample)
	}
	if got, want := input.incomplete, []string{
		benchmarkMissingMetric(reportBenchmarkPkg, "Format", "allocs/op"),
		benchmarkMissingMetric(reportBenchmarkPkg, "AllocsOnly", "B/op"),
	}; !slices.Equal(got, want) {
		t.Fatalf("incomplete diagnostics = %#v, want %#v", got, want)
	}
}

func TestParseBenchmarkFileRecordsNsOnlyBenchmarksAsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.txt")
	writeBenchmarkFixture(t, path, reportFixture(completeBenchmark("Format"), benchmarkFixtureLine("NsOnly", "1000", "100", "ns/op")))

	input, err := parseBenchmarkFile(path)
	if err != nil {
		t.Fatalf("parse benchmark file: %v", err)
	}
	if len(input.data) != 1 {
		t.Fatalf("expected one complete benchmark to remain, got %#v", input.data)
	}
	if got, want := input.incomplete, []string{
		benchmarkMissingMetric(reportBenchmarkPkg, "NsOnly", "B/op and allocs/op"),
	}; !slices.Equal(got, want) {
		t.Fatalf("incomplete diagnostics = %#v, want %#v", got, want)
	}
}

func TestCompareBenchmarksRejectsIncompleteSamples(t *testing.T) {
	t.Parallel()

	tests := []comparisonScenario{
		{
			name:      "base bytes-only sample is incomplete",
			baseLines: reportFixture(bytesOnlyBenchmark("Format", "100")),
			headLines: reportFixture(completeBenchmark("Format")),
			wantContains: []string{
				"Comparison status: incomplete",
				"Incomplete benchmark samples:",
				"base: " + benchmarkMissingMetric(reportBenchmarkPkg, "Format", "allocs/op"),
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
		{
			name:      "head allocs-only sample is incomplete",
			baseLines: reportFixture(completeBenchmark("Format")),
			headLines: reportFixture(allocsOnlyBenchmark("Format", "1")),
			wantContains: []string{
				"Comparison status: incomplete",
				"head: " + benchmarkMissingMetric(reportBenchmarkPkg, "Format", "B/op"),
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
		{
			name:      "base ns-only sample is incomplete",
			baseLines: reportFixture(benchmarkFixtureLine("Format", "1000", "100", "ns/op")),
			headLines: reportFixture(completeBenchmark("Format")),
			wantContains: []string{
				"Comparison status: incomplete",
				"base: " + benchmarkMissingMetric(reportBenchmarkPkg, "Format", "B/op and allocs/op"),
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
		{
			name:      "base complete plus partial duplicate stays incomplete and averages only complete sample",
			baseLines: reportFixture(completeBenchmark("Format"), bytesOnlyBenchmark("Format", "130")),
			headLines: reportFixture(completeBenchmark("Format")),
			wantContains: []string{
				"Comparison status: incomplete",
				okComparisonRow(reportBenchmarkPkg, "Format"),
				"base: " + benchmarkMissingMetric(reportBenchmarkPkg, "Format", "allocs/op"),
			},
			wantOmit: append(incompleteInvalidDiagnostics(), "115.0"),
		},
		{
			name:      "head complete plus partial duplicate stays incomplete and averages only complete sample",
			baseLines: reportFixture(completeBenchmark("Format")),
			headLines: reportFixture(completeBenchmark("Format"), allocsOnlyBenchmark("Format", "4")),
			wantContains: []string{
				"Comparison status: incomplete",
				okComparisonRow(reportBenchmarkPkg, "Format"),
				"head: " + benchmarkMissingMetric(reportBenchmarkPkg, "Format", "B/op"),
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
		{
			name:      "mismatched sample counts are incomplete",
			baseLines: reportFixture(repeatedCompleteBenchmark("Format", 3)...),
			headLines: reportFixture(completeBenchmark("Format")),
			wantContains: []string{
				"Comparison status: incomplete",
				sampleCountMismatchDiagnostic(benchmarkKey(reportBenchmarkPkg, "Format"), 3, 1),
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertComparisonScenario(t, tc)
		})
	}
}

func TestCompareBenchmarksRejectsInvalidSamples(t *testing.T) {
	t.Parallel()

	tests := []comparisonScenario{
		{
			name:      "base bytes nan is invalid",
			baseLines: reportFixture(bytesAllocsBenchmark("Format", "NaN", "1")),
			headLines: reportFixture(completeBenchmark("Format")),
			wantContains: []string{
				"Comparison status: invalid",
				"base: " + invalidBenchmarkMetric(reportBenchmarkPkg, "Format", "B/op", "NaN"),
				"Result: benchmark input contained invalid memory samples; each B/op and allocs/op value must be finite.",
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
		{
			name:      "head allocs inf is invalid",
			baseLines: reportFixture(completeBenchmark("Format")),
			headLines: reportFixture(bytesAllocsBenchmark("Format", "100", "+Inf")),
			wantContains: []string{
				"Comparison status: invalid",
				"head: " + invalidBenchmarkMetric(reportBenchmarkPkg, "Format", "allocs/op", "+Inf"),
				"Result: benchmark input contained invalid memory samples; each B/op and allocs/op value must be finite.",
			},
			wantOmit: incompleteInvalidDiagnostics(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertComparisonScenario(t, tc)
		})
	}
}

func TestParseBenchmarkFileBranches(t *testing.T) {
	t.Run("records non-finite benchmark lines as invalid", func(t *testing.T) {
		input := parseBenchmarkFixture(t, reportFixture(bytesAllocsBenchmark("Invalid", "nan", "1")))
		wantDiagnostic := invalidBenchmarkMetric(reportBenchmarkPkg, "Invalid", "B/op", "nan")
		if !slices.Equal(input.invalid, []string{wantDiagnostic}) {
			t.Fatalf("invalid diagnostics = %#v, want %#v", input.invalid, []string{wantDiagnostic})
		}
		if len(input.incomplete) != 0 || len(input.data) != 0 {
			t.Fatalf("non-finite line classified outside invalid: data=%#v incomplete=%#v", input.data, input.incomplete)
		}
	})

	t.Run("skips invalid benchmark lines in file", func(t *testing.T) {
		input := parseBenchmarkFixture(t, reportFixture(benchmarkFixtureLine("Ignored", "1000", "100", benchmarkMetricNsPerOp), completeBenchmark("Format")))
		if len(input.data) != 1 || len(input.incomplete) != 1 {
			t.Fatalf("expected one parsed benchmark and one incomplete diagnostic, got data=%#v incomplete=%#v", input.data, input.incomplete)
		}
		if input.incomplete[0] != benchmarkMissingMetric(reportBenchmarkPkg, "Ignored", "B/op and allocs/op") {
			t.Fatalf("unexpected incomplete diagnostic: %#v", input.incomplete)
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
		wantOmit   []string
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
			wantOmit:   incompleteInvalidDiagnostics(),
		},
		{
			name:       "issue 1403 head allocs-only incomplete",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "head-allocs-only.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "Comparison status: incomplete",
			wantOmit:   incompleteInvalidDiagnostics(),
		},
		{
			name:       "ns-only incomplete",
			args:       []string{"-base", filepath.Join(dir, "base-ns-only.txt"), "-head", headPath},
			wantCode:   exitCodeInvalid,
			wantOutput: benchmarkMissingMetric(reportBenchmarkPkg, "Format", "B/op and allocs/op"),
			wantOmit:   incompleteInvalidDiagnostics(),
		},
		{
			name:       "sample count mismatch incomplete",
			args:       []string{"-base", filepath.Join(dir, "count-mismatch-base.txt"), "-head", filepath.Join(dir, "count-mismatch-head.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: sampleCountMismatchDiagnostic(benchmarkKey(reportBenchmarkPkg, "Format"), 3, 1),
			wantOmit:   incompleteInvalidDiagnostics(),
		},
		{
			name:       "bytes nan invalid",
			args:       []string{"-base", filepath.Join(dir, "base-bytes-nan.txt"), "-head", headPath},
			wantCode:   exitCodeInvalid,
			wantOutput: invalidBenchmarkMetric(reportBenchmarkPkg, "Format", "B/op", "NaN"),
		},
		{
			name:       "allocs signed mixed-case infinity invalid",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "head-allocs-mixed-infinity.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: invalidBenchmarkMetric(reportBenchmarkPkg, "Format", "allocs/op", "-iNfInItY"),
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
			wantOutput: "base benchmark input could not be read",
		},
		{
			name:       "missing head file",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "missing-head.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "head benchmark input could not be read",
		},
	}

	writeBenchmarkFixture(t, filepath.Join(dir, "regressed.txt"), reportFixture(bytesAllocsBenchmark("Format", "130", "2")))
	writeBenchmarkFixture(t, filepath.Join(dir, "empty.txt"), reportFixture())
	writeBenchmarkFixture(t, filepath.Join(dir, "head-only.txt"), reportFixture(completeBenchmark("Format"), completeBenchmark("FormatHeadOnly")))
	writeBenchmarkFixture(t, filepath.Join(dir, "base-only.txt"), reportFixture(completeBenchmark("Format"), completeBenchmark("FormatBaseOnly")))
	writeBenchmarkFixture(t, filepath.Join(dir, "zero-overlap-base.txt"), reportFixture(completeBenchmark("BaseOnly")))
	writeBenchmarkFixture(t, filepath.Join(dir, "zero-overlap-head.txt"), reportFixture(completeBenchmark("HeadOnly")))
	writeBenchmarkFixture(t, filepath.Join(dir, "base-bytes-only.txt"), reportFixture(bytesOnlyBenchmark("Format", "100")))
	writeBenchmarkFixture(t, filepath.Join(dir, "head-allocs-only.txt"), reportFixture(allocsOnlyBenchmark("Format", "1")))
	writeBenchmarkFixture(t, filepath.Join(dir, "base-ns-only.txt"), reportFixture(benchmarkFixtureLine("Format", "1000", "100", "ns/op")))
	writeBenchmarkFixture(t, filepath.Join(dir, "count-mismatch-base.txt"), reportFixture(repeatedCompleteBenchmark("Format", 3)...))
	writeBenchmarkFixture(t, filepath.Join(dir, "count-mismatch-head.txt"), reportFixture(completeBenchmark("Format")))
	writeBenchmarkFixture(t, filepath.Join(dir, "base-bytes-nan.txt"), reportFixture(bytesAllocsBenchmark("Format", "NaN", "1")))
	writeBenchmarkFixture(t, filepath.Join(dir, "head-allocs-mixed-infinity.txt"), reportFixture(bytesAllocsBenchmark("Format", "100", "-iNfInItY")))

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, exitCode := runBenchdeltaHelper(t, "TestMainExitCodesAndErrorPaths", tc.args...)
			assertBenchdeltaHelperExit(t, output, exitCode, tc.wantCode)
			assertBenchdeltaHelperOutput(t, output, tc.wantOutput)
			assertBenchdeltaHelperOutputOmitsAll(t, output, tc.wantOmit)
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

func TestMainInvalidComparisonsStillWriteDeterministicSummaryArtifacts(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	dir, _, headPath := writeMatchingBenchmarkFixtures(t)
	malformedPath := filepath.Join(dir, "malformed.txt")
	if err := os.WriteFile(malformedPath, []byte(strings.Repeat("x", 70*1024)+"\n"), 0o644); err != nil {
		t.Fatalf("write malformed benchmark file: %v", err)
	}
	writeBenchmarkFixture(t, filepath.Join(dir, "zero-overlap-base.txt"), reportFixture(completeBenchmark("BaseOnly")))
	writeBenchmarkFixture(t, filepath.Join(dir, "zero-overlap-head.txt"), reportFixture(completeBenchmark("HeadOnly")))

	tests := []struct {
		name         string
		args         []string
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "missing base file",
			args: []string{"-base", filepath.Join(dir, "missing.txt"), "-head", headPath},
			wantContains: []string{
				"Comparison status: invalid",
				"Base benchmarks: unavailable",
				"Head benchmarks: unavailable",
				"base benchmark input could not be read",
				"missing.txt",
			},
			wantOmit: []string{
				"Result: memory benchmark gate passed.",
				"Result: memory benchmark regression detected.",
			},
		},
		{
			name: "unreadable base path",
			args: []string{"-base", dir, "-head", headPath},
			wantContains: []string{
				"Comparison status: invalid",
				"Base benchmarks: unavailable",
				"Head benchmarks: unavailable",
				"base benchmark input could not be read",
			},
			wantOmit: []string{
				"Result: memory benchmark gate passed.",
				"Result: memory benchmark regression detected.",
			},
		},
		{
			name: "malformed base file",
			args: []string{"-base", malformedPath, "-head", headPath},
			wantContains: []string{
				"Comparison status: invalid",
				"Base benchmarks: unavailable",
				"Head benchmarks: unavailable",
				"base benchmark input could not be read",
				"token too long",
			},
			wantOmit: []string{
				"Result: memory benchmark gate passed.",
				"Result: memory benchmark regression detected.",
			},
		},
		{
			name: "unrelated head comparison",
			args: []string{"-base", filepath.Join(dir, "zero-overlap-base.txt"), "-head", filepath.Join(dir, "zero-overlap-head.txt")},
			wantContains: []string{
				"Comparison status: invalid",
				"Base benchmarks: 1",
				"Head benchmarks: 1",
				"No overlapping benchmark names were found between base and head.",
			},
			wantOmit: []string{
				"Result: memory benchmark gate passed.",
				"Result: memory benchmark regression detected.",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summaryPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".md")
			args := append(append([]string{}, tc.args...), "-summary-out", summaryPath)
			output, exitCode := runBenchdeltaHelper(t, "TestMainInvalidComparisonsStillWriteDeterministicSummaryArtifacts", args...)
			assertBenchdeltaHelperExit(t, output, exitCode, exitCodeInvalid)

			summaryBytes, err := os.ReadFile(summaryPath)
			if err != nil {
				t.Fatalf("read summary artifact: %v", err)
			}
			summary := string(summaryBytes)
			for _, want := range tc.wantContains {
				if !strings.Contains(summary, want) {
					t.Fatalf("expected summary artifact to contain %q, got:\n%s", want, summary)
				}
			}
			for _, omit := range tc.wantOmit {
				if strings.Contains(summary, omit) {
					t.Fatalf("expected summary artifact to omit %q, got:\n%s", omit, summary)
				}
			}
		})
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

func assertNonFiniteMetricRejected(t *testing.T, metric nonFiniteMetricCase, spelling string) {
	t.Helper()

	bytesValue, allocsValue := nonFiniteMetricSampleValues(metric, spelling)
	line := bytesAllocsBenchmark("NonFinite", bytesValue, allocsValue)
	name, sample, status, diagnostic := parseBenchmarkLine("pkg", line)
	if status != benchmarkLineInvalid {
		t.Fatalf("parseBenchmarkLine(%q) status = %v, want invalid", line, status)
	}
	if name == "" || len(sample.bytesPerOp) != 0 || len(sample.allocsPerOp) != 0 {
		t.Fatalf("parseBenchmarkLine(%q) returned unexpected sample %#v for %q", line, sample, name)
	}
	wantDiagnostic := invalidBenchmarkMetric("pkg", "NonFinite", metric.metric, spelling)
	if diagnostic != wantDiagnostic {
		t.Fatalf("parseBenchmarkLine(%q) diagnostic = %q, want %q", line, diagnostic, wantDiagnostic)
	}
}

func nonFiniteMetricSampleValues(tc nonFiniteMetricCase, spelling string) (string, string) {
	if tc.metric == benchmarkMetricBytesPerOp {
		return spelling, tc.allocsValue
	}
	return tc.bytesValue, spelling
}

func parseBenchmarkFixture(t *testing.T, lines []string) benchmarkInput {
	t.Helper()

	path := filepath.Join(t.TempDir(), "bench.txt")
	writeBenchmarkFixture(t, path, lines)

	input, err := parseBenchmarkFile(path)
	if err != nil {
		t.Fatalf("parse benchmark file: %v", err)
	}
	return input
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
	lines := reportFixture(completeBenchmark("Format"))
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

func assertBenchdeltaHelperOutputOmitsAll(t *testing.T, output []byte, omitted []string) {
	t.Helper()
	assertSummaryOmitsAll(t, string(output), omitted)
}

type comparisonScenario struct {
	name         string
	baseLines    []string
	headLines    []string
	wantContains []string
	wantOmit     []string
}

func assertComparisonScenario(t *testing.T, tc comparisonScenario) {
	t.Helper()

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
	assertSummaryContainsAll(t, summary, tc.wantContains)
	assertSummaryOmitsAll(t, summary, tc.wantOmit)
}

func assertSummaryContainsAll(t *testing.T, summary string, expected []string) {
	t.Helper()
	for _, want := range expected {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected summary to contain %q, got:\n%s", want, summary)
		}
	}
}

func assertSummaryOmitsAll(t *testing.T, summary string, omitted []string) {
	t.Helper()
	for _, omit := range omitted {
		if strings.Contains(summary, omit) {
			t.Fatalf("expected summary to omit %q, got:\n%s", omit, summary)
		}
	}
}

func incompleteInvalidDiagnostics() []string {
	return []string{
		"Head-only benchmarks (missing on base):",
		"Base-only benchmarks (missing on head):",
		"No overlapping benchmark names were found between base and head.",
	}
}

func packageLine(pkg string) string {
	return "pkg: " + pkg
}

func benchmarkFixtureLine(name string, parts ...string) string {
	return "Benchmark" + name + "-8 " + strings.Join(parts, " ")
}

func reportFixture(lines ...string) []string {
	return append([]string{packageLine(reportBenchmarkPkg)}, lines...)
}

func completeBenchmark(name string) string {
	return bytesAllocsBenchmark(name, "100", "1")
}

func bytesAllocsBenchmark(name, bytes, allocs string) string {
	return benchmarkFixtureLine(name, "1000", "100", benchmarkMetricNsPerOp, bytes, benchmarkMetricBytesPerOp, allocs, benchmarkMetricAllocsPerOp)
}

func bytesOnlyBenchmark(name, bytes string) string {
	return benchmarkFixtureLine(name, "1000", "100", benchmarkMetricNsPerOp, bytes, benchmarkMetricBytesPerOp)
}

func allocsOnlyBenchmark(name, allocs string) string {
	return benchmarkFixtureLine(name, "1000", "100", benchmarkMetricNsPerOp, allocs, benchmarkMetricAllocsPerOp)
}

func repeatedCompleteBenchmark(name string, count int) []string {
	lines := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lines = append(lines, completeBenchmark(name))
	}
	return lines
}

func benchmarkKey(pkg, name string) string {
	return pkg + "/Benchmark" + name
}

func benchmarkRef(pkg, name string) string {
	return "`" + benchmarkKey(pkg, name) + "`"
}

func benchmarkMissingMetric(pkg, name, metric string) string {
	return benchmarkRef(pkg, name) + " missing " + metric
}

func invalidBenchmarkMetric(pkg, name, metric, value string) string {
	return benchmarkRef(pkg, name) + " has non-finite " + metric + " value " + strconv.Quote(value)
}

func acceptedNonFiniteSpellings(t *testing.T) []string {
	t.Helper()

	var accepted []string
	for _, token := range []string{"nan", "inf", "infinity"} {
		for _, permutation := range asciiCasePermutations(token) {
			for _, sign := range []string{"", "+", "-"} {
				candidate := sign + permutation
				value, err := strconv.ParseFloat(candidate, 64)
				if err == nil && (math.IsNaN(value) || math.IsInf(value, 0)) {
					accepted = append(accepted, candidate)
				}
			}
		}
	}
	if len(accepted) == 0 {
		t.Fatal("strconv.ParseFloat accepted no non-finite spellings")
	}
	return accepted
}

func asciiCasePermutations(token string) []string {
	permutations := make([]string, 0, 1<<len(token))
	for mask := 0; mask < 1<<len(token); mask++ {
		permutation := []byte(token)
		for i := range permutation {
			if mask&(1<<i) != 0 {
				permutation[i] -= 'a' - 'A'
			}
		}
		permutations = append(permutations, string(permutation))
	}
	return permutations
}

func completeBenchmarkSample() samples {
	return samples{bytesPerOp: []float64{100}, allocsPerOp: []float64{1}}
}

func okComparisonRow(pkg, name string) string {
	return "| " + benchmarkRef(pkg, name) + " | 100.0 | 100.0 | +0.0% | 1.0 | 1.0 | +0.0% | ok |"
}
