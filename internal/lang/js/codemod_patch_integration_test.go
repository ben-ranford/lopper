package js

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCodemodPatchFilenamesApplyOnlyToIntendedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forbids POSIX control and quote filename bytes; portable header encoding is tested in shared")
	}
	filenames := []string{
		"src/plain.js", "src/with space.js", "src/quote\".js", "src/back\\slash.js",
		"src/new\nline.js", "src/carriage\rreturn.js", "src/tab\tname.js",
		"src/control\x01\x1b\x7f.js", "src/café.js",
		"src/attack\n--- a/victim.js\n+++ b/victim.js\n@@ -1 +1 @@\n-old\n+injected\n.js",
	}
	for _, filename := range filenames {
		t.Run(filename, func(t *testing.T) {
			t.Run("replace", func(t *testing.T) { assertCodemodPatchApplies(t, filename, false) })
			t.Run("delete", func(t *testing.T) { assertCodemodPatchApplies(t, filename, true) })
		})
	}
}

func assertCodemodPatchApplies(t *testing.T, filename string, deleteLine bool) {
	t.Helper()
	repo := t.TempDir()
	testutil.RunGit(t, repo, "init", "-q")
	testutil.MustWriteFile(t, filepath.Join(repo, filename), "old\nkeep\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "victim.js"), "old\n")
	testutil.RunGit(t, repo, "add", "-A")
	patch := shared.BuildSingleLinePatch(filename, 1, "old", "new")
	want := "new\nkeep\n"
	if deleteLine {
		patch = shared.BuildDeleteLinePatch(filename, 1, "old")
		want = "keep\n"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// Fixed Git command: apply the real preview parser to the disposable index.
	command := exec.CommandContext(ctx, "/usr/bin/git", "-C", repo, "apply", "--cached", "--unidiff-zero", "-")
	command.Env = gitexec.SanitizedEnv()
	command.Stdin = strings.NewReader(patch + "\n")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply preview %q: %v\n%s", patch, err, output)
	}
	if got := testutil.GitOutput(t, repo, "show", ":"+filename); got != strings.TrimSpace(want) {
		t.Fatalf("patched target = %q, want %q", got, want)
	}
	if got := testutil.GitOutput(t, repo, "show", ":victim.js"); got != "old" {
		t.Fatalf("patch changed victim: %q", got)
	}
	original, err := os.ReadFile(filepath.Join(repo, filename))
	if err != nil || string(original) != "old\nkeep\n" {
		t.Fatalf("preview modified working source: %q, %v", original, err)
	}
}
