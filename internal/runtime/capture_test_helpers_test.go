package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
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
		"pytest",
		"python",
		"python3",
		"uv",
	}
	for _, tool := range tools {
		testutil.MustInstallFile(t, filepath.Join(toolDir, tool), "#!/bin/sh\nexit 0\n", 0o700)
	}
	return toolDir
}

func setupFakeRuntimeToolScript(t *testing.T, tool string, script string) string {
	t.Helper()

	toolDir := setupFakeRuntimeTools(t)
	testutil.MustInstallFile(t, filepath.Join(toolDir, tool), script, 0o700)
	return toolDir
}
