package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	shaMain        = "1111111111111111111111111111111111111111"
	shaTopic       = "2222222222222222222222222222222222222222"
	shaHex         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	expectedGotFmt = "expected %q, got %q"
	packedRefsFile = "packed-refs"
	mainRefPath    = "refs/heads/main"
	topicRefPath   = "refs/heads/topic"
	otherMainRef   = "refs/heads/other"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setupFakeGitResolver(t *testing.T, script string) {
	t.Helper()

	original := resolveGitBinaryPathFn
	originalExec := execGitCommandFn
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
		execGitCommandFn = originalExec
	})

	fakeGit := filepath.Join(t.TempDir(), "fake-git.sh")
	mustWrite(t, fakeGit, script)
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatalf("chmod fake git: %v", err)
	}

	resolveGitBinaryPathFn = func() (string, error) {
		return fakeGit, nil
	}
	execGitCommandFn = func(path string, args ...string) (*exec.Cmd, error) {
		return exec.Command(path, args...), nil
	}
}
