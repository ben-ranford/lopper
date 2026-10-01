//go:build !regressionproof

package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/charmbracelet/x/ansi"
)

func newSummaryTerminalTest() *summaryTerminal {
	s := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	m := &summaryTerminal{ctx: context.Background(), writer: io.Discard, summary: *s, state: summaryState{page: 1, pageSize: 1}, report: summaryReportView{Dependencies: []summaryDependencyView{{Name: "alpha"}, {Name: "beta"}}}}
	m.summary.Out = &m.output
	m.render()
	return m
}

func TestSummaryTerminalEditsCommands(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		keys        []rune
		want        string
		cursor      int
	}{
		{"insert", "pag 2", []rune{tea.KeyLeft, tea.KeyLeft, 'e'}, "page 2", 4},
		{"unicode backspace", "a界b", []rune{tea.KeyLeft, tea.KeyBackspace}, "ab", 1},
		{"delete", "a界b", []rune{tea.KeyHome, tea.KeyRight, tea.KeyDelete}, "ab", 1},
		{"bounds", "ab", []rune{tea.KeyRight, tea.KeyHome, tea.KeyLeft, tea.KeyEnd}, "ab", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSummaryTerminalTest()
			m.insert(tc.input)
			for _, key := range tc.keys {
				msg := tea.KeyPressMsg{Code: key}
				if key == 'e' {
					msg.Text = "e"
				}
				m.Update(msg)
			}
			if string(m.line) != tc.want || m.cursor != tc.cursor {
				t.Fatalf("line=%q cursor=%d", m.line, m.cursor)
			}

		})
	}
}

func TestSummaryTerminalNavigationAndExecution(t *testing.T) {
	m := newSummaryTerminalTest()
	for _, key := range []rune{tea.KeyLeft, tea.KeyRight, tea.KeyRight} {
		m.Update(tea.KeyPressMsg{Code: key})
	}
	if m.state.page != 2 {
		t.Fatal(m.state.page)
	}
	m.insert("page 1")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.state.page != 1 || len(m.line) != 0 {
		t.Fatalf("state=%+v line=%q", m.state, m.line)
	}
	m.Update(tea.PasteMsg{Content: "界\x1b\n"})
	m.Update(tea.KeyReleaseMsg{Code: tea.KeyLeft})
	if string(m.line) != "界" {
		t.Fatal(string(m.line))
	}
	m.Init()
	m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(m.line) != 0 || m.quit {
		t.Fatal("ctrl-d should delete nonempty input")
	}
	m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m.Update(summaryTerminalResult{})
	if !m.quit || m.View().Content != "" {
		t.Fatal("expected clean exit")
	}
}

func TestSummaryTerminalFailuresAndQuit(t *testing.T) {
	for _, msg := range []tea.Msg{tea.InterruptMsg{}, tea.QuitMsg{}, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}} {
		m := newSummaryTerminalTest()
		_, cmd := m.Update(msg)
		if !m.quit || cmd == nil {
			t.Fatal("expected quit command")
		}
	}
	m := newSummaryTerminalTest()
	want := errors.New("format failed")
	m.summary.Formatter = func(summaryDisplayView) (string, error) { return "", want }
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !errors.Is(m.err, want) || cmd == nil {
		t.Fatal(m.err)
	}
	s := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	s.Formatter = m.summary.Formatter
	if err := s.runTerminal(context.Background(), Options{}, summaryReportView{}); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestSummaryTerminalStreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   io.Reader
		out  io.Writer
	}{
		{"input", &staveCoverageErrReader{}, io.Discard},
		{"output", strings.NewReader("q\r"), &staveCoverageErrWriter{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSummary(tc.out, tc.in, &stubAnalyzer{}, report.NewFormatter())
			if err := s.runTerminal(context.Background(), Options{}, summaryReportView{}); err == nil {
				t.Fatal("expected stream error")
			}
		})
	}
}

type summaryShortWriter struct{}

func (*summaryShortWriter) Write([]byte) (int, error) { return 0, nil }

func TestSummaryTerminalRetainsWriteAndRenderErrors(t *testing.T) {
	output := &summaryTerminalOutput{Writer: &summaryShortWriter{}}
	if _, err := output.Write([]byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	output.Writer = io.Discard
	if _, err := output.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(output.err, io.ErrShortWrite) {
		t.Fatal("lost initial write failure")
	}
	s := NewSummary(io.Discard, staveTerminalTestInput(t, strings.NewReader("next\r")), &stubAnalyzer{}, report.NewFormatter())
	calls := 0
	want := errors.New("second render")
	s.Formatter = func(summaryDisplayView) (string, error) {
		calls++
		if calls > 1 {
			return "", want
		}
		return "summary", nil
	}
	if err := s.runTerminal(context.Background(), Options{}, summaryReportView{}); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestSummaryTerminalReplacesFramesAndCompletesActions(t *testing.T) {
	m := newSummaryTerminalTest()
	var output bytes.Buffer
	m.writer = &output
	m.drawFrame()
	if !strings.HasPrefix(output.String(), "\x1b[H\x1b[2J") {
		t.Fatal("initial frame not cleared")
	}
	output.Reset()
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !strings.HasPrefix(output.String(), "\x1b[H\x1b[2J") || !strings.Contains(output.String(), "Page: 2/2") {
		t.Fatal("navigation did not replace frame")
	}
	m.summary.Actions = &stubSummaryActionRunner{savePath: "saved.json"}
	m.insert("save-baseline nightly")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !m.inflight {
		t.Fatal("action was not deferred")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.Update(tea.PasteMsg{Content: "ignored"})
	if len(m.line) != 0 {
		t.Fatal("accepted input during action")
	}
	m.Update(cmd())
	if m.inflight || m.err != nil {
		t.Fatalf("completion: %v", m.err)
	}
	m.writer = &staveCoverageErrWriter{}
	m.drawFrame()
	if m.err == nil {
		t.Fatal("missing render error")
	}
}

func TestSummaryTerminalLongPromptStaysOnOneRow(t *testing.T) {
	for _, width := range []int{1, 2, 8, 20} {
		m := newSummaryTerminalTest()
		var out bytes.Buffer
		m.writer = &out
		m.Update(tea.WindowSizeMsg{Width: width})
		original := "compare-baseline " + strings.Repeat("界x", 40)
		m.insert(original)
		for _, key := range []rune{tea.KeyEnd, tea.KeyLeft, tea.KeyHome, tea.KeyRight} {
			out.Reset()
			m.Update(tea.KeyPressMsg{Code: key})
			text := strings.TrimPrefix(ansi.Strip(out.String()), "\r")
			if ansi.StringWidth(text) >= width {
				t.Fatalf("width%d prompt overflows: %q", width, text)
			}
			if string(m.line) != original {
				t.Fatalf("display truncation modified command: %q", m.line)
			}
		}
	}
}

func TestSummaryTerminalReadErrorStopsSession(t *testing.T) {
	m := newSummaryTerminalTest()
	want := errors.New("terminal read")
	_, command := m.Update(staveTerminalInputError{want})
	if !errors.Is(m.err, want) || command == nil {
		t.Fatalf("read error=%v", m.err)
	}
}
