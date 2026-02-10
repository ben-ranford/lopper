package cli

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestParseArgsDefault(t *testing.T) {
	req, err := ParseArgs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Mode != app.ModeTUI {
		t.Fatalf("expected mode %q, got %q", app.ModeTUI, req.Mode)
	}
}

func TestParseArgsAnalyseDependency(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "lodash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Mode != app.ModeAnalyse {
		t.Fatalf("expected mode %q, got %q", app.ModeAnalyse, req.Mode)
	}
	if req.Analyse.Dependency != "lodash" {
		t.Fatalf("expected dependency lodash, got %q", req.Analyse.Dependency)
	}
	if req.Analyse.Format != report.FormatTable {
		t.Fatalf("expected format %q, got %q", report.FormatTable, req.Analyse.Format)
	}
	if req.Analyse.Language != "auto" {
		t.Fatalf("expected language auto, got %q", req.Analyse.Language)
	}
}

func TestParseArgsAnalyseTop(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "--top", "5", "--format", "json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Analyse.TopN != 5 {
		t.Fatalf("expected top 5, got %d", req.Analyse.TopN)
	}
	if req.Analyse.Format != report.FormatJSON {
		t.Fatalf("expected format %q, got %q", report.FormatJSON, req.Analyse.Format)
	}
}

func TestParseArgsAnalyseLanguage(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "lodash", "--language", "js-ts"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Analyse.Language != "js-ts" {
		t.Fatalf("expected language js-ts, got %q", req.Analyse.Language)
	}
}

func TestParseArgsAnalyseBaseline(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "lodash", "--baseline", "baseline.json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Analyse.BaselinePath != "baseline.json" {
		t.Fatalf("expected baseline path baseline.json, got %q", req.Analyse.BaselinePath)
	}
}

func TestParseArgsTUIFlags(t *testing.T) {
	req, err := ParseArgs([]string{"tui", "--top", "15", "--filter", "lod", "--sort", "name", "--page-size", "5", "--snapshot", "out.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Mode != app.ModeTUI {
		t.Fatalf("expected mode %q, got %q", app.ModeTUI, req.Mode)
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
