package scripts

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestReleaseWorkflowCommitsGoBuildRequirement(t *testing.T) {
	repo := t.TempDir()
	runGitCommand(t, repo, "init", "-q")
	runGitCommand(t, repo, "config", "user.name", "Test")
	runGitCommand(t, repo, "config", "user.email", "test@example.com")
	writeVSCodeReleaseFixture(t, repo, "1.0.0", "^1.90.0")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/fixture\n\ngo 1.27.0\n")
	const history = "## [1.0.0](https://example.com/v1.0.0) (2026-01-01)\n\n* Previous release.\n"
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n\n"+history)
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-qm", "initial release")
	runGitCommand(t, repo, "tag", "v1.0.0")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/fixture\n\ngo 1.27.1\n")
	runGitCommand(t, repo, "add", "go.mod")
	runGitCommand(t, repo, "commit", "-qm", "chore(deps): update toolchain")
	writeVSCodeReleaseFixture(t, repo, "1.0.1", "^1.90.0")
	writeFile(t, filepath.Join(repo, ".release-please-manifest.json"), `{".":"1.0.1"}`)
	writeFile(t, filepath.Join(repo, "CHANGELOG.md"), "# Changelog\n\n## [1.0.1](https://example.com/v1.0.1) (2026-02-02)\n\n### Bug Fixes\n\n* Release changes.\n\n### Code Refactoring\n\n* Internal changes.\n\n"+history)
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-qm", "chore(main): release 1.0.1")
	copyTree(t, repoPath(t, "scripts"), filepath.Join(repo, ".trusted-release-notes-tooling", "scripts"))

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	var refresh workflowStepConfig
	for _, step := range workflow.Jobs["prepare-release"].Steps {
		if step.ID == "refresh_release_notes" {
			refresh = step
		}
	}
	if refresh.Run == "" {
		t.Fatal("release workflow is missing its refresh step")
	}
	runRefresh := func() {
		command := exec.Command("bash", "-euo", "pipefail", "-c", refresh.Run)
		command.Dir = repo
		command.Env = append(gitexec.SanitizedEnv(), "PYTHONDONTWRITEBYTECODE=1", "GITHUB_WORKSPACE="+repo,
			"GITHUB_REF_NAME=main", "GITHUB_OUTPUT="+filepath.Join(t.TempDir(), "output"),
			`RELEASE_PLEASE_PRS=[{"headBranchName":"release-please--branches--main"}]`)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("refresh release notes: %v\n%s", err, output)
		}
	}
	runRefresh()
	committed := runGitCommand(t, repo, "show", "HEAD:CHANGELOG.md")
	const note = "Source builds require Go `1.27.1` or newer (previously `1.27.0`)."
	if !strings.Contains(committed, note) || !strings.HasSuffix(committed, history) {
		t.Fatalf("committed notes must include the Go requirement and preserve history:\n%s", committed)
	}
	if strings.Index(committed, note) > strings.Index(committed, "### Bug Fixes") {
		t.Fatal("build requirement notice must appear before changelog categories")
	}
	head := runGitCommand(t, repo, "rev-parse", "HEAD")
	runRefresh()
	if runGitCommand(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("repeated release preparation created another commit")
	}
}
