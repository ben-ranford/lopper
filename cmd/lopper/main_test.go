package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	in := strings.NewReader("")
	var out bytes.Buffer
	var errOut bytes.Buffer

	code := run([]string{"--help"}, in, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected exit code 0 for help, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected usage output on stdout, got %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("expected no stderr output for help, got %q", errOut.String())
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
