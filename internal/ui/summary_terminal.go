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
	ctx      terminalContext
	summary  Summary
	opts     Options
	report   summaryReportView
	state    summaryState
	output   bytes.Buffer
	frame    string
	line     []rune
	cursor   int
	err      error
	quit     bool
	inflight bool
	cancel   context.CancelFunc
	writer   io.Writer
}

func (s *Summary) runTerminal(ctx context.Context, opts Options, report summaryReportView) (result error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &summaryTerminalOutput{Writer: s.Out}
	m := &summaryTerminal{ctx: runCtx, cancel: cancel, writer: output, summary: *s, opts: opts, report: report, state: buildSummaryState(opts)}
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
	program := tea.NewProgram(m, tea.WithInput(s.In), tea.WithOutput(output), tea.WithoutRenderer())
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

type summaryTerminalReady struct{}

func (m *summaryTerminal) Init() tea.Cmd { return func() tea.Msg { return summaryTerminalReady{} } }

func (m *summaryTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quit {
		return m, tea.Quit
	}
	switch msg := msg.(type) {
	case summaryTerminalReady:
		m.drawFrame()
	case summaryTerminalResult:
		m.inflight = false
		m.opts, m.report, m.state, m.quit, m.err = msg.opts, msg.report, msg.state, msg.quit, msg.err
		m.output.Reset()
		m.output.WriteString(msg.output)
		if !m.quit && m.err == nil {
			m.render()
			m.drawFrame()
		}
	case tea.KeyPressMsg:
		if m.inflight && msg.String() != "ctrl+c" && msg.String() != "ctrl+d" {
			return m, nil
		}
		if (msg.Code == tea.KeyEnter || msg.Code == tea.KeyKpEnter) && m.isAction(string(m.line)) {
			return m, m.beginAction()
		}
		m.key(msg)
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
	// Return to the prompt and erase its previous contents before every edit.
	_, m.err = fmt.Fprintf(m.writer, "\r\x1b[2K> %s", string(m.line))
	if m.err == nil && m.cursor < len(m.line) {
		_, m.err = fmt.Fprintf(m.writer, "\x1b[%dD", ansi.StringWidth(string(m.line[m.cursor:])))
	}
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
