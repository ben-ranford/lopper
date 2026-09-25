package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/ui"
	"github.com/creack/pty"
)

type memoryUIPreference struct {
	choice                     string
	reads, writes, clears      int
	readErr, saveErr, clearErr error
}

func (m *memoryUIPreference) Load() (string, error) { m.reads++; return m.choice, m.readErr }
func (m *memoryUIPreference) Save(choice string) error {
	m.writes++
	if m.saveErr == nil {
		m.choice = choice
	}
	return m.saveErr
}
func (m *memoryUIPreference) Clear() error {
	m.clears++
	if m.clearErr == nil {
		m.choice = ""
	}
	return m.clearErr
}

func TestUIPreferenceStartup(t *testing.T) {
	failure := errors.New("storage unavailable")
	for _, tc := range []struct {
		name, saved, prompt, manage                 string
		explicit, enabled, noninteractive, snapshot bool
		readErr, saveErr, clearErr, promptErr       error
		wantStave, wantNoStart, wantError           bool
		reads, writes, clears, prompts              int
		warning                                     string
	}{
		{name: "try", prompt: "stave", wantStave: true, reads: 1, writes: 1, prompts: 1},
		{name: "keep", prompt: "legacy", reads: 1, writes: 1, prompts: 1},
		{name: "later", reads: 1, prompts: 1},
		{name: "saved stave", saved: "stave", wantStave: true, reads: 1},
		{name: "saved legacy", saved: "legacy", reads: 1},
		{name: "explicit disable", explicit: true, saved: "stave"},
		{name: "explicit enable", explicit: true, enabled: true, saved: "legacy", wantStave: true},
		{name: "old explicit caller", enabled: true, wantStave: true},
		{name: "noninteractive", noninteractive: true, saved: "stave"},
		{name: "snapshot", snapshot: true, saved: "stave", wantNoStart: true},
		{name: "read error", readErr: failure, reads: 1, warning: "could not read"},
		{name: "save error", prompt: "stave", saveErr: failure, reads: 1, writes: 1, prompts: 1, wantStave: true, warning: "not remembered"},
		{name: "eof", promptErr: io.EOF, reads: 1, prompts: 1, wantNoStart: true},
		{name: "cancel prompt", promptErr: context.Canceled, reads: 1, prompts: 1, wantNoStart: true, wantError: true},
		{name: "set stave", manage: "stave", wantStave: true, writes: 1},
		{name: "set legacy", manage: "legacy", saved: "stave", writes: 1},
		{name: "set corrupt recovery", manage: "stave", readErr: failure, writes: 1, wantStave: true},
		{name: "set failure", manage: "stave", saveErr: failure, writes: 1, wantStave: true, warning: "not remembered"},
		{name: "config wins", manage: "stave", explicit: true, writes: 1, warning: "overrides"},
		{name: "config enable wins", manage: "legacy", explicit: true, enabled: true, writes: 1, wantStave: true, warning: "overrides"},
		{name: "reset", manage: "ask", saved: "stave", clears: 1, prompts: 1},
		{name: "reset config", manage: "ask", explicit: true, enabled: true, clears: 1, wantStave: true, warning: "controls"},
		{name: "reset failure", manage: "ask", clearErr: failure, clears: 1, wantError: true, wantNoStart: true},
		{name: "invalid setting", manage: "invalid", wantError: true, wantNoStart: true},
		{name: "noninteractive setting", manage: "stave", noninteractive: true, wantError: true, wantNoStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryUIPreference{choice: tc.saved, readErr: tc.readErr, saveErr: tc.saveErr, clearErr: tc.clearErr}
			tui := &fakeTUI{}
			var output bytes.Buffer
			prompts := 0
			a := &App{TUI: tui, Out: &output, Preferences: store, UIInteractive: func() bool { return !tc.noninteractive }, UIEligible: func() bool { return true }, UIPrompt: func(context.Context) (string, error) { prompts++; return tc.prompt, tc.promptErr }}
			req := DefaultRequest()
			req.TUI.StaveExplicit = tc.explicit
			req.TUI.UseStavePreview = tc.enabled
			req.TUI.UIPreference = tc.manage
			if tc.snapshot {
				req.TUI.SnapshotPath = "-"
			}
			enable := []string{"dart-source-attribution"}
			if tc.enabled {
				enable = append(enable, "stave-tui-preview")
			}
			var err error
			req.TUI.Features, err = featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{Enable: enable})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Execute(context.Background(), req)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if tui.startCalled == tc.wantNoStart {
				t.Fatalf("start=%v", tui.startCalled)
			}
			if tui.startCalled && (tui.lastOptions.UseStavePreview != tc.wantStave || tui.lastOptions.Features.Enabled("stave-tui-preview") != tc.wantStave) {
				t.Fatalf("renderer gates=%+v", tui.lastOptions)
			}
			if tui.startCalled && !tui.lastOptions.Features.Enabled("dart-source-attribution") {
				t.Fatal("lost unrelated flag")
			}
			if store.reads != tc.reads || store.writes != tc.writes || store.clears != tc.clears || prompts != tc.prompts {
				t.Fatalf("storage=%+v prompts=%d", store, prompts)
			}
			if tc.warning != "" && !strings.Contains(output.String(), tc.warning) {
				t.Fatalf("warning=%q", output.String())
			}
		})
	}
}

func TestUIPreferenceCancellationBeforeStorage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &memoryUIPreference{}
	a := &App{TUI: &fakeTUI{}, Preferences: store, UIEligible: func() bool { return true }}
	if _, err := a.Execute(ctx, DefaultRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if store.reads+store.writes+store.clears != 0 {
		t.Fatal("storage accessed after cancellation")
	}
}

func TestUIPreferenceDumbTerminal(t *testing.T) {
	for _, manage := range []string{"", "ask", "stave"} {
		store := &memoryUIPreference{}
		tui := &fakeTUI{}
		a := &App{TUI: tui, Preferences: store, UIInteractive: func() bool { return true }, UIEligible: func() bool { return false }, UIPrompt: func(context.Context) (string, error) { t.Fatal("prompted dumb terminal"); return "", nil }}
		req := DefaultRequest()
		req.TUI.UIPreference = manage
		var err error
		req.TUI.Features, err = featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Execute(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if !tui.startCalled || tui.lastOptions.UseStavePreview != (manage == "stave") {
			t.Fatalf("options=%+v", tui.lastOptions)
		}
	}
}

func TestUIPreferenceDefaultSeams(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := master.Close(); err != nil {
			t.Error(err)
		}
		if err := slave.Close(); err != nil {
			t.Error(err)
		}
	}()
	tui := &fakeTUI{}
	a := &App{TUI: tui, In: slave, Out: slave}
	req := DefaultRequest()
	req.TUI.Features, err = featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("t\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !tui.lastOptions.UseStavePreview {
		t.Fatal("default prompt failed")
	}
	a.In = nil
	a.Out = nil
	a.preferenceWarning("unavailable", errors.New("write failed"))
	if err := applyUIPreference(&ui.Options{}, "stave"); err == nil {
		t.Fatal("invalid feature set accepted")
	}
	req.TUI.SnapshotPath = "-"
	req.TUI.UIPreference = "stave"
	if _, err := a.Execute(context.Background(), req); err == nil {
		t.Fatal("snapshot management accepted")
	}
}

func TestUIPreferenceCancellationDuringPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryUIPreference{}
	a := &App{TUI: &fakeTUI{}, Preferences: store, UIInteractive: func() bool { return true }, UIEligible: func() bool { return true }, UIPrompt: func(context.Context) (string, error) { cancel(); return "stave", nil }}
	if _, err := a.Execute(ctx, DefaultRequest()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if store.writes != 0 {
		t.Fatal("remembered canceled consent")
	}
}
