package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	npmTestCommand     = "npm test"
	runtimeTraceNDJSON = "runtime.ndjson"
	makeVersionCommand = "make -v"
)

func assertCaptureErrorContains(t *testing.T, req CaptureRequest, wantSubstring string) {
	t.Helper()

	err := Capture(context.Background(), req)
	if err == nil {
		t.Fatalf("expected capture error containing %q", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected capture error to contain %q, got %v", wantSubstring, err)
	}
}

func setupFakeRuntimeTools(t *testing.T) string {
	t.Helper()

	toolDir := t.TempDir()
	tools := []string{
		"npm",
		"pnpm",
		"yarn",
		"bun",
		"npx",
		"node",
		"vitest",
		"jest",
		"mocha",
		"ava",
		"deno",
		"make",
	}
	for _, tool := range tools {
		path := filepath.Join(toolDir, tool)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatalf("write fake runtime tool %q: %v", tool, err)
		}
	}
	return toolDir
}

func setupFakeRuntimeToolScript(t *testing.T, tool string, script string) string {
	t.Helper()

	toolDir := setupFakeRuntimeTools(t)
	path := filepath.Join(toolDir, tool)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake runtime tool script %q: %v", tool, err)
	}
	return toolDir
}
