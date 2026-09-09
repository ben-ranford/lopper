package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestToolchainInstallLinuxConfiguresSignedNodeSourceRepositories(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		packageManager string
		wantLog        []string
		wantRepository []string
	}{
		{
			name:           "apt",
			packageManager: "apt-get",
			wantLog: []string{
				"apt-get install -y golang-go zig shellcheck ruby python3 ca-certificates curl gnupg",
				"curl -fsSL --proto =https --tlsv1.2 https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key -o /usr/share/keyrings/nodesource-repo.gpg.key",
				"gpg --dearmor --yes --output /usr/share/keyrings/nodesource.gpg /usr/share/keyrings/nodesource-repo.gpg.key",
				"apt-get install -y nodejs",
			},
			wantRepository: []string{
				"signed-by=/usr/share/keyrings/nodesource.gpg",
				"https://deb.nodesource.com/node_24.x nodistro main",
			},
		},
		{
			name:           "dnf",
			packageManager: "dnf",
			wantLog: []string{
				"dnf install -y golang zig ShellCheck ruby python3",
				"dnf install -y nodejs",
			},
			wantRepository: []string{
				"baseurl=https://rpm.nodesource.com/pub_24.x/nodistro/nodejs/$basearch",
				"gpgcheck=1",
				"gpgkey=https://rpm.nodesource.com/gpgkey/ns-operations-public.key",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, repository, err := runToolchainInstallLinuxFixture(t, tc.packageManager, 0)
			if err != nil {
				t.Fatalf("toolchain installer failed: %v\n%s", err, output)
			}
			assertOutputContainsAll(t, output, tc.wantLog)
			assertOutputContainsAll(t, repository, tc.wantRepository)
		})
	}
}

func TestToolchainInstallLinuxFailsWhenInstalledNodeIsTooOld(t *testing.T) {
	t.Parallel()

	output, _, err := runToolchainInstallLinuxFixture(t, "apt-get", 1)
	if err == nil {
		t.Fatalf("expected incompatible Node.js to fail, got success:\n%s", output)
	}
	assertOutputContainsAll(t, output, []string{"NodeSource installation did not provide Node.js >=22.12.0."})
}

func runToolchainInstallLinuxFixture(t *testing.T, packageManager string, nodeExitCode int) (string, string, error) {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, packageManager+".log")
	writeToolchainInstallStub(t, binDir, "id", "printf '0\\n'\n")
	writeToolchainInstallStub(t, binDir, packageManager, "")
	writeToolchainInstallStub(t, binDir, "install", "")
	writeToolchainInstallStub(t, binDir, "curl", "")
	writeToolchainInstallStub(t, binDir, "gpg", "")
	writeToolchainInstallStub(t, binDir, "rm", "")
	writeToolchainInstallStub(t, binDir, "tee", "/bin/cat > \"${TOOLCHAIN_INSTALL_LOG}.repository\"\n")
	writeToolchainInstallStub(t, binDir, "node", "exit \"${TOOLCHAIN_NODE_EXIT}\"\n")

	cmd := exec.Command("make", "toolchain-install-linux")
	cmd.Dir = repoPath(t, ".")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir,
		"TOOLCHAIN_INSTALL_LOG="+logPath,
		"TOOLCHAIN_NODE_EXIT="+strconv.Itoa(nodeExitCode),
	)
	output, err := cmd.CombinedOutput()
	if data, readErr := os.ReadFile(logPath); readErr == nil {
		output = append(output, data...)
	}
	repository, readErr := os.ReadFile(logPath + ".repository")
	if readErr != nil {
		return string(output), "", readErr
	}
	return string(output), string(repository), err
}

func writeToolchainInstallStub(t *testing.T, binDir, name, body string) {
	t.Helper()

	path := filepath.Join(binDir, name)
	script := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$TOOLCHAIN_INSTALL_LOG\"\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
}
