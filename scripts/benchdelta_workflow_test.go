package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const workflowReportPkg = "github.com/ben-ranford/lopper/internal/report"

func TestBenchdeltaRejectsInvalidAndIncompleteInputs(t *testing.T) {
	t.Parallel()

	repoRoot := repoPath(t, "")
	dir := t.TempDir()
	binaryPath := buildBenchdeltaBinary(t, repoRoot, dir)

	tests := []workflowComparisonCase{
		{
			name:     "empty comparison invalid",
			baseID:   "empty",
			headID:   "empty",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"No overlapping benchmark names were found between base and head.",
			},
		},
		{
			name:     "head only invalid",
			baseID:   "shared",
			headID:   "head-only",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"Head-only benchmarks (missing on base):",
			},
		},
		{
			name:     "base only invalid",
			baseID:   "base-only",
			headID:   "shared",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"Base-only benchmarks (missing on head):",
			},
		},
		{
			name:     "zero overlap invalid",
			baseID:   "zero-overlap-base",
			headID:   "zero-overlap-head",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"No overlapping benchmark names were found between base and head.",
			},
		},
		{
			name:     "base bytes only incomplete",
			baseID:   "base-bytes-only",
			headID:   "shared",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				"base: " + workflowMissingMetric("Format", "allocs/op"),
			},
			wantOmit: workflowIncompleteInvalidDiagnostics(),
		},
		{
			name:     "head allocs only incomplete",
			baseID:   "shared",
			headID:   "head-allocs-only",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				"head: " + workflowMissingMetric("Format", "B/op"),
			},
			wantOmit: workflowIncompleteInvalidDiagnostics(),
		},
		{
			name:     "base ns only incomplete",
			baseID:   "base-ns-only",
			headID:   "shared",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				"base: " + workflowMissingMetric("Format", "B/op and allocs/op"),
			},
			wantOmit: workflowIncompleteInvalidDiagnostics(),
		},
		{
			name:     "sample count mismatch incomplete",
			baseID:   "count-mismatch-base",
			headID:   "count-mismatch-head",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				workflowSampleCountMismatch("Format", 3, 1),
			},
			wantOmit: workflowIncompleteInvalidDiagnostics(),
		},
		{
			name:     "complete plus partial duplicates stay incomplete",
			baseID:   "partial-base",
			headID:   "partial-head",
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				workflowOKRow("Format"),
				"base: " + workflowMissingMetric("Format", "allocs/op"),
				"head: " + workflowMissingMetric("Format", "B/op"),
			},
			wantOmit: workflowIncompleteInvalidDiagnostics(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertWorkflowBenchdeltaCase(t, repoRoot, dir, binaryPath, tc)
		})
	}
}

type workflowComparisonCase struct {
	name         string
	baseID       string
	headID       string
	wantCode     int
	wantContains []string
	wantOmit     []string
}

func buildBenchdeltaBinary(t *testing.T, repoRoot, dir string) string {
	t.Helper()

	binaryPath := filepath.Join(dir, "benchdelta")
	build := exec.Command("go", "build", "-o", binaryPath, "./tools/benchdelta")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build benchdelta: %v\n%s", err, output)
	}
	return binaryPath
}

func assertWorkflowBenchdeltaCase(t *testing.T, repoRoot, dir, binaryPath string, tc workflowComparisonCase) {
	t.Helper()

	basePath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+"-base.txt")
	headPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+"-head.txt")
	writeWorkflowFixture(t, basePath, tc.baseID)
	writeWorkflowFixture(t, headPath, tc.headID)

	output, exitCode := runWorkflowBenchdelta(t, repoRoot, binaryPath, basePath, headPath)
	if exitCode != tc.wantCode {
		t.Fatalf("exit code = %d, want %d\n%s", exitCode, tc.wantCode, output)
	}
	assertWorkflowOutputContainsAll(t, string(output), tc.wantContains)
	assertWorkflowOutputOmitsAll(t, string(output), tc.wantOmit)
}

func runWorkflowBenchdelta(t *testing.T, repoRoot, binaryPath, basePath, headPath string) ([]byte, int) {
	t.Helper()

	cmd := exec.Command(binaryPath, "-base", basePath, "-head", headPath)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, 0
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run benchdelta: %v\n%s", err, output)
	}
	return output, exitErr.ExitCode()
}

func assertWorkflowOutputContainsAll(t *testing.T, output string, expected []string) {
	t.Helper()
	for _, want := range expected {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}
}

func assertWorkflowOutputOmitsAll(t *testing.T, output string, omitted []string) {
	t.Helper()
	for _, omit := range omitted {
		if strings.Contains(output, omit) {
			t.Fatalf("expected output to omit %q, got:\n%s", omit, output)
		}
	}
}

func writeWorkflowFixture(t *testing.T, path, fixtureID string) {
	t.Helper()
	content := append([]string{"goos: darwin"}, workflowFixtureLines(fixtureID)...)
	if err := os.WriteFile(path, []byte(strings.Join(content, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write workflow fixture %q: %v", fixtureID, err)
	}
}

func workflowFixtureLines(fixtureID string) []string {
	switch fixtureID {
	case "empty":
		return workflowReportFixture()
	case "shared":
		return workflowReportFixture(workflowCompleteBenchmark("Format"))
	case "head-only":
		return workflowReportFixture(workflowCompleteBenchmark("Format"), workflowCompleteBenchmark("FormatHeadOnly"))
	case "base-only":
		return workflowReportFixture(workflowCompleteBenchmark("Format"), workflowCompleteBenchmark("FormatBaseOnly"))
	case "zero-overlap-base":
		return workflowReportFixture(workflowCompleteBenchmark("BaseOnly"))
	case "zero-overlap-head":
		return workflowReportFixture(workflowCompleteBenchmark("HeadOnly"))
	case "base-bytes-only":
		return workflowReportFixture(workflowBytesOnlyBenchmark("Format", "100"))
	case "head-allocs-only":
		return workflowReportFixture(workflowAllocsOnlyBenchmark("Format", "1"))
	case "base-ns-only":
		return workflowReportFixture(workflowNsOnlyBenchmark("Format"))
	case "partial-base":
		return workflowReportFixture(workflowCompleteBenchmark("Format"), workflowBytesOnlyBenchmark("Format", "130"))
	case "partial-head":
		return workflowReportFixture(workflowCompleteBenchmark("Format"), workflowAllocsOnlyBenchmark("Format", "4"))
	case "count-mismatch-base":
		return workflowReportFixture(workflowRepeatedCompleteBenchmarks("Format", 3)...)
	case "count-mismatch-head":
		return workflowReportFixture(workflowCompleteBenchmark("Format"))
	default:
		return nil
	}
}

func workflowReportFixture(lines ...string) []string {
	return append([]string{"pkg: " + workflowReportPkg}, lines...)
}

func workflowCompleteBenchmark(name string) string {
	return workflowBenchmarkLine(name, "1000", "100", "ns/op", "100", "B/op", "1", "allocs/op")
}

func workflowBytesOnlyBenchmark(name, bytes string) string {
	return workflowBenchmarkLine(name, "1000", "100", "ns/op", bytes, "B/op")
}

func workflowAllocsOnlyBenchmark(name, allocs string) string {
	return workflowBenchmarkLine(name, "1000", "100", "ns/op", allocs, "allocs/op")
}

func workflowNsOnlyBenchmark(name string) string {
	return workflowBenchmarkLine(name, "1000", "100", "ns/op")
}

func workflowBenchmarkLine(name string, parts ...string) string {
	return "Benchmark" + name + "-8 " + strings.Join(parts, " ")
}

func workflowRepeatedCompleteBenchmarks(name string, count int) []string {
	lines := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lines = append(lines, workflowCompleteBenchmark(name))
	}
	return lines
}

func workflowMissingMetric(name, metric string) string {
	return "`" + workflowReportPkg + "/Benchmark" + name + "` missing " + metric
}

func workflowOKRow(name string) string {
	return "| `" + workflowReportPkg + "/Benchmark" + name + "` | 100.0 | 100.0 | +0.0% | 1.0 | 1.0 | +0.0% | ok |"
}

func workflowSampleCountMismatch(name string, baseCount, headCount int) string {
	return "sample-count mismatch for `" + workflowReportPkg + "/Benchmark" + name + "`: base=" + strconv.Itoa(baseCount) + " head=" + strconv.Itoa(headCount)
}

func workflowIncompleteInvalidDiagnostics() []string {
	return []string{
		"Head-only benchmarks (missing on base):",
		"Base-only benchmarks (missing on head):",
		"No overlapping benchmark names were found between base and head.",
	}
}
