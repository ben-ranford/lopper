package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/terminal"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/event"
	staveinput "github.com/ben-ranford/stave/input"
	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/render"
	"github.com/ben-ranford/stave/semantic"
	"github.com/ben-ranford/stave/theme"
)

// staveTerminal is the small adapter between Bubble Tea's input loop and the
// Lopper/Stave session. It intentionally does not implement report or action
// logic: those remain owned by StavePreview and stave_program.go.
type staveTerminal struct {
	// Bubble Tea's Model methods do not accept a context, so the execution
	// context is retained at this adapter boundary and passed to Stave calls.
	ctx           terminalContext
	prepared      any
	sendEvent     func(context.Context, any, event.Event) error
	snapshot      func(context.Context, any) (staveTerminalSnapshot, error)
	width         int
	height        int
	err           error
	quit          bool
	alt           bool
	shutdownSent  bool
	callCounter   uint64
	currentCallID string
	inflight      bool
	actionCancel  context.CancelFunc
}

type staveActionCompletion struct {
	callID   string
	actionID action.ID
	result   staveActionResult
	err      error
}

type staveTextCompletion struct {
	err error
}

type terminalContext interface {
	context.Context
}

type staveTerminalSnapshot struct {
	model staveSummaryModel
	tree  semantic.Tree
	caps  capability.Manifest
	theme theme.Resolved
}

// staveTerminalModel is exported only through its constructor for tests in
// this package; callers should use runStaveTerminal.
type staveTerminalModel struct {
	bridge *staveTerminal
}

func (m *staveTerminalModel) Init() tea.Cmd { return nil }

func (m *staveTerminalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	b := m.bridge
	if b.quit {
		return m, tea.Quit
	}
	switch msg := msg.(type) {
	case tea.QuitMsg, tea.InterruptMsg:
		b.cancelInflight()
		b.shutdown()
		b.quit = true
		return m, tea.Quit
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			b.width = msg.Width
		}
		if msg.Height > 0 {
			b.height = msg.Height
		}
		if err := b.resize(b.width, b.height); err != nil {
			b.fail(err)
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyMsg:
		if _, release := msg.(tea.KeyReleaseMsg); release {
			return m, nil
		}
		commandMode := false
		if snap, err := b.sessionSnapshot(); err == nil {
			commandMode = snap.model.interaction.commandMode
		}
		if (msg.Key().Text == "q" && b.inflight && !commandMode) || (msg.Key().Mod == tea.ModCtrl && (msg.Key().Text == "c" || msg.Key().Text == "d")) {
			b.cancelInflight()
			b.shutdown()
			b.quit = true
			return m, tea.Quit
		}
		var command string
		var selectedAction string
		var selectedDep string
		if msg.Key().Code == tea.KeyEnter || msg.Key().Code == tea.KeyKpEnter {
			if snap, err := b.sessionSnapshot(); err == nil {
				if snap.model.interaction.commandMode {
					command = snap.model.interaction.filterBuffer
				} else if snap.model.interaction.focusPane != "detail" {
					selectedAction, selectedDep = staveActionOpen, selectedDependencyForRow(snap.model)
				}
			}
		}
		if msg.Key().Text == "r" && msg.Key().Mod == 0 {
			if snap, err := b.sessionSnapshot(); err == nil && snap.model.interaction.commandMode {
				selectedAction = ""
			} else {
				selectedAction = staveActionRefresh
			}
		}
		if err := b.key(msg); err != nil {
			var inputErr *staveInputError
			if errors.As(err, &inputErr) {
				b.reportError(inputErr)
				return m, nil
			}
			b.fail(err)
			return m, tea.Quit
		}
		if b.inflight {
			return m, nil
		}
		if snap, err := b.sessionSnapshot(); err == nil && snap.model.interaction.quit {
			b.shutdown()
			b.quit = true
			return m, tea.Quit
		}
		if command != "" {
			return m, b.beginCommand(command)
		}
		if selectedAction != "" {
			args := map[string]any{}
			if selectedAction == staveActionOpen {
				args["dependency"] = selectedDep
			}
			return m, b.beginAction(action.ID(selectedAction), args, false)
		}
	case tea.PasteMsg:
		if err := b.paste(msg.Content); err != nil {
			b.reportError(err)
		}
	case staveActionCompletion:
		if b.context().Err() != nil {
			// Signal cleanup publishes the one correlated cancellation result
			// with a fresh bounded context. Do not race that path by trying to
			// publish the command completion through the canceled run context.
			return m, nil
		}
		b.inflight = false
		b.currentCallID = ""
		b.actionCancel = nil
		var outcomeValue any
		if msg.result.Outcome != nil {
			outcomeValue = msg.result.Outcome.Value
		}
		payload := event.EffectResultPayload{CallID: msg.callID, Status: "completed", Value: outcomeValue}
		if msg.result.Error != nil {
			payload.Status = "error"
			payload.Error = msg.result.Error.Message
		} else if msg.err != nil {
			payload.Status = "error"
			payload.Error = terminal.SanitizeString(msg.err.Error())
		}
		ev, err := event.New(event.EffectResult, payload)
		if err != nil {
			b.fail(err)
			return m, tea.Quit
		}
		if err = b.sendAndWait(ev); err != nil {
			b.fail(err)
			return m, tea.Quit
		}
		if msg.actionID == action.ID(staveActionQuit) && payload.Status != "error" {
			b.shutdown()
			b.quit = true
			return m, tea.Quit
		}
	case staveTextCompletion:
		b.inflight = false
		if msg.err != nil {
			b.fail(msg.err)
			return m, tea.Quit
		}
	}
	return m, nil
}

type staveInputError struct{ err error }

func (e *staveInputError) Error() string { return e.err.Error() }
func (e *staveInputError) Unwrap() error { return e.err }

func (t *staveTerminal) beginCommand(input string) tea.Cmd {
	if t.inflight {
		return nil
	}
	snapshot, err := t.sessionSnapshot()
	if err != nil {
		return func() tea.Msg { return staveActionCompletion{err: err} }
	}
	id, args, confirm, handled := lopperStaveInput(input, snapshot.model.interaction.summary)
	if !handled {
		ev, eventErr := staveCommandEvent(input, snapshot.model)
		if eventErr != nil {
			return func() tea.Msg { return staveTextCompletion{err: eventErr} }
		}
		t.inflight = true
		return func() tea.Msg { return staveTextCompletion{err: t.sendAndWait(ev)} }
	}
	return t.beginAction(id, args, confirm)
}

func staveCommandEvent(input string, model staveSummaryModel) (event.Event, error) {
	probe := model.interaction.summary
	if _, commandErr := applyStaveCommand(&probe, input, model.view); commandErr != "" {
		return event.New(event.Diagnostic, event.DiagnosticPayload{
			Code:    "LOPPER_INPUT_REJECTED",
			Message: terminal.SanitizeString(commandErr),
		})
	}
	return event.New(event.Text, event.TextPayload{Text: input, Committed: true})
}

func (t *staveTerminal) beginAction(id action.ID, args any, confirm bool) tea.Cmd {
	if t.inflight {
		return nil
	}
	snapshot, err := t.sessionSnapshot()
	if err != nil {
		return func() tea.Msg { return staveTextCompletion{err: err} }
	}
	preparedArgs, err := prepareLopperActionArgs(snapshot.model, id, args)
	if err != nil {
		return func() tea.Msg { return staveTextCompletion{err: err} }
	}
	args = preparedArgs
	t.callCounter++
	callID := fmt.Sprintf("lopper-terminal-%d", t.callCounter)
	t.inflight = true
	t.currentCallID = callID
	callCtx, cancel := context.WithCancel(t.context())
	t.actionCancel = cancel
	if t.sendEvent != nil {
		ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: callID, ActionID: string(id), Arguments: args})
		if err != nil {
			t.inflight = false
			return func() tea.Msg { return staveActionCompletion{callID: callID, actionID: id, err: err} }
		}
		if err = t.sendAndWait(ev); err != nil {
			t.inflight = false
			return func() tea.Msg { return staveActionCompletion{callID: callID, actionID: id, err: err} }
		}
	}
	return func() tea.Msg {
		execution := startLopperAction(callCtx, t.prepared.(*stave.Prepared[staveSummaryModel]), id, args, "lopper-preview", confirm, callID)
		completed := <-execution
		cancel()
		return staveActionCompletion{callID: callID, actionID: id, result: completed.result, err: completed.err}
	}
}

func (t *staveTerminal) paste(content string) error {
	if !t.ready() {
		return nil
	}
	snap, err := t.sessionSnapshot()
	if err != nil {
		return err
	}
	if !snap.model.interaction.commandMode && !strings.HasPrefix(strings.TrimSpace(content), ":") && !strings.HasPrefix(strings.TrimSpace(content), "/") {
		return fmt.Errorf("paste requires command mode")
	}
	if len([]byte(content)) > staveinput.DefaultMaxPasteBytes {
		return fmt.Errorf("pasted command exceeds %d bytes", staveinput.DefaultMaxPasteBytes)
	}
	text, _, err := staveinput.NormalizePaste([]byte(content), staveinput.DefaultMaxPasteBytes)
	if err != nil {
		return err
	}
	if text.Truncated {
		return fmt.Errorf("pasted command exceeds %d bytes", staveinput.DefaultMaxPasteBytes)
	}
	for _, r := range text.Value {
		if r == '\n' || r == '\r' {
			continue
		}
		chord, err := staveinput.ParseKey(string(r))
		if err != nil {
			return err
		}
		if err := t.sendAndWait(staveinput.KeyEvent(chord)); err != nil {
			return err
		}
	}
	return nil
}

func (t *staveTerminal) text(value string) error {
	if !t.ready() || value == "" {
		return nil
	}
	ev, err := event.New(event.Text, event.TextPayload{Text: value, Committed: true})
	if err != nil {
		return err
	}
	return t.sendAndWait(ev)
}

func (t *staveTerminal) key(msg tea.KeyMsg) error {
	if !t.ready() {
		return nil
	}
	key := msg.Key()
	// Bubble Tea may deliver a paste as one KeyMsg whose Text contains several
	// runes. Feed those runes through Stave's canonical key parser one at a
	// time so command-mode editors and Unicode filters behave exactly like
	// interactive typing.
	if len([]rune(key.Text)) > 1 {
		for _, r := range key.Text {
			if unicode.IsControl(r) {
				return &staveInputError{err: fmt.Errorf("unsupported terminal key text")}
			}
			chord, err := staveinput.ParseKey(string(r))
			if err != nil {
				return &staveInputError{err: err}
			}
			if err := t.sendAndWait(staveinput.KeyEvent(chord)); err != nil {
				return err
			}
		}
		return nil
	}
	chord, err := staveinput.ParseKey(msg.String())
	if err != nil {
		// Bubble Tea's key text is authoritative for printable keys, while
		// special key String values are canonical Stave names.
		k := key
		if k.Text == "" {
			return &staveInputError{err: fmt.Errorf("unsupported terminal key %q: %w", msg.String(), err)}
		}
		chord, err = staveinput.ParseKey(k.Text)
		if err != nil {
			return &staveInputError{err: err}
		}
	}
	ev := staveinput.KeyEvent(chord)
	return t.sendAndWait(ev)
}

func (m *staveTerminalModel) View() tea.View {
	b := m.bridge
	if b.quit {
		// Bubble Tea performs terminal restoration after the final view. Do not
		// repaint the application frame while leaving the alternate screen.
		return tea.NewView("")
	}
	if b.err != nil {
		return tea.NewView(terminalSafeText(b.err.Error()))
	}
	snapshot, err := b.sessionSnapshot()
	if err != nil {
		return tea.NewView(terminalSafeText(err.Error()))
	}
	width, height := b.width, b.height
	if width <= 0 {
		width = snapshot.caps.Width
	}
	if height <= 0 {
		height = snapshot.caps.Height
	}
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	snapshot.caps.Width, snapshot.caps.Height = width, height
	output, err := render.Render(render.Request{Context: b.context(), Tree: snapshot.tree, Theme: snapshot.theme, Capabilities: snapshot.caps, Viewport: layout.Size{Width: width, Height: height}})
	if err != nil {
		return tea.NewView(terminalSafeText(err.Error()))
	}
	v := tea.NewView(output.Terminal)
	v.AltScreen = b.alt
	return v
}

func (t *staveTerminal) sessionSnapshot() (staveTerminalSnapshot, error) {
	if t.snapshot != nil {
		return t.snapshot(t.context(), t.prepared)
	}
	prepared, ok := t.prepared.(*stave.Prepared[staveSummaryModel])
	if !ok || prepared == nil || prepared.Session == nil {
		return staveTerminalSnapshot{}, fmt.Errorf("stave session is not prepared")
	}
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		return staveTerminalSnapshot{}, err
	}
	return staveTerminalSnapshot{model: snapshot.Model, tree: snapshot.Tree, caps: snapshot.Capabilities, theme: prepared.Theme}, nil
}

func (t *staveTerminal) fail(err error) {
	if t.err == nil {
		t.err = err
	}
	t.shutdown()
	t.quit = true
}

func (t *staveTerminal) reportError(err error) {
	if err == nil {
		return
	}
	if !t.ready() {
		t.err = err
		return
	}
	ev, eventErr := event.New(event.Diagnostic, event.DiagnosticPayload{Code: "LOPPER_INPUT_REJECTED", Message: terminal.SanitizeString(err.Error())})
	if eventErr != nil || t.sendAndWait(ev) != nil {
		t.err = err
	}
}

func (t *staveTerminal) shutdown() {
	if t.shutdownSent || !t.ready() {
		return
	}
	t.shutdownSent = true
	if ev, err := event.New(event.Shutdown, nil); err == nil {
		if sendErr := t.sendAndWait(ev); sendErr != nil && t.err == nil {
			t.err = sendErr
		}
	}
}

func (t *staveTerminal) cancelInflight() {
	if t.actionCancel != nil {
		t.actionCancel()
		t.actionCancel = nil
	}
	if t.currentCallID != "" && t.ready() {
		if ev, err := event.New(event.EffectResult, event.EffectResultPayload{CallID: t.currentCallID, Status: "cancelled", Error: "cancellation requested; final action outcome unknown"}); err == nil {
			if sendErr := t.sendAndWait(ev); sendErr != nil && t.err == nil {
				t.err = sendErr
			}
		}
	}
	t.currentCallID = ""
	t.inflight = false
}

func (t *staveTerminal) resize(width, height int) error {
	if !t.ready() {
		return nil
	}
	// The concrete session is intentionally handled by dispatchSessionEvent.
	ev, err := event.New(event.Resize, event.ResizePayload{Width: width, Height: height})
	if err != nil {
		return err
	}
	return t.sendAndWait(ev)
}

func (t *staveTerminal) sendAndWait(ev event.Event) error {
	if !t.ready() {
		return nil
	}
	return t.sendEvent(t.context(), t.prepared, ev)
}

func (t *staveTerminal) context() context.Context {
	if t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

func (t *staveTerminal) ready() bool {
	return t.sendEvent != nil && t.prepared != nil
}

func selectedDependencyForRow(model staveSummaryModel) string {
	view := model.view
	if view == nil {
		return ""
	}
	_, deps, _, _ := runSummaryDependencyPipeline(*view, model.interaction.summary)
	if model.interaction.selectedRow < 0 || model.interaction.selectedRow >= len(deps) {
		return ""
	}
	return deps[model.interaction.selectedRow].Language + ":" + deps[model.interaction.selectedRow].Name
}

func staveKeyText(msg tea.KeyMsg) (text string, immediate, ok bool) {
	k := msg.Key()
	if k.Text != "" && !strings.ContainsAny(k.Text, "\x00\x1b\x7f") {
		return k.Text, false, true
	}
	return "", false, false
}

func terminalSafeText(s string) string { return fmt.Sprintf("%s\n", terminal.SanitizeString(s)) }

// runStaveTerminal starts Bubble Tea with explicit streams/context. The
// generic Stave session is wired here, keeping all type assertions out of the
// model's input and rendering paths.
func (p *StavePreview) runStaveTerminal(ctx context.Context, opts Options, view summaryReportView, state summaryState, prepared any, input io.Reader, output io.Writer, alt bool) error {
	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	sendEvent := func(c context.Context, raw any, ev event.Event) error {
		s := raw.(*stave.Prepared[staveSummaryModel])
		return sendLopperEvent(c, s, ev)
	}
	snapshot := func(_ context.Context, raw any) (staveTerminalSnapshot, error) {
		s := raw.(*stave.Prepared[staveSummaryModel])
		current, err := s.Session.Snapshot()
		if err != nil {
			return staveTerminalSnapshot{}, err
		}
		return staveTerminalSnapshot{model: current.Model, tree: current.Tree, caps: current.Capabilities, theme: s.Theme}, nil
	}
	m := &staveTerminalModel{bridge: &staveTerminal{ctx: runCtx, prepared: prepared, sendEvent: sendEvent, snapshot: snapshot, width: opts.Width, height: 24, alt: alt}}
	program := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	programFinished := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			// Use Bubble Tea's graceful quit path so its input loop finishes
			// before the cancel reader is closed. The same run context still
			// cancels the in-flight Lopper action immediately.
			program.Quit()
		case <-programFinished:
		}
	}()
	_, err := program.Run()
	close(programFinished)
	return finishStaveTerminalRun(ctx, runCtx, m.bridge, prepared, sendEvent, err)
}

func finishStaveTerminalRun(ctx, runCtx context.Context, bridge *staveTerminal, prepared any, sendEvent func(context.Context, any, event.Event) error, runErr error) error {
	if runCtx.Err() != nil && ctx.Err() == nil {
		if bridge.actionCancel != nil {
			bridge.actionCancel()
			bridge.actionCancel = nil
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cleanupCancel()
		if bridge.currentCallID != "" {
			cancelled, eventErr := event.New(event.EffectResult, event.EffectResultPayload{CallID: bridge.currentCallID, Status: "cancelled", Error: "cancellation requested by signal; final action outcome unknown"})
			if eventErr == nil {
				if sendErr := sendEvent(cleanupCtx, prepared, cancelled); sendErr != nil && bridge.err == nil {
					bridge.err = sendErr
				}
			}
		}
		if shutdown, eventErr := event.New(event.Shutdown, nil); eventErr == nil {
			if sendErr := sendEvent(cleanupCtx, prepared, shutdown); sendErr != nil && bridge.err == nil {
				bridge.err = sendErr
			}
		}
	}
	if bridge.err != nil {
		return bridge.err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if runCtx.Err() != nil {
		return nil
	}
	if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
		return runErr
	}
	return nil
}
