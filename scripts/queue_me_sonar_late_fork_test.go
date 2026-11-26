package scripts

import (
	"os/exec"
	"testing"
)

func TestQueueMeSonarAcceptsLateVerifiedForkSuite(t *testing.T) {
	command := exec.Command(queueLifecycleNode(t), "-e", `
const { verifySonar } = require('./queue_me_sonar');
const head = 'a'.repeat(40), base = 'b'.repeat(40);
const app = { id: 12526, slug: 'sonarqubecloud', owner: { id: 545988, login: 'SonarSource' } };
const run = { id: 80, check_suite: { id: 70 }, name: 'SonarCloud Code Analysis', app, head_sha: head,
  url: 'https://api.github.com/repos/ben-ranford/lopper/check-runs/80', details_url: 'https://sonarcloud.io/dashboard?id=ben-ranford_lopper&pullRequest=42',
  status: 'completed', conclusion: 'success', started_at: '2026-09-30T15:20:35Z', completed_at: '2026-09-30T15:21:21Z', pull_requests: [] };
const suite = { id: 70, app, head_sha: head, repository: { full_name: 'ben-ranford/lopper', private: false }, status: 'completed', conclusion: 'success',
  created_at: '2026-09-30T15:21:22Z', updated_at: '2026-09-30T15:21:22Z', pull_requests: [] };
const fork = { number: 42, url: 'https://api.github.com/repos/ben-ranford/lopper/pulls/42', state: 'open', draft: false,
  head: { sha: head, ref: 'feature', repo: { id: 2, fork: true, full_name: 'contributor/lopper', url: 'https://api.github.com/repos/contributor/lopper' } },
  base: { sha: base, ref: 'main', repo: { id: 1, full_name: 'ben-ranford/lopper', private: false, url: 'https://api.github.com/repos/ben-ranford/lopper' } } };
const fetchImpl = async (raw) => {
  const path = new URL(raw).pathname;
  let body;
  if (path === '/api/project_pull_requests/list') body = { pullRequests: [{ key: '42', base: 'main', target: 'main', url: 'https://github.com/ben-ranford/lopper/pull/42', commit: { sha: head }, analysisDate: '2026-09-30T15:20:35+0000', pullRequestUuidV1: 'pr-component' }] };
  else if (path === '/repos/ben-ranford/lopper/pulls/42') body = fork;
  else if (path.endsWith('/check-runs')) body = { total_count: 1, check_runs: [run] };
  else if (path.endsWith('/check-suites')) body = { total_count: 1, check_suites: [suite] };
  else if (path === '/api/qualitygates/project_status') body = { projectStatus: { status: 'OK', ignoredConditions: false, conditions: [{ status: 'OK' }] } };
  else if (path === '/api/issues/search') body = { paging: { pageIndex: 1, pageSize: 500, total: 0 }, issues: [], total: 0, p: 1, ps: 500 };
  else if (path === '/api/hotspots/search') body = { paging: { pageIndex: 1, pageSize: 500, total: 0 }, hotspots: [] };
  else throw new Error('unexpected path');
  return new Response(JSON.stringify(body), { headers: { 'content-type': 'application/json' } });
};
verifySonar({ owner: 'ben-ranford', repo: 'lopper', pullNumber: 42, headSHA: head, baseRef: 'main', baseSHA: base, fetchImpl }).catch((error) => { console.error(error.message); process.exitCode = 1; });
`)
	command.Dir = repoPath(t, "scripts")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("late fork Sonar suite was rejected: %v\n%s", err, output)
	}
}
