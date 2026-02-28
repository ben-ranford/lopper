package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

type fakeRunner struct {
	output string
	err    error
}

type failWriter struct{}
type failOnNthWrite struct {
	n     int
	count int
}

func (f *fakeRunner) Execute(context.Context, app.Request) (string, error) {
	return f.output, f.err
}

func (*failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (w *failOnNthWrite) Write(b []byte) (int, error) {
	w.count++
	if w.count == w.n {
		return 0, errors.New("write failed")
	}
	return len(b), nil
}

func TestNew(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{}, &out, &errOut)
	if c == nil {
		t.Fatalf("expected cli to be created")
	}
}

func TestRunHelp(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{}, &out, &errOut)
	code := c.Run(context.Background(), []string{"--help"})
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected usage output")
	}
}

func TestRunHelpWriterFailure(t *testing.T) {
	c := New(&fakeRunner{}, &failWriter{}, &bytes.Buffer{})
	code := c.Run(context.Background(), []string{"--help"})
	if code != 1 {
		t.Fatalf("expected help writer failure to return code 1, got %d", code)
	}
}

func TestRunParseError(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{}, &out, &errOut)
	code := c.Run(context.Background(), []string{"nope"})
	if code != 2 {
		t.Fatalf("expected parse error code 2, got %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("expected parse error output, got %q", errOut.String())
	}
}

func TestRunParseErrorWriterFailure(t *testing.T) {
	c := New(&fakeRunner{}, &bytes.Buffer{}, &failWriter{})
	code := c.Run(context.Background(), []string{"nope"})
	if code != 1 {
		t.Fatalf("expected parse-error writer failure to return code 1, got %d", code)
	}
}

func TestRunParseErrorUsageWriterFailure(t *testing.T) {
	errOut := &failOnNthWrite{n: 2}
	c := New(&fakeRunner{}, &bytes.Buffer{}, errOut)
	code := c.Run(context.Background(), []string{"nope"})
	if code != 1 {
		t.Fatalf("expected usage writer failure to return code 1, got %d", code)
	}
}

func assertRunExitCodeForRunnerError(t *testing.T, runnerErr error, wantCode int, label string) {
	t.Helper()
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{err: runnerErr}, &out, &errOut)
	code := c.Run(context.Background(), []string{"analyse", "lodash"})
	if code != wantCode {
		t.Fatalf("expected %s exit code %d, got %d", label, wantCode, code)
	}
}

func TestRunFailOnIncreaseError(t *testing.T) {
	assertRunExitCodeForRunnerError(t, app.ErrFailOnIncrease, 3, "fail-on-increase")
}

func TestRunLockfileDriftError(t *testing.T) {
	assertRunExitCodeForRunnerError(t, app.ErrLockfileDrift, 4, "lockfile-drift")
}

func TestRunUncertaintyThresholdError(t *testing.T) {
	assertRunExitCodeForRunnerError(t, app.ErrUncertaintyThresholdExceeded, 3, "uncertainty threshold")
}

func TestRunGenericRunnerError(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{err: errors.New("boom")}, &out, &errOut)
	code := c.Run(context.Background(), []string{"analyse", "lodash"})
	if code != 1 {
		t.Fatalf("expected generic error code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("expected runner error output")
	}
}

func TestRunOutputNewlineHandling(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{output: "ok"}, &out, &errOut)
	code := c.Run(context.Background(), []string{"analyse", "lodash"})
	if code != 0 {
		t.Fatalf("expected success code 0, got %d", code)
	}
	if out.String() != "ok\n" {
		t.Fatalf("expected newline-appended output, got %q", out.String())
	}
}

func TestRunOutputWriterFailure(t *testing.T) {
	c := New(&fakeRunner{output: "ok"}, &failWriter{}, &bytes.Buffer{})
	code := c.Run(context.Background(), []string{"analyse", "lodash"})
	if code != 1 {
		t.Fatalf("expected output writer failure to return code 1, got %d", code)
	}
}

func TestUsageReturnsText(t *testing.T) {
	if !strings.Contains(Usage(), "lopper analyse") {
		t.Fatalf("expected usage text to include analyse command")
	}
}
