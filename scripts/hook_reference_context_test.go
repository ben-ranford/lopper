package scripts

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

type hookReferenceCase struct {
	name                              string
	alias, conditional, bare, missing bool
	gitdir                            bool
	direct                            bool
	nonmatching                       bool
}

var hookReferenceCases = []hookReferenceCase{
	{name: "unrelated relative"},
	{name: "managed relative alias", alias: true},
	{name: "managed relative path", alias: true, direct: true},
	{name: "conditional unrelated", conditional: true},
	{name: "gitdir conditional unrelated", conditional: true, gitdir: true},
	{name: "gitdir conditional managed", conditional: true, gitdir: true, alias: true},
	{name: "conditional managed alias", conditional: true, alias: true},
	{name: "nonmatching conditional managed relative", conditional: true, alias: true, direct: true, nonmatching: true},
	{name: "bare common repository", bare: true},
	{name: "missing reference", missing: true},
}

func TestHooksUninstallResolvesOwningWorktree(t *testing.T) {
	for _, tc := range hookReferenceCases {
		t.Run(tc.name, func(t *testing.T) { assertHookReferenceCase(t, tc) })
	}
}

func assertHookReferenceCase(t *testing.T, tc hookReferenceCase) {
	t.Helper()
	repo, managed, linked := newHookReferenceWorktree(t, tc)
	configureHookReference(t, linked, managed, tc)
	caller := repo
	if tc.bare {
		caller = linked
	}
	runCommand(t, caller, "make", "hooks-uninstall")
	assertHookSnapshotRetention(t, managed, tc)
}

func newHookReferenceWorktree(t *testing.T, tc hookReferenceCase) (repo, managed, linked string) {
	t.Helper()
	repo = newHookFixture(t)
	managed = filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	linkedName := "linked space\nroot\n"
	if runtime.GOOS == "windows" {
		linkedName = "linked space"
	}
	linked = filepath.Join(t.TempDir(), linkedName)
	runCommand(t, repo, "git", "worktree", "add", "-b", "other", linked)
	runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
	if tc.bare {
		runCommand(t, repo, "git", "config", "core.bare", "true")
		runCommand(t, linked, "git", "config", "--worktree", "core.bare", "false")
	}
	return repo, managed, linked
}

func configureHookReference(t *testing.T, linked, managed string, tc hookReferenceCase) {
	t.Helper()
	hookPath := createHookReferencePath(t, linked, managed, tc)
	if tc.conditional {
		configureConditionalHookReference(t, linked, hookPath, tc)
	} else {
		runCommand(t, linked, "git", "config", "--worktree", "core.hooksPath", managed)
		runCommand(t, linked, "git", "config", "--worktree", "--add", "core.hooksPath", hookPath)
	}
}

func createHookReferencePath(t *testing.T, linked, managed string, tc hookReferenceCase) string {
	t.Helper()
	hookPath := "custom hooks"
	switch {
	case tc.direct:
		var err error
		hookPath, err = filepath.Rel(linked, managed)
		if err != nil {
			t.Fatal(err)
		}
	case tc.alias:
		if err := os.Symlink(managed, filepath.Join(linked, hookPath)); err != nil {
			t.Fatal(err)
		}
	case !tc.missing:
		if err := os.Mkdir(filepath.Join(linked, hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return hookPath
}

func configureConditionalHookReference(t *testing.T, linked, hookPath string, tc hookReferenceCase) {
	t.Helper()
	included := filepath.Join(t.TempDir(), "include.cfg")
	runCommand(t, linked, "git", "config", "--file", included, "core.hooksPath", hookPath)
	condition := "includeIf.onbranch:other.path"
	if tc.nonmatching {
		condition = "includeIf.onbranch:inactive.path"
	}
	if tc.gitdir {
		condition = "includeIf.gitdir:" + testutil.GitOutput(t, linked, "rev-parse", "--absolute-git-dir") + ".path"
	}
	runCommand(t, linked, "git", "config", "--worktree", condition, included)
}

func assertHookSnapshotRetention(t *testing.T, managed string, tc hookReferenceCase) {
	t.Helper()
	_, err := os.Stat(filepath.Join(managed, "pre-commit"))
	shouldRetain := tc.alias && !tc.nonmatching || tc.missing
	if shouldRetain && err != nil {
		t.Fatalf("referenced/ambiguous snapshot removed: %v", err)
	}
	if !shouldRetain && !os.IsNotExist(err) {
		t.Fatalf("unreferenced snapshot retained: %v", err)
	}
}

func TestHooksCleanupRetainsOnInspectionFailure(t *testing.T) {
	for _, failure := range []string{"missing", "malformed", "replaced"} {
		t.Run(failure, func(t *testing.T) {
			repo := newHookFixture(t)
			managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
			linked := filepath.Join(t.TempDir(), "missing")
			runCommand(t, repo, "git", "worktree", "add", "-b", "other", linked)
			runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
			if failure == "malformed" {
				config := filepath.Join(t.TempDir(), "bad.cfg")
				writeFile(t, config, "[broken\n")
				runCommand(t, linked, "git", "config", "--worktree", "include.path", config)
			} else if err := os.RemoveAll(linked); err != nil {
				t.Fatal(err)
			}
			if failure == "replaced" {
				runCommand(t, repo, "git", "init", linked)
			}
			runCommand(t, repo, "make", "hooks-uninstall")
			if _, err := os.Stat(filepath.Join(managed, "pre-commit")); err != nil {
				t.Fatalf("inspection failure removed snapshot: %v", err)
			}
		})
	}
}

func TestHooksCleanupForeignWindowsPathsAreAmbiguous(t *testing.T) {
	for _, reference := range []string{`C:/custom-hooks`, `C:\custom-hooks`, `C:custom-hooks`, `C:`, `\\server\share\hooks`, `//server/share/hooks`} {
		t.Run(reference, func(t *testing.T) {
			repo := newHookFixture(t)
			managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
			runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repo, "git", "config", "--worktree", "core.hooksPath", reference)
			runCommand(t, repo, "make", "hooks-uninstall")
			if _, err := os.Stat(filepath.Join(managed, "pre-commit")); err != nil {
				t.Fatalf("ambiguous Windows reference removed snapshot: %v", err)
			}
		})
	}
}
