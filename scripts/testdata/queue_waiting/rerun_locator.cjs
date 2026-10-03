'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verifyCI } = require('../../queue_me_ci');
const { API, WEB, BASE, CI_ID, WINDOWS_ID, ARTIFACT_ID, run, harness } = require('./ci_fixture.cjs');

const UNRESOLVED = 'suppression-artifact-${{ needs.verify-checks.outputs.pr_report_artifact_id }}';
const TERMINAL_FAILURES = ['failure', 'cancelled', 'timed_out', 'neutral', 'skipped', 'action_required', 'stale'];

function historicalLocator(template, change, index) {
  const id = 99000 + index;
  return {
    ...template, id, run_attempt: 1, name: UNRESOLVED,
    status: 'completed', conclusion: 'cancelled',
    runner_id: null, runner_name: null, runner_group_id: null,
    url: `${API}/actions/jobs/${id}`, html_url: `${WEB}/actions/runs/100/job/${id}`,
    ...change,
  };
}

function rerunFixture(history = [{}], current = (jobs) => jobs) {
  return harness({
    runs: [{ ...run(CI_ID), run_attempt: 3 }, run(WINDOWS_ID)],
    jobs: (jobs, selected) => selected.workflow_id === CI_ID
      ? [...history.map((change, index) => historicalLocator(jobs.at(-1), change, index)), ...current(jobs)]
      : jobs,
  });
}

function sequentialCases(cases, verify) {
  return cases.reduce((pending, value) => pending.then(() => verify(value)), Promise.resolve());
}

test('successful rerun retains cancelled unresolved locator history without approving it', async () => {
  await sequentialCases(TERMINAL_FAILURES, async (conclusion) => {
    const evidence = await verifyCI(rerunFixture([{ conclusion }]).input);
    const ci = evidence.workflows[0];
    assert.equal(ci.runAttempt, 3);
    assert.equal(ci.artifactId, ARTIFACT_ID);
    assert.equal(ci.jobs.length, 14);
    assert.ok(ci.jobs.every((job) => job.runAttempt === 3 && job.id !== 99000));
    assert.equal(ci.jobs.filter((job) => job.name.startsWith('suppression-artifact-')).length, 1);
  });
});

test('historical unresolved locator cannot replace current artifact evidence', async () => {
  await assert.rejects(verifyCI(rerunFixture([{}], (jobs) => jobs.slice(0, -1)).input), /missing suppression artifact locator for the current run attempt/);
  await sequentialCases(['success', 'cancelled'], async (conclusion) => {
    const fixture = rerunFixture([], (jobs) => jobs.map((job) => job.name.startsWith('suppression-artifact-')
      ? { ...job, name: UNRESOLVED, conclusion } : job));
    await assert.rejects(verifyCI(fixture.input), /malformed suppression artifact locator/);
  });
});

test('historical unresolved locator rejects successful, unknown and nonterminal states', async () => {
  await sequentialCases([
    { conclusion: 'success' }, { conclusion: null }, { conclusion: 'unknown' },
    ...['queued', 'in_progress', 'waiting', 'pending', 'requested'].map((status) => ({ status, conclusion: null })),
    { status: 'waiting', conclusion: 'cancelled' }, { status: null },
  ], (change) => assert.rejects(verifyCI(rerunFixture([change]).input), /malformed suppression artifact locator/));
});

test('historical unresolved locator preserves strict static-job carry-forward', async () => {
  await sequentialCases(['success', 'failure'], async (conclusion) => {
    const fixture = rerunFixture([{}], (jobs) => jobs.map((job) => job.name === 'verify-checks'
      ? { ...job, run_attempt: 1, conclusion } : job));
    if (conclusion === 'failure') {
      await assert.rejects(verifyCI(fixture.input), /ci job verify-checks is completed\/failure/);
    } else {
      const ci = (await verifyCI(fixture.input)).workflows[0];
      assert.equal(ci.jobs.find((job) => job.name === 'verify-checks').runAttempt, 1);
      assert.equal(ci.artifactId, ARTIFACT_ID);
    }
  });
});

test('historical unresolved locator requires exact name and authenticated prior-attempt identity', async () => {
  await sequentialCases([
    { name: `${UNRESOLVED} ` }, { name: 'suppression-artifact-${{ other }}' },
    { name: 'suppression-artifact-01' }, { name: 'unknown job' },
    { run_attempt: 0 }, { run_attempt: 3 }, { run_attempt: 4 },
    { run_id: 99 }, { head_sha: BASE }, { head_branch: 'other' },
    { workflow_name: 'other' }, { url: 'https://example.test/job' },
  ], (change) => assert.rejects(verifyCI(rerunFixture([change]).input), /CI audit paused/));
});

test('historical unresolved locators still participate in duplicate detection in either order', async () => {
  const numeric = { name: 'suppression-artifact-123', conclusion: 'success' };
  await sequentialCases([[{}, numeric], [numeric, {}], [{}, {}]], async (history) => {
    await assert.rejects(verifyCI(rerunFixture(history).input), /duplicate suppression artifact locator in one run attempt/);
  });
});

test('unresolved locator is unknown in a workflow without the trusted locator contract', async () => {
  const fixture = harness({
    runs: [run(CI_ID), { ...run(WINDOWS_ID), run_attempt: 3 }],
    jobs: (jobs, selected) => selected.workflow_id === WINDOWS_ID ? [...jobs, {
      ...historicalLocator(jobs[0], {}, 0),
      html_url: `${WEB}/actions/runs/200/job/99000`,
    }] : jobs,
  });
  await assert.rejects(verifyCI(fixture.input), /unknown job.*windows runtime/);
});
