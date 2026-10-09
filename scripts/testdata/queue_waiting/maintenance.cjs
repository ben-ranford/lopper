'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verifyCI, assertUnchangedCI } = require('../../queue_me_ci');
const { API, WEB, CREATED, CI_ID, PATHS, harness } = require('./ci_fixture.cjs');

function maintenanceFixture(mutate = job => job, sourceTransform = value => value) {
  const { createHash } = require('node:crypto');
  const source = sourceTransform("jobs:\n  sonar-maintenance:\n    if: ${{ github.event_name == 'workflow_dispatch' && inputs.sonar_operation != 'none' }}\n    runs-on: ubuntu-latest\n");
  const bytes = Buffer.from(source);
  return harness({
    source: (data, args) => args.path === PATHS[0] ? { ...data, encoding: 'base64',
      content: bytes.toString('base64'), sha: createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex') } : data,
    jobs: (jobs, run) => run.workflow_id !== CI_ID ? jobs : [...jobs, mutate({ ...jobs[0],
      id: 55555, name: 'sonar-maintenance', url: `${API}/actions/jobs/55555`,
      html_url: `${WEB}/actions/runs/${run.id}/job/55555`, conclusion: 'skipped',
      runner_id: 0, runner_group_id: 0, runner_name: '', labels: [], started_at: null, completed_at: null })],
  });
}

test('ordinary CI retains exact optional skipped maintenance identity outside passing jobs', async () => {
  const result = await verifyCI(maintenanceFixture().input);
  assert.equal(result.workflows[0].jobs.length, 14);
  assert.equal(result.workflows[0].maintenance.length, 1);
  assert.equal(result.workflows[0].maintenance[0].status, 'completed');
  assert.equal(result.workflows[0].maintenance[0].conclusion, 'skipped');
  assert.equal(Object.hasOwn((await verifyCI(harness().input)).workflows[0], 'maintenance'), false);
});

test('maintenance compatibility rejects executing, arbitrary, or malformed skipped records', async () => {
  for (const change of [{ name: 'arbitrary-skipped' }, { conclusion: 'success' }, { conclusion: 'failure' },
    { conclusion: 'cancelled' }, { status: 'in_progress' }, { status: 'queued' },
    { started_at: 'bad' }, { completed_at: 'bad' }, { run_attempt: 0 }]) {
    await assert.rejects(verifyCI(maintenanceFixture(job => ({ ...job, ...change })).input));
  }
  await assert.rejects(verifyCI(maintenanceFixture(undefined, text => text.replace('workflow_dispatch', 'pull_request')).input), /canonical guarded/);
  await assert.rejects(verifyCI(maintenanceFixture(undefined, () => 'jobs: {}').input), /canonical guarded/);
});

test('optional inventory mutation cannot survive a stable CI reread', async () => {
  const first = await verifyCI(maintenanceFixture().input);
  const second = await verifyCI(maintenanceFixture(job => ({ ...job, completed_at: CREATED })).input);
  assert.throws(() => assertUnchangedCI(first, second), /jobs or logical epoch changed/);
});
