package scripts

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomationYAMLParsing(t *testing.T) {
	t.Parallel()

	for _, script := range []struct {
		name string
		path string
	}{
		{"check-managed-output.sh", ".golangci.yml"},
		{"check-release-automation.sh", ".github/workflows/release.yml"},
	} {
		for _, input := range []struct {
			name    string
			content string
			error   string
		}{
			{"aliases", "defaults: &defaults {enabled: true}\ncopy: *defaults\n", ""},
			{"unsafe-tag", "--- !ruby/object:Object {}\n", "Psych::DisallowedClass"},
			{"malformed", "value: [unterminated\n", "Psych::SyntaxError"},
		} {
			t.Run(script.name+"/"+input.name, func(t *testing.T) {
				t.Parallel()
				repoDir := automationYAMLFixture(t)
				writeRepoScriptFixture(t, repoDir, "scripts/"+script.name)
				writeFixtureFile(t, repoDir, script.path, input.content)
				cmd := exec.Command(filepath.Join(repoDir, "scripts", script.name))
				cmd.Dir = repoDir
				output, err := cmd.CombinedOutput()
				if input.error == "" {
					if err != nil {
						t.Fatalf("valid YAML failed: %v\n%s", err, output)
					}
				} else if err == nil || !strings.Contains(string(output), input.error) {
					t.Fatalf("expected %s, got %v\n%s", input.error, err, output)
				}
			})
		}
	}
}

func automationYAMLFixture(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	for _, path := range []string{
		".golangci.yml", ".gostyle.yml", "action.yml",
		".github/workflows/release.yml", ".github/workflows/release-orchestration.yml",
		".github/workflows/release-source-ci.yml", ".github/workflows/rolling.yml",
		"internal/featureflags/features.json", "internal/featureflags/release_locks.json",
		"renovate.json", "release-please-config.json", ".release-please-manifest.json",
	} {
		writeFixtureFile(t, repoDir, path, "{}\n")
	}
	for _, path := range []string{
		"scripts/read_package_version.py", "scripts/vscode_release_notes.py", "scripts/runtime/sitecustomize.py",
		"scripts/queue_me_controller.js", "scripts/runtime/require-hook.cjs", "scripts/runtime/loader.mjs",
	} {
		writeFixtureFile(t, repoDir, path, "")
	}
	for _, path := range []string{
		"scripts/check-manpage.sh", "scripts/generate-manpage.sh", "scripts/release-image-tags.sh", ".githooks/pre-commit",
	} {
		writeFixtureFileMode(t, repoDir, path, "#!/bin/sh\nexit 0\n", 0o755)
	}
	return repoDir
}
