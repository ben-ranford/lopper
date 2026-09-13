package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/creack/pty"
)

func TestStaveSnapshotUsesDestinationCapabilities(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	capture := newSignalPTYCapture(master)
	t.Cleanup(func() { waitSignalCapture(t, capture) })
	cleanupStaveTestCloser(t, "snapshot master", master)
	cleanupStaveTestCloser(t, "snapshot terminal", terminal)
	opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80, Color: boolPtr(true)}
	terminalFile := readStaveFileSnapshot(t, terminal, opts)
	plainFile := readStaveFileSnapshot(t, io.Discard, opts)
	if terminalFile != plainFile || strings.Contains(terminalFile, "\x1b") || !strings.Contains(terminalFile, "alpha") {
		t.Fatalf("file snapshot depended on stdout capabilities: terminal=%q plain=%q", terminalFile, plainFile)
	}
	preview := snapshotDestinationPreview(terminal)
	if err := preview.Snapshot(context.Background(), opts, "-"); err != nil {
		t.Fatal(err)
	}
	waitSignalOutput(t, capture, nil, func(text string) bool { return strings.Contains(text, "alpha") && strings.Contains(text, "\x1b[") })
}

func readStaveFileSnapshot(t *testing.T, out io.Writer, opts Options) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preview.txt")
	if err := snapshotDestinationPreview(out).Snapshot(context.Background(), opts, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions = %o", info.Mode().Perm())
	}
	return string(data)
}

func snapshotDestinationPreview(out io.Writer) TUI {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 50}}}
	return NewStavePreview(NewSummary(out, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter()))
}
