package cli

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

func TestParseArgsDefault(t *testing.T) {
	req := mustParseArgs(t, nil)
	if req.Mode != app.ModeTUI {
		t.Fatalf(modeMismatchFmt, app.ModeTUI, req.Mode)
	}
}

func TestParseArgsTUIFlags(t *testing.T) {
	req := mustParseArgs(t, []string{"tui", "--top", "15", "--filter", "lod", "--sort", "name", "--page-size", "5", "--snapshot", "out.txt"})
	if req.Mode != app.ModeTUI {
		t.Fatalf(modeMismatchFmt, app.ModeTUI, req.Mode)
	}
	if req.TUI.TopN != 15 {
		t.Fatalf("expected top 15, got %d", req.TUI.TopN)
	}
	if req.TUI.Filter != "lod" {
		t.Fatalf("expected filter lod, got %q", req.TUI.Filter)
	}
	if req.TUI.Sort != "name" {
		t.Fatalf("expected sort name, got %q", req.TUI.Sort)
	}
	if req.TUI.PageSize != 5 {
		t.Fatalf("expected page size 5, got %d", req.TUI.PageSize)
	}
	if req.TUI.SnapshotPath != "out.txt" {
		t.Fatalf("expected snapshot out.txt, got %q", req.TUI.SnapshotPath)
	}
}

func TestParseArgsTUIInvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "negative_top", args: []string{"tui", "--top", "-1"}},
		{name: "negative_page_size", args: []string{"tui", "--page-size", "-1"}},
		{name: "unexpected_arg", args: []string{"tui", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseArgs(tc.args); err == nil {
				t.Fatalf("expected parse error")
			}
		})
	}
}
