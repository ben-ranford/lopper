//go:build !windows

package scripts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookPreflightKeepsAnchorNoticesOutOfGitDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, config string }{
		{name: "missing key"},
		{name: "present key", config: "[core]\n hooksPath = custom-hooks\n"},
		{name: "invalid config", config: "[unterminated\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			configPath := filepath.Join(tmp, "config")
			writeFile(t, configPath, tc.config)
			witness := filepath.Join(tmp, "hold-reaped")
			helper := preflightAnchorNotificationFixture(t, "hook-config-preflight.sh")
			args := []string{"config", "--file", configPath, "--get", "core.hooksPath"}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			wantOut, wantErr, wantStatus := preflightDiagnosticResult(t, exec.CommandContext(ctx, "git", args...))
			shellArgs := []string{"-c", `. "$1"
shift
trap cleanup_preflight_git EXIT
run_preflight_git git "$@"
`, "sh", helper}
			command := exec.CommandContext(ctx, "sh", append(shellArgs, args...)...)
			command.Env = append(os.Environ(), "HOOK_ANCHOR_REAPED="+witness)
			command.WaitDelay = time.Second
			gotOut, gotErr, gotStatus := preflightDiagnosticResult(t, command)
			if status, err := os.ReadFile(witness); err != nil || strings.TrimSpace(string(status)) != "137" {
				t.Fatalf("anchor did not reap its SIGKILLed hold child: status=%q err=%v", status, err)
			}
			if ctx.Err() != nil || gotOut != wantOut || gotErr != wantErr || gotStatus != wantStatus {
				t.Fatalf("anchor teardown altered Git result: status=%d want=%d stdout=%q want=%q stderr=%q want=%q context=%v", gotStatus, wantStatus, gotOut, wantOut, gotErr, wantErr, ctx.Err())
			}
		})
	}
}

func TestHooksInstallIgnoresAnchorTeardownNotices(t *testing.T) {
	assertHooksInstallIgnoresAnchorTeardownNotices(t, "hook-config-preflight.sh")
}

func assertHooksInstallIgnoresAnchorTeardownNotices(t *testing.T, sourcePath string) {
	t.Helper()
	fixture := newPreflightTimeoutFixture(t, "hooks-install")
	witness := filepath.Join(t.TempDir(), "hold-reaped")
	helper := preflightAnchorNotificationFixture(t, sourcePath)
	copyHookFixtureFile(t, helper, filepath.Join(fixture.repoDir, "scripts", "hook-config-preflight.sh"), 0o644)
	output, err := runMakeWithPreflightTimeout(t, fixture.repoDir, "hooks-install", "HOOK_ANCHOR_REAPED="+witness)
	if status, readErr := os.ReadFile(witness); readErr != nil || strings.TrimSpace(string(status)) != "137" {
		t.Fatalf("installer did not reap its SIGKILLed hold child: status=%q err=%v output=%s", status, readErr, output)
	}
	if err != nil {
		t.Fatalf("anchor teardown blocked initial hook installation: %v\n%s", err, output)
	}
	actual, err := hookCommand(fixture.repoDir, "git", "config", "--local", "--get", "core.hooksPath")
	if err != nil || strings.TrimSpace(actual) != filepath.Dir(fixture.managedHook) {
		t.Fatalf("managed hook was not activated: path=%q err=%v", actual, err)
	}
}

func preflightAnchorNotificationFixture(t *testing.T, sourcePath string) string {
	t.Helper()
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	// Deliver the normal final group kill to the hold child first, then let the
	// anchor finish wait before delivering it to the remaining owned group.
	// This forces the kernel's possible signal ordering without changing Git.
	for _, change := range []struct{ before, after string }{
		{
			before: `) & hold_pid=$!`,
			after:  ") & hold_pid=$!\n\t\tprintf \"%s\\n\" \"$hold_pid\" >\"$state_dir/test-hold-pid\"",
		},
		{
			before: `do wait "$hold_pid" || :; done`,
			after: `do
			printf x >"$state_dir/test-hold-waiting"
			wait "$hold_pid" || {
				printf "%s\n" "$?" >"$HOOK_ANCHOR_REAPED"
				printf x >"$state_dir/test-hold-reaped"
				test_hold_deadline=$((SECONDS + 2))
				while [ "$SECONDS" -lt "$test_hold_deadline" ]; do :; done
			}
		done`,
		},
		{
			before: `kill -KILL -- "-$reader_group" || :`,
			after: `test_kill_owned_group() {
			reader_group_is_running_or_interrupted || return 1
			kill -0 -- "-$reader_group" 2>/dev/null || return 1
			kill -KILL -- "-$reader_group"
		}
		test_wait_for_file() {
			for ((test_attempt = 0; test_attempt < 100; test_attempt++)); do
				[ ! -f "$1" ] || return 0
				sleep 0.01
			done
			test_kill_owned_group || :
			return 1
		}
		test_wait_for_file "$state_dir/test-hold-waiting" || exit 125
		read -r test_hold_pid <"$state_dir/test-hold-pid"
		kill -KILL "$test_hold_pid"
		test_wait_for_file "$state_dir/test-hold-reaped" || exit 125
		test_kill_owned_group || exit 125`,
		},
	} {
		if strings.Count(source, change.before) != 1 {
			t.Fatalf("anchor notification boundary unavailable: %s", change.before)
		}
		source = strings.Replace(source, change.before, change.after, 1)
	}
	helper := filepath.Join(t.TempDir(), "preflight.sh")
	writeFile(t, helper, source)
	return helper
}
