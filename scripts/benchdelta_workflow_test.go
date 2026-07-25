package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchdeltaRejectsInvalidAndIncompleteInputs(t *testing.T) {
	t.Parallel()

	repoRoot := repoPath(t, "")
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "benchdelta")
	build := exec.Command("go", "build", "-o", binaryPath, "./tools/benchdelta")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build benchdelta: %v\n%s", err, output)
	}

	tests := []struct {
		name         string
		baseLines    []string
		headLines    []string
		wantCode     int
		wantContains []string
	}{
		{
			name: "empty comparison invalid",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"No overlapping benchmark names were found between base and head.",
			},
		},
		{
			name: "head only invalid",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
				"BenchmarkFormatHeadOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"Head-only benchmarks (missing on base):",
			},
		},
		{
			name: "base only invalid",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
				"BenchmarkFormatBaseOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"Base-only benchmarks (missing on head):",
			},
		},
		{
			name: "zero overlap invalid",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkBaseOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkHeadOnly-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: invalid",
				"No overlapping benchmark names were found between base and head.",
			},
		},
		{
			name: "base bytes only incomplete",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				"base: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing allocs/op",
			},
		},
		{
			name: "head allocs only incomplete",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 1 allocs/op",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				"head: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing B/op",
			},
		},
		{
			name: "complete plus partial duplicates stay incomplete",
			baseLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
				"BenchmarkFormat-8 1000 100 ns/op 130 B/op",
			},
			headLines: []string{
				"pkg: github.com/ben-ranford/lopper/internal/report",
				"BenchmarkFormat-8 1000 100 ns/op 100 B/op 1 allocs/op",
				"BenchmarkFormat-8 1000 100 ns/op 4 allocs/op",
			},
			wantCode: 2,
			wantContains: []string{
				"Comparison status: incomplete",
				"| `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` | 100.0 | 100.0 | +0.0% | 1.0 | 1.0 | +0.0% | ok |",
				"base: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing allocs/op",
				"head: `github.com/ben-ranford/lopper/internal/report/BenchmarkFormat` missing B/op",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			basePath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+"-base.txt")
			headPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+"-head.txt")
			baseContent := append([]string{"goos: darwin"}, tc.baseLines...)
			if err := os.WriteFile(basePath, []byte(strings.Join(baseContent, "\n")+"\n"), 0o644); err != nil {
				t.Fatalf("write base fixture: %v", err)
			}
			headContent := append([]string{"goos: darwin"}, tc.headLines...)
			if err := os.WriteFile(headPath, []byte(strings.Join(headContent, "\n")+"\n"), 0o644); err != nil {
				t.Fatalf("write head fixture: %v", err)
			}

			cmd := exec.Command(binaryPath, "-base", basePath, "-head", headPath)
			cmd.Dir = repoRoot
			output, err := cmd.CombinedOutput()
			exitCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run benchdelta: %v\n%s", err, output)
				}
				exitCode = exitErr.ExitCode()
			}
			if exitCode != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", exitCode, tc.wantCode, output)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(string(output), want) {
					t.Fatalf("expected output to contain %q, got:\n%s", want, output)
				}
			}
		})
	}
}
