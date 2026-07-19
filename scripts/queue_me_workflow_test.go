package scripts

import (
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQueueMeWorkflowContract(t *testing.T) {
	workflowText := readConfig(t, ".github/workflows/queue-me.yml")
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(workflowText), &workflow); err != nil {
		t.Fatalf("parse queue-me workflow: %v", err)
	}

	required := []string{
		"pull_request_target:",
		"workflow_dispatch:",
		"push:",
		"- main",
		"- labeled",
		"- unlabeled",
		"- synchronize",
		"- converted_to_draft",
		"- closed",
		"cancel-in-progress: false",
		"permissions:\n  contents: read",
		"actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd",
		"ref: ${{ github.workflow_sha }}",
		"persist-credentials: false",
		"actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1",
		"client-id: ${{ vars.QUEUE_APP_CLIENT_ID }}",
		"private-key: ${{ secrets.QUEUE_APP_PRIVATE_KEY }}",
		"permission-contents: write",
		"permission-issues: write",
		"permission-pull-requests: write",
		"actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
		"QUEUE_LABEL: queue-me",
		"scripts/queue_me_controller.js",
	}
	for _, fragment := range required {
		if !strings.Contains(workflowText, fragment) {
			t.Fatalf("queue-me workflow missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"actions/checkout@v",
		"actions/github-script@v",
		"pull_request:\n",
		"persist-credentials: true",
	} {
		if strings.Contains(workflowText, forbidden) {
			t.Fatalf("queue-me workflow contains unsafe fragment %q", forbidden)
		}
	}
}

func TestQueueMeControllerContract(t *testing.T) {
	controller := readConfig(t, "scripts/queue_me_controller.js")
	for _, fragment := range []string{
		"compareCommitsWithBasehead",
		"updatePullRequestBranch",
		"expectedHeadOid",
		"updateMethod: REBASE",
		"enablePullRequestAutoMerge",
		"disablePullRequestAutoMerge",
		"mergePullRequest",
		"mergeMethod: SQUASH",
		"maintainer_can_modify",
		"left.number - right.number",
		"COMMENT_MARKER",
	} {
		if !strings.Contains(controller, fragment) {
			t.Fatalf("queue-me controller missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"requestReviews",
		"force-push",
		"process.env.QUEUE_APP_PRIVATE_KEY",
	} {
		if strings.Contains(controller, forbidden) {
			t.Fatalf("queue-me controller contains forbidden fragment %q", forbidden)
		}
	}
}

func TestQueueMeControllerNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to test the queue-me controller")
	}
	command := exec.Command(node, "--test", "queue_me_controller.test.js")
	command.Dir = "."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("queue-me node tests failed: %v\n%s", err, output)
	}
}
