package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLockfileDriftPropagatesGitContextErrors(t *testing.T) {
	original := resolveGitBinaryPathFn
	defer func() { resolveGitBinaryPathFn = original }()

	resolveGitBinaryPathFn = func() (string, error) { return writeFakeGitBinary(t), nil }
	useFakeGitCommandContext(t)

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")
	writeFakeGitMode(t, repo, "lsfail")

	for _, stopOnFirst := range []bool{false, true} {
		_, err := detectLockfileDrift(context.Background(), repo, stopOnFirst)
		if err == nil || !strings.Contains(err.Error(), "ls-files") {
			t.Fatalf("expected ls-files error with stopOnFirst=%v, got %v", stopOnFirst, err)
		}
	}
}
