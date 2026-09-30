package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestStaveTUIConfigSelectsRendererForLineAndSnapshot(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	for _, mode := range []string{"bare", "line", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newStaveConfigFixture(t)
			enabled := runStaveConfigOutput(t, bin, fixture, mode, false)
			if !strings.Contains(enabled, "Stave preview") {
				t.Fatalf("config did not select Stave: %q", enabled)
			}
			if mode != "bare" {
				disabled := runStaveConfigOutput(t, bin, fixture, mode, true)
				if strings.Contains(disabled, "Stave preview") || !strings.Contains(disabled, "Lopper TUI (summary)") {
					t.Fatalf("CLI rollback did not select legacy: %q", disabled)
				}
			}
			assertStaveConfigUnchanged(t, fixture)
		})
	}
}

func runStaveConfigOutput(t *testing.T, bin, fixture, mode string, rollback bool) string {
	t.Helper()
	var args []string
	if mode != "bare" {
		args = []string{"tui", "--repo", fixture}
	}
	if mode == "snapshot" {
		args = append(args, "--snapshot", "-")
	}
	if rollback {
		args = append(args, "--disable-feature", "LOP-FEAT-0029")
	}
	ctx, cancel := context.WithTimeout(context.Background(), stavePTYTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = fixture
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "CI=1")
	cmd.Stdin = strings.NewReader("q\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("config %s failed: %v; output=%s", mode, err, output)
	}
	return string(output)
}

func TestStaveTUIConfigSelectsInteractiveRenderer(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	for _, rollback := range []bool{false, true} {
		name := "config enable"
		if rollback {
			name = "CLI rollback"
		}
		t.Run(name, func(t *testing.T) { checkStaveConfigPTY(t, bin, rollback) })
	}
}

func checkStaveConfigPTY(t *testing.T, bin string, rollback bool) {
	t.Helper()
	fixture := newStaveConfigFixture(t)
	args := []string{"tui", "--repo", fixture}
	marker := "Stave preview"
	if rollback {
		args = append(args, "--disable-feature", "LOP-FEAT-0029")
		marker = "Lopper TUI (summary)"
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = fixture
	cmd.Env = terminalCapableTestEnvironment()
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("start config PTY: %v", err)
	}
	defer closePTYProcess(t, terminal, cmd)
	output := readPTYUntil(t, terminal, stavePTYTimeout, func(text string) bool { return strings.Contains(text, marker) })
	if !strings.Contains(output, marker) {
		t.Fatalf("missing renderer marker %q: %q", marker, output)
	}
	if _, err := terminal.Write([]byte("q\n")); err != nil {
		t.Fatal(err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("config PTY quit: %v", err)
	}
	assertStaveConfigUnchanged(t, fixture)
}

const staveFixtureConfig = "features:\n  enable: [stave-tui-preview]\n"

func newStaveConfigFixture(t *testing.T) string {
	t.Helper()
	fixture := t.TempDir()
	writeFile(t, filepath.Join(fixture, "package.json"), `{"name":"tui-config-fixture","dependencies":{"example-dep":"1.0.0"}}`)
	writeFile(t, filepath.Join(fixture, "index.js"), "import dependency from 'example-dep';\ndependency();\n")
	writeFile(t, filepath.Join(fixture, ".lopper.yml"), staveFixtureConfig)
	return fixture
}

func assertStaveConfigUnchanged(t *testing.T, fixture string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(fixture, ".lopper.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != staveFixtureConfig {
		t.Fatalf("TUI changed config: %q", contents)
	}
}
