package main

import (
	"bytes"
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
			wantOutput: "parse base benchmarks",
		},
		{
			name:       "missing head file",
			args:       []string{"-base", basePath, "-head", filepath.Join(dir, "missing-head.txt")},
			wantCode:   exitCodeInvalid,
			wantOutput: "parse head benchmarks",
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

func TestResolveBenchmarkDefinitionCapturesExactHeadDefinition(t *testing.T) {
	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
			"benchpkg/helper_test.go": `package benchpkg

import "testing"

func TestHelper(t *testing.T) {}
`,
			"benchpkg/goleak_test.go": `package benchpkg

import "testing"

func TestMain(m *testing.M) {
	testing.Init()
	m.Run()
}
`,
			"benchpkg2/subject.go": "package benchpkg2\n\nfunc SharedValue() int { return 7 }\n",
			"benchpkg2/second_benchmark_test.go": `package benchpkg2

import "testing"

func BenchmarkSecond(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
		},
		commit: true,
	})

	definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg", "./benchpkg2"}, 7, "450ms")
	if err != nil {
		t.Fatalf("resolve benchmark definition: %v", err)
	}

	if definition.Version != definitionVersion || definition.Count != 7 || definition.Benchtime != "450ms" || !definition.BenchMem {
		t.Fatalf("unexpected execution definition: %#v", definition)
	}
	if definition.RunPattern != defaultRunPattern {
		t.Fatalf("run pattern = %q, want %q", definition.RunPattern, defaultRunPattern)
	}
	if want := []string{"./benchpkg", "./benchpkg2"}; !slices.Equal(definition.PackageTargets, want) {
		t.Fatalf("package targets = %#v, want %#v", definition.PackageTargets, want)
	}
	if want := []resolvedBenchmark{
		{PackageTarget: "./benchpkg", Name: "BenchmarkHeadOnly"},
		{PackageTarget: "./benchpkg2", Name: "BenchmarkSecond"},
	}; !slices.Equal(definition.Benchmarks, want) {
		t.Fatalf("benchmarks = %#v, want %#v", definition.Benchmarks, want)
	}
	if definition.BenchPattern != "^(BenchmarkHeadOnly|BenchmarkSecond)$" {
		t.Fatalf("bench pattern = %q", definition.BenchPattern)
	}
	headCommit := strings.TrimSpace(runGitOutput(t, headRepo, "rev-parse", "HEAD"))
	if definition.ResolvedFrom != headCommit {
		t.Fatalf("resolved from = %q, want %q", definition.ResolvedFrom, headCommit)
	}
	if definition.ResolvedCommit != headCommit {
		t.Fatalf("resolved commit = %q, want %q", definition.ResolvedCommit, headCommit)
	}
	wantHarnessPaths := []string{
		"benchpkg/goleak_test.go",
		"benchpkg/head_benchmark_test.go",
		"benchpkg/helper_test.go",
		"benchpkg2/second_benchmark_test.go",
	}
	gotHarnessPaths := make([]string, 0, len(definition.HarnessFiles))
	for _, harness := range definition.HarnessFiles {
		gotHarnessPaths = append(gotHarnessPaths, harness.Path)
	}
	if !slices.Equal(gotHarnessPaths, wantHarnessPaths) {
		t.Fatalf("harness paths = %#v, want %#v", gotHarnessPaths, wantHarnessPaths)
	}
	for _, harness := range definition.HarnessFiles {
		content, ok := overlayFiles[harness.OverlayPath]
		if !ok {
			t.Fatalf("missing overlay content for %s", harness.OverlayPath)
		}
		if got := bytesDigest(content); got != harness.SHA256 {
			t.Fatalf("overlay digest for %s = %s, want %s", harness.Path, got, harness.SHA256)
		}
	}
}

func TestResolveBenchmarkDefinitionKeepsHeadIdentityForUnrelatedDirtyFiles(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
		},
		commit: true,
	})
	headCommit := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty but unrelated\n"), 0o600); err != nil {
		t.Fatalf("write unrelated dirty file: %v", err)
	}

	definition, _, err := resolveBenchmarkDefinition(repo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolve benchmark definition: %v", err)
	}
	if definition.ResolvedFrom != headCommit {
		t.Fatalf("resolved from = %q, want HEAD commit %q for unrelated dirt", definition.ResolvedFrom, headCommit)
	}
	if definition.ResolvedCommit != headCommit {
		t.Fatalf("resolved commit = %q, want %q", definition.ResolvedCommit, headCommit)
	}
}

func TestResolveBenchmarkDefinitionUsesContentIdentityForDirtyHarnessInputs(t *testing.T) {
	repo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/head_benchmark_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkHeadOnly(b *testing.B) {}\n",
		},
		commit: true,
	})
	headCommit := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
	dirtyHarness := []byte("package benchpkg\n\nimport \"testing\"\n\nfunc BenchmarkDirty(b *testing.B) {}\n")
	harnessPath := filepath.Join(repo, "benchpkg", "head_benchmark_test.go")
	if err := os.WriteFile(harnessPath, dirtyHarness, 0o600); err != nil {
		t.Fatalf("write dirty harness: %v", err)
	}
	runGitCommand(t, repo, "add", "benchpkg/head_benchmark_test.go")

	definition, overlayFiles, err := resolveBenchmarkDefinition(repo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolve benchmark definition: %v", err)
	}
	if definition.ResolvedFrom == headCommit || !strings.HasPrefix(definition.ResolvedFrom, "content-sha256:") {
		t.Fatalf("resolved from = %q, want content identity distinct from HEAD %q", definition.ResolvedFrom, headCommit)
	}
	if definition.ResolvedCommit != headCommit {
		t.Fatalf("resolved commit = %q, want HEAD commit %q", definition.ResolvedCommit, headCommit)
	}
	if got := overlayFiles["benchpkg/head_benchmark_test.go"]; !bytes.Equal(got, dirtyHarness) {
		t.Fatalf("captured harness bytes = %q, want %q", got, dirtyHarness)
	}
}

func TestRunDefinitionUsesResolvedHeadHarnessOnBase(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
		},
		commit: true,
	})
	definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolve benchmark definition: %v", err)
	}

	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "definition.json")
	overlayDir := filepath.Join(artifactDir, "overlay")
	definition.OverlayDir = filepath.Base(overlayDir)
	if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
		t.Fatalf("write benchmark definition: %v", err)
	}

	baseRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkBaseOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
		},
	})

	outputPath := filepath.Join(t.TempDir(), "bench.out")
	output, exitCode := runBenchdeltaHelper(t, "TestRunDefinitionUsesResolvedHeadHarnessOnBase", "run", "-repo", baseRepo, "-definition", definitionPath, "-output", outputPath)
	assertBenchdeltaHelperExit(t, output, exitCode, 0)
	assertBenchdeltaHelperOutput(t, output, "BenchmarkHeadOnly")
	if strings.Contains(string(output), "BenchmarkBaseOnly") {
		t.Fatalf("expected run output to use resolved head harness only, got:\n%s", output)
	}

	writtenOutput, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read bench output: %v", err)
	}
	assertBenchdeltaHelperOutput(t, writtenOutput, "lopper-bench-definition:")
	assertBenchdeltaHelperOutput(t, writtenOutput, "lopper-bench-resolved-from:")
	assertBenchdeltaHelperOutput(t, writtenOutput, "lopper-bench-resolved-commit:")
	assertBenchdeltaHelperOutput(t, writtenOutput, "lopper-bench-selection: ^(BenchmarkHeadOnly)$")
	assertBenchdeltaHelperOutput(t, writtenOutput, "lopper-bench-harness: benchpkg/head_benchmark_test.go")
}

func TestRunDefinitionRejectsBaseOnlyPackageTestFilesBeforeGoTest(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
		},
		commit: true,
	})
	definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolve benchmark definition: %v", err)
	}

	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "definition.json")
	overlayDir := filepath.Join(artifactDir, "overlay")
	definition.OverlayDir = filepath.Base(overlayDir)
	if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
		t.Fatalf("write benchmark definition: %v", err)
	}

	sentinelPath := filepath.Join(t.TempDir(), "unexpected-execution.txt")
	baseRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/base_testmain_test.go": `package benchpkg

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.WriteFile(os.Getenv("BENCHDELTA_SENTINEL"), []byte("testmain"), 0o600)
	os.Exit(m.Run())
}
`,
			"benchpkg/base_init_test.go": `package benchpkg

import "os"

func init() {
	_ = os.WriteFile(os.Getenv("BENCHDELTA_SENTINEL"), []byte("init"), 0o600)
}
`,
			"benchpkg/base_helper_test.go": `package benchpkg

var _ = missingHelperSymbol
`,
		},
	})

	cmd := exec.Command(os.Args[0], "-test.run=TestRunDefinitionRejectsBaseOnlyPackageTestFilesBeforeGoTest", "--", "run", "-repo", baseRepo, "-definition", definitionPath)
	cmd.Env = make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GO_WANT_BENCHDELTA_HELPER=") || strings.HasPrefix(entry, "BENCHDELTA_SENTINEL=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GO_WANT_BENCHDELTA_HELPER=1", "BENCHDELTA_SENTINEL="+sentinelPath)

	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected subprocess exit error, got %v\n%s", err, output)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitErr.ExitCode(), output)
	}
	assertBenchdeltaHelperOutput(t, output, "package test files not in benchmark manifest")
	assertBenchdeltaHelperOutput(t, output, "benchpkg/base_helper_test.go")
	assertBenchdeltaHelperOutput(t, output, "benchpkg/base_init_test.go")
	assertBenchdeltaHelperOutput(t, output, "benchpkg/base_testmain_test.go")
	if strings.Contains(string(output), "undefined: missingHelperSymbol") {
		t.Fatalf("expected fail-closed manifest rejection before go test compilation, got:\n%s", output)
	}
	if _, statErr := os.Stat(sentinelPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected sentinel to remain absent, got %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(baseRepo, "benchpkg", "head_benchmark_test.go")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected overlay target to remain absent after rejection, got %v", statErr)
	}
}

func TestRunDefinitionApplicationFailureExitsStatusTwo(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
		},
		commit: true,
	})
	definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg"}, 1, "1x")
	if err != nil {
		t.Fatalf("resolve benchmark definition: %v", err)
	}

	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "definition.json")
	overlayDir := filepath.Join(artifactDir, "overlay")
	definition.OverlayDir = filepath.Base(overlayDir)
	if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
		t.Fatalf("write benchmark definition: %v", err)
	}

	baseRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc DifferentValue() int { return 7 }\n",
		},
	})

	output, exitCode := runBenchdeltaHelper(t, "TestRunDefinitionApplicationFailureExitsStatusTwo", "run", "-repo", baseRepo, "-definition", definitionPath)
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "run benchmark definition")
	assertBenchdeltaHelperOutput(t, output, "undefined: SharedValue")
}

func TestMainResolveExitCodesAndOutput(t *testing.T) {
	if runBenchdeltaMainIfRequested(t) {
		return
	}

	headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
		modulePath: "example.com/benchrepo",
		files: map[string]string{
			"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
		},
		commit: true,
	})
	artifactDir := canonicalTempDir(t)
	definitionPath := filepath.Join(artifactDir, "definition.json")
	output, exitCode := runBenchdeltaHelper(t, "TestMainResolveExitCodesAndOutput", "resolve", "-repo", headRepo, "-package", "./benchpkg", "-count", "1", "-benchtime", "1x", "-out", definitionPath)
	assertBenchdeltaHelperExit(t, output, exitCode, 0)
	assertBenchdeltaHelperOutput(t, output, "lopper-bench-definition:")

	output, exitCode = runBenchdeltaHelper(t, "TestMainResolveExitCodesAndOutput", "resolve", "-repo", headRepo, "-package", "./benchpkg")
	assertBenchdeltaHelperExit(t, output, exitCode, 2)
	assertBenchdeltaHelperOutput(t, output, "resolve requires -out")
}

func TestDefinitionHelpers(t *testing.T) {
	t.Run("package flag string and set", func(t *testing.T) {
		var flags packageListFlag
		if err := flags.Set("./benchpkg"); err != nil {
			t.Fatalf("set package flag: %v", err)
		}
		if got := flags.String(); got != "./benchpkg" {
			t.Fatalf("package flag string = %q", got)
		}
		if err := flags.Set(" "); err == nil {
			t.Fatal("expected empty package target to fail")
		}
	})

	t.Run("package target confinement", func(t *testing.T) {
		repoRoot := t.TempDir()
		if _, err := packageTargetDir(repoRoot, "../escape"); err == nil || !strings.Contains(err.Error(), "escapes repository root") {
			t.Fatalf("expected package target escape error, got %v", err)
		}
		if _, err := packageTargetDir(repoRoot, "."); err == nil || !strings.Contains(err.Error(), "repo root") {
			t.Fatalf("expected repo root rejection, got %v", err)
		}
		if _, err := packageTargetDir(repoRoot, "/tmp/abs"); err == nil || !strings.Contains(err.Error(), "must be relative") {
			t.Fatalf("expected absolute path rejection, got %v", err)
		}
	})

	t.Run("goflags injection", func(t *testing.T) {
		env := withGoFlagsDisabledBuildVCS([]string{"PATH=/bin", "GOFLAGS=-mod=readonly"})
		if !slices.Contains(env, "GOFLAGS=-mod=readonly -buildvcs=false") {
			t.Fatalf("expected GOFLAGS injection, got %#v", env)
		}
		env = withGoFlagsDisabledBuildVCS([]string{"PATH=/bin"})
		if !slices.Contains(env, "GOFLAGS=-buildvcs=false") {
			t.Fatalf("expected default GOFLAGS injection, got %#v", env)
		}
	})

	t.Run("invalid definition rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "definition.json")
		if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatalf("write invalid definition: %v", err)
		}
		if _, _, err := readBenchmarkDefinition(path); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("expected incomplete definition error, got %v", err)
		}
	})

	t.Run("resolve and run command success", func(t *testing.T) {
		headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
			},
			commit: true,
		})
		artifactDir := canonicalTempDir(t)
		definitionPath := filepath.Join(artifactDir, "definition.json")
		overlayDir := filepath.Join(artifactDir, "overlay")
		if err := runResolveCommand([]string{
			"-repo", headRepo,
			"-package", "./benchpkg",
			"-count", "2",
			"-benchtime", "1x",
			"-out", definitionPath,
			"-overlay-dir", overlayDir,
		}); err != nil {
			t.Fatalf("runResolveCommand: %v", err)
		}

		definition, _, err := readBenchmarkDefinition(definitionPath)
		if err != nil {
			t.Fatalf("readBenchmarkDefinition: %v", err)
		}
		if definition.OverlayDir != "overlay" {
			t.Fatalf("overlay dir = %q", definition.OverlayDir)
		}

		baseRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkBaseOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
			},
		})
		outputPath := filepath.Join(t.TempDir(), "bench.out")
		if err := runBenchmarkCommand([]string{
			"-repo", baseRepo,
			"-definition", definitionPath,
			"-output", outputPath,
		}); err != nil {
			t.Fatalf("runBenchmarkCommand: %v", err)
		}
		output, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("read benchmark output: %v", err)
		}
		assertBenchdeltaHelperOutput(t, output, "BenchmarkHeadOnly")
	})

	t.Run("resolve validation errors", func(t *testing.T) {
		cases := [][]string{
			{"-unknown"},
			{"-package", "./benchpkg"},
			{"-out", filepath.Join(t.TempDir(), "definition.json")},
			{"-out", filepath.Join(t.TempDir(), "definition.json"), "-package", "./benchpkg", "-count", "0"},
			{"-out", filepath.Join(t.TempDir(), "definition.json"), "-package", "./benchpkg", "-benchtime", " "},
		}
		for _, args := range cases {
			if err := runResolveCommand(args); err == nil {
				t.Fatalf("expected resolve command error for args %#v", args)
			}
		}
	})

	t.Run("run validation errors", func(t *testing.T) {
		if err := runBenchmarkCommand(nil); err == nil || !strings.Contains(err.Error(), "run requires -definition") {
			t.Fatalf("expected missing definition error, got %v", err)
		}
		if err := runBenchmarkCommand([]string{"-unknown"}); err == nil {
			t.Fatal("expected flag parse error")
		}

		definitionPath := filepath.Join(t.TempDir(), "definition.json")
		if err := os.WriteFile(definitionPath, []byte(`{"version":99,"package_targets":["./benchpkg"],"benchmarks":[{"package_target":"./benchpkg","name":"BenchmarkHeadOnly"}],"bench_pattern":"^(BenchmarkHeadOnly)$","run_pattern":"^$","count":1,"benchtime":"1x","benchmem":true,"harness_files":[{"path":"benchpkg/head_benchmark_test.go","sha256":"abc","overlay_path":"benchpkg/head_benchmark_test.go"}]}`), 0o600); err != nil {
			t.Fatalf("write definition: %v", err)
		}
		if err := runBenchmarkCommand([]string{"-definition", definitionPath}); err == nil || !strings.Contains(err.Error(), "unsupported benchmark definition version") {
			t.Fatalf("expected unsupported version error, got %v", err)
		}
		definitionPath = filepath.Join(t.TempDir(), "bad-definition.json")
		if err := os.WriteFile(definitionPath, []byte("{"), 0o600); err != nil {
			t.Fatalf("write invalid json: %v", err)
		}
		if err := runBenchmarkCommand([]string{"-definition", definitionPath}); err == nil || !strings.Contains(err.Error(), "parse benchmark definition") {
			t.Fatalf("expected parse definition error, got %v", err)
		}
	})

	t.Run("definition write and apply errors", func(t *testing.T) {
		headRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = SharedValue()
	}
}
`,
			},
			commit: true,
		})
		definition, overlayFiles, err := resolveBenchmarkDefinition(headRepo, []string{"./benchpkg"}, 1, "1x")
		if err != nil {
			t.Fatalf("resolve benchmark definition: %v", err)
		}

		blocker := filepath.Join(canonicalTempDir(t), "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		if err := writeBenchmarkDefinition(filepath.Join(blocker, "definition.json"), filepath.Join(canonicalTempDir(t), "overlay"), definition, overlayFiles); err == nil {
			t.Fatal("expected writeBenchmarkDefinition to fail for blocked path")
		}

		artifactDir := canonicalTempDir(t)
		definitionPath := filepath.Join(artifactDir, "definition.json")
		overlayDir := filepath.Join(artifactDir, "overlay")
		definition.OverlayDir = filepath.Base(overlayDir)
		if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
			t.Fatalf("write benchmark definition: %v", err)
		}
		if err := os.Remove(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go")); err != nil {
			t.Fatalf("remove overlay: %v", err)
		}
		if err := applyBenchmarkOverlay(t.TempDir(), definitionPath, definition); err == nil || !strings.Contains(err.Error(), "read overlay file") {
			t.Fatalf("expected missing overlay error, got %v", err)
		}

		if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
			t.Fatalf("rewrite benchmark definition: %v", err)
		}
		if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), []byte("corrupt"), 0o600); err != nil {
			t.Fatalf("corrupt overlay: %v", err)
		}
		if err := applyBenchmarkOverlay(t.TempDir(), definitionPath, definition); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("expected digest mismatch, got %v", err)
		}

		outputBlocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(outputBlocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write output blocker: %v", err)
		}
		validRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/subject.go": "package benchpkg\n\nfunc SharedValue() int { return 42 }\n",
			},
		})
		if err := writeBenchmarkDefinition(definitionPath, overlayDir, definition, overlayFiles); err != nil {
			t.Fatalf("rewrite definition for output error: %v", err)
		}
		if err := runBenchmarkCommand([]string{
			"-repo", validRepo,
			"-definition", definitionPath,
			"-output", filepath.Join(outputBlocker, "bench.out"),
		}); err == nil || !strings.Contains(err.Error(), "write benchmark output") {
			t.Fatalf("expected write benchmark output error, got %v", err)
		}
	})

	t.Run("resolution parsing errors", func(t *testing.T) {
		repo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/bad_benchmark_test.go": "package benchpkg\n\nfunc BenchmarkBroken(\n",
			},
			commit: true,
		})
		if _, _, err := resolveBenchmarkDefinition(repo, []string{"./benchpkg"}, 1, "1x"); err == nil || !strings.Contains(err.Error(), "parse benchmark harness") {
			t.Fatalf("expected parse error, got %v", err)
		}

		noBenchRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/helper_test.go": "package benchpkg\n\nimport \"testing\"\n\nfunc TestOnly(t *testing.T) {}\n",
			},
			commit: true,
		})
		if _, _, err := resolveBenchmarkDefinition(noBenchRepo, []string{"./benchpkg"}, 1, "1x"); err == nil || !strings.Contains(err.Error(), "does not define any benchmarks") {
			t.Fatalf("expected no benchmark error, got %v", err)
		}

		noGitRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
			},
		})
		if _, _, err := resolveBenchmarkDefinition(noGitRepo, []string{"./benchpkg"}, 1, "1x"); err == nil || !strings.Contains(err.Error(), "resolve HEAD commit") {
			t.Fatalf("expected missing git HEAD error, got %v", err)
		}

		validRepo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
			},
			commit: true,
		})
		if _, _, err := resolveBenchmarkDefinition(validRepo, nil, 1, "1x"); err == nil || !strings.Contains(err.Error(), "no benchmarks resolved") {
			t.Fatalf("expected no package benchmark resolution error, got %v", err)
		}
	})

	t.Run("resolve and parse helpers", func(t *testing.T) {
		repo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

type receiver struct{}

func (receiver) BenchmarkMethod(b *testing.B) {}
func benchmarkLower(b *testing.B) {}
func BenchmarkNoParams() {}
func BenchmarkWrongParam(b testing.B) {}
func BenchmarkWrongSelector(b *benchpkg.B) {}
func BenchmarkValid(b *testing.B) {}
`,
			},
			commit: true,
		})
		names, err := benchmarkFunctionsInFile(filepath.Join(repo, "benchpkg", "head_benchmark_test.go"))
		if err != nil {
			t.Fatalf("benchmarkFunctionsInFile: %v", err)
		}
		if want := []string{"BenchmarkValid"}; !slices.Equal(names, want) {
			t.Fatalf("benchmark names = %#v, want %#v", names, want)
		}

		if _, _, err := resolvePackageBenchmarks(repo, "./missing"); err == nil || !strings.Contains(err.Error(), "read package") {
			t.Fatalf("expected missing package read error, got %v", err)
		}
	})

	t.Run("read definition defaults overlay and requires execution fields", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "definition.json")
		content := `{"version":1,"package_targets":["./benchpkg"],"benchmarks":[{"package_target":"./benchpkg","name":"BenchmarkHeadOnly"}],"bench_pattern":"^(BenchmarkHeadOnly)$","run_pattern":"^$","count":1,"benchtime":"1x","benchmem":true,"harness_files":[{"path":"benchpkg/head_benchmark_test.go","sha256":"abc","overlay_path":"benchpkg/head_benchmark_test.go"}]}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write definition: %v", err)
		}
		definition, _, err := readBenchmarkDefinition(path)
		if err != nil {
			t.Fatalf("readBenchmarkDefinition default overlay: %v", err)
		}
		if definition.OverlayDir != defaultOverlayDir {
			t.Fatalf("default overlay dir = %q, want %q", definition.OverlayDir, defaultOverlayDir)
		}

		badPath := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(badPath, []byte(`{"version":1,"package_targets":["./benchpkg"],"benchmarks":[{"package_target":"./benchpkg","name":"BenchmarkHeadOnly"}],"bench_pattern":"","run_pattern":"^$","count":1,"benchtime":"1x","benchmem":true,"harness_files":[{"path":"benchpkg/head_benchmark_test.go","sha256":"abc","overlay_path":"benchpkg/head_benchmark_test.go"}]}`), 0o600); err != nil {
			t.Fatalf("write bad definition: %v", err)
		}
		if _, _, err := readBenchmarkDefinition(badPath); err == nil || !strings.Contains(err.Error(), "missing required execution fields") {
			t.Fatalf("expected missing execution fields error, got %v", err)
		}

		if _, _, err := readBenchmarkDefinition(filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "read benchmark definition") {
			t.Fatalf("expected missing definition read error, got %v", err)
		}
	})

	t.Run("overlay and manifest path collisions", func(t *testing.T) {
		parent := canonicalTempDir(t)
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
			HarnessFiles:   []benchmarkHarnessFile{{Path: "benchpkg/head_benchmark_test.go", SHA256: bytesDigest([]byte("package benchpkg")), OverlayPath: "benchpkg/head_benchmark_test.go"}},
			OverlayDir:     "overlay",
		}

		blockedOverlayRoot := filepath.Join(parent, "blocked")
		if err := os.WriteFile(blockedOverlayRoot, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocked overlay root: %v", err)
		}
		if err := writeBenchmarkDefinition(filepath.Join(parent, "definition.json"), filepath.Join(blockedOverlayRoot, "overlay"), definition, map[string][]byte{"benchpkg/head_benchmark_test.go": []byte("package benchpkg")}); err == nil || !strings.Contains(err.Error(), "clear benchmark overlay") && !strings.Contains(err.Error(), "write overlay file") {
			t.Fatalf("expected create benchmark overlay error, got %v", err)
		}

	})

	t.Run("apply overlay blocked target paths", func(t *testing.T) {
		artifactDir := t.TempDir()
		overlayDir := filepath.Join(artifactDir, "overlay")
		if err := os.MkdirAll(filepath.Join(overlayDir, "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir overlay package dir: %v", err)
		}
		content := []byte("package benchpkg\n")
		if err := os.WriteFile(filepath.Join(overlayDir, "benchpkg", "head_benchmark_test.go"), content, 0o600); err != nil {
			t.Fatalf("write overlay content: %v", err)
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
			HarnessFiles:   []benchmarkHarnessFile{{Path: "blocked/head_benchmark_test.go", SHA256: bytesDigest(content), OverlayPath: "benchpkg/head_benchmark_test.go"}},
			OverlayDir:     "overlay",
		}
		definitionPath := filepath.Join(artifactDir, "definition.json")
		if err := os.WriteFile(definitionPath, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("write placeholder definition: %v", err)
		}

		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "blocked"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocked repo file: %v", err)
		}
		if err := applyBenchmarkOverlay(repo, definitionPath, definition); err == nil || !strings.Contains(err.Error(), `benchmark harness path "blocked/head_benchmark_test.go" is outside benchmark package targets`) {
			t.Fatalf("expected apply mkdir error, got %v", err)
		}

		definition.HarnessFiles[0].Path = "benchpkg"
		repo = t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "benchpkg"), 0o755); err != nil {
			t.Fatalf("mkdir benchpkg dir: %v", err)
		}
		if err := applyBenchmarkOverlay(repo, definitionPath, definition); err == nil || !strings.Contains(err.Error(), "benchmark harness path must reference a package _test.go file") {
			t.Fatalf("expected apply write error, got %v", err)
		}
	})

	t.Run("parse benchmark file and execute options", func(t *testing.T) {
		if _, err := parseBenchmarkFile(filepath.Join(t.TempDir(), "missing.txt")); err == nil || !strings.Contains(err.Error(), "file does not exist") {
			t.Fatalf("expected parseBenchmarkFile open error, got %v", err)
		}

		repo := newBenchmarkRepo(t, benchmarkRepoSpec{
			modulePath: "example.com/benchrepo",
			files: map[string]string{
				"benchpkg/head_benchmark_test.go": `package benchpkg

import "testing"

func BenchmarkHeadOnly(b *testing.B) {}
`,
			},
		})
		output, err := executeBenchmarkDefinition(repo, benchmarkDefinition{PackageTargets: []string{"./benchpkg"}, BenchPattern: "^(BenchmarkHeadOnly)$", RunPattern: "^$", Count: 1, Benchtime: "1x", BenchMem: false}, "-X example.com/benchrepo/internal/version.buildChannel=test")
		if err != nil {
			t.Fatalf("executeBenchmarkDefinition: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "BenchmarkHeadOnly") {
			t.Fatalf("expected benchmark output, got:\n%s", output)
		}
	})
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

type benchmarkRepoSpec struct {
	modulePath string
	files      map[string]string
	commit     bool
}

func newBenchmarkRepo(t *testing.T, spec benchmarkRepoSpec) string {
	t.Helper()

	repo := t.TempDir()
	modulePath := spec.modulePath
	if modulePath == "" {
		modulePath = "example.com/benchrepo"
	}
	files := map[string]string{
		"go.mod": "module " + modulePath + "\n\ngo 1.26.0\n",
	}
	for path, content := range spec.files {
		files[path] = content
	}
	for path, content := range files {
		absPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			t.Fatalf("create directory for %s: %v", path, err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if spec.commit {
		initGitRepo(t, repo)
	}
	return repo
}

func initGitRepo(t *testing.T, repo string) {
	t.Helper()
	runGitCommand(t, repo, "init", "-b", "main")
	runGitCommand(t, repo, "config", "user.name", "Bench Delta Test")
	runGitCommand(t, repo, "config", "user.email", "benchdelta@example.com")
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "fixture")
}

func runGitCommand(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = envWithoutGitOverrides(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func runGitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = envWithoutGitOverrides(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
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
