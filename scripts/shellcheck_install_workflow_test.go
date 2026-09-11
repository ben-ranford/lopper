package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type shellcheckInstallWorkflowCase struct {
	path    string
	jobName string
}

type shellcheckInstallMocks struct {
	binPath          string
	installedBinPath string
	logPath          string
}

func TestLinuxWorkflowShellcheckInstall(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name                 string
		preinstalled         bool
		failSudo, failUpdate bool
		wantFailure          bool
		wantLog              string
	}{
		{
			name:         "reuses preinstalled binary when sudo is unavailable",
			preinstalled: true,
			failSudo:     true,
			wantLog:      "shellcheck --version\n",
		},
		{
			name:    "installs when missing",
			wantLog: "sudo apt-get update\napt-get update\nsudo apt-get install -y shellcheck\napt-get install -y shellcheck\nshellcheck --version\n",
		},
		{
			name:        "propagates apt update failures",
			failUpdate:  true,
			wantFailure: true,
			wantLog:     "sudo apt-get update\napt-get update\n",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			for _, workflowCase := range linuxWorkflowShellcheckInstallCases() {
				t.Run(workflowCase.path+"/"+workflowCase.jobName, func(t *testing.T) {
					t.Parallel()

					step := linuxWorkflowShellcheckInstallStep(t, workflowCase)
					mocks := newShellcheckInstallMocks(t, scenario.preinstalled)
					output, err := runShellcheckInstallStep(step.Run, mocks, scenario.failSudo, scenario.failUpdate)
					if (err != nil) != scenario.wantFailure {
						t.Fatalf("shellcheck step failure = %t, want %t: %v\n%s", err != nil, scenario.wantFailure, err, output)
					}
					if got := readShellcheckInstallLog(t, mocks.logPath); got != scenario.wantLog {
						t.Fatalf("shellcheck commands = %q, want %q", got, scenario.wantLog)
					}
				})
			}
		})
	}
}

func linuxWorkflowShellcheckInstallCases() []shellcheckInstallWorkflowCase {
	return []shellcheckInstallWorkflowCase{
		{path: ".github/workflows/ci.yml", jobName: "verify-checks"},
		{path: ".github/workflows/ci.yml", jobName: "verify-rolling-checks"},
		{path: ".github/workflows/release-source-ci.yml", jobName: "verify-source-ci"},
		{path: ".github/workflows/release-orchestration.yml", jobName: "build-linux-windows"},
	}
}

func linuxWorkflowShellcheckInstallStep(t *testing.T, tc shellcheckInstallWorkflowCase) workflowStepConfig {
	t.Helper()

	var workflow workflowConfig
	readYAMLConfig(t, tc.path, &workflow)
	return workflowStepByName(t, workflow.Jobs, tc.jobName, "Install shellcheck")
}

func newShellcheckInstallMocks(t *testing.T, preinstalled bool) shellcheckInstallMocks {
	t.Helper()

	mockBin := t.TempDir()
	mocks := shellcheckInstallMocks{
		binPath:          mockBin,
		installedBinPath: filepath.Join(mockBin, ".shellcheck-after-install"),
		logPath:          filepath.Join(t.TempDir(), "commands.log"),
	}
	if err := os.WriteFile(mocks.installedBinPath, []byte("#!/bin/sh\nprintf 'shellcheck --version\\n' >> \"$SHELLCHECK_INSTALL_LOG\"\nprintf 'ShellCheck - shell script analysis tool\\n'\n"), 0o755); err != nil {
		t.Fatalf("write installed shellcheck mock: %v", err)
	}
	writeShellcheckInstallMock(t, mockBin, "sudo", `
printf 'sudo %s\n' "$*" >> "$SHELLCHECK_INSTALL_LOG"
if [ "${SHELLCHECK_SUDO_FAIL:-}" = 1 ]; then
  exit 99
fi
exec "$@"
`)
	writeShellcheckInstallMock(t, mockBin, "apt-get", `
printf 'apt-get %s\n' "$*" >> "$SHELLCHECK_INSTALL_LOG"
if [ "$1" = update ] && [ "${SHELLCHECK_FAIL_UPDATE:-}" = 1 ]; then
  exit 42
fi
if [ "$1" = install ]; then
  "$SHELLCHECK_COPY" "$SHELLCHECK_INSTALLED_BINARY" "$SHELLCHECK_MOCK_BIN/shellcheck"
  "$SHELLCHECK_CHMOD" +x "$SHELLCHECK_MOCK_BIN/shellcheck"
fi
`)
	if preinstalled {
		writeShellcheckInstallMock(t, mockBin, "shellcheck", `
printf 'shellcheck --version\n' >> "$SHELLCHECK_INSTALL_LOG"
printf 'ShellCheck - shell script analysis tool\n'
`)
	}
	return mocks
}

func writeShellcheckInstallMock(t *testing.T, dir, name, script string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0o755); err != nil {
		t.Fatalf("write mock %s: %v", name, err)
	}
}

func runShellcheckInstallStep(script string, mocks shellcheckInstallMocks, failSudo, failUpdate bool) (string, error) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		return "", err
	}
	copyPath, err := exec.LookPath("cp")
	if err != nil {
		return "", err
	}
	chmodPath, err := exec.LookPath("chmod")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bashPath, "-e", "-c", script)
	cmd.Env = []string{
		"PATH=" + mocks.binPath,
		"SHELLCHECK_INSTALL_LOG=" + mocks.logPath,
		"SHELLCHECK_MOCK_BIN=" + mocks.binPath,
		"SHELLCHECK_INSTALLED_BINARY=" + mocks.installedBinPath,
		"SHELLCHECK_COPY=" + copyPath,
		"SHELLCHECK_CHMOD=" + chmodPath,
	}
	if failSudo {
		cmd.Env = append(cmd.Env, "SHELLCHECK_SUDO_FAIL=1")
	}
	if failUpdate {
		cmd.Env = append(cmd.Env, "SHELLCHECK_FAIL_UPDATE=1")
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func readShellcheckInstallLog(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}
