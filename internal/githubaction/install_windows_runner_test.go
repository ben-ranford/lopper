package githubaction_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstallScriptWindowsReleaseRouting(t *testing.T) {
	for _, tc := range []struct{ os, arch, suffix string }{
		{"Windows_NT", "x86_64", "windows_amd64.zip"},
		{"Windows", "arm64", "windows_arm64.zip"},
		{"MINGW64_NT-10.0-26100", "AMD64", "windows_amd64.zip"},
		{"MSYS_NT-10.0-26100", "aarch64", "windows_arm64.zip"},
		{"Linux", "x86_64", "linux_amd64.tar.gz"},
		{"Darwin", "arm64", "darwin_arm64.tar.gz"},
	} {
		t.Run(tc.os, func(t *testing.T) {
			command := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "github-action", "install-lopper.sh"))
			command.Env = append(os.Environ(), "LOPPER_INSTALL_DRY_RUN=1", "LOPPER_VERSION=v1.8.2", "LOPPER_ACTION_OS="+tc.os, "LOPPER_ACTION_ARCH="+tc.arch, "GITHUB_OUTPUT=")
			output, err := command.CombinedOutput()
			want := "https://github.com/ben-ranford/lopper/releases/download/v1.8.2/lopper_v1.8.2_" + tc.suffix
			if err != nil || !strings.Contains(string(output), "download-url="+want+"\n") {
				t.Fatalf("installer routing failed for %s/%s: %v\n%s", tc.os, tc.arch, err, output)
			}
		})
	}
}

func TestInstallScriptWindowsArchiveRuns(t *testing.T) {
	binary := nativeActionFixture(t)
	for _, version := range []string{"v1.8.12", "v1.8"} {
		t.Run(version, func(t *testing.T) {
			fixture := windowsActionFixture(t)
			writeWindowsActionArchive(t, filepath.Join(fixture.dir, "archive"), binary, "lopper.exe")
			output, err := fixture.run(t, version, "", false, "")
			if err != nil || !strings.Contains(output, "Installed lopper fixture v1.8.12") {
				t.Fatalf("Windows ZIP installation failed on %s: %v\n%s", runtime.GOOS, err, output)
			}
			assertWindowsInstalledAction(t, fixture)
		})
	}
}

func TestActionFixtureIgnoresEnvironmentOutputPath(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fixture"+filepath.Ext(actionBinaryName(runtime.GOOS)))
	if err := os.WriteFile(binary, nativeActionFixture(t), 0o700); err != nil {
		t.Fatal(err)
	}
	workingDir := t.TempDir()
	outsideDir := t.TempDir()
	argsPath := filepath.Join(workingDir, "analysis-args.json")
	redirectedPath := filepath.Join(outsideDir, "analysis-args.json")
	command := exec.Command(binary, "analyse")
	command.Dir = workingDir
	command.Env = append(os.Environ(), "LOPPER_FIXTURE_ARGS="+redirectedPath)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "analysis-fixture-ok") {
		t.Fatalf("fixture analysis failed: %v\n%s", err, output)
	}
	if data, err := os.ReadFile(argsPath); err != nil || string(data) != `["analyse"]` {
		t.Fatalf("fixture did not create expected working-directory args: %v %s", err, data)
	}
	if _, err := os.Stat(redirectedPath); !os.IsNotExist(err) {
		t.Fatalf("fixture followed environment output path %q: %v", redirectedPath, err)
	}
}

func TestInstallScriptWindowsArchiveFailures(t *testing.T) {
	for _, kind := range []string{"missing executable", "duplicate executable", "missing runtime hooks", "corrupt ZIP"} {
		t.Run(kind, func(t *testing.T) {
			fixture := windowsActionFixture(t)
			archive := filepath.Join(fixture.dir, "archive")
			if kind == "missing executable" {
				writeWindowsActionArchive(t, archive, []byte("not an executable"), "other.exe")
			} else if kind == "duplicate executable" {
				member := "lopper_v1.8.12_windows_" + runtime.GOARCH + "/lopper.exe"
				writeActionZIP(t, archive, []actionZIPEntry{{member, []byte("one")}, {member, []byte("two")}})
			} else if kind == "missing runtime hooks" {
				writeActionZIP(t, archive, []actionZIPEntry{{"lopper_v1.8.12_windows_" + runtime.GOARCH + "/lopper.exe", nativeActionFixture(t)}})
			} else if err := os.WriteFile(archive, []byte("invalid zip"), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := fixture.run(t, "v1.8.12", "", false, "")
			if err == nil {
				t.Fatalf("invalid Windows archive installed: %s", output)
			}
			assertReleaseRunnerClean(t, fixture.runner)
			assertReleaseFileAbsent(t, fixture.pathFile)
		})
	}
}

// TestInstallScriptWindowsPublishedReleaseProof runs only when the trusted
// Python provider is present in the captured Go root. Ordinary Windows tests
// therefore remain offline while the provider-owned proof lane exercises real
// fixed and floating releases.
func TestInstallScriptWindowsPublishedReleaseProof(t *testing.T) {
	assertWindowsProofGoRootAvailable(t)
	for _, requested := range []string{"v1.8.9", "v1.8"} {
		t.Run(requested, func(t *testing.T) {
			verifyPublishedWindowsRelease(t, requested)
		})
	}
}

func assertWindowsProofGoRootAvailable(t *testing.T) {
	t.Helper()
	goRoot := os.Getenv("REGRESSION_PROOF_GO_ROOT")
	if runtime.GOOS != "windows" || goRoot == "" || os.Getenv("GOROOT") != goRoot {
		t.Skip("requires the captured native Windows proof Go root")
	}
	for _, name := range []string{"python.exe", "python3.exe", "lopper-proof-python.receipt"} {
		info, err := os.Lstat(filepath.Join(goRoot, "bin", name))
		if err != nil || !info.Mode().IsRegular() {
			t.Skip("requires the authenticated Python provider artifacts")
		}
	}
}

func verifyPublishedWindowsRelease(t *testing.T, requested string) {
	t.Helper()
	installDir, githubPath, outputPath := runPublishedWindowsInstaller(t, requested)
	outputs, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	binaryPath, _, installedVersion := assertPublishedWindowsOutputs(t, outputs, requested, installDir)
	versionOutput, err := exec.Command(binaryPath, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(versionOutput)) != installedVersion {
		t.Fatalf("installed executable identity differs from installer receipt: %v\n%s\nwant %q", err, versionOutput, installedVersion)
	}
	pathBytes, err := os.ReadFile(githubPath)
	if err != nil || !strings.EqualFold(strings.TrimSpace(string(pathBytes)), installDir) {
		t.Fatalf("installer PATH output does not name the spaced install directory: %v\n%s", err, pathBytes)
	}
}

func runPublishedWindowsInstaller(t *testing.T, requested string) (string, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "release proof with spaces")
	runnerTemp := filepath.Join(root, "runner temp")
	installDir := filepath.Join(root, "installed binary")
	if err := os.MkdirAll(runnerTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "github output")
	githubPath := filepath.Join(root, "github path")
	script := filepath.Join(repoRoot(t), "scripts", "github-action", "install-lopper.sh")
	command := exec.Command("bash", script)
	command.Env = replaceEnvironment(os.Environ(), map[string]string{
		"GITHUB_OUTPUT":      outputPath,
		"GITHUB_PATH":        githubPath,
		"LOPPER_INSTALL_DIR": installDir,
		"LOPPER_VERSION":     requested,
		"RUNNER_TEMP":        runnerTemp,
		"TMPDIR":             runnerTemp,
	})
	result, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install published Windows release %s: %v\n%s", requested, err, result)
	}
	return installDir, githubPath, outputPath
}

func assertPublishedWindowsOutputs(t *testing.T, outputs []byte, requested, installDir string) (string, string, string) {
	t.Helper()
	binaryPath := outputValue(string(outputs), "binary-path")
	resolvedVersion := outputValue(string(outputs), "resolved-version")
	installedVersion := outputValue(string(outputs), "lopper-version")
	if binaryPath == "" || resolvedVersion == "" || installedVersion == "" || !strings.Contains(binaryPath, "release proof with spaces") {
		t.Fatalf("installer did not report its spaced Windows path and version: %s", outputs)
	}
	wantBinary := filepath.Join(installDir, "lopper.exe")
	if !strings.EqualFold(filepath.Clean(binaryPath), filepath.Clean(wantBinary)) {
		t.Fatalf("reported binary path %q, want %q", binaryPath, wantBinary)
	}
	if requested == "v1.8.9" && resolvedVersion != "v1.8.9" {
		t.Fatalf("fixed release resolved unexpected tag %q", resolvedVersion)
	}
	if requested == "v1.8" && !strings.HasPrefix(resolvedVersion, "v1.8.") {
		t.Fatalf("floating release resolved outside the v1.8 series: %q", resolvedVersion)
	}
	if !strings.Contains(installedVersion, strings.TrimPrefix(resolvedVersion, "v")) {
		t.Fatalf("installed binary version %q does not match resolved tag %q", installedVersion, resolvedVersion)
	}
	return binaryPath, resolvedVersion, installedVersion
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := replacements[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

func outputValue(output, name string) string {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && key == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func TestInstallScriptRejectsUnsupportedPlatform(t *testing.T) {
	for _, tc := range []struct{ os, arch, want string }{
		{"FreeBSD", "amd64", "Unsupported runner OS"},
		{"Windows_NT", "386", "Unsupported runner architecture"},
	} {
		t.Run(tc.os, func(t *testing.T) {
			command := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "github-action", "install-lopper.sh"))
			command.Env = append(os.Environ(), "LOPPER_VERSION=v1.8.12", "LOPPER_INSTALL_DRY_RUN=1", "LOPPER_ACTION_OS="+tc.os, "LOPPER_ACTION_ARCH="+tc.arch)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.want) {
				t.Fatalf("expected %q, got %v\n%s", tc.want, err, output)
			}
		})
	}
}

func windowsActionFixture(t *testing.T) releaseResponseFixture {
	t.Helper()
	fixture := newReleaseResponseFixture(t, []byte(`[{"tag_name":"v1.8.12"}]`))
	unzip := strings.TrimSpace(runCommand(t, exec.Command("bash", "-c", "command -v unzip")))
	writeExecutable(t, filepath.Join(fixture.dir, "bin", "unzip"), `#!/usr/bin/env bash
set -euo pipefail
"$FIXTURE_UNZIP" "$@"
if [[ "$1" == -q ]]; then
  find "${@: -1}" -type f ! -path "$2" -print | sort > "$FIXTURE_EXTRACTED"
fi
`)
	fixture.env = append(fixture.env, "FIXTURE_UNZIP="+unzip, "FIXTURE_EXTRACTED="+filepath.Join(fixture.dir, "extracted"), "LOPPER_ACTION_OS=Windows_NT", "LOPPER_ACTION_ARCH="+runtime.GOARCH, "FIXTURE_OS=windows", "FIXTURE_ARCHIVE_EXTENSION=zip")
	if runtime.GOOS != "windows" {
		// This models path routing only. Native Windows uses real Git Bash cygpath.
		writeExecutable(t, filepath.Join(fixture.dir, "bin", "cygpath"), `#!/usr/bin/env bash
set -euo pipefail
[[ "$#" == 2 && ( "$1" == -u || "$1" == -w ) ]] || exit 91
printf '%s\n' "$1" >> "$FIXTURE_PATH_CALLS"
printf '%s\n' "$2"
`)
		fixture.env = append(fixture.env, "FIXTURE_PATH_CALLS="+filepath.Join(fixture.dir, "path-conversions"))
	}
	return fixture
}

func assertWindowsInstalledAction(t *testing.T, fixture releaseResponseFixture) {
	t.Helper()
	installDir := filepath.Join(fixture.dir, "installed binary")
	binary := filepath.Join(installDir, "lopper.exe")
	assertReleaseFileContains(t, fixture.output, "binary-path="+binary+"\n")
	assertReleaseFileContains(t, fixture.pathFile, installDir+"\n")
	assertReleaseRunnerClean(t, fixture.runner)
	extracted, err := os.ReadFile(filepath.Join(fixture.dir, "extracted"))
	root := "/lopper_v1.8.12_windows_" + runtime.GOARCH + "/"
	lines := strings.Split(strings.TrimSuffix(string(extracted), "\n"), "\n")
	if err != nil || len(lines) != 1+len(windowsActionRuntimeHooks) || !strings.HasSuffix(lines[0], root+"lopper.exe") {
		t.Fatalf("unexpected ZIP extraction layout: %v %s", err, extracted)
	}
	for index, hook := range windowsActionRuntimeHooks {
		if !strings.HasSuffix(lines[index+1], root+"share/lopper/scripts/runtime/"+hook) {
			t.Fatalf("unexpected ZIP extraction layout: %s", extracted)
		}
		assertReleaseFileContains(t, filepath.Join(installDir, "share", "lopper", "scripts", "runtime", hook), "hook "+hook)
	}
	assertReleaseFileAbsent(t, filepath.Join(fixture.dir, "outside"))
	command := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "github-action", "run-lopper.sh"))
	command.Dir = fixture.dir
	command.Env = append(os.Environ(), "LOPPER_BINARY="+binary, "INPUT_MODE=analyse", "INPUT_REPO=repository with spaces", "INPUT_OUTPUT=", "GITHUB_OUTPUT=")
	output := runCommand(t, command)
	if !strings.Contains(output, "analysis-fixture-ok") {
		t.Fatalf("installed executable did not run analysis: %s", output)
	}
	assertReleaseFileContains(t, filepath.Join(fixture.dir, "analysis-args.json"), `"analyse","--repo","repository with spaces"`)
	if runtime.GOOS != "windows" {
		assertReleaseFileContains(t, filepath.Join(fixture.dir, "path-conversions"), "-u\n-u\n-w\n-w\n")
	}
}

func archiveExtension(goos string) string {
	if goos == "windows" {
		return "zip"
	}
	return "tar.gz"
}

func actionBinaryName(goos string) string {
	if goos == "windows" {
		return "lopper.exe"
	}
	return "lopper"
}

func nativeActionFixture(t *testing.T) []byte {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fixture.exe")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-p=1", "-o", binary, filepath.Join(repoRoot(t), "internal", "githubaction", "testdata", "action-fixture", "main.go"))
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	runCommand(t, command)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeWindowsActionArchive(t *testing.T, path string, binary []byte, name string) {
	t.Helper()
	root := "lopper_v1.8.12_windows_" + runtime.GOARCH
	entries := []actionZIPEntry{{root + "/" + name, binary}}
	for _, hook := range windowsActionRuntimeHooks {
		entries = append(entries, actionZIPEntry{root + "/share/lopper/scripts/runtime/" + hook, []byte("hook " + hook)})
	}
	entries = append(entries, actionZIPEntry{root + "/share/man/man1/lopper.1", []byte("must not extract unrelated entries")}, actionZIPEntry{"../outside", []byte("must not extract unrelated entries")})
	writeActionZIP(t, path, entries)
}

var windowsActionRuntimeHooks = []string{"loader.mjs", "require-hook.cjs", "sitecustomize.py"}

type actionZIPEntry struct {
	name string
	data []byte
}

func writeActionZIP(t *testing.T, path string, entries []actionZIPEntry) {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(0o755)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallScriptWindowsPythonFallback(t *testing.T) {
	fixture := windowsActionFixture(t)
	python := strings.TrimSpace(runCommand(t, exec.Command("bash", "-c", "command -v python3 || command -v python")))
	writeExecutable(t, filepath.Join(fixture.dir, "bin", "python"), `#!/usr/bin/env bash
set -euo pipefail
printf 'fallback\n' > "$FIXTURE_PYTHON_CALL"
exec "$FIXTURE_PYTHON" "$@"
`)
	command := exec.Command("bash", "-c", `command() {
  if [[ "$1" == -v && "$2" == python3 ]]; then return 1; fi
  builtin command "$@"
}
export -f command
exec bash "$1"
`, "bash", filepath.Join(repoRoot(t), "scripts", "github-action", "install-lopper.sh"))
	marker := filepath.Join(fixture.dir, "python-call")
	command.Env = fixture.env
	command.Env = append(command.Env, "LOPPER_VERSION=v1.8", "LOPPER_INSTALL_DRY_RUN=1", "FIXTURE_PYTHON="+python, "FIXTURE_PYTHON_CALL="+marker)
	output := runCommand(t, command)
	if !strings.Contains(output, "resolved-version=v1.8.12\n") {
		t.Fatalf("Windows Python fallback did not resolve floating version: %s", output)
	}
	assertReleaseFileContains(t, marker, "fallback\n")
}

func TestInstallScriptWindowsRejectsPathOutputNewline(t *testing.T) {
	for _, separator := range []string{"\n", "\r"} {
		t.Run(fmt.Sprintf("U+%04X", separator[0]), func(t *testing.T) {
			fixture := windowsActionFixture(t)
			fixture.env = append(fixture.env, "LOPPER_INSTALL_DIR="+filepath.Join(fixture.dir, "install"+separator+"injected"))
			assertRawWindowsPathRejected(t, fixture)
		})
	}
}

func TestInstallScriptWindowsRejectsRawPathsBeforeTools(t *testing.T) {
	for _, variable := range []string{"LOPPER_INSTALL_DIR", "RUNNER_TEMP", "TMPDIR"} {
		t.Run(variable, func(t *testing.T) {
			for _, separator := range []string{"\n", "\r"} {
				t.Run(fmt.Sprintf("U+%04X", separator[0]), func(t *testing.T) {
					fixture := windowsActionFixture(t)
					marker := filepath.Join(fixture.dir, "forbidden-path-tool")
					for _, tool := range []string{"cygpath", "mkdir"} {
						writeExecutable(t, filepath.Join(fixture.dir, "bin", tool), "#!/usr/bin/env bash\nprintf called > \"$FIXTURE_PATH_TOOL_MARKER\"\nexit 91\n")
					}
					fixture.env = append(fixture.env, "FIXTURE_PATH_TOOL_MARKER="+marker, variable+"="+filepath.Join(fixture.dir, "raw"+separator))
					if variable == "TMPDIR" {
						fixture.env = append(fixture.env, "RUNNER_TEMP=")
					}
					assertRawWindowsPathRejected(t, fixture)
					assertReleaseFileAbsent(t, marker)
				})
			}
		})
	}
}

func assertRawWindowsPathRejected(t *testing.T, fixture releaseResponseFixture) {
	t.Helper()
	output, err := fixture.run(t, "v1.8.12", "", false, "")
	if err == nil || !strings.Contains(output, "must not contain line breaks") {
		t.Fatalf("unsafe raw Windows path was accepted: %v\n%s", err, output)
	}
	assertReleaseFileAbsent(t, fixture.pathFile)
	assertReleaseFileAbsent(t, fixture.output)
	assertReleaseRunnerClean(t, fixture.runner)
}

func TestInstallScriptWindowsPythonFailures(t *testing.T) {
	for _, kind := range []string{"missing", "Python 2"} {
		t.Run(kind, func(t *testing.T) {
			fixture := windowsActionFixture(t)
			python := strings.TrimSpace(runCommand(t, exec.Command("bash", "-c", "command -v python3 || command -v python")))
			writeExecutable(t, filepath.Join(fixture.dir, "bin", "python"), `#!/usr/bin/env bash
set -euo pipefail
exec "$FIXTURE_PYTHON" -c 'import sys; sys.version_info = (2, 7, 18); exec(sys.argv[1])' "$2"
`)
			command := exec.Command("bash", "-c", `command() {
  if [[ "$1" == -v && "$2" == python3 ]]; then return 1; fi
  if [[ "$1" == -v && "$2" == python && "$FIXTURE_PYTHON_FAILURE" == missing ]]; then return 1; fi
  builtin command "$@"
}
export -f command
exec bash "$1"
`, "bash", filepath.Join(repoRoot(t), "scripts", "github-action", "install-lopper.sh"))
			command.Env = fixture.env
			command.Env = append(command.Env, "LOPPER_VERSION=v1.8", "LOPPER_INSTALL_DRY_RUN=1", "FIXTURE_PYTHON="+python, "FIXTURE_PYTHON_FAILURE="+kind)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(strings.ReplaceAll(strings.ToLower(string(output)), " ", ""), "python3isrequired") {
				t.Fatalf("expected clear Python 3 failure: %v\n%s", err, output)
			}
			assertReleaseFileAbsent(t, fixture.output)
			assertReleaseFileAbsent(t, fixture.pathFile)
			assertReleaseRunnerClean(t, fixture.runner)
		})
	}
}
