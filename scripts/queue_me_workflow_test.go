package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		"schedule:",
		"check_run:",
		"check_suite:",
		"status:",
		"workflow_run:",
		"workflows: [ci, windows runtime]",
		"github.event_name == 'pull_request_target' &&\n" +
			"        github.ref == 'refs/heads/main' &&\n" +
			"        github.workflow_ref == format('{0}/.github/workflows/queue-me.yml@refs/heads/main', github.repository) &&",
		"'queue_me_reviews.js'",
		"'queue_me_sonar.js'",
		"'queue_me_suppressions.js'",
		"'inline_suppression_tracker.js'",
		"'queue_me_ci.js'",
		"'queue_me_ci_intent.js'",
		"'queue_me_public_api.js'",
		"'queue_me_reuse.js'",
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
		"actions/github-script@v",
		"github.event.pull_request.head",
		"github.event.pull_request.base.ref",
		"pull_request:\n",
		"pull_request_review:",
		"pull_request_review_comment:",
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
		"verifyQueueEvidence",
		"revalidateQueueEvidence",
		"verifyQueueCI",
		"mergeVerifiedQueuedPull",
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
		"enablePullRequestAutoMerge",
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
	command := exec.Command(node, "--test", "queue_me_controller.test.js", "queue_me_reviews.test.js",
		"queue_me_sonar.test.js", "queue_me_suppressions.test.js", "queue_me_ci.test.js",
		"queue_me_ci_intent.test.js", "queue_me_public_api.test.js", "queue_me_reuse.test.js")
	command.Dir = repoPath(t, "scripts")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("queue-me node tests failed: %v\n%s", err, output)
	}
}

func TestQueueMeProtectedPhaseIsolation(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(readConfig(t, ".github/workflows/queue-me.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	jobs := workflow["jobs"].(map[string]any)
	if len(jobs) != 4 {
		t.Fatal("queue must separate preparation, analysis, suppression and final advance")
	}
	for _, name := range []string{"analyze", "suppression", "advance"} {
		t.Run(name, func(t *testing.T) {
			job := jobs[name].(map[string]any)
			assertQueueReadPermissions(t, name, job)
			assertQueueProtectedPin(t, name, job)
			if name != "advance" {
				assertQueueReadOnlyPhase(t, name, job)
			}
		})
	}
	prepare := queuePhaseYAML(t, jobs["prepare"])
	if !strings.Contains(prepare, "runController.prepareQueue(") || strings.Contains(prepare, "actions/checkout@") {
		t.Fatal("preparation must use the protected selector without checking out candidate code")
	}
	assertQueueFinalWriter(t, queuePhaseYAML(t, jobs["advance"]))
}

func queuePhaseYAML(t *testing.T, job any) string {
	t.Helper()
	data, err := yaml.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertQueueReadPermissions(t *testing.T, name string, job map[string]any) {
	t.Helper()
	permissions := job["permissions"].(map[string]any)
	for scope, permission := range permissions {
		if permission != "read" {
			t.Fatalf("%s grants ambient %s: %v", name, scope, permission)
		}
	}
	if permissions["contents"] != "read" || permissions["pull-requests"] != "read" {
		t.Fatalf("%s must explicitly bound its read-only token", name)
	}
	if name != "analyze" && permissions["actions"] != "read" {
		t.Fatalf("%s must read exact CI artifact provenance", name)
	}
}

func assertQueueProtectedPin(t *testing.T, name string, job map[string]any) {
	t.Helper()
	steps := job["steps"].([]any)
	checkout := steps[0].(map[string]any)
	with := checkout["with"].(map[string]any)
	if checkout["uses"] != "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1" ||
		with["ref"] != nil || with["persist-credentials"] != false || with["fetch-depth"] != 0 {
		t.Fatalf("%s must fetch protected history without dynamic refs or persisted credentials", name)
	}
	binding := steps[1].(map[string]any)
	environment := binding["env"].(map[string]any)
	if environment["TRUSTED_CONTROLLER_REF"] != "${{ github.workflow_sha }}" ||
		environment["QUEUE_TICKET"] != "${{ needs.prepare.outputs.ticket }}" {
		t.Fatalf("%s pin must bind trusted workflow source to selected ticket", name)
	}
	for _, fragment := range []string{
		"python3 -I -S -B -", "re.fullmatch(r'[0-9a-f]{40}', selected)",
		"selected != os.environ['TRUSTED_CONTROLLER_REF']", "actual != selected",
		"not key.startswith('GIT_')", "GIT_CONFIG_NOSYSTEM='1'", "GIT_CONFIG_GLOBAL=os.devnull", "GIT_NO_REPLACE_OBJECTS='1'",
		"['git', 'remote', 'get-url', '--all', 'origin']", "if origin not in ('https://github.com/ben-ranford/lopper', 'https://github.com/ben-ranford/lopper.git'):",
		"['merge-base', '--is-ancestor', selected, 'refs/remotes/origin/main']", "['checkout', '--detach', selected]",
		"'core.hooksPath=/dev/null'", "env=environment, check=True",
	} {
		if !strings.Contains(binding["run"].(string), fragment) {
			t.Fatalf("%s protected pin lacks %q", name, fragment)
		}
	}
}

func assertQueueReadOnlyPhase(t *testing.T, name string, job map[string]any) {
	t.Helper()
	data := queuePhaseYAML(t, job)
	for _, forbidden := range []string{"secrets.", "create-github-app-token", "queue_token", "permission-", "statuses: write"} {
		if strings.Contains(data, forbidden) {
			t.Fatalf("read-only %s job contains privileged source %q", name, forbidden)
		}
	}
	if job["needs"] != "prepare" {
		t.Fatalf("%s must consume its ticket directly from protected preparation", name)
	}
}

func assertQueueFinalWriter(t *testing.T, advance string) {
	t.Helper()
	for _, fragment := range []string{"needs.analyze.outputs.analysis", "needs.suppression.outputs.receipt",
		"needs.analyze.result", "needs.suppression.result", "needs.prepare.outputs.ticket",
		"QUEUE_REUSE_READ_TOKEN: ${{ github.token }}", "github-token: ${{ steps.queue_token.outputs.token }}"} {
		if !strings.Contains(advance, fragment) {
			t.Fatalf("final writer lacks protected same-run evidence %q", fragment)
		}
	}
	for _, forbidden := range []string{"check_reuse.py", "queue_me_reuse.py analyze", "setup-go@", "reuse_suppression.js"} {
		if strings.Contains(advance, forbidden) {
			t.Fatalf("final writer cannot execute analysis or candidate artifact parsing: %s", forbidden)
		}
	}
}

func TestQueueMeSharedBridgePythonSuite(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required to test shared protected queue validation")
	}
	command := exec.Command(python, "-B", "-m", "unittest", "queue_me_reuse_test")
	command.Dir = repoPath(t, "scripts")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("shared queue bridge tests failed: %v\n%s", err, output)
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

func TestQueueMeWaitingCIEventLifecycle(t *testing.T) {
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(readConfig(t, ".github/workflows/queue-me.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(fixture, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(queueLifecycleNode(t), "--test", "testdata/queue_waiting/lifecycle.cjs")
	command.Dir = repoPath(t, "scripts")
	command.Env = append(os.Environ(), "QUEUE_WORKFLOW_FIXTURE="+fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("queue CI event lifecycle failed: %v\n%s", err, output)
	}
}

func queueLifecycleNode(t *testing.T) string {
	t.Helper()
	if node, err := exec.LookPath("node"); err == nil {
		return node
	}
	// The proof runner excludes package-manager directories from PATH. Hosted
	// Ubuntu uses n's /usr/local prefix; macOS uses fixed Homebrew prefixes.
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/opt/homebrew/bin/node", "/usr/local/bin/node"}
	case "linux":
		candidates = []string{"/usr/local/bin/node"}
	}
	for _, candidate := range candidates {
		if node, err := exec.LookPath(candidate); err == nil {
			return node
		}
	}
	t.Fatal("node is required to test the queue CI event lifecycle")
	return ""
}
