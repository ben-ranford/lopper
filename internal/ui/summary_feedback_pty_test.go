//go:build darwin || linux

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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
		visible := summaryVisibleFrame(t, capture.String())
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

func TestSummaryInPlaceCancellationReturnsInputErrorUnchanged(t *testing.T) {
	master, terminal := openSummaryFeedbackPTY(t)
	capture := newSignalPTYCapture(master)
	t.Cleanup(func() { waitSignalCapture(t, capture) })
	cleanupStaveTestCloser(t, "feedback cancellation master", master)
	cleanupStaveTestCloser(t, "feedback cancellation terminal", terminal)

	inputErr := &staveInputError{err: context.Canceled}
	input := io.MultiReader(strings.NewReader("open alpha\n"), &errReader{err: inputErr})
	summary := newFeedbackSummary(t, terminal, input)
	if !supportsScreenRefresh(summary.Out) || supportsStaveInteractiveTerminal(summary.In, summary.Out) {
		t.Fatal("fixture must exercise line input with actual terminal refresh output")
	}

	err := summary.Start(context.Background(), Options{})
	if !reflect.ValueOf(err).Equal(reflect.ValueOf(inputErr)) || !errors.Is(err, context.Canceled) {
		t.Fatalf("input cancellation identity lost: %v", err)
	}
	if summary.In != input || summary.Out != terminal {
		t.Fatal("input error changed stream ownership")
	}
	const marker = "CANCELLATION-FRAME-END"
	if _, err := fmt.Fprintln(terminal, marker); err != nil {
		t.Fatal(err)
	}
	waitSignalOutput(t, capture, nil, func(output string) bool { return strings.Contains(output, marker) })
	if frame := summaryVisibleFrame(t, capture.String()); !strings.Contains(frame, "Dependency detail") {
		t.Fatalf("feedback missing from the cancelled session's final frame: %q", frame)
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

func summaryVisibleFrame(t *testing.T, output string) string {
	t.Helper()
	sequence := summaryClearSequence(t)
	if index := strings.LastIndex(output, sequence); index >= 0 {
		return output[index+len(sequence):]
	}
	t.Fatalf("output has no clear-screen sequence: %q", output)
	return ""
}

func summaryClearSequence(t *testing.T) string {
	t.Helper()
	var clearScreen strings.Builder
	if err := clearSummaryScreen(&clearScreen); err != nil {
		t.Fatalf("capture clear-screen sequence: %v", err)
	}
	sequence := clearScreen.String()
	if sequence == "" {
		t.Fatal("clear-screen sequence is empty")
	}
	return sequence
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
	if strings.Contains(output.String(), summaryClearSequence(t)) {
		t.Fatal("redirected output cleared")
	}
}
