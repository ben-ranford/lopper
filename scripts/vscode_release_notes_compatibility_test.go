package scripts

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestVSCodeReleaseNotesIncludeVSCodeCompatibilityRequirement(t *testing.T) {
	repo := t.TempDir()
	runGitCommand(t, repo, "init", "-q")
	runGitCommand(t, repo, "config", "user.name", "Test")
	runGitCommand(t, repo, "config", "user.email", "test@example.com")

	writeVSCodeReleaseFixture(t, repo, "1.0.0", "^1.90.0")
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n")
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "initial release")
	runGitCommand(t, repo, "tag", "v1.0.0")

	writeVSCodeReleaseFixture(t, repo, "1.0.1", "^1.101.0")
	runVSCodeReleaseNotes(t, repo)
	changelogPath := filepath.Join(repo, "extensions", "vscode-lopper", "CHANGELOG.md")
	first := readFile(t, changelogPath)
	const note = "Requires VS Code `^1.101.0` (previously `^1.90.0`)."
	if !strings.Contains(first, note) {
		t.Fatalf("generated release notes missing compatibility requirement %q:\n%s", note, first)
	}

	runVSCodeReleaseNotes(t, repo)
	if got := readFile(t, changelogPath); got != first {
		t.Fatalf("release-note refresh changed generated compatibility note:\n%s", got)
	}
}

func writeVSCodeReleaseFixture(t *testing.T, repo, version, vscodeRange string) {
	t.Helper()
	extension := filepath.Join(repo, "extensions", "vscode-lopper")
	packageJSON := `{"name":"vscode-lopper","version":"` + version + `","engines":{"vscode":"` + vscodeRange + `"}}`
	lockfileJSON := `{"name":"vscode-lopper","version":"` + version + `","packages":{"":{"name":"vscode-lopper","version":"` + version + `","engines":{"vscode":"` + vscodeRange + `"}}}}`
	writeFile(t, filepath.Join(extension, "package.json"), packageJSON)
	writeFile(t, filepath.Join(extension, "package-lock.json"), lockfileJSON)
	writeFile(t, filepath.Join(extension, "CHANGELOG.md"), "# Changelog\n\n## 1.0.0 (2026-01-01)\n\n- Previous release.\n")
}

func runVSCodeReleaseNotes(t *testing.T, repo string) {
	t.Helper()
	command := exec.Command("python3", repoPath(t, "scripts/vscode_release_notes.py"), "--repo", repo, "--previous-tag", "v1.0.0", "--date", "2026-02-02")
	command.Env = append(gitexec.SanitizedEnv(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generate VS Code release notes: %v\n%s", err, output)
	}
}
