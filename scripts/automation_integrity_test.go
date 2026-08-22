package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAutomationIntegrityIsDirectCIPrequisite(t *testing.T) {
	t.Parallel()

	makefile := readRepoFile(t, "Makefile")
	if !regexp.MustCompile(`(?m)^ci:.*\bautomation-integrity\b`).MatchString(makefile) {
		t.Fatal("make ci must list automation-integrity as a direct prerequisite")
	}

	script := readRepoFile(t, "scripts/check-automation-integrity.sh")
	for _, required := range []string{
		"check-github-actions-pinning.sh",
		"check-github-actions-runners.rb",
		"check-automation-examples.sh",
		"check-release-automation.sh",
		"check-managed-output.sh",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("automation integrity script must run %s", required)
		}
	}
	if strings.Contains(script, "check-inline-suppressions") {
		t.Fatal("automation integrity must not duplicate the existing suppression gate")
	}
}

func TestGitHubActionsPinningRejectsMutableActionRef(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-github-actions-pinning.sh")
	writeFixtureFile(t, repoDir, ".github/workflows/ci.yml", `name: ci
on: [pull_request]
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v7
`)

	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-github-actions-pinning.sh"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected mutable action ref to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"GitHub Actions pinning check failed",
		"actions/checkout@v7",
		"pin external GitHub Actions to a 40-character commit SHA",
	})
}

func TestGitHubActionsPinningSupportsAnchoredYamlWorkflow(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-github-actions-pinning.sh")
	writeFixtureFile(t, repoDir, ".github/workflows/anchored.yaml", `name: anchored
on: [pull_request]
x-trusted-steps: &trusted_steps
  - name: Checkout
    uses: actions/checkout@0123456789abcdef0123456789abcdef01234567
jobs:
  verify:
    runs-on: ubuntu-latest
    steps: *trusted_steps
`)

	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-github-actions-pinning.sh"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected anchored .yaml workflow to pass, got %v:\n%s", err, output)
	}
	assertOutputContainsAll(t, string(output), []string{"GitHub Actions pinning check passed."})
}

func TestGitHubActionsRunnerCheckRejectsUnapprovedMatrixRunner(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-github-actions-runners.rb")
	writeFixtureFile(t, repoDir, ".github/workflows/ci.yml", `name: ci
on: [pull_request]
jobs:
  smoke:
    strategy:
      matrix:
        os:
          - ubuntu-latest
          - windows-latest
    runs-on: ${{ matrix.os }}
    steps:
      - run: make smoke
`)

	cmd := exec.Command("ruby", filepath.Join(repoDir, "scripts", "check-github-actions-runners.rb"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected unapproved runner to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"GitHub Actions runner allowlist check failed",
		"windows-latest",
		"allowed runners:",
	})
}

func TestGitHubActionsRunnerCheckSupportsAnchoredYamlWorkflow(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-github-actions-runners.rb")
	writeFixtureFile(t, repoDir, ".github/workflows/anchored.yaml", `name: anchored
on: [pull_request]
x-approved-runner: &approved_runner ubuntu-latest
jobs:
  smoke:
    runs-on: *approved_runner
    steps:
      - run: make smoke
`)

	cmd := exec.Command("ruby", filepath.Join(repoDir, "scripts", "check-github-actions-runners.rb"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected anchored .yaml workflow to pass, got %v:\n%s", err, output)
	}
	assertOutputContainsAll(t, string(output), []string{"GitHub Actions runner allowlist check passed."})
}

func TestAutomationExamplesRejectsFakeCommandSubstrings(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-automation-examples.sh")
	writeFixtureFile(t, repoDir, "examples/lefthook.yml", `# make automation-integrity
# go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
# git diff --exit-code -- . ':!.artifacts'
pre-commit:
  commands:
    automation-integrity:
      run: echo "make automation-integrity"
    lopper-json-report:
      run: echo "go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json"
    mutation-guard:
      run: echo "git diff --exit-code -- . ':!.artifacts'"
`)

	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-automation-examples.sh"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected fake command substrings to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"must preserve the automation example contract",
		"missing automation integrity command",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesAcceptsParsedLefthookCommands(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-automation-examples.sh")
	writeFixtureFile(t, repoDir, "examples/lefthook.yml", readRepoFile(t, "examples/lefthook.yml"))

	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-automation-examples.sh"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected parsed lefthook commands to pass, got %v:\n%s", err, output)
	}
	assertOutputContainsAll(t, string(output), []string{"Automation examples preserve JSON and mutation-guard contracts."})
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeRepoScriptFixture(t *testing.T, repoDir string, path string) {
	t.Helper()

	data := readRepoFile(t, path)
	writeFixtureFileMode(t, repoDir, path, data, 0o755)
}

func writeFixtureFile(t *testing.T, repoDir string, path string, content string) {
	t.Helper()
	writeFixtureFileMode(t, repoDir, path, content, 0o644)
}

func writeFixtureFileMode(t *testing.T, repoDir string, path string, content string, mode os.FileMode) {
	t.Helper()

	fullPath := filepath.Join(repoDir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertOutputContainsAll(t *testing.T, output string, wants []string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
