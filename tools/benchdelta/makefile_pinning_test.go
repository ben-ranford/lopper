package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestBenchMemMakeTargetRunsBenchdeltaFromRepoRoot(t *testing.T) {
	makefile := readRepoMakefile(t)
	if !strings.Contains(makefile, `(cd "$(CURDIR)" && $(GO_CMD) run ./tools/benchdelta run`) {
		t.Fatal("expected bench-mem to run benchdelta from the head repo root")
	}
	if !strings.Contains(makefile, `-repo "$$head_tree"`) {
		t.Fatal("expected bench-mem to pass the detached head worktree through -repo")
	}
	if strings.Contains(makefile, `run "$(CURDIR)/tools/benchdelta"`) {
		t.Fatal("expected Makefile to avoid absolute go run tool paths")
	}
	if strings.Contains(makefile, `> /dev/null 2>&1`) {
		t.Fatal("expected Makefile benchdelta invocations to retain actionable command output")
	}
}

func TestBenchGateMakeTargetRunsBenchdeltaAgainstBaseWorktree(t *testing.T) {
	target := makeTargetBody(t, "bench-gate")
	if !strings.Contains(target, `benchdelta_bin="$$bench_dir/benchdelta"`) {
		t.Fatal("expected bench-gate to build a reusable benchdelta binary inside the temp bench dir")
	}
	if !strings.Contains(target, `GOFLAGS=-buildvcs=false $(GO_CMD) build -o "$$benchdelta_bin" ./tools/benchdelta`) {
		t.Fatal("expected bench-gate to build tools/benchdelta once before resolve/run/compare")
	}
	if !strings.Contains(target, `"$$benchdelta_bin" resolve -repo "$(CURDIR)"`) {
		t.Fatal("expected bench-gate to resolve benchmark definitions with the single built binary")
	}
	if !strings.Contains(target, `-repo "$$base_tree"`) {
		t.Fatal("expected bench-gate to run the head-resolved definition against the base worktree via -repo")
	}
	if !strings.Contains(target, `"$$benchdelta_bin" run`) {
		t.Fatal("expected bench-gate to execute pinned benchmark runs with the single built binary")
	}
	if !strings.Contains(target, `base_log_tmp=$$(mktemp)`) || !strings.Contains(target, `head_log_tmp=$$(mktemp)`) {
		t.Fatal("expected bench-gate to capture separate base/head benchdelta logs")
	}
	if strings.Contains(target, `$(GO_CMD) run ./tools/benchdelta resolve`) || strings.Contains(target, `$(GO_CMD) run ./tools/benchdelta run`) {
		t.Fatal("expected bench-gate to avoid go run for resolve/run once the pinned binary is built")
	}
}

func TestBenchdeltaCoverageGatePinnedTo98Percent(t *testing.T) {
	makefile := readRepoMakefile(t)
	if !strings.Contains(makefile, `-min=98.0`) {
		t.Fatal("expected benchdelta total coverage gate to be pinned to 98.0")
	}
	if !strings.Contains(makefile, `-package-min=98.0`) {
		t.Fatal("expected benchdelta per-package coverage gate to be pinned to 98.0")
	}
}

func TestBenchTargetsAcceptAbsoluteDefinitionOverrides(t *testing.T) {
	tempDir := t.TempDir()
	tests := []struct {
		name           string
		override       string
		definitionPath string
		overlayPath    string
	}{
		{
			name:           "definition path",
			override:       "MEMORY_BENCH_DEFINITION=" + filepath.Join(tempDir, "custom", "benchmark.json"),
			definitionPath: filepath.Join(tempDir, "custom", "benchmark.json"),
			overlayPath:    filepath.Join(tempDir, "custom", "overlay"),
		},
		{
			name:           "definition directory",
			override:       "MEMORY_BENCH_DEFINITION_DIR=" + filepath.Join(tempDir, "directory"),
			definitionPath: filepath.Join(tempDir, "directory", "definition.json"),
			overlayPath:    filepath.Join(tempDir, "directory", "overlay"),
		},
	}

	for _, test := range tests {
		for _, target := range []string{"bench-mem", "bench-gate"} {
			t.Run(test.name+"/"+target, func(t *testing.T) {
				output := dryRunMakeTarget(t, target, test.override)
				for _, expected := range []string{
					`-out "` + test.definitionPath + `"`,
					`-overlay-dir "` + test.overlayPath + `"`,
					`-definition "` + test.definitionPath + `"`,
				} {
					if !strings.Contains(output, expected) {
						t.Fatalf("make %s output does not contain %q:\n%s", target, expected, output)
					}
				}
				brokenPath := repoRoot(t) + string(filepath.Separator) + test.definitionPath
				if strings.Contains(output, brokenPath) {
					t.Fatalf("make %s prepends the repo root to absolute path %q:\n%s", target, test.definitionPath, output)
				}
			})
		}
	}
}

func dryRunMakeTarget(t *testing.T, target, override string) string {
	t.Helper()
	cmd := exec.Command("make", "--no-print-directory", "-n", target, override)
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run make %s: %v\n%s", target, err, output)
	}
	return string(output)
}

func readRepoMakefile(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(content)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func makeTargetBody(t *testing.T, target string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(target) + `:\n(?:\t.*\n)+`)
	body := pattern.FindString(readRepoMakefile(t))
	if body == "" {
		t.Fatalf("Makefile target %s not found", target)
	}
	return body
}
