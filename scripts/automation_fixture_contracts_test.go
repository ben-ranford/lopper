package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomationFixtureShellInvocationWithSpaces(t *testing.T) {
	t.Parallel()

	for _, mode := range []os.FileMode{0o755, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			repoDir := filepath.Join(t.TempDir(), "repository with spaces")
			script := "scripts/check automation examples.sh"
			writeFixtureFileMode(t, repoDir, script, readRepoFile(t, "scripts/check-automation-examples.sh"), mode)
			writeFixtureFile(t, repoDir, "examples/lefthook.yml", readRepoFile(t, "examples/lefthook.yml"))
			cmd := automationExamplesFixtureCommand(filepath.Join(repoDir, script))
			if len(cmd.Args) != 2 || cmd.Args[0] != "sh" || cmd.Args[1] != filepath.Join(repoDir, script) {
				t.Fatalf("expected shell and one literal script argument, got %q", cmd.Args)
			}
			// The script must find its own root even from an unrelated directory.
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			assertAutomationFixtureStatus(t, output, err, 0)
			assertOutputContainsAll(t, string(output), []string{"Automation examples preserve JSON and mutation-guard contracts."})
		})
	}
}

func TestAutomationFixtureExecutablePermissions(t *testing.T) {
	t.Parallel()

	repoDir := filepath.Join(t.TempDir(), "repository with spaces")
	script := "scripts/check-github-actions-pinning.sh"
	writeRepoScriptFixture(t, repoDir, script)
	writeFixtureFile(t, repoDir, ".github/workflows/pinned.yaml", automationFixturePinnedWorkflow())
	path := filepath.Join(repoDir, script)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("repository script fixture must be executable, got %s", info.Mode())
	}
	cmd := exec.Command(path)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	assertAutomationFixtureStatus(t, output, err, 0)
	assertOutputContainsAll(t, string(output), []string{"GitHub Actions pinning check passed."})

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(path)
	cmd.Dir = repoDir
	output, err = cmd.CombinedOutput()
	if !errors.Is(err, os.ErrPermission) || cmd.ProcessState != nil || len(output) != 0 {
		t.Fatalf("non-executable direct invocation must fail before launch with permission error, got %v, state=%v, output=%q", err, cmd.ProcessState, output)
	}
	cmd = exec.Command("sh", path)
	cmd.Dir = repoDir
	output, err = cmd.CombinedOutput()
	assertAutomationFixtureStatus(t, output, err, 0)
	assertOutputContainsAll(t, string(output), []string{"GitHub Actions pinning check passed."})
}

func TestAutomationFixtureYAMLAndActionMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		path     string
		content  string
		status   int
		contains string
	}{
		{"workflow aliases", ".github/workflows/pinned.yaml", automationFixturePinnedWorkflow(), 0, "GitHub Actions pinning check passed."},
		{"malformed workflow", ".github/workflows/pinned.yaml", "jobs: [unterminated\n", 1, "Psych::SyntaxError"},
		{"unsafe workflow tag", ".github/workflows/pinned.yaml", "--- !ruby/object:Object {}\n", 1, "Psych::DisallowedClass"},
		{"mutable reusable workflow", ".github/workflows/pinned.yaml", "jobs:\n  reuse:\n    uses: owner/repo/.github/workflows/build.yml@main\n", 1, "external reusable workflows must be pinned"},
		{"pinned nested composite", ".github/actions/action with spaces/action.yaml", "runs:\n  using: composite\n  steps:\n    - uses: actions/cache@0123456789abcdef0123456789abcdef01234567\n    - uses: ./local-action\n", 0, "GitHub Actions pinning check passed."},
		{"mutable nested composite", ".github/actions/action with spaces/action.yaml", "runs:\n  using: composite\n  steps:\n    - uses: actions/cache@v4\n", 1, "composite step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repoDir := filepath.Join(t.TempDir(), "repository with spaces")
			writeRepoScriptFixture(t, repoDir, "scripts/check-github-actions-pinning.sh")
			writeFixtureFile(t, repoDir, ".github/workflows/pinned.yaml", automationFixturePinnedWorkflow())
			writeFixtureFile(t, repoDir, tc.path, tc.content)
			cmd := exec.Command(filepath.Join(repoDir, "scripts/check-github-actions-pinning.sh"))
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			assertAutomationFixtureStatus(t, output, err, tc.status)
			assertOutputContainsAll(t, string(output), []string{tc.contains})
			if tc.status != 0 && tc.name != "unsafe workflow tag" && !strings.Contains(string(output), tc.path) {
				t.Fatalf("rejection must identify metadata path %q:\n%s", tc.path, output)
			}
		})
	}
}

func automationFixturePinnedWorkflow() string {
	return `name: pinned
x-steps: &steps
  - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567
  - uses: ./local-action
jobs:
  check:
    steps: *steps
  reuse:
    uses: owner/repo/.github/workflows/build.yml@0123456789abcdef0123456789abcdef01234567
`
}

func assertAutomationFixtureStatus(t *testing.T, output []byte, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("expected exit status 0, got %v:\n%s", err, output)
		}
		return
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != want {
		t.Fatalf("expected exit status %d, got %v:\n%s", want, err, output)
	}
}
