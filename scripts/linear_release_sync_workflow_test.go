package scripts

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestLinearReleaseSyncWorkflowContract(t *testing.T) {
	workflow := readConfig(t, ".github/workflows/linear-release-sync.yml")
	required := []string{
		"issues:",
		"schedule:",
		"workflow_dispatch:",
		"- opened",
		"- edited",
		"- closed",
		"- reopened",
		"- labeled",
		"- unlabeled",
		"- milestoned",
		"- demilestoned",
		`cron: "17 3 * * *"`,
		"group: linear-release-sync",
		"cancel-in-progress: false",
		"permissions:\n  contents: read\n  issues: read",
		"actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd",
		"persist-credentials: false",
		"LINEAR_API_KEY: ${{ secrets.LINEAR_API_KEY }}",
		"python3 scripts/linear_release_sync.py",
		"--dry-run",
	}
	for _, fragment := range required {
		if !strings.Contains(workflow, fragment) {
			t.Fatalf("linear release sync workflow missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"pull_request_target:",
		"contents: write",
		"issues: write",
		"LINEAR_API_KEY: lin_",
		"actions/checkout@v",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("linear release sync workflow contains unsafe fragment %q", forbidden)
		}
	}
}

func TestLinearReleaseSyncConfigContract(t *testing.T) {
	var config struct {
		GitHub struct {
			Repository string `json:"repository"`
		} `json:"github"`
		Milestones map[string]struct {
			ReleaseLabelID     string `json:"release_label_id"`
			ProjectMilestoneID string `json:"project_milestone_id"`
		} `json:"milestones"`
	}
	if err := json.Unmarshal([]byte(readConfig(t, ".github/linear-release-sync.json")), &config); err != nil {
		t.Fatalf("parse linear release sync config: %v", err)
	}
	if config.GitHub.Repository != "ben-ranford/lopper" {
		t.Fatalf("unexpected sync repository %q", config.GitHub.Repository)
	}
	for _, title := range []string{"v2.0.0", "v3.0.0", "v4.0.0"} {
		mapping, ok := config.Milestones[title]
		if !ok {
			t.Fatalf("sync config missing milestone %q", title)
		}
		if mapping.ReleaseLabelID == "" || mapping.ProjectMilestoneID == "" {
			t.Fatalf("sync config milestone %q has incomplete Linear IDs", title)
		}
	}
}

func TestLinearReleaseSyncPythonSuite(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required to test Linear release synchronization")
	}
	command := exec.Command(python, "-B", "-m", "unittest", "scripts/linear_release_sync_test.py")
	command.Dir = repoPath(t, ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Linear release sync Python tests failed: %v\n%s", err, output)
	}
}
