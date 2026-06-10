package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
)

func TestWriteOutputAdditionalBranches(t *testing.T) {
	c := New(&fakeRunner{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := c.writeOutput(""); err != nil {
		t.Fatalf("expected empty output to be ignored, got %v", err)
	}

	var out bytes.Buffer
	c = New(&fakeRunner{}, &out, &bytes.Buffer{})
	if err := c.writeOutput("ok\n"); err != nil {
		t.Fatalf("write output with existing newline: %v", err)
	}
	if out.String() != "ok\n" {
		t.Fatalf("expected output to be left unchanged, got %q", out.String())
	}

	c = New(&fakeRunner{}, &failOnNthWrite{n: 2}, &bytes.Buffer{})
	if c.writeOutput("ok") == nil {
		t.Fatalf("expected newline append write to fail")
	}
}

func TestExitCodeForDeniedLicenses(t *testing.T) {
	if got := exitCodeForRunError(app.ErrDeniedLicenses); got != 3 {
		t.Fatalf("expected denied-license error to use exit code 3, got %d", got)
	}
}

func TestRunRunnerErrorWriteFailure(t *testing.T) {
	c := New(&fakeRunner{err: app.ErrLockfileDrift}, &bytes.Buffer{}, &failWriter{})
	if code := c.Run(context.Background(), []string{"analyse", "lodash"}); code != 1 {
		t.Fatalf("expected err writer failure to return exit code 1, got %d", code)
	}
}

func TestRunPreservesRunErrorExitCodeOnOutputWriteFailure(t *testing.T) {
	cases := []struct {
		name     string
		runErr   error
		wantCode int
	}{
		{name: "threshold_breach", runErr: app.ErrFailOnIncrease, wantCode: 3},
		{name: "lockfile_drift", runErr: app.ErrLockfileDrift, wantCode: 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(&fakeRunner{output: "partial report", err: tc.runErr}, &failWriter{}, &bytes.Buffer{})
			if code := c.Run(context.Background(), []string{"analyse", "lodash"}); code != tc.wantCode {
				t.Fatalf("expected mapped run error exit code %d, got %d", tc.wantCode, code)
			}
		})
	}
}
