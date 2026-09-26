//go:build !windows

package scripts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestHooksCleanupBoundsForeignWorktreeConfig(t *testing.T) {
	repo := newHookFixture(t)
	managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	linked := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repo, "git", "worktree", "add", "-b", "other", linked)
	runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
	fifo := filepath.Join(t.TempDir(), "include.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	runCommand(t, linked, "git", "config", "--worktree", "include.path", fifo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "hooks-uninstall")
	cmd.Dir = repo
	tmp := t.TempDir()
	cmd.Env = append(withoutGitEnv(), "TMPDIR="+tmp)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Timed out while reading Git preflight configuration") {
		t.Fatalf("foreign config timeout: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(managed, "pre-commit")); err != nil {
		t.Fatalf("ambiguous reference removed snapshot: %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup left temporary files: %v %v", entries, err)
	}
}

// Exercise Windows spelling normalization on POSIX using a Windows host marker
// and local stand-ins for the drive/UNC mounts. Native mount behavior belongs to
// Git's shell; this checks the resolver's separator and alias handling.
func TestHooksCleanupWindowsPathNormalization(t *testing.T) {
	repo := newHookFixture(t)
	managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	bin := t.TempDir()
	writeFileMode(t, filepath.Join(bin, "uname"), "#!/bin/sh\nprintf 'MINGW64_NT\\n'\n", 0o755)
	drive := filepath.Join(repo, "C:")
	if err := os.Mkdir(drive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(managed, filepath.Join(drive, "managed")); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(drive, "unrelated")
	if err := os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "C:unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path     string
		retained bool
	}{
		{"C:/managed", true}, {`C:\managed`, true},
		{"C:unrelated", true}, {"C:", true},
		{"C:/unrelated", false}, {`C:\unrelated`, false},
		{"/" + managed, true}, {strings.ReplaceAll("/"+managed, "/", `\`), true},
		{"/" + unrelated, false}, {strings.ReplaceAll("/"+unrelated, "/", `\`), false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			cmd := exec.Command("sh", "scripts/cleanup-hook-snapshot.sh", "reference", managed, tc.path)
			cmd.Dir = repo
			cmd.Env = append(withoutGitEnv(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.retained {
				t.Fatalf("path %q retained=%v: %v\n%s", tc.path, tc.retained, err, output)
			}
		})
	}
}
