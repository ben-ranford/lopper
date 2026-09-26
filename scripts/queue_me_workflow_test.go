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
		"- edited",
		"- auto_merge_enabled",
		"cancel-in-progress: false",
		"github.event_name == 'workflow_dispatch' &&",
		"github.ref == 'refs/heads/main'",
		"permissions:\n  contents: read",
		"actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1",
		"client-id: ${{ vars.QUEUE_APP_CLIENT_ID }}",
		"private-key: ${{ secrets.QUEUE_APP_PRIVATE_KEY }}",
		"permission-contents: write",
		"permission-issues: write",
		"permission-pull-requests: write",
		"permission-workflows: write",
		"actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3",
		"QUEUE_CONTROLLER_PATH: ${{ runner.temp }}/queue_me_controller.js",
		"TRUSTED_CONTROLLER_REF: ${{ github.workflow_sha }}",
		"github.rest.repos.getContent",
		"path: 'scripts/queue_me_controller.js'",
		"ref: process.env.TRUSTED_CONTROLLER_REF",
		"flag: 'wx'",
		"QUEUE_LABEL: queue-me",
		"QUEUE_APP_SLUG: ${{ steps.queue_token.outputs.app-slug }}",
		"require(process.env.QUEUE_CONTROLLER_PATH)",
	}
	for _, fragment := range required {
		if !strings.Contains(workflowText, fragment) {
			t.Fatalf("queue-me workflow missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"actions/checkout@",
		"actions/github-script@v",
		"github.event.pull_request.head",
		"pull_request:\n",
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
		"assertCanonicalCommitIdentity",
		"Queue identity audit failed",
		"expectedHeadOid",
		"enablePullRequestAutoMerge",
		"disablePullRequestAutoMerge",
		"mergePullRequest",
		"updateBranch",
		"mergeMethod: SQUASH",
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
		"updateMethod: REBASE",
		"process.env.QUEUE_APP_PRIVATE_KEY",
	} {
		if strings.Contains(controller, forbidden) {
			t.Fatalf("queue-me controller contains forbidden fragment %q", forbidden)
		}
	}
}

func TestQueueMeControllerAdvancesPastConflictingLeaderContract(t *testing.T) {
	controller := readConfig(t, "scripts/queue_me_controller.js")
	docs := readConfig(t, "docs/ci-usage.md")
	for _, fragment := range []string{
		"function advanceQueuedPull(",
		"needsCurrentBase",
		"The queue will continue with the next queued pull request.",
		"Every queued pull request is waiting for a clean queue identity audit after a base branch update.",
	} {
		if !strings.Contains(controller, fragment) {
			t.Fatalf("queue-me controller conflict handling missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"skipped while GitHub performs its branch update",
		"later run observes the updated head and reruns the identity audit",
	} {
		if !strings.Contains(docs, fragment) {
			t.Fatalf("queue-me docs conflict ordering contract missing %q", fragment)
		}
	}
}

func TestQueueMeControllerNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to test the queue-me controller")
	}
	command := exec.Command(node, "--test", "queue_me_controller.test.js")
	command.Dir = repoPath(t, "scripts")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("queue-me node tests failed: %v\n%s", err, output)
	}
}

func TestQueueMeControllerRenovateIdentityRegression(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to prove Renovate queue identity handling")
	}
	const script = `
const controller = require('./queue_me_controller.js');
const pull = {
  base: { repo: { name: 'lopper', owner: { login: 'octo' } } },
  head: { repo: { full_name: 'octo/lopper' } },
  user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
};
const renovate = { login: 'renovate[bot]', type: 'Bot', id: 29139614 };
const raw = { name: 'renovate[bot]', email: '29139614+renovate[bot]@users.noreply.github.com' };
controller.testables.assertCanonicalCommitIdentity({
  total_commits: 1,
  commits: [{
    sha: 'renovate-commit', author: renovate,
    committer: { login: 'web-flow', type: 'User', id: 19864447 },
    commit: {
      author: raw,
      committer: { name: 'GitHub', email: 'noreply@github.com' },
      verification: { verified: true, reason: 'valid' },
    },
  }],
}, pull);
`
	command := exec.Command(node, "-e", script)
	command.Dir = repoPath(t, "scripts")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("verified same-repository Renovate identity must pass: %v\n%s", err, output)
	}
}
