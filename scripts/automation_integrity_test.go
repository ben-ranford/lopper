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

func TestGitHubActionsPinningRejectsMutableCompositeActionRef(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-github-actions-pinning.sh")
	writeFixtureFile(t, repoDir, ".github/workflows/ci.yml", `name: ci
on: [pull_request]
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567
`)
	writeFixtureFile(t, repoDir, "action.yml", `name: unsafe composite
runs:
  using: composite
  steps:
    - name: Mutable nested action
      uses: actions/cache@v4
`)

	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-github-actions-pinning.sh"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected mutable composite action ref to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"GitHub Actions pinning check failed",
		"action.yml",
		"actions/cache@v4",
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

	output, err := runAutomationExamplesFixture(t, `# make automation-integrity
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

func TestAutomationExamplesRejectsNonPreCommitContracts(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: make automation-integrity
pre-push:
  commands:
    lopper-json-report:
      run: go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts'
`)
	if err == nil {
		t.Fatalf("expected non-pre-commit contracts to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"must preserve the automation example contract",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesRejectsOrFallbackRequiredCommands(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: true || make automation-integrity
    lopper-json-report:
      run: true || go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
    mutation-guard:
      run: true || git diff --exit-code -- . ':!.artifacts'
`)
	if err == nil {
		t.Fatalf("expected OR-fallback contracts to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"missing automation integrity command",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesRejectsSkippedAndMaskedRequiredCommands(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: false && make automation-integrity || true
    lopper-json-report:
      run: mkdir -p .artifacts && false && go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json || true
    mutation-guard:
      run: false && git diff --exit-code -- . ':!.artifacts' || true
`)
	if err == nil {
		t.Fatalf("expected skipped and masked contracts to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"missing automation integrity command",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesRejectsShellTerminatingAndChain(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: exit 0 && make automation-integrity
    lopper-json-report:
      run: exec true && go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
    mutation-guard:
      run: exit 0 && git diff --exit-code -- . ':!.artifacts'
`)
	if err == nil {
		t.Fatalf("expected exit/exec-guarded contracts to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"missing automation integrity command",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesRejectsMaskedRequiredCommandFailures(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: make automation-integrity || true
    lopper-json-report:
      run: mkdir -p .artifacts && go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json || true
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts' || true
`)
	if err == nil {
		t.Fatalf("expected masked contract failures to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"missing automation integrity command",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesRejectsNonNoopFallbackMaskedRequiredCommands(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: test -f /missing && make automation-integrity || echo skipped
    lopper-json-report:
      run: test -f /missing && go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json || echo skipped
    mutation-guard:
      run: test -f /missing && git diff --exit-code -- . ':!.artifacts' || echo skipped
`)
	if err == nil {
		t.Fatalf("expected non-noop fallback masked contracts to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"missing automation integrity command",
		"missing lopper JSON report command",
		"missing mutation guard command",
	})
}

func TestAutomationExamplesRejectsLeadingEnvironmentAssignmentForRequiredCommand(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: MAKEFLAGS=-n make automation-integrity
    lopper-json-report:
      run: go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts'
`)
	if err == nil {
		t.Fatalf("expected env-assigned automation integrity command to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"must preserve the automation example contract",
		"missing automation integrity command",
	})
}

func TestAutomationExamplesRejectsLopperReportWithoutAnalyseTarget(t *testing.T) {
	t.Parallel()

	assertAutomationExamplesRejectLopperReportRuns(t, []lopperReportRunCase{
		{
			name: "missing dependency and top",
			run:  "mkdir -p .artifacts && go run ./cmd/lopper analyse --repo . --language all --format json --output .artifacts/lopper-pre-commit.json",
		},
		{
			name: "zero top",
			run:  "mkdir -p .artifacts && go run ./cmd/lopper analyse --top 0 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json",
		},
	}, "expected invalid lopper analyse target to fail")
}

func TestAutomationExamplesRejectsDuplicateLopperScalarFlags(t *testing.T) {
	t.Parallel()

	assertAutomationExamplesRejectLopperReportRuns(t, []lopperReportRunCase{
		{
			name: "format override",
			run:  "mkdir -p .artifacts && go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --format text --output .artifacts/lopper-pre-commit.json",
		},
		{
			name: "output override",
			run:  "mkdir -p .artifacts && go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json --output /tmp/lopper.json",
		},
		{
			name: "short output alias conflict",
			run:  "mkdir -p .artifacts && go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json -o /tmp/lopper.json",
		},
		{
			name: "equals syntax duplicate",
			run:  "mkdir -p .artifacts && go run ./cmd/lopper analyse --top 20 --repo . --language all --format=json --format=text --output .artifacts/lopper-pre-commit.json",
		},
	}, "expected duplicate lopper scalar flags to fail")
}

func TestAutomationExamplesRejectsLopperReportFlagsAfterArgumentTerminator(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: make automation-integrity
    lopper-json-report:
      run: go run ./cmd/lopper analyse -- --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts'
`)
	if err == nil {
		t.Fatalf("expected report flags after argument terminator to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"must preserve the automation example contract",
		"missing lopper JSON report command",
	})
}

func TestAutomationExamplesRejectsLopperReportOverridesAfterArgumentTerminator(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: make automation-integrity
    lopper-json-report:
      run: go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json -- --format text
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts'
`)
	if err == nil {
		t.Fatalf("expected report flags after argument terminator to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		"must preserve the automation example contract",
		"missing lopper JSON report command",
	})
}

func TestAutomationExamplesRejectsUnrecognizedLopperReportArguments(t *testing.T) {
	t.Parallel()

	assertAutomationExamplesRejectLopperReportRuns(t, []lopperReportRunCase{
		{
			name: "unknown flag",
			run:  "go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json --threshold-min-usage-percent 10",
		},
		{
			name: "positional argument",
			run:  "go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json extra.json",
		},
	}, "expected unrecognized lopper report arguments to fail")
}

func TestAutomationExamplesRejectsConditionallyDisabledLefthookCommands(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		option string
	}{
		{name: "skip", option: "skip: merge"},
		{name: "only", option: "only: rebase"},
		{name: "glob", option: `glob: "*.go"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: make automation-integrity
    lopper-json-report:
      `+tc.option+`
      run: go run ./cmd/lopper analyse --top 20 --repo . --language all --format json --output .artifacts/lopper-pre-commit.json
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts'
`)
			if err == nil {
				t.Fatalf("expected conditionally disabled lopper report command to fail, got success:\n%s", output)
			}
			assertOutputContainsAll(t, string(output), []string{
				"must preserve the automation example contract",
				"missing lopper JSON report command",
			})
		})
	}
}

func TestAutomationExamplesAcceptsParsedLefthookCommands(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixture(t, readRepoFile(t, "examples/lefthook.yml"))
	if err != nil {
		t.Fatalf("expected parsed lefthook commands to pass, got %v:\n%s", err, output)
	}
	assertOutputContainsAll(t, string(output), []string{"Automation examples preserve JSON and mutation-guard contracts."})
}

func TestMakefileToolchainIncludesPython3ForCI(t *testing.T) {
	t.Parallel()

	makefile := readRepoFile(t, "Makefile")
	assertContainsAll(t, makefile, []string{
		`@command -v python3 >/dev/null 2>&1 || (echo "python3 not found in PATH (required for Python-based CI checks)"; exit 1)`,
		`brew install go zig shellcheck ruby node python`,
		`$$SUDO apt-get install -y golang-go zig shellcheck ruby nodejs python3`,
		`$$SUDO dnf install -y golang zig ShellCheck ruby nodejs python3`,
		`$$SUDO pacman -Syu --noconfirm --needed go zig shellcheck ruby nodejs python`,
	})
}

func runAutomationExamplesFixture(t *testing.T, lefthookYAML string) (string, error) {
	t.Helper()

	repoDir := t.TempDir()
	writeRepoScriptFixture(t, repoDir, "scripts/check-automation-examples.sh")
	writeFixtureFile(t, repoDir, "examples/lefthook.yml", lefthookYAML)

	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-automation-examples.sh"))
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

type lopperReportRunCase struct {
	name string
	run  string
}

func assertAutomationExamplesRejectLopperReportRuns(t *testing.T, cases []lopperReportRunCase, failure string) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := runAutomationExamplesFixture(t, `pre-commit:
  commands:
    automation-integrity:
      run: make automation-integrity
    lopper-json-report:
      run: `+tc.run+`
    mutation-guard:
      run: git diff --exit-code -- . ':!.artifacts'
`)
			if err == nil {
				t.Fatalf("%s, got success:\n%s", failure, output)
			}
			assertOutputContainsAll(t, string(output), []string{
				"must preserve the automation example contract",
				"missing lopper JSON report command",
			})
		})
	}
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

	assertContainsAll(t, output, wants)
}

func assertContainsAll(t *testing.T, output string, wants []string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
