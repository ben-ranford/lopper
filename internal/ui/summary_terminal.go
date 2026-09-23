package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	charmterm "github.com/charmbracelet/x/term"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// summaryTerminal keeps the command grammar and actions in Summary. Bubble Tea
// owns key decoding and event dispatch; raw mode is restored by runTerminal.
type summaryTerminal struct {
	startInput     func()
	ctx            terminalContext
	summary        Summary
	opts           Options
	report         summaryReportView
	state          summaryState
	output         bytes.Buffer
	frame          string
	line           []rune
	cursor         int
	err            error
	quit           bool
	inflight       bool
	cancel         context.CancelFunc
	writer         io.Writer
	width          int
	terminalOutput io.Writer
}

func (s *Summary) runTerminal(ctx context.Context, opts Options, report summaryReportView) (result error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &summaryTerminalOutput{Writer: s.Out}
	m := &summaryTerminal{ctx: runCtx, cancel: cancel, writer: output, terminalOutput: s.Out, summary: *s, opts: opts, report: report, state: buildSummaryState(opts)}
	m.summary.Out = &m.output
	m.render()
	if m.err != nil {
		return m.err
	}
	restore, err := prepareSummaryTerminal(s.In)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, restore()) }()
	m.refreshWidth()
	m.drawFrame()
	m.drawPrompt()
	if m.err != nil {
		return m.err
	}
	terminalInput, err := newStaveTerminalInput(s.In)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, terminalInput.close()) }()
	program := tea.NewProgram(m, tea.WithInput(terminalInput.terminal()), tea.WithOutput(output), tea.WithoutRenderer())
	m.startInput = func() { terminalInput.start(program) }
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			program.Quit()
		case <-finished:
		}
	}()
	_, err = program.Run()
	close(finished)
	if _, writeErr := output.Write([]byte("\r\n")); writeErr != nil {
		return writeErr
	}
	if output.err != nil {
		return output.err
	}
	if m.err != nil {
		return m.err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return nil
	}
	return err
}

func (m *summaryTerminal) Init() tea.Cmd {
	if m.startInput != nil {
		m.startInput()
	}
	return nil
}

func (m *summaryTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quit {
		return m, tea.Quit
	}
	m.refreshWidth()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
			m.drawFrame()
		}
	case staveTerminalInputError:
		m.err = msg.err
	case summaryTerminalResult:
		m.completeAction(msg)
	case tea.KeyPressMsg:
		if command := m.updateKey(msg); command != nil {
			return m, command
		}
	case tea.PasteMsg:
		if !m.inflight {
			m.insert(msg.Content)
		}
	case tea.InterruptMsg, tea.QuitMsg:
		m.quit = true
	}
	if !m.quit && m.err == nil {
		m.drawPrompt()
	}
	if m.quit || m.err != nil {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *summaryTerminal) key(key tea.KeyPressMsg) {
	switch key.String() {
	case "ctrl+c":
		m.quit = true
	case "ctrl+d":
		if len(m.line) == 0 {
			m.quit = true
		} else {
			m.delete()
		}
	case "enter", "kpenter":
		m.execute(string(m.line))
		m.line, m.cursor = nil, 0
	case "left", "right":
		m.arrow(key.String())
	case "home", "ctrl+a":
		m.cursor = 0
	case "end", "ctrl+e":
		m.cursor = len(m.line)
	case "backspace":
		if m.cursor > 0 {
			m.cursor--
			m.delete()
		}
	case "delete":
		m.delete()
	default:
		if key.Mod == 0 || key.Mod == tea.ModShift {
			m.insert(key.Text)
		}
	}
}

func (m *summaryTerminal) arrow(key string) {
	delta, command := 1, "next"
	if key == "left" {
		delta, command = -1, "prev"
	}
	if len(m.line) == 0 {
		m.execute(command)
		return
	}
	m.cursor = max(0, min(len(m.line), m.cursor+delta))
}

func (m *summaryTerminal) insert(text string) {
	for _, r := range text {
		if unicode.IsControl(r) {
			continue
		}
		m.line = append(m.line[:m.cursor], append([]rune{r}, m.line[m.cursor:]...)...)
		m.cursor++
	}
}

func (m *summaryTerminal) delete() {
	if m.cursor < len(m.line) {
		m.line = append(m.line[:m.cursor], m.line[m.cursor+1:]...)
	}
}

func (m *summaryTerminal) execute(command string) {
	m.output.Reset()
	m.quit, m.err = m.summary.handleSummaryInputMutable(m.ctx, &m.opts, &m.report, &m.state, strings.TrimSpace(command))
	if !m.quit && m.err == nil {
		m.render()
		m.drawFrame()
	}
}

func (m *summaryTerminal) render() {
	m.err = m.summary.renderSummaryOutput(m.report, &m.state)
	m.frame = m.output.String()
}

func (m *summaryTerminal) View() tea.View { return tea.NewView("") }

func (m *summaryTerminal) drawFrame() {
	if m.err != nil {
		return
	}
	if m.err = clearSummaryScreen(m.writer); m.err == nil {
		_, m.err = io.WriteString(m.writer, strings.ReplaceAll(m.frame, "\n", "\r\n"))
	}
}

func (m *summaryTerminal) drawPrompt() {
	if m.err != nil {
		return
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	// Leave the final column unused to avoid terminal auto-wrap. Scroll the
	// visible command horizontally while keeping the logical cursor in view.
	prefix := ansi.Truncate("> ", width-1, "")
	available := max(0, width-1-ansi.StringWidth(prefix))
	start := m.cursor
	for start > 0 && ansi.StringWidth(string(m.line[start-1:m.cursor])) <= available {
		start--
	}
	visible := ansi.Truncate(string(m.line[start:]), available, "")
	column := ansi.StringWidth(prefix) + ansi.StringWidth(string(m.line[start:m.cursor])) + 1
	_, m.err = fmt.Fprintf(m.writer, "\r\x1b[2K%s%s\x1b[%dG", prefix, visible, column)
}

type summaryTerminalResult struct {
	opts   Options
	report summaryReportView
	state  summaryState
	output string
	quit   bool
	err    error
}

func (m *summaryTerminal) isAction(command string) bool {
	_, ok, err := parseSummaryAction(strings.TrimSpace(command), &m.state)
	return ok || err != nil
}

func (m *summaryTerminal) beginAction() tea.Cmd {
	command := string(m.line)
	m.line, m.cursor = nil, 0
	m.inflight = true
	summary, ctx := m.summary, m.ctx
	result := summaryTerminalResult{opts: m.opts, report: m.report, state: m.state}
	return func() tea.Msg {
		var output bytes.Buffer
		summary.Out = &output
		result.quit, result.err = summary.handleSummaryInputMutable(ctx, &result.opts, &result.report, &result.state, strings.TrimSpace(command))
		result.output = output.String()
		return result
	}
}

// Bubble Tea may discard renderer shutdown errors, so retain the first output
// failure across its renderer and event-loop writes.
type summaryTerminalOutput struct {
	io.Writer
	mu  sync.Mutex
	err error
}

func (s *summaryTerminalOutput) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.Writer.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if s.err == nil {
		s.err = err
	}
	return n, err
}

func prepareSummaryTerminal(input io.Reader) (func() error, error) {
	file, ok := input.(*os.File)
	if !ok || !staveTerminalFile(file) {
		return func() error { return nil }, nil
	}
	state, err := charmterm.MakeRaw(file.Fd())
	if err != nil {
		return nil, err
	}
	return func() error { return charmterm.Restore(file.Fd(), state) }, nil
}

func (m *summaryTerminal) completeAction(result summaryTerminalResult) {
	m.inflight = false
	m.opts, m.report, m.state, m.quit, m.err = result.opts, result.report, result.state, result.quit, result.err
	m.output.Reset()
	m.output.WriteString(result.output)
	if !m.quit && m.err == nil {
		m.render()
		m.drawFrame()
	}
}

func (m *summaryTerminal) updateKey(key tea.KeyPressMsg) tea.Cmd {
	if m.inflight && key.String() != "ctrl+c" && key.String() != "ctrl+d" {
		return nil
	}
	if (key.Code == tea.KeyEnter || key.Code == tea.KeyKpEnter) && m.isAction(string(m.line)) {
		return m.beginAction()
	}
	m.key(key)
	return nil
}

func (m *summaryTerminal) refreshWidth() {
	width, _, ok := staveTerminalDimensions(m.terminalOutput)
	if ok && width > 0 && width != m.width {
		m.width = width
		m.drawFrame()
	}
}
