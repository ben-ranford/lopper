package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestNewApp(t *testing.T) {
	appInstance := New(&bytes.Buffer{}, strings.NewReader(""))
	if appInstance == nil {
		t.Fatalf("expected app instance")
	}
}

func TestExecuteTUIStartAndSnapshot(t *testing.T) {
	tui := &fakeTUI{}
	application := &App{
		Analyzer:  &fakeAnalyzer{},
		Formatter: report.NewFormatter(),
		TUI:       tui,
	}

	req := DefaultRequest()
	req.Mode = ModeTUI
	req.TUI.TopN = 5
	req.TUI.Language = "all"
	req.TUI.Filter = "lod"
	req.TUI.Sort = "name"
	req.TUI.PageSize = 3

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute tui start: %v", err)
	}
	if !tui.startCalled || tui.snapshotCalled {
		t.Fatalf("expected Start to be called only once")
	}

	req.TUI.SnapshotPath = testSnapshotPath
	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute tui snapshot: %v", err)
	}
	if !tui.snapshotCalled || tui.lastSnapshot != testSnapshotPath {
		t.Fatalf("expected Snapshot call with output path, got called=%v path=%q", tui.snapshotCalled, tui.lastSnapshot)
	}
}

func TestExecuteUnknownMode(t *testing.T) {
	application := &App{Analyzer: &fakeAnalyzer{}, Formatter: report.NewFormatter()}
	_, err := application.Execute(context.Background(), Request{Mode: "unknown"})
	if !errors.Is(err, ErrUnknownMode) {
		t.Fatalf("expected ErrUnknownMode, got %v", err)
	}
}

func TestExecuteTUIPropagatesErrors(t *testing.T) {
	tui := &fakeTUI{startErr: errors.New("start failed")}
	application := &App{
		Analyzer:  &fakeAnalyzer{},
		Formatter: report.NewFormatter(),
		TUI:       tui,
	}
	req := DefaultRequest()
	req.Mode = ModeTUI
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected start error")
	}

	tui = &fakeTUI{snapshotErr: errors.New("snapshot failed")}
	application.TUI = tui
	req.TUI.SnapshotPath = testSnapshotPath
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected snapshot error")
	}
}
