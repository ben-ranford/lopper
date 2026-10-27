//go:build darwin || linux

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestSummaryRefreshPreservesCommandFeedback(t *testing.T) {
	for _, command := range []struct{ input, want string }{
		{"open alpha\n", "Dependency detail"},
		{"not-a-command\n", "Unknown command. Type 'help' for options."},
		{"apply-codemod\n", "Open a dependency detail first"},
	} {
		t.Run(strings.TrimSpace(command.input), func(t *testing.T) { checkSummaryFeedbackPTY(t, command.input, command.want) })
	}
}

func checkSummaryFeedbackPTY(t *testing.T, command, want string) {
	t.Helper()
	master, terminal := openSummaryFeedbackPTY(t)
	capture := newSignalPTYCapture(master)
	t.Cleanup(func() { waitSignalCapture(t, capture) })
	cleanupStaveTestCloser(t, "feedback master", master)
	cleanupStaveTestCloser(t, "feedback terminal", terminal)
	input := &summaryFeedbackReader{commands: []string{command, "sort name\n", "q\n"}}
	input.beforeRead = func(index int) {
		marker := fmt.Sprintf("FRAME-%d-END", index)
		if _, err := fmt.Fprintln(terminal, marker); err != nil {
			t.Fatal(err)
		}
		waitSignalOutput(t, capture, nil, func(output string) bool { return strings.Contains(output, marker) })
		visible := summaryVisibleFrame(capture.String())
		if index == 1 && !strings.Contains(visible, want) {
			t.Fatalf("feedback erased before next input: want %q, visible=%q", want, visible)
		}
		if index == 2 && strings.Contains(visible, want) {
			t.Fatalf("previous feedback survived next command: %q", visible)
		}
	}
	summary := newFeedbackSummary(t, terminal, input)
	if !supportsScreenRefresh(terminal) || supportsStaveInteractiveTerminal(input, terminal) {
		t.Fatal("fixture must exercise line input with actual terminal refresh output")
	}
	if err := summary.Start(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if summary.Out != terminal || summary.In != input {
		t.Fatal("summary stream ownership changed")
	}
}

func newFeedbackSummary(t *testing.T, out io.Writer, in io.Reader) *Summary {
	t.Helper()
	analyzer := &stubAnalyzer{}
	if err := json.Unmarshal([]byte(`{"dependencies":[{"name":"alpha","language":"go"}]}`), &analyzer.report); err != nil {
		t.Fatal(err)
	}
	return NewSummary(out, in, analyzer, nil)
}

func summaryVisibleFrame(output string) string {
	const clearScreen = "\x1b[H\x1b[2J"
	if index := strings.LastIndex(output, clearScreen); index >= 0 {
		return output[index+len(clearScreen):]
	}
	return output
}

type summaryFeedbackReader struct {
	commands   []string
	beforeRead func(int)
	index      int
}

func (r *summaryFeedbackReader) Read(buffer []byte) (int, error) {
	if r.index >= len(r.commands) {
		return 0, io.EOF
	}
	r.beforeRead(r.index)
	command := r.commands[r.index]
	r.index++
	return copy(buffer, command), nil
}

func TestSummaryRedirectedFeedbackRemainsSingleCopy(t *testing.T) {
	output := &strings.Builder{}
	summary := newFeedbackSummary(t, output, strings.NewReader("open alpha\nnot-a-command\nq\n"))
	if err := summary.Start(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	for _, feedback := range []string{"Dependency detail", "Unknown command."} {
		if count := strings.Count(output.String(), feedback); count != 1 {
			t.Fatalf("feedback %q appeared %d times", feedback, count)
		}
	}
	if strings.Contains(output.String(), "\x1b[2J") {
		t.Fatal("redirected output cleared")
	}
}
