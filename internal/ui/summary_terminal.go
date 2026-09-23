package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// summaryTerminal keeps the command grammar and actions in Summary. Bubble Tea
// owns terminal setup, key decoding, cancellation and restoration.
type summaryTerminal struct {
	ctx     terminalContext
	summary Summary
	opts    Options
	report  summaryReportView
	state   summaryState
	output  bytes.Buffer
	frame   string
	line    []rune
	cursor  int
	err     error
	quit    bool
}

func (s *Summary) runTerminal(ctx context.Context, opts Options, report summaryReportView) error {
	m := &summaryTerminal{ctx: ctx, summary: *s, opts: opts, report: report, state: buildSummaryState(opts)}
	m.summary.Out = &m.output
	m.render()
	if m.err != nil {
		return m.err
	}
	output := &summaryTerminalOutput{Writer: s.Out}
	program := tea.NewProgram(m, tea.WithInput(s.In), tea.WithOutput(output))
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			program.Quit()
		case <-finished:
		}
	}()
	_, err := program.Run()
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

func (m *summaryTerminal) Init() tea.Cmd { return tea.Println(m.frame) }

func (m *summaryTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.frame = ""
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		m.key(msg)
	case tea.PasteMsg:
		m.insert(msg.Content)
	case tea.InterruptMsg, tea.QuitMsg:
		m.quit = true
	}
	if m.quit || m.err != nil {
		return m, tea.Quit
	}
	if m.frame != "" {
		return m, tea.Println(m.frame)
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
	}
}

func (m *summaryTerminal) render() {
	m.err = m.summary.renderSummaryOutput(m.report, &m.state)
	m.frame = m.output.String()
}

func (m *summaryTerminal) View() tea.View {
	if m.quit || m.err != nil {
		return tea.NewView("")
	}
	view := tea.NewView("> " + string(m.line))
	view.Cursor = tea.NewCursor(2+ansi.StringWidth(string(m.line[:m.cursor])), 0)
	return view
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
