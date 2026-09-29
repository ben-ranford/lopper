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

func TestDuplicationProtectedGateRejectsContributorNoOps(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/duplication-verify.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "verify", "Run protected duplication gate")
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".github", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Makefile":                     "GO ?= go\nDUPL_VERSION ?= trusted\nDUPLICATION_TOKEN_THRESHOLD ?= 55\nDUPLICATION_MAX ?= 3\n",
		"scripts/check_duplication.py": "import os, subprocess, sys\nassert subprocess.check_output(['git', 'show', os.environ['LOPPER_DUPLICATION_REVISION'] + ':contributor.txt'], text=True) == 'scan this revision'\nprint('protected-gate-rejected-clone', flush=True)\nsys.exit(23)\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitCommand(t, repo, "init")
	runGitCommand(t, repo, "config", "user.name", "Ben Ranford")
	runGitCommand(t, repo, "config", "user.email", "84072202+ben-ranford@users.noreply.github.com")
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "protected policy")
	base := strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "HEAD"))
	for name, content := range map[string]string{
		"Makefile":                                 "ci-checks dup-check:\n\t@true\n",
		"scripts/check_duplication.py":             "raise SystemExit(0)\n",
		"contributor.txt":                          "scan this revision",
		".github/workflows/ci.yml":                 "jobs: {verify: {steps: [{run: true}]}}",
		".github/workflows/duplication-verify.yml": "jobs: {verify: {steps: [{run: true}]}}",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "contributor bypass")
	head := strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "HEAD"))
	runGitCommand(t, repo, "update-ref", "refs/pull/7/head", head)
	runGitCommand(t, repo, "remote", "add", "origin", repo)
	runGitCommand(t, repo, "checkout", "--detach", base)
	command := exec.Command("bash", "-euo", "pipefail", "-c", step.Run)
	command.Dir = repo
	command.Env = overlayShellEnv(withoutGitEnv(), map[string]string{
		"DUPLICATION_EVENT_BASE": base, "DUPLICATION_EVENT_HEAD": head, "DUPLICATION_EVENT_NUMBER": "7", "RUNNER_TEMP": t.TempDir(),
	})
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "protected-gate-rejected-clone") {
		t.Fatalf("contributor no-op bypassed protected code: %v\n%s", err, output)
	}
	for _, event := range []struct{ head, number string }{
		{base, "7"},      // The PR ref advanced after the event was queued.
		{head, "7/head"}, // Ref syntax must never come from an unchecked number.
	} {
		command := exec.Command("bash", "-euo", "pipefail", "-c", step.Run)
		command.Dir = repo
		command.Env = overlayShellEnv(withoutGitEnv(), map[string]string{
			"DUPLICATION_EVENT_BASE": base, "DUPLICATION_EVENT_HEAD": event.head,
			"DUPLICATION_EVENT_NUMBER": event.number, "RUNNER_TEMP": t.TempDir(),
		})
		output, err := command.CombinedOutput()
		if err == nil || strings.Contains(string(output), "protected-gate-rejected-clone") {
			t.Fatalf("invalid event identity must fail before analysis: %v\n%s", err, output)
		}
	}
}

func TestDuplicationGateUsesBaseOwnedWorkflow(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/duplication-verify.yml", &workflow)
	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowStepOrder(t, verify, "Publish pending duplication check", "Fetch protected base", "Setup Go", "Run protected duplication gate", "Complete duplication check")
	fetch := workflowStepByName(t, workflow.Jobs, "verify", "Fetch protected base")
	if fetch.Uses != "" || fetch.Env["GH_TOKEN"] != "${{ github.token }}" {
		t.Fatal("protected base must be fetched without a checkout action and with a step-scoped token")
	}
	assertWorkflowStepRunContainsAll(t, fetch, "ephemeral protected-base fetch", []string{
		`git -c core.hooksPath=/dev/null init`, `DUPLICATION_EVENT_BASE`,
		`http.extraheader=AUTHORIZATION: basic`, `unset authorization GH_TOKEN`,
	})
	content, err := os.ReadFile(repoPath(t, ".github/workflows/duplication-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	if strings.Contains(source, "actions/checkout") {
		t.Fatal("privileged duplication workflow must not use actions/checkout")
	}
	for _, required := range []string{"  pull_request_target:", "name: 'duplication-verify'", "head_sha: context.payload.pull_request.head.sha", "cache: false"} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing trusted enforcement contract %q", required)
		}
	}
	for _, forbidden := range []string{"  pull_request:", "make ", "pull_request.head.ref", "pull_request.head.repo"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("unsafe enforcement contract %q", forbidden)
		}
	}
}
