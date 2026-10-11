//go:build windows && lopper_native_windows

package scripts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandruntime "github.com/ben-ranford/lopper/internal/runtime"
)

// Native execution is required: the _windows suffix is a platform build
// constraint, not a substitute for the CI job checking every named outcome.
func TestHooksCleanupNativeWindows(t *testing.T) {
	owner := &nativeCleanupOwner{quiescent: true}
	t.Log(owner.command(t, "", "git", "--version"))
	t.Log(owner.command(t, "", "bash", "-c", "uname -a; command -v git; command -v stat"))
	for _, scheme := range []string{"drive", "unc"} {
		t.Run(scheme, func(t *testing.T) {
			for _, scenario := range []string{"equivalent", "equivalent-backslashes", "equivalent-case", "unrelated", "shadowed", "missing", "drive-relative", "failed-config"} {
				t.Run(scenario, func(t *testing.T) { assertNativeCleanup(t, scheme, scenario) })
			}
		})
	}
}

type nativeCleanupFixture struct {
	repo, linked, managed, custom, referenceRoot string
	owner                                        *nativeCleanupOwner
}

func assertNativeCleanup(t *testing.T, scheme, scenario string) {
	t.Helper()
	f := newNativeCleanupFixture(t, scheme)
	reference := f.reference(t, scenario)
	f.owner.command(t, f.linked, "git", "config", "--worktree", "--add", "core.hooksPath", reference)
	if scenario == "shadowed" {
		f.owner.command(t, f.linked, "git", "config", "--worktree", "--add", "core.hooksPath", f.custom)
	}
	f.assertReferencePremise(t, scenario, reference)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.ToSlash(filepath.Join(cwd, "cleanup-hook-snapshot.sh"))
	// Preserve the resolver's account-home invariant without intercepting Bash,
	// Git, stat, config selectors or the production identity comparison.
	output := f.owner.command(t, f.repo, "bash", "-c", `unset HOME XDG_CONFIG_HOME; HOME=$(printf '%s' ~); export HOME; script=$(cygpath -u -- "$1") || exit; case "$script" in /*) exec sh "$script" ;; *) exit 125 ;; esac`, "cleanup-test", script)
	t.Log(output)
	_, err = os.Stat(filepath.Join(f.managed, "pre-commit"))
	if scenario == "unrelated" {
		if !os.IsNotExist(err) {
			t.Fatalf("provably unrelated snapshot retained: %v", err)
		}
	} else if err != nil {
		t.Fatalf("equivalent or ambiguous snapshot removed: %v", err)
	} else if got := readFile(t, filepath.Join(f.managed, "pre-commit")); got != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("retained snapshot content changed: %q", got)
	}
	if got := readFile(t, filepath.Join(f.custom, "sentinel")); got != "unrelated hooks remain\n" {
		t.Fatalf("unrelated hook directory changed: %q", got)
	}
}

func newNativeCleanupFixture(t *testing.T, scheme string) nativeCleanupFixture {
	t.Helper()
	owner := &nativeCleanupOwner{quiescent: true}
	root, referenceRoot := nativeCleanupRoot(t, scheme, owner)
	f := nativeCleanupFixture{owner: owner, repo: filepath.Join(root, "repo space"), linked: filepath.Join(root, "linked space"), custom: filepath.Join(root, "custom hooks"), referenceRoot: referenceRoot}
	owner.command(t, root, "git", "init", "-b", "main", f.repo)
	f.owner.command(t, f.repo, "git", "-c", "user.name=Hook Test", "-c", "user.email=hook-test@example.com", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "native cleanup fixture")
	f.owner.command(t, f.repo, "git", "worktree", "add", "-b", "linked", f.linked)
	f.owner.command(t, f.repo, "git", "config", "extensions.worktreeConfig", "true")
	f.managed = filepath.Join(f.repo, ".git", "lopper-hooks")
	writeFile(t, filepath.Join(f.managed, "pre-commit"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(f.custom, "sentinel"), "unrelated hooks remain\n")
	f.owner.command(t, f.repo, "git", "config", "--local", "core.hooksPath", f.custom)
	return f
}

func nativeCleanupRoot(t *testing.T, scheme string, owner *nativeCleanupOwner) (string, string) {
	t.Helper()
	local, unc := os.Getenv("LOPPER_NATIVE_SMB_LOCAL"), os.Getenv("LOPPER_NATIVE_SMB_UNC")
	if !filepath.IsAbs(local) || !strings.HasPrefix(unc, `\\localhost\`) {
		t.Fatal("UNC fixture requires absolute LOPPER_NATIVE_SMB_LOCAL and a provisioned \\\\localhost\\ share in LOPPER_NATIVE_SMB_UNC")
	}
	root, err := os.MkdirTemp(local, "cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.remove(root); err != nil {
			t.Errorf("remove owned SMB fixture: %v", err)
		}
	})
	if scheme == "drive" {
		return root, root
	}
	reference := filepath.Join(unc, filepath.Base(root))
	// Prove this is the provisioned shared directory, not a spelling surrogate.
	marker := fmt.Sprintf("owned fixture %s", root)
	writeFile(t, filepath.Join(root, "ownership"), marker)
	if got := readFile(t, filepath.Join(reference, "ownership")); got != marker {
		t.Fatalf("UNC share does not expose owned fixture: %q", got)
	}
	t.Log(owner.command(t, root, "bash", "-c", `stat -L -c '%d:%i %n' "$1" "$2"`, "native-share-identity", filepath.ToSlash(root), filepath.ToSlash(reference)))
	return root, reference
}

func (f nativeCleanupFixture) reference(t *testing.T, scenario string) string {
	t.Helper()
	value := filepath.Join(f.referenceRoot, "repo space", ".git", "lopper-hooks")
	switch scenario {
	case "unrelated":
		value = filepath.Join(f.referenceRoot, "custom hooks")
	case "missing":
		value = filepath.Join(f.referenceRoot, "missing hooks")
	case "drive-relative":
		return filepath.VolumeName(f.repo) + "ambiguous-hooks"
	case "equivalent-case":
		return strings.ToUpper(filepath.ToSlash(value))
	case "equivalent-backslashes":
		return value
	}
	return filepath.ToSlash(value)
}

type nativeCleanupOwner struct {
	quiescent bool
}

func (owner *nativeCleanupOwner) command(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	tool := os.Getenv("LOPPER_NATIVE_" + strings.ToUpper(name))
	if !filepath.IsAbs(tool) {
		t.Fatalf("native %s requires an absolute independently provisioned runtime path", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := owner.run(ctx, dir, tool, args...)
	if err != nil {
		t.Fatalf("native %s %q failed: %v (context %v)\n%s", name, args, err, ctx.Err(), output)
	}
	return output
}

func (owner *nativeCleanupOwner) run(ctx context.Context, dir, tool string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir, cmd.Env = dir, withoutGitEnv()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	commandruntime.ConfigureCommandCancellation(cmd)
	cmd.WaitDelay = 5 * time.Second
	owner.quiescent = false
	cleanup, err := commandruntime.StartCommand(cmd)
	if err != nil {
		return output.String(), err
	}
	waitErr := cmd.Wait()
	cleanupErr := cleanup()
	owner.quiescent = cleanupErr == nil
	return output.String(), errors.Join(waitErr, cleanupErr)
}

func (owner *nativeCleanupOwner) remove(root string) error {
	if !owner.quiescent {
		return fmt.Errorf("retain owned fixture %q for trusted outer cleanup: command descendants not proven stopped", root)
	}
	return os.RemoveAll(root)
}

func (f nativeCleanupFixture) assertReferencePremise(t *testing.T, scenario, reference string) {
	t.Helper()
	config := strings.TrimSpace(f.owner.command(t, f.linked, "git", "rev-parse", "--path-format=absolute", "--git-path", "config.worktree"))
	readback := f.owner.command(t, f.linked, "git", "config", "--null", "--show-origin", "--show-scope", "--get-all", "core.hooksPath")
	assertNativeConfigValue(t, readback, "local", filepath.Join(f.repo, ".git", "config"), f.custom)
	assertNativeConfigValue(t, readback, "worktree", config, reference)
	if scenario == "shadowed" {
		assertNativeConfigValue(t, readback, "worktree", config, f.custom)
	}
	if scenario == "failed-config" {
		writeFile(t, config, "[invalid\n")
		f.assertFailedInspection(t)
		return
	}
	if scenario == "missing" || scenario == "drive-relative" {
		t.Logf("intentional %s reference premise: %q", scenario, reference)
		if scenario == "missing" {
			if _, err := os.Stat(reference); !os.IsNotExist(err) {
				t.Fatalf("missing reference is not missing: %v", err)
			}
		}
		return
	}
	if scenario == "unrelated" {
		return
	}
	if got := readFile(t, filepath.Join(reference, "pre-commit")); got != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("exact equivalent reference is inaccessible or changed: %q", got)
	}
	t.Logf("exact reference spelling: %q", reference)
	t.Log(f.owner.command(t, f.repo, "bash", "-c", `stat -L -c '%d:%i %n' "$1" "$2"`, "native-reference-identity", reference, f.managed))
}

func assertNativeConfigValue(t *testing.T, output, scope, origin, value string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(origin)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(fields)%3 != 0 {
		t.Fatalf("invalid config scope/origin/value fields: %q", output)
	}
	for i := 0; i < len(fields); i += 3 {
		if fields[i] == scope && fields[i+1] == "file:"+filepath.ToSlash(resolved) && fields[i+2] == value {
			t.Logf("config scope=%s origin=%s value=%q", scope, fields[i+1], value)
			return
		}
	}
	t.Fatalf("missing exact config scope=%s value=%q: %q", scope, value, output)
}

func (f nativeCleanupFixture) assertFailedInspection(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := f.owner.run(ctx, f.linked, os.Getenv("LOPPER_NATIVE_GIT"), "config", "--show-origin", "--show-scope", "--get-all", "core.hooksPath")
	var exitErr *exec.ExitError
	if !f.owner.quiescent || ctx.Err() != nil || !errors.As(err, &exitErr) || !strings.Contains(output, "bad config line") {
		t.Fatalf("malformed config did not produce joined Git inspection failure: %v\n%s", err, output)
	}
	t.Log(output)
}
