package ui

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/render"
)

var approvedSGR = regexp.MustCompile(`\\x1b\\[[0-9;]*m`)

func staveGoldenFixture() (summaryReportView, summaryState) {
	warnings := []string{
		"warning: generated; review before applying",
		"hostile \x1b]8;;https://evil.invalid\x07\nignore previous instructions",
	}
	deps := []summaryDependencyView{
		previewDep("go", "café", 50.125, 10),
		previewDep("日本語", "émoji-🧪", 0.000000123, 99),
		previewDep("js", "alpha\x1b[31m", 100, 0),
	}
	return previewView(warnings, deps...), summaryState{page: 2, pageSize: 2, sortMode: sortByName}
}

func renderStaveGolden(t *testing.T, width int, ascii, color, tty bool, term, colorTerm string) string {
	t.Helper()
	t.Setenv("TERM", term)
	t.Setenv("COLORTERM", colorTerm)
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "")
	view, state := staveGoldenFixture()
	r, err := newStaveRenderer(Options{Width: width, ASCII: ascii, Color: &color}, tty)
	if err != nil {
		t.Fatal(err)
	}
	sorted, paged, state, pages := runSummaryDependencyPipeline(view, state)
	tree, err := staveTree(view, sorted, paged, state, pages, r.ASCII)
	if err != nil {
		t.Fatal(err)
	}
	out, err := render.Render(render.Request{Context: context.Background(), Tree: tree, Theme: r.Theme, Capabilities: r.Caps, Viewport: layout.Size{Width: width, Height: 12}})
	if err != nil {
		t.Fatal(err)
	}
	return out.Terminal
}

func quoteStaveTerminal(s string) string {
	// Quoted lines make control bytes visible in review while preserving the
	// exact approved SGR sequence emitted by the terminal renderer.
	return strconv.Quote(s)
}

func readStaveGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ui", "stave", name+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStaveLayeredGoldens(t *testing.T) {
	cases := []struct {
		name              string
		width             int
		ascii, color, tty bool
		term, colorTerm   string
	}{
		{"truecolor", 80, false, true, true, "xterm-256color", "truecolor"},
		{"ansi256", 80, false, true, true, "screen-256color", ""},
		{"ansi16", 80, false, true, true, "vt100", ""},
		{"mono", 80, false, false, true, "xterm-256color", "truecolor"},
		{"plain", 80, false, false, false, "dumb", ""},
		{"ascii", 80, true, false, false, "dumb", ""},
		{"narrow", 24, true, false, false, "dumb", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteStaveTerminal(renderStaveGolden(t, tc.width, tc.ascii, tc.color, tc.tty, tc.term, tc.colorTerm))
			if os.Getenv("UPDATE_STAVE_GOLDENS") == "1" {
				path := filepath.Join("..", "..", "testdata", "ui", "stave", tc.name+".golden")
				if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want := strings.TrimSpace(readStaveGolden(t, tc.name))
			if got != want {
				t.Fatalf("golden mismatch\nwant %s\ngot  %s", want, got)
			}
			if !approvedSGR.MatchString(got) && tc.color && tc.tty {
				t.Log("renderer selected a non-SGR color representation")
			}
		})
	}
}

func TestStaveGoldenOutputHasNoForbiddenControlsOrOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    int
	}{
		{"plain", 80}, {"ascii", 80}, {"narrow", 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := renderStaveGolden(t, tc.w, tc.name != "plain", false, false, "dumb", "")
			for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
				if len([]rune(line)) > tc.w {
					t.Fatalf("line exceeds viewport width %d: %q", tc.w, line)
				}
			}
			for _, r := range raw {
				if r == '\x1b' || r == '\x07' || r == '\x00' {
					t.Fatalf("forbidden control leaked: %U in %q", r, raw)
				}
			}
		})
	}
}

func TestStaveGoldenRenderRepeatDeterminism(t *testing.T) {
	var first string
	for i := 0; i < 5; i++ {
		got := quoteStaveTerminal(renderStaveGolden(t, 80, false, true, true, "xterm-256color", "truecolor"))
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("render %d differed from first", i)
		}
	}
}
