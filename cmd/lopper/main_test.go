package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunTopLevelFlags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		arg        string
		wantStdout string
	}{
		{name: "help", arg: "--help", wantStdout: "Usage:"},
		{name: "version", arg: "--version", wantStdout: "lopper "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := strings.NewReader("")
			var out bytes.Buffer
			var errOut bytes.Buffer

			code := run([]string{tc.arg}, in, &out, &errOut)
			if code != 0 {
				t.Fatalf("expected exit code 0 for %s, got %d", tc.name, code)
			}
			if !strings.Contains(out.String(), tc.wantStdout) {
				t.Fatalf("expected %s output on stdout, got %q", tc.name, out.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("expected no stderr output for %s, got %q", tc.name, errOut.String())
			}
		})
	}
}

func TestRunParseError(t *testing.T) {
	in := strings.NewReader("")
	var out bytes.Buffer
	var errOut bytes.Buffer

	code := run([]string{"nope"}, in, &out, &errOut)
	if code != 2 {
		t.Fatalf("expected parse error exit code 2, got %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("expected parse error details on stderr, got %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("expected usage text on stderr for parse error, got %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("expected no stdout output for parse error, got %q", out.String())
	}
}

func TestMainInvokesExitFuncWithRunCode(t *testing.T) {
	oldExit := exitFunc
	oldArgs := os.Args
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	defer func() {
		exitFunc = oldExit
		os.Args = oldArgs
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = outW
	os.Stderr = errW
	defer func() {
		if err := outR.Close(); err != nil {
			t.Fatalf("close stdout reader: %v", err)
		}
		if err := errR.Close(); err != nil {
			t.Fatalf("close stderr reader: %v", err)
		}
	}()

	code := -1
	exitFunc = func(c int) { code = c }
	os.Args = []string{"lopper", "--help"}

	main()
	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	if err := errW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	if _, err := io.ReadAll(outR); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if _, err := io.ReadAll(errR); err != nil {
		t.Fatalf("read stderr: %v", err)
	}

	if code != 0 {
		t.Fatalf("expected main to exit with code 0 for --help, got %d", code)
	}
}
