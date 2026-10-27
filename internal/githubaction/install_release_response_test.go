package githubaction_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type releaseResponseFixture struct {
	dir, runner, output, pathFile, response, calls string
	env                                            []string
}

func TestInstallScriptLargeReleaseResponse(t *testing.T) {
	payload := largeReleaseResponse(t)
	for _, tc := range []struct{ version, ref, want string }{
		{"v1.8", "", "v1.8.12"}, {"1.8", "", "v1.8.12"},
		{"v1", "", "v1.12.0"}, {"1", "", "v1.12.0"},
		{"action", "v1.8", "v1.8.12"}, {"action", "v1", "v1.12.0"},
	} {
		t.Run(tc.version+tc.ref, func(t *testing.T) {
			fixture := newReleaseResponseFixture(t, payload)
			output, err := fixture.run(t, tc.version, tc.ref, true, "")
			if err != nil {
				t.Fatalf("installer could not resolve %d-byte release response: %v\n%s", len(payload), err, output)
			}
			assertReleaseResolution(t, fixture, output, tc.want)
			assertReleaseRunnerClean(t, fixture.runner)
		})
	}
}

func largeReleaseResponse(t *testing.T) []byte {
	t.Helper()
	releases := []any{
		map[string]any{"tag_name": "v1.8.9"}, map[string]any{"tag_name": "v1.8.2"},
		map[string]any{"tag_name": "v1.99.0", "prerelease": true},
		map[string]any{"tag_name": "v1.98.0", "draft": true},
		map[string]any{"tag_name": "v1.97.0-beta.1"},
		map[string]any{"tag_name": "v2.99.0"}, "ignored non-object", nil,
	}
	for range 90 {
		releases = append(releases, map[string]any{"tag_name": "v2.0.0", "body": strings.Repeat("release notes ", 3000)})
	}
	releases = append(releases, map[string]any{"tag_name": "v1.8.12"}, map[string]any{"tag_name": "v1.12.0"})
	payload, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= 3*1024*1024 {
		t.Fatalf("fixture must exceed exec environment bounds: %d bytes", len(payload))
	}
	return payload
}

func TestInstallScriptReleaseResponseErrors(t *testing.T) {
	for _, tc := range []struct{ name, response, curlStatus, want string }{
		{"malformed", "[", "", "Unable to parse GitHub releases"},
		{"object", "{}", "", "Unable to parse GitHub releases"},
		{"empty body", "", "", "Unable to parse GitHub releases"},
		{"empty list", "[]", "", "Unable to resolve a stable Lopper release"},
		{"no match", `[{"tag_name":"v2.0.0"}]`, "", "Unable to resolve a stable Lopper release"},
		{"network failure", "[]", "22", "Unable to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newReleaseResponseFixture(t, []byte(tc.response))
			output, err := fixture.run(t, "v1.8", "", true, tc.curlStatus)
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("expected %q failure, got %v\n%s", tc.want, err, output)
			}
			assertReleaseRunnerClean(t, fixture.runner)
			assertReleaseFileAbsent(t, fixture.output)
			assertReleaseFileAbsent(t, fixture.pathFile)
		})
	}
}

func TestInstallScriptLargeResponseInstallsAndCleans(t *testing.T) {
	fixture := newReleaseResponseFixture(t, largeReleaseResponse(t))
	writeReleaseArchive(t, filepath.Join(fixture.dir, "archive.tar.gz"))
	output, err := fixture.run(t, "v1.8", "", false, "")
	if err != nil || !strings.Contains(output, "Installed lopper fixture v1.8.12") {
		t.Fatalf("install failed: %v\n%s", err, output)
	}
	installDir := filepath.Join(fixture.dir, "installed binary")
	assertReleaseFileContains(t, fixture.pathFile, installDir+"\n")
	assertReleaseFileContains(t, fixture.output, "binary-path="+filepath.Join(installDir, "lopper")+"\n")
	assertReleaseFileContains(t, fixture.calls, "asset\n")
	assertReleaseRunnerClean(t, fixture.runner)
}

func TestInstallScriptDownloadFailureCleans(t *testing.T) {
	fixture := newReleaseResponseFixture(t, []byte(`[{"tag_name":"v1.8.12"}]`))
	writeReleaseArchive(t, filepath.Join(fixture.dir, "archive.tar.gz"))
	output, err := fixture.run(t, "v1.8", "", false, "22")
	if err == nil {
		t.Fatalf("download failure unexpectedly installed: %s", output)
	}
	assertReleaseFileContains(t, fixture.calls, "asset\n")
	assertReleaseRunnerClean(t, fixture.runner)
	assertReleaseFileAbsent(t, fixture.pathFile)
}

func newReleaseResponseFixture(t *testing.T, payload []byte) releaseResponseFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := releaseResponseFixture{
		dir: dir, runner: filepath.Join(dir, "runner temp"), output: filepath.Join(dir, "output"),
		pathFile: filepath.Join(dir, "path"), response: filepath.Join(dir, "response.json"), calls: filepath.Join(dir, "calls"),
	}
	bin := filepath.Join(dir, "bin")
	for _, path := range []string{bin, fixture.runner} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fixture.response, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$#" == 2 && "$1" == -fsSL && "$2" == 'https://api.github.com/repos/ben-ranford/lopper/releases?per_page=100' ]]; then
  printf 'releases\n' >> "$FIXTURE_CALLS"
  cat "$FIXTURE_RESPONSE"
  exit "${FIXTURE_CURL_STATUS:-0}"
fi
if [[ "$#" == 4 && "$1" == -fsSL && "$2" == "https://github.com/ben-ranford/lopper/releases/download/v1.8.12/lopper_v1.8.12_${FIXTURE_OS}_${FIXTURE_ARCH}.tar.gz" && "$3" == -o ]]; then
  printf 'asset\n' >> "$FIXTURE_CALLS"
  cp "$FIXTURE_ARCHIVE" "$4"
  exit "${FIXTURE_ASSET_STATUS:-0}"
fi
printf 'unexpected HTTP request: %s\n' "$*" >&2
exit 90
`)
	fixture.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+fixture.runner,
		"TMPDIR="+fixture.runner, "GITHUB_OUTPUT="+fixture.output, "GITHUB_PATH="+fixture.pathFile,
		"LOPPER_INSTALL_DIR="+filepath.Join(dir, "installed binary"), "LOPPER_GITHUB_TOKEN=",
		"LOPPER_ACTION_OS=", "LOPPER_ACTION_ARCH=", "FIXTURE_RESPONSE="+fixture.response,
		"FIXTURE_CALLS="+fixture.calls, "FIXTURE_ARCHIVE="+filepath.Join(dir, "archive.tar.gz"),
		"FIXTURE_OS="+runtime.GOOS, "FIXTURE_ARCH="+runtime.GOARCH)
	return fixture
}

func (f *releaseResponseFixture) run(t *testing.T, version, ref string, dryRun bool, curlStatus string) (string, error) {
	t.Helper()
	before, present := os.LookupEnv("RELEASES_JSON")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot(t), "scripts", "github-action", "install-lopper.sh"))
	command.WaitDelay = time.Second
	command.Env = append(append([]string(nil), f.env...), "FIXTURE_CURL_STATUS=", "FIXTURE_ASSET_STATUS=", "LOPPER_VERSION="+version, "LOPPER_ACTION_REF="+ref, "LOPPER_INSTALL_DRY_RUN="+map[bool]string{true: "1", false: "0"}[dryRun])
	if dryRun {
		command.Env = append(command.Env, "FIXTURE_CURL_STATUS="+curlStatus)
	} else {
		command.Env = append(command.Env, "FIXTURE_ASSET_STATUS="+curlStatus)
	}
	output, err := command.CombinedOutput()
	after, afterPresent := os.LookupEnv("RELEASES_JSON")
	if before != after || present != afterPresent {
		t.Fatal("installer mutated parent release environment")
	}
	return string(output), err
}

func assertReleaseResolution(t *testing.T, fixture releaseResponseFixture, output, version string) {
	t.Helper()
	url := fmt.Sprintf("https://github.com/ben-ranford/lopper/releases/download/%s/lopper_%s_%s_%s.tar.gz", version, version, runtime.GOOS, runtime.GOARCH)
	for _, expected := range []string{"resolved-version=" + version + "\n", "download-url=" + url + "\n"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in %s", expected, output)
		}
		assertReleaseFileContains(t, fixture.output, expected)
	}
	assertReleaseFileContains(t, fixture.calls, "releases\n")
	assertReleaseFileAbsent(t, fixture.pathFile)
}

func assertReleaseRunnerClean(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("installer leaked temporary resources: %v %v", entries, err)
	}
}

func assertReleaseFileAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected installer output %s: %v", path, err)
	}
}

func assertReleaseFileContains(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), expected) {
		t.Fatalf("file %s missing %q: %v\n%s", path, expected, err, content)
	}
}

func writeReleaseArchive(t *testing.T, path string) {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	binary := "#!/usr/bin/env bash\n[[ \"$1\" == --version ]] || exit 1\nprintf 'lopper fixture v1.8.12\\n'\n"
	if err := archive.WriteHeader(&tar.Header{Name: "release/lopper", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(binary)); err != nil {
		t.Fatal(err)
	}
	for _, closeWriter := range []func() error{archive.Close, compressed.Close} {
		if err := closeWriter(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
