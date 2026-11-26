package scripts

import (
	"os/exec"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestReuseRunnerTrustBoundaries(t *testing.T) {
	t.Parallel()

	command := exec.Command("python3", "-B", repoPath(t, "scripts/check_reuse_test.py"))
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reuse runner trust-boundary tests: %v\n%s", err, output)
	}
}

func TestReuseSuppressionNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to test reuse suppression evidence")
	}
	command := exec.Command(node, "--test", "reuse_suppression.test.js")
	command.Dir = repoPath(t, "scripts")
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reuse suppression node tests: %v\n%s", err, output)
	}
}

func TestReuseAcceptsRecordedForkMergeSource(t *testing.T) {
	const proof = `
from unittest.mock import Mock, patch
import reuse_event as event

base, head, merge = "a" * 40, "b" * 40, "c" * 40
repo = "ben-ranford/lopper"
snapshot = dict(repository=repo, repository_id=10, head_repository_id=20,
                pull_number=12, head=head, base=base, policy_source=base)
run = dict(id=41, workflow_id=6, run_attempt=2, path=event.CI_WORKFLOW, name="ci",
           event="pull_request", status="completed", conclusion="success", head_sha=head,
           repository={"id": 10}, head_repository={"id": 20}, pull_requests=[],
           referenced_workflows=[dict(sha=merge, ref="refs/pull/12/merge",
               path=f"{repo}/.github/workflows/ci-tests.yml@{merge}")])

def request(endpoint):
    if endpoint == f"/repos/{repo}/git/commits/{merge}":
        return dict(sha=merge, parents=[dict(sha=base), dict(sha=head)])
    prefix = f"/repos/{repo}/contents/"
    if endpoint.startswith(prefix):
        path, ref = endpoint[len(prefix):].split("?ref=")
        if path in (event.CI_WORKFLOW, ".github/workflows/ci-tests.yml") and ref in (base, head, merge):
            return dict(type="file", path=path, sha="d" * 40)
    raise AssertionError(endpoint)

with patch.object(event, "same_live_pair"), patch.object(event, "ci_workflow", return_value={"id": 6}), patch.object(event, "latest_ci_run", return_value=run):
    assert event.current_ci(Mock(request=request), snapshot) == run
`
	command := exec.Command("python3", "-B", "-c", proof)
	command.Dir = repoPath(t, "scripts")
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fork CI evidence: %v\n%s", err, output)
	}
}
