package runtime

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestBoundedRuntimeCommandOutputKeepsTail(t *testing.T) {
	output := &boundedRuntimeCommandOutput{data: make([]byte, 5)}
	if written, err := output.Write(nil); err != nil || written != 0 {
		t.Fatalf("expected empty write to be ignored, written=%d err=%v", written, err)
	}
	for _, value := range []string{"ab", "cdef"} {
		if _, err := output.Write([]byte(value)); err != nil {
			t.Fatalf("write bounded output: %v", err)
		}
	}
	diagnostic := output.diagnostic()
	if !strings.Contains(string(diagnostic), "truncated") {
		t.Fatalf("expected truncation notice, got %q", diagnostic)
	}
	if !bytes.HasSuffix(diagnostic, []byte("bcdef")) {
		t.Fatalf("expected newest output tail, got %q", diagnostic)
	}
}

func TestBoundedRuntimeCommandOutputHandlesOversizedWrite(t *testing.T) {
	output := &boundedRuntimeCommandOutput{data: make([]byte, 4)}
	if _, err := output.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("write oversized output: %v", err)
	}
	if diagnostic := output.diagnostic(); !bytes.HasSuffix(diagnostic, []byte("efgh")) {
		t.Fatalf("expected oversized write tail, got %q", diagnostic)
	}
}

func TestCaptureBoundsFailureOutput(t *testing.T) {
	repo := t.TempDir()
	padding := strconv.Itoa(runtimeCommandOutputLimit + 1024)
	script := "#!/bin/sh\nprintf prefix-marker\nhead -c " + padding + " /dev/zero | tr '\\000' x\nprintf tail-marker\nexit 2\n"
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "make", script))

	err := Capture(context.Background(), CaptureRequest{RepoPath: repo, Command: "make test"})
	if err == nil {
		t.Fatal("expected runtime command failure")
	}
	message := err.Error()
	if !strings.Contains(message, "output truncated") || !strings.Contains(message, "tail-marker") {
		t.Fatalf("expected bounded tail diagnostics, got %q", message)
	}
	if strings.Contains(message, "prefix-marker") {
		t.Fatalf("expected oldest command output to be discarded, got %q", message)
	}
	if len(message) > runtimeCommandOutputLimit+256 {
		t.Fatalf("expected bounded failure message, got %d bytes", len(message))
	}
}
