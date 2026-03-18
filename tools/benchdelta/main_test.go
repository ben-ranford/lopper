package main

import (
	"os"
	"path/filepath"
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
