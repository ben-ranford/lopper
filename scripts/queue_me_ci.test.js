'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verifyCI, checkReadiness, isWaiting, assertUnchangedCI } = require('./queue_me_ci');

const { HEAD, BASE, MERGE, API, WEB, CREATED, CI_ID, WINDOWS_ID, ARTIFACT_ID, PATHS, JOBS, run, jobsFor, harness } = require('./testdata/queue_waiting/ci_fixture.cjs');
require('./testdata/queue_waiting/rerun_locator.cjs');

test('binds all 14 CI jobs, one artifact locator and Windows runtime to exact source and PR pair', async () => {
  const fixture = harness();
  const evidence = await verifyCI(fixture.input);
  assert.equal(evidence.workflows[0].jobs.length, 15);
  assert.equal(evidence.workflows[0].artifactId, ARTIFACT_ID);
  assert.equal(Object.hasOwn(evidence.workflows[1], 'artifactId'), false);
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
    const current = [jobs[0], jobs.at(-1)].map((job, index) => {
      const id = 99000 + index;
      return { ...job, id, url: `${API}/actions/jobs/${id}`, html_url: `${WEB}/actions/runs/100/job/${id}` };
    });
    return [...older, ...current];
  } });
  const evidence = await verifyCI(fixture.input);
  assert.equal(evidence.workflows[0].jobs.find((job) => job.name === 'verify-checks').runAttempt, 2);
  assert.equal(evidence.workflows[0].jobs.find((job) => job.name === 'verify').runAttempt, 1);
  assert.equal(evidence.workflows[0].jobs.find((job) => job.name.startsWith('suppression-artifact-')).runAttempt, 2);
  assert.equal(evidence.workflows[0].artifactId, ARTIFACT_ID);
});

function locatorHarness(change, options = {}) {
  return harness({ ...options, jobs: (jobs, selected) => selected.workflow_id === CI_ID ? change(jobs, selected) : jobs });
}

function changedLocator(change) {
  return locatorHarness((jobs) => jobs.map((job) => job.name.startsWith('suppression-artifact-') ? { ...job, ...change } : job));
}

test('artifact locator IDs must be canonical positive safe integers', async () => {
  for (const artifactId of [1, Number.MAX_SAFE_INTEGER]) {
    const evidence = await verifyCI(changedLocator({ name: `suppression-artifact-${artifactId}` }).input);
    assert.equal(evidence.workflows[0].artifactId, artifactId);
  }
  for (const suffix of ['', '0', '01', '-1', '+1', '1.0', '1e3', ' 1', '1 ', '1\n', '9007199254740992', '12345678901234567890', '${{ needs.verify-checks.outputs.pr_report_artifact_id }}']) {
    await assert.rejects(verifyCI(changedLocator({ name: `suppression-artifact-${suffix}` }).input), /malformed suppression artifact locator/);
  }
});

test('locator is mandatory and cannot replace any of the 13 static jobs', async () => {
  await assert.rejects(verifyCI(locatorHarness((jobs) => jobs.slice(0, -1)).input), /missing suppression artifact locator/);
  for (const [name] of JOBS) {
    await assert.rejects(verifyCI(locatorHarness((jobs) => jobs.filter((job) => job.name !== name)).input), /missing expected ci job or matrix member/);
  }
});

test('duplicate locator IDs, competing locators and arbitrary extra jobs hold', async () => {
  for (const name of [`suppression-artifact-${ARTIFACT_ID}`, 'suppression-artifact-123', 'unknown job']) {
    const fixture = locatorHarness((jobs) => [...jobs, { ...jobs.at(-1), name, id: 99000, url: `${API}/actions/jobs/99000`, html_url: `${WEB}/actions/runs/100/job/99000` }]);
    await assert.rejects(verifyCI(fixture.input), /duplicate suppression artifact locator|unknown job/);
  }
  const fixture = harness({ jobs: (jobs, selected) => selected.workflow_id === WINDOWS_ID ? [...jobs, { ...jobs[0], id: 99000, name: 'suppression-artifact-123', url: `${API}/actions/jobs/99000`, html_url: `${WEB}/actions/runs/200/job/99000` }] : jobs });
  await assert.rejects(verifyCI(fixture.input), /unknown job.*windows runtime/);
});

test('locator requires successful current-attempt exact-run/head hosted job evidence', async () => {
  for (const change of [
    { status: 'queued', conclusion: null }, { status: 'in_progress', conclusion: null },
    { conclusion: 'skipped' }, { conclusion: 'failure' }, { conclusion: 'cancelled' },
    { run_id: 99 }, { run_attempt: 0 }, { run_attempt: 2 }, { head_sha: BASE },
    { head_branch: 'other' }, { workflow_name: 'other' }, { labels: ['windows-latest'] },
    { runner_id: 0 }, { html_url: 'https://example.test/locator' },
  ]) await assert.rejects(verifyCI(changedLocator(change).input), /CI audit paused/);
  const fixture = locatorHarness((jobs) => jobs.map((job) => job.name.startsWith('suppression-artifact-') ? { ...job, run_attempt: 1 } : job), { runs: [{ ...run(CI_ID), run_attempt: 2 }, run(WINDOWS_ID)] });
  await assert.rejects(verifyCI(fixture.input), /missing suppression artifact locator for the current run attempt/);
});

test('historical locators cannot supply current artifact IDs or conceal malformed evidence', async () => {
  const fixtureFor = (name) => locatorHarness((jobs) => [...jobs, {
    ...jobs.at(-1), name, run_attempt: 1, id: 99000,
    url: `${API}/actions/jobs/99000`, html_url: `${WEB}/actions/runs/100/job/99000`,
  }], { runs: [{ ...run(CI_ID), run_attempt: 2 }, run(WINDOWS_ID)] });
  const evidence = await verifyCI(fixtureFor('suppression-artifact-123').input);
  assert.equal(evidence.workflows[0].artifactId, ARTIFACT_ID);
  assert.equal(evidence.workflows[0].jobs.length, 15);
  assert.equal(evidence.workflows[0].jobs.some((job) => job.id === 99000), false);
  await assert.rejects(verifyCI(fixtureFor('suppression-artifact-01').input), /malformed suppression artifact locator/);
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
      return { ...job, id, run_attempt: attempt + 1, url: `${API}/actions/jobs/${id}`, html_url: `${WEB}/actions/runs/100/job/${id}`, conclusion: attempt === 7 && index === JOBS.length - 1 ? 'failure' : 'success' };
    })).flat();
  } });
  await assert.rejects(verifyCI(fixture.input), /homebrew-tap-verify.*failure/);
  assert.equal(fixture.requests.filter(({ url }) => url.pathname.endsWith('/jobs')).length, 2);
});

for (const status of ['queued', 'in_progress', 'waiting', 'pending', 'requested']) {
  test(`readiness authenticates newest ${status} CI without auditing incomplete jobs`, async () => {
    const fixture = harness({ runs: [run(CI_ID), { ...run(CI_ID, 101), status, conclusion: null, referenced_workflows: [] }, run(WINDOWS_ID)] });
    assert.equal((await checkReadiness(fixture.input)).state, 'WAITING');
    assert.equal(fixture.requests.some(({ url }) => url.pathname.endsWith('/jobs')), false);
    await assert.rejects(verifyCI(fixture.input), isWaiting);
  });
}

test('complete absence and valid prior-intent CI defer registration; post-intent success resumes', async () => {
  const empty = harness({ runs: [] });
  assert.equal((await checkReadiness(empty.input)).state, 'WAITING');
  const prior = harness();
  prior.input.ciNotBefore = CREATED;
  assert.match((await checkReadiness(prior.input)).reasons.join(), /generation/);
  assert.equal((await checkReadiness(harness().input)).state, 'READY');
  await assert.rejects(verifyCI(prior.input), /same-second/);
});

test('initial readiness blocks known unsuccessful terminal outcomes without authorizing final CI', async () => {
  for (const workflow of [CI_ID, WINDOWS_ID]) {
    for (const conclusion of ['failure', 'cancelled', 'timed_out', 'neutral', 'skipped', 'action_required', 'stale', 'startup_failure']) {
      const runs = [run(CI_ID), run(WINDOWS_ID)].map(value => value.workflow_id === workflow
        ? { ...value, conclusion, referenced_workflows: [] } : value);
      const fixture = harness({ runs });
      const result = await checkReadiness(fixture.input);
      assert.equal(result.state, 'BLOCKED');
      assert.match(result.reasons.join(), new RegExp(`completed with ${conclusion}`));
      assert.equal(fixture.requests.some(({ url }) => url.pathname.endsWith('/jobs')), false);
      await assert.rejects(verifyCI(fixture.input), error => !isWaiting(error) && error.message.includes(conclusion));
    }
  }
});

test('cancelled initial selection recovers only through the fresh current CI generation', async () => {
  const runs = [run(CI_ID, 99), { ...run(CI_ID), conclusion: 'cancelled', referenced_workflows: undefined }, run(WINDOWS_ID)];
  const fixture = harness({ runs });
  assert.equal((await checkReadiness(fixture.input)).state, 'BLOCKED');
  await assert.rejects(verifyCI(fixture.input), /latest ci run 100.*cancelled/);
  runs.push({ ...run(CI_ID, 101), status: 'queued', conclusion: null, referenced_workflows: [] });
  const waiting = await checkReadiness(fixture.input);
  assert.equal(waiting.state, 'WAITING');
  assert.match(waiting.reasons.join(), /run 101 attempt 1/);
  Object.assign(runs.at(-1), run(CI_ID, 101));
  assert.deepEqual(await checkReadiness(fixture.input), { state: 'READY', reasons: [] });
  assert.equal((await verifyCI(fixture.input)).workflows[0].runId, 101);
});

test('BLOCKED dominates waiting across workflows but never masks malformed evidence', async () => {
  const pending = { ...run(CI_ID), status: 'queued', conclusion: null };
  const failedWindows = harness({ runs: [pending, { ...run(WINDOWS_ID), conclusion: 'failure' }] });
  const blocked = await checkReadiness(failedWindows.input);
  assert.equal(blocked.state, 'BLOCKED');
  assert.match(blocked.reasons.join(), /windows runtime.*failure/);
  assert.match(blocked.reasons.join(), /ci run 100.*queued/);
  await assert.rejects(verifyCI(failedWindows.input), error => !isWaiting(error) && /latest windows runtime/.test(error.message));
  const failed = { ...run(CI_ID), conclusion: 'cancelled', referenced_workflows: [] };
  const waitingWindows = { ...run(WINDOWS_ID), status: 'in_progress', conclusion: null };
  assert.equal((await checkReadiness(harness({ runs: [failed, waitingWindows] }).input)).state, 'BLOCKED');
  await assert.rejects(checkReadiness(harness({ runs: [failed, { ...waitingWindows, pull_requests: [] }] }).input), /association/);
});

test('readiness preserves contradictory, identity, source and inventory failures', async () => {
  const changes = [
    { conclusion: null }, { conclusion: 'unknown' },
    { status: 'in_progress', conclusion: 'startup_failure' },
    { conclusion: 'startup_failure', pull_requests: [] },
    { status: 'queued', conclusion: 'success' }, { status: 'unknown', conclusion: null },
    { status: 'queued', conclusion: null, pull_requests: [] }, { path: '.github/workflows/other.yml' },
    { status: 'queued', conclusion: null, referenced_workflows: [{}] },
    { conclusion: 'cancelled', pull_requests: [] },
    { conclusion: 'cancelled', referenced_workflows: [{}] },
    { conclusion: 'cancelled', referenced_workflows: null },
    { conclusion: 'cancelled', referenced_workflows: 'missing' },
  ];
  for (const change of changes) {
    await assert.rejects(checkReadiness(harness({ runs: [run(CI_ID), { ...run(CI_ID, 101), ...change }, run(WINDOWS_ID)] }).input), error => !isWaiting(error));
  }
  await assert.rejects(checkReadiness(harness({ source: data => ({ ...data, sha: null }) }).input), /workflow/);
  await assert.rejects(checkReadiness(harness({ response: data => ({ ...data, total_count: 5 }) }).input), /incomplete/);
  const unavailable = harness();
  unavailable.input.fetchImpl = async () => { throw new Error('unavailable'); };
  await assert.rejects(checkReadiness(unavailable.input), error => !isWaiting(error));
});

test('only monotonic authenticated rerun pending can defer the final run reread', async () => {
  const pending = { run_attempt: 2, status: 'in_progress', conclusion: null };
  await assert.rejects(verifyCI(harness({ reread: selected => ({ ...selected, ...pending }) }).input), isWaiting);
  for (const change of [{ run_attempt: 1 }, { run_attempt: 0 }, { run_number: 99 }, { created_at: '2026-10-01T00:00:00Z' }, { id: 999 }, { head_sha: BASE }]) {
    await assert.rejects(verifyCI(harness({ reread: selected => ({ ...selected, ...pending, ...change }) }).input), error => !isWaiting(error));
  }
  assert.equal(isWaiting(Object.assign(new Error('CI audit waiting'), { queuePauseMessage: 'waiting', code: 'CI_WAITING' })), false);
});

test('fully validated newer successful epochs defer but same epoch evidence drift fails', async () => {
  const previous = await verifyCI(harness().input);
  for (const latest of [run(CI_ID, 101), { ...run(CI_ID), run_attempt: 2 }]) {
    const current = await verifyCI(harness({ runs: [latest, run(WINDOWS_ID)] }).input);
    assert.throws(() => assertUnchangedCI(previous, current), isWaiting);
  }
  assert.doesNotThrow(() => assertUnchangedCI(previous, previous));
  const changed = structuredClone(previous);
  changed.workflows[0].artifactId++;
  assert.throws(() => assertUnchangedCI(previous, changed), error => !isWaiting(error));
  changed.workflows[0].runNumber--;
  assert.throws(() => assertUnchangedCI(previous, changed), error => !isWaiting(error));
});
