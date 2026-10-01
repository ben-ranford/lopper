'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verifyCI } = require('./queue_me_ci');

const HEAD = 'a'.repeat(40);
const BASE = 'b'.repeat(40);
const MERGE = 'c'.repeat(40);
const API = 'https://api.github.com/repos/ben-ranford/lopper';
const WEB = 'https://github.com/ben-ranford/lopper';
const CREATED = '2026-10-01T01:00:00Z';
const FINISHED = '2026-10-01T02:00:00Z';
const REPO = { id: 1155023607, full_name: 'ben-ranford/lopper', name: 'lopper', url: API };
const CI_ID = 232814257;
const WINDOWS_ID = 354077369;
const PATHS = ['.github/workflows/ci.yml', '.github/workflows/ci-tests.yml', '.github/workflows/windows-runtime.yml'];
const JOBS = [
  ['verify-checks', 'ubuntu-latest'], ['publish-pr-reports', 'ubuntu-latest'],
  ['verification checks (rolling)', 'ubuntu-latest'], ['verify-tests / tests', 'ubuntu-latest'],
  ['verify-rolling-tests / tests', 'ubuntu-latest'], ['regression-proof-windows', 'windows-latest'],
  ['verify', 'ubuntu-latest'], ['verify (rolling)', 'ubuntu-latest'],
  ['os-smoke (ubuntu-latest)', 'ubuntu-latest'], ['os-smoke (macos-26)', 'macos-26'],
  ['vscode-smoke (ubuntu-latest)', 'ubuntu-latest'], ['vscode-smoke (macos-26)', 'macos-26'],
  ['homebrew-tap-verify', 'ubuntu-latest'],
];

function run(workflow, id = workflow === CI_ID ? 100 : 200) {
  const ci = workflow === CI_ID;
  return {
    id, run_number: id, run_attempt: 1, workflow_id: workflow,
    name: ci ? 'ci' : 'windows runtime', path: ci ? PATHS[0] : PATHS[2],
    event: 'pull_request', head_sha: HEAD, head_branch: 'feature',
    repository: { ...REPO }, head_repository: { ...REPO },
    url: `${API}/actions/runs/${id}`, html_url: `${WEB}/actions/runs/${id}`,
    created_at: CREATED, updated_at: FINISHED, status: 'completed', conclusion: 'success',
    pull_requests: [{
      id: 300, number: 1777, url: `${API}/pulls/1777`,
      head: { sha: HEAD, ref: 'feature', repo: { ...REPO } },
      base: { sha: BASE, ref: 'main', repo: { ...REPO } },
    }],
    referenced_workflows: ci ? [{ path: `ben-ranford/lopper/${PATHS[1]}@${MERGE}`, sha: MERGE, ref: 'refs/pull/1777/merge' }] : [],
  };
}

function jobsFor(selected) {
  const names = selected.workflow_id === CI_ID ? JOBS : [['runtime-cancellation', 'windows-latest']];
  return names.map(([name, label], index) => ({
    id: selected.id * 100 + index, run_id: selected.id, run_attempt: selected.run_attempt,
    workflow_name: selected.name, head_sha: HEAD, head_branch: 'feature', name,
    run_url: selected.url, url: `${API}/actions/jobs/${selected.id * 100 + index}`,
    html_url: `${WEB}/actions/runs/${selected.id}/job/${selected.id * 100 + index}`,
    status: 'completed', conclusion: 'success', labels: [label],
    runner_id: 100000 + index, runner_group_id: 0, runner_name: `Hosted Agent ${index}`,
    created_at: CREATED, started_at: CREATED, completed_at: FINISHED,
  }));
}

function harness(options = {}) {
  const runs = options.runs ?? [run(CI_ID), run(WINDOWS_ID)];
  const requests = [];
  const contents = [];
  const pull = {
    id: 300, number: 1777, state: 'open', draft: false,
    head: { sha: HEAD, ref: 'feature', repo: { ...REPO } },
    base: { sha: BASE, ref: 'main', repo: { ...REPO } },
  };
  const input = {
    owner: 'ben-ranford', repo: 'lopper', pullNumber: 1777,
    headSHA: HEAD, baseSHA: BASE, baseRef: 'main', trustedPolicySHA: BASE,
    ciNotBefore: '2026-10-01T00:59:59Z',
    github: { rest: {
      pulls: { get: async () => ({ data: options.pull ? options.pull(structuredClone(pull)) : pull }) },
      repos: { getContent: async (args) => {
        contents.push(args);
        const data = { type: 'file', path: args.path, sha: String(PATHS.indexOf(args.path) + 4).repeat(40) };
        return { data: options.source ? options.source(data, args) : data };
      } },
      git: { getCommit: async (args) => {
        assert.equal(args.commit_sha, MERGE);
        const data = { sha: MERGE, parents: [{ sha: BASE }, { sha: HEAD }] };
        return { data: options.merge ? options.merge(data) : data };
      } },
    } },
    fetchImpl: async (address, init) => {
      const url = new URL(address);
      requests.push({ url, init });
      assert.equal(url.origin, 'https://api.github.com');
      let data;
      const workflow = /^\/repos\/ben-ranford\/lopper\/actions\/workflows\/(\d+)\/runs$/.exec(url.pathname);
      const jobs = /^\/repos\/ben-ranford\/lopper\/actions\/runs\/(\d+)\/jobs$/.exec(url.pathname);
      if (workflow) {
        assert.equal(url.searchParams.get('event'), 'pull_request');
        assert.equal(url.searchParams.get('head_sha'), HEAD);
        const selected = runs.filter((candidate) => candidate.workflow_id === Number(workflow[1]));
        const offset = (Number(url.searchParams.get('page')) - 1) * 100;
        data = { total_count: selected.length, workflow_runs: selected.slice(offset, offset + 100) };
      } else if (jobs) {
        assert.equal(url.searchParams.get('filter'), 'all');
        const selected = runs.find((candidate) => candidate.id === Number(jobs[1]));
        const listed = options.jobs ? options.jobs(jobsFor(selected), selected) : jobsFor(selected);
        const offset = (Number(url.searchParams.get('page')) - 1) * 100;
        data = { total_count: listed.length, jobs: listed.slice(offset, offset + 100) };
      } else {
        const selected = runs.find((candidate) => url.pathname === `/repos/ben-ranford/lopper/actions/runs/${candidate.id}`);
        assert.ok(selected, url.pathname);
        data = options.reread ? options.reread(structuredClone(selected)) : selected;
      }
      if (options.response) data = options.response(structuredClone(data), url);
      return new Response(JSON.stringify(data), { headers: { 'content-type': 'application/json' } });
    },
  };
  return { input, requests, contents };
}

test('binds all 13 CI jobs and Windows runtime to exact source and PR pair', async () => {
  const fixture = harness();
  const evidence = await verifyCI(fixture.input);
  assert.equal(evidence.workflows[0].jobs.length, 13);
  assert.equal(evidence.workflows[1].jobs[0].name, 'runtime-cancellation');
  assert.equal(evidence.workflows[0].mergeSHA, MERGE);
  assert.equal(evidence.workflows[1].mergeSHA, null);
  assert.equal(evidence.headSHA, HEAD);
  assert.equal(evidence.baseSHA, BASE);
  assert.equal(fixture.requests.length, 6);
  assert.equal(fixture.contents.length, 9);
  assert.ok(fixture.requests.every(({ init }) => init.credentials === 'omit' && init.headers.Authorization === undefined));
  assert.deepEqual(await verifyCI(harness().input), evidence);
});

test('failed post-label macOS job blocks even when run and required aggregate are green', async () => {
  const fixture = harness({ jobs: (jobs, selected) => jobs.map((job) => selected.workflow_id === CI_ID && job.name === 'os-smoke (macos-26)' ? { ...job, conclusion: 'failure' } : job) });
  await assert.rejects(verifyCI(fixture.input), /os-smoke \(macos-26\).*failure/);
});

for (const [status, conclusion] of [['queued', null], ['in_progress', null], ['completed', 'failure'], ['completed', 'cancelled'], ['completed', 'skipped']]) {
  test(`newest logical CI ${status}/${conclusion} cannot fall back to old green`, async () => {
    const latest = { ...run(CI_ID, 101), status, conclusion };
    await assert.rejects(verifyCI(harness({ runs: [run(CI_ID), latest, run(WINDOWS_ID)] }).input), /latest ci run 101/);
  });
}

test('superseded cancelled generations cannot contaminate a newer complete successful run', async () => {
  const older = { ...run(CI_ID, 99), status: 'completed', conclusion: 'cancelled', pull_requests: [] };
  const evidence = await verifyCI(harness({ runs: [older, run(CI_ID), run(WINDOWS_ID)] }).input);
  assert.equal(evidence.workflows[0].runId, 100);
});

test('newer unassociated potential run holds rather than falling back to green', async () => {
  const unknown = { ...run(CI_ID, 101), pull_requests: [] };
  await assert.rejects(verifyCI(harness({ runs: [run(CI_ID), unknown, run(WINDOWS_ID)] }).input), /association/);
  await assert.rejects(verifyCI(harness({ runs: [run(CI_ID), { ...run(WINDOWS_ID), pull_requests: [] }] }).input), /association/);
});

test('definitively foreign PR generation is ignored', async () => {
  const foreign = run(CI_ID, 101);
  foreign.pull_requests[0] = { ...foreign.pull_requests[0], number: 999, id: 999, url: `${API}/pulls/999` };
  foreign.conclusion = 'failure';
  const evidence = await verifyCI(harness({ runs: [run(CI_ID), foreign, run(WINDOWS_ID)] }).input);
  assert.equal(evidence.workflows[0].runId, 100);
});

test('latest stale base association is not replaced with older matching success', async () => {
  const latest = run(CI_ID, 101);
  latest.pull_requests[0].base.sha = 'd'.repeat(40);
  await assert.rejects(verifyCI(harness({ runs: [run(CI_ID), latest, run(WINDOWS_ID)] }).input), /recorded pull request base/);
});

test('CI must start after live metadata intent while Windows may predate a label', async () => {
  const fixture = harness();
  fixture.input.ciNotBefore = '2026-10-01T01:00:01Z';
  await assert.rejects(verifyCI(fixture.input), /new CI run after/);
  fixture.input.ciNotBefore = CREATED;
  await assert.rejects(verifyCI(fixture.input), /same-second ordering/);
  const ci = { ...run(CI_ID), created_at: '2026-10-01T01:30:00Z' };
  const valid = harness({ runs: [ci, run(WINDOWS_ID)] });
  valid.input.ciNotBefore = '2026-10-01T01:29:59Z';
  await verifyCI(valid.input);
});

test('missing workflow, matrix member, unknown job or skipped job holds', async () => {
  await assert.rejects(verifyCI(harness({ runs: [run(CI_ID)] }).input), /missing windows runtime/);
  for (const mutate of [
    (jobs) => jobs.slice(1),
    (jobs) => jobs.map((job, index) => index === 0 ? { ...job, name: 'unknown job' } : job),
    (jobs) => jobs.map((job, index) => index === 0 ? { ...job, conclusion: 'skipped' } : job),
  ]) await assert.rejects(verifyCI(harness({ jobs: mutate }).input), /CI audit paused/);
});

test('manual event, wrong workflow identity and noncanonical run URLs cannot pass', async () => {
  for (const change of [{ event: 'workflow_dispatch' }, { path: '.github/workflows/other.yml' }, { url: 'https://example.test/run' }, { head_branch: 'other' }]) {
    const ci = { ...run(CI_ID), ...change };
    await assert.rejects(verifyCI(harness({ runs: [ci, run(WINDOWS_ID)] }).input), /CI audit paused/);
  }
});

test('partial rerun carries successes only within the same immutable run', async () => {
  const ci = { ...run(CI_ID), run_attempt: 2 };
  const fixture = harness({ runs: [ci, run(WINDOWS_ID)], jobs: (jobs, selected) => {
    if (selected.workflow_id !== CI_ID) return jobs;
    const older = jobs.map((job) => ({ ...job, run_attempt: 1 }));
    older[0].conclusion = 'failure';
    return [...older, { ...jobs[0], id: 99000, url: `${API}/actions/jobs/99000`, html_url: `${WEB}/actions/runs/100/job/99000` }];
  } });
  const evidence = await verifyCI(fixture.input);
  assert.equal(evidence.workflows[0].jobs.find((job) => job.name === 'verify-checks').runAttempt, 2);
  assert.equal(evidence.workflows[0].jobs.find((job) => job.name === 'verify').runAttempt, 1);
});

test('job attempts cannot cross runs, exceed the selected attempt, duplicate names or omit current attempt', async () => {
  for (const change of [{ run_id: 99 }, { run_attempt: 2 }, { head_sha: BASE }, { labels: ['self-hosted'] }, { runner_group_id: 2 }, { html_url: 'https://example.test/job' }]) {
    await assert.rejects(verifyCI(harness({ jobs: (jobs) => jobs.map((job, index) => index === 0 ? { ...job, ...change } : job) }).input), /CI audit paused/);
  }
  await assert.rejects(verifyCI(harness({ jobs: (jobs) => [...jobs, { ...jobs[0], id: 88000, url: `${API}/actions/jobs/88000`, html_url: `${WEB}/actions/runs/100/job/88000` }] }).input), /duplicate job name/);
  await assert.rejects(verifyCI(harness({ runs: [{ ...run(CI_ID), run_attempt: 2 }, run(WINDOWS_ID)], jobs: (jobs) => jobs.map((job) => ({ ...job, run_attempt: 1 })) }).input), /current run attempt/);
});

test('a changed run attempt, conclusion or source during job audit invalidates the snapshot', async () => {
  for (const change of [{ run_attempt: 2 }, { conclusion: 'failure' }, { status: 'queued', conclusion: null }, { updated_at: '2026-10-01T02:00:01Z' }, { referenced_workflows: [] }]) {
    await assert.rejects(verifyCI(harness({ reread: (selected) => ({ ...selected, ...change }) }).input), /CI audit paused/);
  }
  await assert.rejects(verifyCI(harness({ reread: (selected) => selected.workflow_id === WINDOWS_ID ? { ...selected, referenced_workflows: undefined } : selected }).input), /changed while auditing/);
});

test('candidate or merge workflow edits cannot bless their own job manifest', async () => {
  for (const ref of [HEAD, MERGE]) {
    for (const path of PATHS) {
      const fixture = harness({ source: (data, args) => args.ref === ref && args.path === path ? { ...data, sha: 'f'.repeat(40) } : data });
      await assert.rejects(verifyCI(fixture.input), /workflow source/);
    }
  }
});

test('reusable workflow must identify immutable same-PR merge source and exact parent order', async () => {
  for (const references of [[], [{ path: 'other/repo/workflow.yml', sha: MERGE, ref: 'refs/pull/1777/merge' }], [{ path: `ben-ranford/lopper/${PATHS[1]}@${MERGE}`, sha: MERGE, ref: 'refs/heads/main' }]]) {
    await assert.rejects(verifyCI(harness({ runs: [{ ...run(CI_ID), referenced_workflows: references }, run(WINDOWS_ID)] }).input), /workflow/);
  }
  await assert.rejects(verifyCI(harness({ merge: (data) => ({ ...data, parents: [...data.parents].reverse() }) }).input), /exact base and head parents/);
});

test('live head/base drift, draft and unknown repository metadata hold before API evidence', async () => {
  for (const mutate of [
    (pull) => ({ ...pull, draft: true }),
    (pull) => ({ ...pull, head: { ...pull.head, sha: BASE } }),
    (pull) => ({ ...pull, base: { ...pull.base, sha: HEAD } }),
    (pull) => ({ ...pull, head: { ...pull.head, repo: null } }),
  ]) {
    const fixture = harness({ pull: mutate });
    await assert.rejects(verifyCI(fixture.input), /CI audit paused/);
    assert.equal(fixture.requests.length, 0);
  }
});

test('fork repository identity is bound independently from the base repository', async () => {
  const fork = { id: 456, full_name: 'contributor/lopper', name: 'lopper', url: 'https://api.github.com/repos/contributor/lopper' };
  const runs = [run(CI_ID), run(WINDOWS_ID)].map((selected) => {
    selected.head_repository = { ...fork };
    selected.pull_requests[0].head.repo = { ...fork };
    return selected;
  });
  await verifyCI(harness({ runs, pull: (pull) => ({ ...pull, head: { ...pull.head, repo: fork } }) }).input);
  await assert.rejects(verifyCI(harness({ runs }).input), /head repository/);
});

test('run inventory pagination is complete and duplicate, changed or truncated pages hold', async () => {
  const older = Array.from({ length: 101 }, (_, index) => ({ ...run(CI_ID, index + 1), conclusion: 'cancelled' }));
  older.at(-1).conclusion = 'success';
  const fixture = harness({ runs: [...older, run(WINDOWS_ID)] });
  const evidence = await verifyCI(fixture.input);
  assert.equal(evidence.workflows[0].runId, 101);
  assert.equal(fixture.requests.filter(({ url }) => url.pathname.endsWith(`workflows/${CI_ID}/runs`)).length, 2);
  for (const response of [
    (data) => data.workflow_runs ? { ...data, total_count: 1000 } : data,
    (data) => data.workflow_runs ? { ...data, total_count: 1001 } : data,
    (data) => data.workflow_runs ? { ...data, workflow_runs: [] } : data,
    (data) => data.workflow_runs ? { ...data, workflow_runs: [data.workflow_runs[0], data.workflow_runs[0]], total_count: 2 } : data,
  ]) await assert.rejects(verifyCI(harness({ response }).input), /CI audit paused/);
  const changed = harness({ runs: [...older, run(WINDOWS_ID)], response: (data, url) =>
    data.workflow_runs && url.searchParams.get('page') === '2' ? { ...data, total_count: data.total_count + 1 } : data });
  await assert.rejects(verifyCI(changed.input), /changed during pagination/);
  const duplicateNumber = { ...run(CI_ID, 101), run_number: 100 };
  await assert.rejects(verifyCI(harness({ runs: [run(CI_ID), duplicateNumber, run(WINDOWS_ID)] }).input), /duplicate logical workflow run number/);
});

test('malformed job pages and duplicate job IDs never become partial success', async () => {
  for (const response of [
    (data) => data.jobs ? { ...data, total_count: data.total_count + 1 } : data,
    (data) => data.jobs ? { ...data, jobs: [data.jobs[0], data.jobs[0]], total_count: 2 } : data,
    (data) => data.jobs ? { ...data, total_count: -1 } : data,
    (data) => data.jobs ? { ...data, jobs: undefined } : data,
  ]) await assert.rejects(verifyCI(harness({ response }).input), /CI audit paused/);
});

test('jobs paginate across partial attempts without dropping the last failed job', async () => {
  const ci = { ...run(CI_ID), run_attempt: 8 };
  const fixture = harness({ runs: [ci, run(WINDOWS_ID)], jobs: (jobs, selected) => {
    if (selected.workflow_id !== CI_ID) return jobs;
    return Array.from({ length: 8 }, (_, attempt) => jobs.map((job, index) => {
      const id = 100000 + attempt * 100 + index;
      return { ...job, id, run_attempt: attempt + 1, url: `${API}/actions/jobs/${id}`, html_url: `${WEB}/actions/runs/100/job/${id}`, conclusion: attempt === 7 && index === 12 ? 'failure' : 'success' };
    })).flat();
  } });
  await assert.rejects(verifyCI(fixture.input), /homebrew-tap-verify.*failure/);
  assert.equal(fixture.requests.filter(({ url }) => url.pathname.endsWith('/jobs')).length, 2);
});
