package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDuplicationRunnerFailsClosed(t *testing.T) {
	t.Parallel()
	command := exec.Command("python3", "-B", repoPath(t, "scripts/check_duplication_test.py"))
	// CI's checked SHA belongs to the caller, never to the fixture repositories.
	command.Env = append(os.Environ(), "LOPPER_DUPLICATION_REVISION="+strings.Repeat("0", 40))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("duplication runner regression fixtures failed: %v\n%s", err, output)
	}
}

func TestDuplicationMissingBaseFailsClosed(t *testing.T) {
	command := exec.Command("make", "dup-check", "DUPLICATION_BASE=refs/heads/lopper-nonexistent-duplication-fixture")
	command.Dir = repoPath(t, ".")
	command.Env = withoutGitEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Cannot compare requested base") || !strings.Contains(string(output), "No fallback comparison was used") {
		t.Fatalf("missing comparison base must fail with recovery instructions: %v\n%s", err, output)
	}
}

func TestDuplicationOccurrencePolicy(t *testing.T) {
	t.Parallel()
	command := exec.Command("python3", "-B", repoPath(t, "scripts/duplication_policy_test.py"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("duplication occurrence policy fixtures failed: %v\n%s", err, output)
	}
}

func TestDuplicationWorkflowCapturesExecutablesBeforeTooling(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	verify := workflowJobByName(t, workflow.Jobs, "verify-checks")
	assertWorkflowStepOrder(t, verify, "Setup Go", "Capture duplication executables", "Resolve gosec version", "Install Go tooling", "Run CI target")
	capture := workflowStepByName(t, workflow.Jobs, "verify-checks", "Capture duplication executables")
	if capture.ID != "duplication_executables" {
		t.Fatalf("unexpected executable capture ID: %q", capture.ID)
	}
	assertWorkflowStepRunContainsAll(t, capture, "trusted duplication executables", []string{
		`"$(command -v go)"`, `"$(command -v python3)"`, `"$(command -v git)"`, `"$(command -v make)"`, `"$GITHUB_OUTPUT"`,
	})
	assertWorkflowStepRunContainsAll(t, capture, "isolated tooling checkout", []string{
		`revision="$(git rev-parse --verify HEAD)"`, `git clone --no-local --no-checkout`, `checkout --detach "$revision"`,
	})
	for _, name := range []string{"Resolve gosec version", "Install Go tooling"} {
		step := workflowStepByName(t, workflow.Jobs, "verify-checks", name)
		if step.WorkingDirectory != "${{ steps.duplication_executables.outputs.tooling }}" {
			t.Fatalf("%s must run in the disposable tooling checkout", name)
		}
	}
}

func TestDuplicationCIShellIgnoresStartupInjection(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	runCI := workflowStepByName(t, workflow.Jobs, "verify-checks", "Run CI target")
	directory := t.TempDir()
	startup := filepath.Join(directory, "startup.sh")
	script := filepath.Join(directory, "step.sh")
	if err := os.WriteFile(startup, []byte("exit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo gate-reached\nexit 23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shell := runCI.Shell
	if shell == "" {
		shell = "/bin/bash --noprofile --norc -e -o pipefail {0}"
	}
	args := strings.Fields(shell)
	args[len(args)-1] = script
	command := exec.Command(args[0], args[1:]...)
	command.Env = overlayShellEnv(defaultShellEnv(), map[string]string{"BASH_ENV": startup, "ENV": startup})
	output, err := command.CombinedOutput()
	if err == nil || command.ProcessState.ExitCode() != 23 || !strings.Contains(string(output), "gate-reached") {
		t.Fatalf("CI shell skipped the gate via startup injection: %v\n%s", err, output)
	}
}

func TestDuplicationCIIgnoresMakeFlagsAndPathWrapper(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	runCI := workflowStepByName(t, workflow.Jobs, "verify-checks", "Run CI target")
	trustedMake, err := exec.LookPath("make")
	if err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{"flags", "executable"} {
		t.Run(attack, func(t *testing.T) {
			directory := t.TempDir()
			script := filepath.Join(directory, "step.sh")
			files := map[string]string{"step.sh": runCI.Run, "Makefile": "ci-checks:\n\t@touch gate-ran\n", "make": "#!/bin/sh\nexit 0\n"}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{"GH_EVENT_NAME": "workflow_dispatch", "ACT": "", "LOPPER_CI_MAKE": trustedMake}
			if attack == "flags" {
				env["MAKEFLAGS"], env["GNUMAKEFLAGS"], env["MFLAGS"] = "-n", "-n", "-n"
			} else {
				env["PATH"] = directory + string(os.PathListSeparator) + os.Getenv("PATH")
			}
			args := strings.Fields(runCI.Shell)
			args[len(args)-1] = script
			command := exec.Command(args[0], args[1:]...)
			command.Dir = directory
			command.Env = overlayShellEnv(defaultShellEnv(), env)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("CI step failed: %v\n%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(directory, "gate-ran")); err != nil {
				t.Fatalf("CI gate was bypassed by %s: %v", attack, err)
			}
		})
	}
}

func TestDuplicationAnalyzerDoesNotActivateStandalonePublisher(t *testing.T) {
	t.Parallel()
	// Protected publication belongs to the separately approved combined reuse
	// controller. Landing the analyzer must not activate another write job.
	if _, err := os.Lstat(repoPath(t, ".github/workflows/duplication-verify.yml")); !os.IsNotExist(err) {
		t.Fatalf("standalone duplication publisher must be absent: %v", err)
	}
}
