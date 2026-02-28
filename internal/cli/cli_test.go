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

func (f *fakeRunner) Execute(context.Context, app.Request) (string, error) {
	return f.output, f.err
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

func TestRunFailOnIncreaseError(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{err: app.ErrFailOnIncrease}, &out, &errOut)
	code := c.Run(context.Background(), []string{"analyse", "lodash"})
	if code != 3 {
		t.Fatalf("expected fail-on-increase exit code 3, got %d", code)
	}
}

func TestRunLockfileDriftError(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	c := New(&fakeRunner{err: app.ErrLockfileDrift}, &out, &errOut)
	code := c.Run(context.Background(), []string{"analyse", "lodash"})
	if code != 4 {
		t.Fatalf("expected lockfile-drift exit code 4, got %d", code)
	}
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

func TestUsageReturnsText(t *testing.T) {
	if !strings.Contains(Usage(), "lopper analyse") {
		t.Fatalf("expected usage text to include analyse command")
	}
}
