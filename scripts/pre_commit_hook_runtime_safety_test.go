//go:build !windows

package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestManagedHookRejectsCheckoutControlledPathTools(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name, tool, pathPrefix string
	}{
		{name: "relative gofmt", tool: "gofmt", pathPrefix: "."},
		{name: "absolute checkout sh", tool: "sh"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			markerPath := filepath.Join(repoDir, "checkout-tool-ran")
			toolBody := "#!/bin/sh\ntouch " + markerPath + "\n"
			if testCase.tool == "gofmt" {
				toolBody += "exit 0\n"
			} else {
				toolBody += "exec /bin/sh \"$@\"\n"
			}
			writeFileMode(t, filepath.Join(repoDir, testCase.tool), toolBody, 0o755)
			writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
			runCommand(t, repoDir, "git", "add", "unformatted.go")
			pathPrefix := testCase.pathPrefix
			if pathPrefix == "" {
				pathPrefix = repoDir
			}

			command := exec.Command(managedHookPath(t, repoDir))
			command.Dir = repoDir
			command.Env = append(hookTestEnv(), "PATH="+pathPrefix+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
				t.Fatalf("hook with checkout %s = %v\n%s", testCase.tool, err, output)
			}
			if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
				t.Fatalf("checkout-controlled %s ran: %v", testCase.tool, err)
			}
		})
	}
}

func TestManagedHookRejectsUnsafePathEntries(t *testing.T) {
	t.Parallel()

	for _, pathValue := range []string{"", ".", "checkout-tools", "tools"} {
		t.Run(fmt.Sprintf("PATH=%q", pathValue), func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			markerPath := filepath.Join(repoDir, "checkout-git-ran")
			toolDirectory := repoDir
			if pathValue == "checkout-tools" || pathValue == "tools" {
				toolDirectory = filepath.Join(repoDir, pathValue)
			}
			if err := os.MkdirAll(toolDirectory, 0o755); err != nil {
				t.Fatalf("create checkout tool directory: %v", err)
			}
			writeFileMode(t, filepath.Join(toolDirectory, "git"), "#!/bin/sh\ntouch "+markerPath+"\nexit 1\n", 0o755)

			command := exec.Command(managedHookPath(t, repoDir))
			command.Dir = repoDir
			command.Env = append(hookTestEnv(), "PATH="+pathValue)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("hook with PATH=%q = %v\n%s", pathValue, err, output)
			}
			if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
				t.Fatalf("checkout-controlled git ran with PATH=%q: %v", pathValue, err)
			}
		})
	}
}

func TestManagedHookCleansFirstChildTemporaryFileWhenSecondAllocationFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")
	mktempPath, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("find mktemp: %v", err)
	}
	tempDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "mktemp-count")
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mktemp"), fmt.Sprintf(`#!/bin/sh
count=$(cat %q 2>/dev/null || echo 0)
count=$((count + 1))
echo "$count" > %q
if [ "$count" -eq 3 ]; then echo "forced second child mktemp failure" >&2; exit 73; fi
exec %q "$@"
`, statePath, statePath, mktempPath), 0o755)

	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+tempDir)
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "forced second child mktemp failure") {
		t.Fatalf("hook with second child mktemp failure = %v\n%s", err, output)
	}
	if leftovers, err := filepath.Glob(filepath.Join(tempDir, "lopper-pre-commit.*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files after second child mktemp failure = %#v err=%v", leftovers, err)
	}
}

func TestManagedHookRejectsChildOutputProcessingFailures(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"awk", "cat"} {
		t.Run(tool, func(t *testing.T) { assertManagedHookRejectsChildOutputProcessingFailure(t, tool) })
	}
}

func assertManagedHookRejectsChildOutputProcessingFailure(t *testing.T, tool string) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, tool), "#!/bin/sh\necho forced "+tool+" failure >&2\nexit 73\n", 0o755)
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "forced "+tool+" failure") {
		t.Fatalf("hook with failed child %s = %v\n%s", tool, err, output)
	}
}

func TestManagedHookSignalsExitNonzeroAndCleanTemporaryFiles(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name, tool string
		code       int
	}{
		{name: "top HUP", tool: "git", code: int(syscall.SIGHUP)},
		{name: "top INT", tool: "git", code: int(syscall.SIGINT)},
		{name: "top TERM", tool: "git", code: int(syscall.SIGTERM)},
		{name: "child HUP", tool: "gofmt", code: int(syscall.SIGHUP)},
		{name: "child INT", tool: "gofmt", code: int(syscall.SIGINT)},
		{name: "child TERM", tool: "gofmt", code: int(syscall.SIGTERM)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			writeFile(t, filepath.Join(repoDir, "formatted.go"), "package fixture\n\nfunc formatted() {}\n")
			runCommand(t, repoDir, "git", "add", "formatted.go")
			toolPath, err := exec.LookPath(testCase.tool)
			if err != nil {
				t.Fatalf("find %s: %v", testCase.tool, err)
			}
			wrapperDir := t.TempDir()
			wrapperBody := fmt.Sprintf("#!/bin/sh\nkill -%d \"$PPID\"\nexec %q \"$@\"\n", testCase.code, toolPath)
			if testCase.tool == "git" {
				wrapperBody = fmt.Sprintf("#!/bin/sh\nfor arg do\n\tif [ \"$arg\" = \"--name-only\" ]; then kill -%d \"$PPID\"; exit 0; fi\ndone\nexec %q \"$@\"\n", testCase.code, toolPath)
			}
			writeFileMode(t, filepath.Join(wrapperDir, testCase.tool), wrapperBody, 0o755)
			tempDir := t.TempDir()

			command := exec.Command(managedHookPath(t, repoDir))
			command.Dir = repoDir
			command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+tempDir)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("hook interrupted with %s succeeded:\n%s", testCase.name, output)
			}
			leftovers, globErr := filepath.Glob(filepath.Join(tempDir, "lopper-pre-commit.*"))
			if globErr != nil || len(leftovers) != 0 {
				t.Fatalf("temporary files after %s = %#v err=%v", testCase.name, leftovers, globErr)
			}
		})
	}
}
