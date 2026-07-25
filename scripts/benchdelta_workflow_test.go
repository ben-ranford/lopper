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
		workflowInvalidCase("empty comparison invalid", "empty", "empty", "No overlapping benchmark names were found between base and head."),
		workflowInvalidCase("head only invalid", "shared", "head-only", "Head-only benchmarks (missing on base):"),
		workflowInvalidCase("base only invalid", "base-only", "shared", "Base-only benchmarks (missing on head):"),
		workflowInvalidCase("zero overlap invalid", "zero-overlap-base", "zero-overlap-head", "No overlapping benchmark names were found between base and head."),
		workflowIncompleteCase("base bytes only incomplete", "base-bytes-only", "shared", "base: "+workflowMissingMetric("Format", "allocs/op")),
		workflowIncompleteCase("head allocs only incomplete", "shared", "head-allocs-only", "head: "+workflowMissingMetric("Format", "B/op")),
		workflowIncompleteCase("base ns only incomplete", "base-ns-only", "shared", "base: "+workflowMissingMetric("Format", "B/op and allocs/op")),
		workflowIncompleteCase("sample count mismatch incomplete", "count-mismatch-base", "count-mismatch-head", workflowSampleCountMismatch("Format", 3, 1)),
		workflowIncompleteCase("complete plus partial duplicates stay incomplete", "partial-base", "partial-head", workflowOKRow("Format"), "base: "+workflowMissingMetric("Format", "allocs/op"), "head: "+workflowMissingMetric("Format", "B/op")),
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

func workflowInvalidCase(name, baseID, headID string, expected ...string) workflowComparisonCase {
	return workflowComparisonCase{
		name:         name,
		baseID:       baseID,
		headID:       headID,
		wantCode:     2,
		wantContains: append([]string{"Comparison status: invalid"}, expected...),
	}
}

func workflowIncompleteCase(name, baseID, headID string, expected ...string) workflowComparisonCase {
	return workflowComparisonCase{
		name:         name,
		baseID:       baseID,
		headID:       headID,
		wantCode:     2,
		wantContains: append([]string{"Comparison status: incomplete"}, expected...),
		wantOmit:     workflowIncompleteInvalidDiagnostics(),
	}
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
	case "shared", "count-mismatch-head":
		return workflowCompleteFixture("Format")
	case "head-only":
		return workflowCompleteFixture("Format", "FormatHeadOnly")
	case "base-only":
		return workflowCompleteFixture("Format", "FormatBaseOnly")
	case "zero-overlap-base":
		return workflowCompleteFixture("BaseOnly")
	case "zero-overlap-head":
		return workflowCompleteFixture("HeadOnly")
	case "base-bytes-only":
		return workflowBenchmarkFixture(workflowBytesOnlyBenchmark("Format", "100"))
	case "head-allocs-only":
		return workflowBenchmarkFixture(workflowAllocsOnlyBenchmark("Format", "1"))
	case "base-ns-only":
		return workflowBenchmarkFixture(workflowNsOnlyBenchmark("Format"))
	case "partial-base":
		return workflowBenchmarkFixture(workflowCompleteBenchmark("Format"), workflowBytesOnlyBenchmark("Format", "130"))
	case "partial-head":
		return workflowBenchmarkFixture(workflowCompleteBenchmark("Format"), workflowAllocsOnlyBenchmark("Format", "4"))
	case "count-mismatch-base":
		return workflowReportFixture(workflowRepeatedCompleteBenchmarks("Format", 3)...)
	default:
		return nil
	}
}

func workflowCompleteFixture(names ...string) []string {
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, workflowCompleteBenchmark(name))
	}
	return workflowBenchmarkFixture(lines...)
}

func workflowBenchmarkFixture(lines ...string) []string {
	return workflowReportFixture(lines...)
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
