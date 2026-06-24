package githubaction_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type actionMetadata struct {
	Name    string                 `yaml:"name"`
	Inputs  map[string]actionInput `yaml:"inputs"`
	Outputs map[string]struct {
		Value string `yaml:"value"`
	} `yaml:"outputs"`
	Runs struct {
		Using string       `yaml:"using"`
		Steps []actionStep `yaml:"steps"`
	} `yaml:"runs"`
}

type actionInput struct {
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
}

type actionStep struct {
	ID    string            `yaml:"id"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`
	Run   string            `yaml:"run"`
}

func TestActionMetadataDefinesCompositeWrapper(t *testing.T) {
	root := repoRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Fatalf("read action metadata: %v", err)
	}

	var metadata actionMetadata
	if err := yaml.Unmarshal(content, &metadata); err != nil {
		t.Fatalf("parse action metadata: %v", err)
	}

	if metadata.Name != "Lopper" {
		t.Fatalf("unexpected action name %q", metadata.Name)
	}
	if metadata.Runs.Using != "composite" {
		t.Fatalf("expected composite action, got %q", metadata.Runs.Using)
	}

	requireInputDescriptions(t, metadata, []string{
		"version",
		"repo",
		"dependency",
		"top",
		"language",
		"scope-mode",
		"format",
		"output",
		"baseline",
		"baseline-store",
		"baseline-key",
		"baseline-label",
		"save-baseline",
		"threshold-fail-on-increase",
		"threshold-low-confidence-warning",
		"threshold-min-usage-percent",
		"threshold-max-uncertain-imports",
		"cache",
		"cache-readonly",
	})
	requireNoInput(t, metadata, "extra-args")
	requireInputDefault(t, metadata, "version", "action")
	requireInputDefault(t, metadata, "format", "table")
	requireInputDefault(t, metadata, "cache", "true")

	requireOutput(t, metadata, "report-path", "${{ steps.analyse.outputs.report-path }}")
	requireOutput(t, metadata, "lopper-version", "${{ steps.install.outputs.lopper-version }}")
	requireOutput(t, metadata, "resolved-version", "${{ steps.install.outputs.resolved-version }}")
	requireScriptStep(t, metadata, "install", "install-lopper.sh")
	requireScriptStep(t, metadata, "analyse", "run-lopper.sh")
}

func TestRunScriptBuildsPRCommentCommandSafely(t *testing.T) {
	outputFile := filepath.Join(t.TempDir(), "github-output")
	injectedFile := filepath.Join(t.TempDir(), "injected")

	env := []string{
		"GITHUB_OUTPUT=" + outputFile,
		"INPUT_REPO=repo; touch " + injectedFile,
		"INPUT_LANGUAGE=all",
		"INPUT_TOP=10",
		"INPUT_FORMAT=pr-comment",
		"INPUT_SCOPE_MODE=package",
		"INPUT_OUTPUT=.artifacts/lopper-pr-comment.md",
		"INPUT_BASELINE=.artifacts/lopper-base.json",
		"INPUT_THRESHOLD_FAIL_ON_INCREASE=0",
		"INPUT_CACHE=false",
		"INPUT_CACHE_READONLY=true",
	}
	got := runLopperScript(t, env)

	if _, err := os.Stat(injectedFile); !os.IsNotExist(err) {
		t.Fatalf("unexpected shell evaluation created %s", injectedFile)
	}

	want := []string{
		"analyse",
		"--repo", "repo; touch " + injectedFile,
		"--language", "all",
		"--format", "pr-comment",
		"--scope-mode", "package",
		"--cache=false",
		"--top", "10",
		"--output", ".artifacts/lopper-pr-comment.md",
		"--baseline", ".artifacts/lopper-base.json",
		"--runtime-profile", "node-import",
		"--cache-readonly",
		"--threshold-fail-on-increase", "0",
	}
	assertArgs(t, got, want)

	outputContent, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read github output: %v", err)
	}
	if got, want := string(outputContent), "report-path=.artifacts/lopper-pr-comment.md\n"; got != want {
		t.Fatalf("github output = %q, want %q", got, want)
	}
}

func TestRunScriptWritesMultilineReportPathOutputSafely(t *testing.T) {
	outputFile := filepath.Join(t.TempDir(), "github-output")
	reportPath := ".artifacts/lopper-pr-comment.md\ninjected=true"

	env := []string{
		"GITHUB_OUTPUT=" + outputFile,
		"INPUT_REPO=.",
		"INPUT_LANGUAGE=all",
		"INPUT_TOP=10",
		"INPUT_FORMAT=pr-comment",
		"INPUT_SCOPE_MODE=package",
		"INPUT_OUTPUT=" + reportPath,
		"INPUT_CACHE=false",
	}
	got := runLopperScript(t, env)

	want := []string{
		"analyse",
		"--repo", ".",
		"--language", "all",
		"--format", "pr-comment",
		"--scope-mode", "package",
		"--cache=false",
		"--top", "10",
		"--output", reportPath,
		"--runtime-profile", "node-import",
	}
	assertArgs(t, got, want)

	outputContent, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read github output: %v", err)
	}
	wantOutput := "report-path<<lopper_report-path_EOF\n.artifacts/lopper-pr-comment.md\ninjected=true\nlopper_report-path_EOF\n"
	if got := string(outputContent); got != wantOutput {
		t.Fatalf("github output = %q, want %q", got, wantOutput)
	}
}

func TestRunScriptBuildsSARIFCommand(t *testing.T) {
	env := []string{
		"INPUT_REPO=.",
		"INPUT_LANGUAGE=all",
		"INPUT_TOP=20",
		"INPUT_FORMAT=sarif",
		"INPUT_SCOPE_MODE=repo",
		"INPUT_OUTPUT=lopper.sarif",
		"INPUT_CACHE=true",
	}
	got := runLopperScript(t, env)

	want := []string{
		"analyse",
		"--repo", ".",
		"--language", "all",
		"--format", "sarif",
		"--scope-mode", "repo",
		"--cache=true",
		"--top", "20",
		"--output", "lopper.sarif",
		"--runtime-profile", "node-import",
	}
	assertArgs(t, got, want)
}

func TestRunScriptOmitsExplicitlyEmptyRuntimeProfile(t *testing.T) {
	env := []string{
		"INPUT_REPO=.",
		"INPUT_LANGUAGE=all",
		"INPUT_TOP=20",
		"INPUT_FORMAT=table",
		"INPUT_SCOPE_MODE=package",
		"INPUT_RUNTIME_PROFILE=",
		"INPUT_CACHE=true",
	}
	got := runLopperScript(t, env)

	want := []string{
		"analyse",
		"--repo", ".",
		"--language", "all",
		"--format", "table",
		"--scope-mode", "package",
		"--cache=true",
		"--top", "20",
	}
	assertArgs(t, got, want)
}

func TestRunScriptRejectsFlagLikeDependency(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "github-action", "run-lopper.sh"))
	cmd.Env = append(os.Environ(), []string{
		"LOPPER_BINARY=/bin/true",
		"INPUT_DEPENDENCY=--license-fail-on-deny",
		"INPUT_CACHE=true",
	}...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected flag-like dependency input to fail")
	}
	if !strings.Contains(stderr.String(), "dependency input must not start with '-'") {
		t.Fatalf("stderr missing dependency validation message: %s", stderr.String())
	}
}

func TestInstallScriptDryRunResolvesPinnedVersion(t *testing.T) {
	root := repoRoot(t)
	outputFile := filepath.Join(t.TempDir(), "github-output")

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "github-action", "install-lopper.sh"))
	cmd.Env = append(os.Environ(), []string{
		"LOPPER_INSTALL_DRY_RUN=1",
		"LOPPER_VERSION=1.6.1",
		"LOPPER_ACTION_OS=linux",
		"LOPPER_ACTION_ARCH=x86_64",
		"GITHUB_OUTPUT=" + outputFile,
	}...)
	stdout := runCommand(t, cmd)

	wantURL := "https://github.com/ben-ranford/lopper/releases/download/v1.6.1/lopper_v1.6.1_linux_amd64.tar.gz"
	if !strings.Contains(stdout, "resolved-version=v1.6.1") {
		t.Fatalf("dry-run stdout missing resolved version: %s", stdout)
	}
	if !strings.Contains(stdout, "download-url="+wantURL) {
		t.Fatalf("dry-run stdout missing download URL %q: %s", wantURL, stdout)
	}

	outputContent, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read github output: %v", err)
	}
	if !strings.Contains(string(outputContent), "resolved-version=v1.6.1\n") {
		t.Fatalf("github output missing resolved version: %s", outputContent)
	}
	if !strings.Contains(string(outputContent), "download-url="+wantURL+"\n") {
		t.Fatalf("github output missing download URL: %s", outputContent)
	}
}

func TestInstallScriptWritesMultilineGitHubOutputSafely(t *testing.T) {
	root := repoRoot(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create fake bin dir: %v", err)
	}
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/usr/bin/env bash
set -euo pipefail
output=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -o)
      shift
      output="${1:-}"
      ;;
  esac
  shift || true
done
if [[ -n "$output" ]]; then
  printf 'archive' > "$output"
fi
`)
	writeExecutable(t, filepath.Join(binDir, "tar"), `#!/usr/bin/env bash
set -euo pipefail
dest=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -C)
      shift
      dest="${1:-}"
      ;;
  esac
  shift || true
done
mkdir -p "$dest/extracted"
cat > "$dest/extracted/lopper" <<'LOPPER'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then
  printf 'lopper 1.6.1\ninjected=true'
  exit 0
fi
exit 1
LOPPER
chmod +x "$dest/extracted/lopper"
`)
	outputFile := filepath.Join(tempDir, "github-output")

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "github-action", "install-lopper.sh"))
	cmd.Env = append(os.Environ(), []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"RUNNER_TEMP=" + tempDir,
		"GITHUB_OUTPUT=" + outputFile,
		"LOPPER_VERSION=1.6.1",
		"LOPPER_ACTION_OS=linux",
		"LOPPER_ACTION_ARCH=amd64",
	}...)
	runCommand(t, cmd)

	outputContent, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read github output: %v", err)
	}
	want := "lopper-version<<lopper_lopper-version_EOF\nlopper 1.6.1\ninjected=true\nlopper_lopper-version_EOF\n"
	if !strings.Contains(string(outputContent), want) {
		t.Fatalf("github output missing multiline lopper-version: %s", outputContent)
	}
	if strings.Contains(string(outputContent), "lopper-version=lopper 1.6.1\ninjected=true\n") {
		t.Fatalf("github output used unsafe single-line format: %s", outputContent)
	}
}

func TestInstallScriptDryRunDefaultsToConcreteActionRef(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "github-action", "install-lopper.sh"))
	cmd.Env = append(os.Environ(), []string{
		"LOPPER_INSTALL_DRY_RUN=1",
		"LOPPER_VERSION=action",
		"LOPPER_ACTION_REF=v1.7.0",
		"LOPPER_ACTION_OS=linux",
		"LOPPER_ACTION_ARCH=arm64",
	}...)
	stdout := runCommand(t, cmd)

	wantURL := "https://github.com/ben-ranford/lopper/releases/download/v1.7.0/lopper_v1.7.0_linux_arm64.tar.gz"
	if !strings.Contains(stdout, "resolved-version=v1.7.0") {
		t.Fatalf("dry-run stdout missing action ref version: %s", stdout)
	}
	if !strings.Contains(stdout, "download-url="+wantURL) {
		t.Fatalf("dry-run stdout missing action ref download URL %q: %s", wantURL, stdout)
	}
}

func TestInstallScriptRejectsUnsafeVersion(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "github-action", "install-lopper.sh"))
	cmd.Env = append(os.Environ(), []string{
		"LOPPER_INSTALL_DRY_RUN=1",
		"LOPPER_VERSION=bad/version",
		"LOPPER_ACTION_OS=linux",
		"LOPPER_ACTION_ARCH=amd64",
	}...)

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected invalid version to fail")
	}
}

func TestActionScriptsAvoidEval(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join("scripts", "github-action", "install-lopper.sh"),
		filepath.Join("scripts", "github-action", "run-lopper.sh"),
	} {
		content, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if bytes.Contains(content, []byte("eval")) {
			t.Fatalf("%s contains eval", rel)
		}
	}
}

func requireInputDescriptions(t *testing.T, metadata actionMetadata, names []string) {
	t.Helper()
	for _, name := range names {
		input, ok := metadata.Inputs[name]
		if !ok {
			t.Fatalf("missing input %q", name)
		}
		if strings.TrimSpace(input.Description) == "" {
			t.Fatalf("input %q has empty description", name)
		}
	}
}

func requireNoInput(t *testing.T, metadata actionMetadata, name string) {
	t.Helper()
	if _, ok := metadata.Inputs[name]; ok {
		t.Fatalf("action should not expose raw %s shell passthrough", name)
	}
}

func requireInputDefault(t *testing.T, metadata actionMetadata, name, want string) {
	t.Helper()
	if got := metadata.Inputs[name].Default; got != want {
		t.Fatalf("%s default = %q, want %q", name, got, want)
	}
}

func requireOutput(t *testing.T, metadata actionMetadata, name, value string) {
	t.Helper()
	output, ok := metadata.Outputs[name]
	if !ok {
		t.Fatalf("missing output %q", name)
	}
	if output.Value != value {
		t.Fatalf("output %q value = %q, want %q", name, output.Value, value)
	}
}

func requireScriptStep(t *testing.T, metadata actionMetadata, id, scriptName string) {
	t.Helper()
	step := requireStep(t, metadata, id)
	if step.Shell != "bash" || !strings.Contains(step.Run, scriptName) {
		t.Fatalf("%s step does not call %s with bash: %#v", id, scriptName, step)
	}
}

func requireStep(t *testing.T, metadata actionMetadata, id string) actionStep {
	t.Helper()
	for _, step := range metadata.Runs.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("missing step %q", id)
	return actionStep{}
}

func writeStubLopper(t *testing.T, argsFile string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lopper-stub")
	content := "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s\\0' \"$@\" > \"$LOPPER_ARGS_FILE\"\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write stub lopper: %v", err)
	}
	t.Setenv("LOPPER_ARGS_FILE", argsFile)
	return path
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func runLopperScript(t *testing.T, env []string) []string {
	t.Helper()
	root := repoRoot(t)
	argsFile := filepath.Join(t.TempDir(), "args.bin")
	stub := writeStubLopper(t, argsFile)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "github-action", "run-lopper.sh"))
	cmd.Env = append(os.Environ(), "LOPPER_BINARY="+stub)
	cmd.Env = append(cmd.Env, env...)
	runCommand(t, cmd)
	return readArgsFile(t, argsFile)
}

func runCommand(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(cmd.Args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func readArgsFile(t *testing.T, argsFile string) []string {
	t.Helper()
	content, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	parts := bytes.Split(bytes.TrimSuffix(content, []byte{0}), []byte{0})
	got := make([]string, 0, len(parts))
	for _, part := range parts {
		got = append(got, string(part))
	}
	return got
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("args mismatch\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
