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
)

type clearingPreferenceTUI struct {
	fakeTUI
	output *bytes.Buffer
	before string
}

func (f *clearingPreferenceTUI) Start(ctx context.Context, opts ui.Options) error {
	f.before = f.output.String()
	// A full-screen first frame replaces text printed before Start.
	f.output.Reset()
	return f.fakeTUI.Start(ctx, opts)
}

func TestUIPreferenceWarningsSurviveTerminalSession(t *testing.T) {
	failure := errors.New("storage unavailable")
	for _, tc := range []uiPreferenceScenario{
		{name: "read failure", readErr: failure, warning: "could not read"},
		{name: "prompted legacy", prompt: "legacy", saveErr: failure, warning: "not remembered"},
		{name: "prompted stave", prompt: "stave", saveErr: failure, warning: "not remembered"},
		{name: "managed legacy", manage: "legacy", saveErr: failure, warning: "not remembered"},
		{name: "managed stave", manage: "stave", saveErr: failure, warning: "not remembered"},
		{name: "configuration override", manage: "legacy", explicit: true, enabled: true, saveErr: failure, warning: "overrides"},
		{name: "reset configuration", manage: "ask", explicit: true, warning: "controls"},
	} {
		t.Run(tc.name, func(t *testing.T) { checkPreferenceWarningSession(t, tc, nil) })
	}
}

func TestUIPreferenceWarningsSurviveTerminalErrors(t *testing.T) {
	for _, err := range []error{context.Canceled, errors.New("terminal failed")} {
		t.Run(err.Error(), func(t *testing.T) {
			checkPreferenceWarningSession(t, uiPreferenceScenario{readErr: errors.New("storage unavailable"), warning: "could not read"}, err)
		})
	}
}

func checkPreferenceWarningSession(t *testing.T, tc uiPreferenceScenario, startErr error) {
	t.Helper()
	var output bytes.Buffer
	tui := &clearingPreferenceTUI{fakeTUI: fakeTUI{startErr: startErr}, output: &output}
	store := &memoryUIPreference{readErr: tc.readErr, saveErr: tc.saveErr}
	a := &App{TUI: tui, Out: &output, Preferences: store, UIInteractive: func() bool { return true }, UIEligible: func() bool { return true }, UIPrompt: func(context.Context) (string, error) {
		if _, err := output.WriteString("preference prompt\n"); err != nil {
			t.Fatal(err)
		}
		return tc.prompt, nil
	}}
	req := DefaultRequest()
	req.TUI.UIPreference, req.TUI.StaveExplicit, req.TUI.UseStavePreview = tc.manage, tc.explicit, tc.enabled
	var err error
	req.TUI.Features, err = featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(context.Background(), req)
	if !errors.Is(err, startErr) {
		t.Fatalf("start error: got %v, want %v", err, startErr)
	}
	if !tui.startCalled || !strings.Contains(output.String(), tc.warning) {
		t.Fatalf("warning erased by terminal: start=%v output=%q", tui.startCalled, output.String())
	}
	if strings.Contains(tui.before, tc.warning) {
		t.Fatalf("warning printed before terminal: %q", tui.before)
	}
	if tc.prompt != "" && tui.before != "preference prompt\n" {
		t.Fatalf("prompt did not reach original output: %q", tui.before)
	}
	if tc.saveErr != nil && strings.Count(output.String(), "not remembered") != 1 {
		t.Fatalf("lost or repeated save warning: %q", output.String())
	}
	// No warning from a prior invocation may leak into a later launch.
	store.readErr, store.saveErr, store.choice = nil, nil, "legacy"
	req.TUI.UIPreference = ""
	if _, err := a.Execute(context.Background(), req); !errors.Is(err, startErr) {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("replayed stale warning: %q", output.String())
	}
}

func TestUIPreferenceWarningsBeforeStartupFailure(t *testing.T) {
	var output bytes.Buffer
	tui := &fakeTUI{}
	a := &App{TUI: tui, Out: &output, Preferences: &memoryUIPreference{saveErr: errors.New("storage unavailable")}, UIInteractive: func() bool { return true }}
	req := DefaultRequest()
	req.TUI.UIPreference = "legacy"
	// A missing feature registry rejects the choice after its save has failed.
	if _, err := a.Execute(context.Background(), req); err == nil {
		t.Fatal("startup error lost")
	}
	if tui.startCalled || !strings.Contains(output.String(), "not remembered") {
		t.Fatalf("startup warning lost: start=%v output=%q", tui.startCalled, output.String())
	}
}

type preferenceWarningFailedWriter struct{ writes int }

func (w *preferenceWarningFailedWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, io.ErrClosedPipe
}

func TestUIPreferenceWarningOutputFailuresPreserveStartError(t *testing.T) {
	writer := &preferenceWarningFailedWriter{}
	for _, output := range []io.Writer{nil, writer} {
		a := &App{TUI: &fakeTUI{startErr: context.Canceled}, Out: output, Preferences: &memoryUIPreference{saveErr: errors.New("storage unavailable")}, UIInteractive: func() bool { return true }}
		req := DefaultRequest()
		req.TUI.UIPreference, req.TUI.StaveExplicit = "legacy", true
		if _, err := a.Execute(context.Background(), req); !errors.Is(err, context.Canceled) {
			t.Fatalf("warning output replaced start error: %v", err)
		}
	}
	if writer.writes != 2 {
		t.Fatalf("expected both warnings, got %d writes", writer.writes)
	}
}
