'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const childProcess = require('node:child_process');
const { gitBlobIdentity } = require('../../queue_me_git_object');
const { verifyCI, assertUnchangedCI } = require('../../queue_me_ci');
const { API, WEB, CREATED, CI_ID, PATHS, harness } = require('./ci_fixture.cjs');

const maintenanceSource = "jobs:\n  sonar-maintenance:\n    if: ${{ github.event_name == 'workflow_dispatch' && inputs.sonar_operation != 'none' }}\n    runs-on: ubuntu-latest\n";
const corpus = new Map([
  [maintenanceSource, '2f6263b02f0b417799c71dd202f62920b3f648c3'],
  [maintenanceSource.replace('workflow_dispatch', 'pull_request'), '6ec07b8e5e47cfad233fda64aed5784c630a7b7d'],
  ['jobs: {}', '5e72412993045bcf590e2d4c5fe3a16a9c908272'],
]);

function verifiedFixtureIdentity(bytes) {
  const expected = corpus.get(bytes.toString('utf8'));
  assert(expected && bytes.equals(Buffer.from(bytes.toString('utf8'))), 'unknown maintenance corpus bytes');
  const actual = gitBlobIdentity(bytes);
  assert.equal(actual, expected, 'maintenance independent Git blob golden');
  return actual;
}

function simulateMaintenanceBackend(t) {
  t.mock.method(os, 'platform', () => 'linux');
  let admitted;
  const chmod = fs.chmodSync;
  const permissions = new Set();
  t.mock.method(fs, 'chmodSync', (directory, mode) => {
    assert.equal(mode, 0o700);
    const result = chmod(directory, mode);
    permissions.add(fs.realpathSync(directory));
    return result;
  });
  t.mock.method(childProcess, 'spawnSync', (executable, argv, options) => {
    assert.equal(executable, '/usr/bin/git');
    assert.equal(fs.realpathSync(options.cwd), options.cwd);
    assert(permissions.has(options.cwd), 'owned cwd must receive chmod0700 before process launch');
    assert.deepEqual(fs.readdirSync(options.cwd), []);
    assert.deepEqual(options.env, { PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null', GIT_NO_REPLACE_OBJECTS: '1',
      GIT_CEILING_DIRECTORIES: path.dirname(options.cwd) });
    const actualKeys = Object.keys(options).sort((left, right) => left.localeCompare(right));
    const expectedKeys = ['cwd', 'shell', 'stdio', 'timeout', 'killSignal', 'maxBuffer', 'env',
      ...(argv[0] === 'version' ? [] : ['input'])].sort((left, right) => left.localeCompare(right));
    assert.deepEqual(actualKeys, expectedKeys);
    if (['linux', 'darwin'].includes(process.platform)) assert.equal(fs.statSync(options.cwd).mode & 0o777, 0o700);
    assert.equal(options.shell, false);
    assert.deepEqual(options.stdio, ['pipe', 'pipe', 'pipe']);
    assert.equal(options.timeout, 10000);
    assert.equal(options.killSignal, 'SIGKILL');
    if (argv[0] === 'version') {
      assert.deepEqual(argv, ['version', '--build-options']);
      assert.equal(Object.hasOwn(options, 'input'), false);
      assert.equal(options.maxBuffer, 65536);
      assert.equal(admitted, undefined);
      admitted = options.cwd;
      return { status: 0, signal: null, stdout: Buffer.from('SHA-1: SHA1_DC\n'), stderr: Buffer.alloc(0) };
    }
    assert.equal(admitted, options.cwd);
    admitted = undefined;
    assert.deepEqual(argv, ['-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
      'hash-object', '-t', 'blob', '--stdin', '--no-filters']);
    assert.equal(options.maxBuffer, 4096);
    assert(Buffer.isBuffer(options.input));
    const expected = corpus.get(options.input.toString('utf8'));
    assert(expected && options.input.equals(Buffer.from(options.input.toString('utf8'))), 'unknown simulated corpus');
    return { status: 0, signal: null, stdout: Buffer.from(`${expected}\n`), stderr: Buffer.alloc(0) };
  });
}

function maintenanceTest(name, run) {
  test(name, async t => {
    if (!['linux', 'darwin'].includes(process.platform)) simulateMaintenanceBackend(t);
    try { await run(t); } finally { t.mock.restoreAll(); }
  });
}

function maintenanceFixture(mutate = job => job, sourceTransform = value => value) {
  const source = sourceTransform(maintenanceSource);
  const bytes = Buffer.from(source);
  return harness({
    source: (data, args) => args.path === PATHS[0] ? { ...data, encoding: 'base64',
      content: bytes.toString('base64'), sha: verifiedFixtureIdentity(bytes) } : data,
    jobs: (jobs, run) => run.workflow_id !== CI_ID ? jobs : [...jobs, mutate({ ...jobs[0],
      id: 55555, name: 'sonar-maintenance', url: `${API}/actions/jobs/55555`,
      html_url: `${WEB}/actions/runs/${run.id}/job/55555`, conclusion: 'skipped',
      runner_id: 0, runner_group_id: 0, runner_name: '', labels: [], started_at: null, completed_at: null })],
  });
}

maintenanceTest('ordinary CI retains exact optional skipped maintenance identity outside passing jobs', async () => {
  const result = await verifyCI(maintenanceFixture().input);
  assert.equal(result.workflows[0].jobs.length, 14);
  assert.equal(result.workflows[0].maintenance.length, 1);
  assert.equal(result.workflows[0].maintenance[0].status, 'completed');
  assert.equal(result.workflows[0].maintenance[0].conclusion, 'skipped');
  assert.equal(Object.hasOwn((await verifyCI(harness().input)).workflows[0], 'maintenance'), false);
});

for (const change of [{ name: 'arbitrary-skipped' }, { conclusion: 'success' }, { conclusion: 'failure' },
  { conclusion: 'cancelled' }, { status: 'in_progress' }, { status: 'queued' },
  { started_at: 'bad' }, { completed_at: 'bad' }, { run_attempt: 0 }]) {
  maintenanceTest(`maintenance compatibility rejects ${JSON.stringify(change)}`, async () => {
    await assert.rejects(verifyCI(maintenanceFixture(job => ({ ...job, ...change })).input));
  });
}

maintenanceTest('maintenance compatibility rejects malformed trusted source guards', async () => {
  await assert.rejects(verifyCI(maintenanceFixture(undefined, text => text.replace('workflow_dispatch', 'pull_request')).input), /canonical guarded/);
  await assert.rejects(verifyCI(maintenanceFixture(undefined, () => 'jobs: {}').input), /canonical guarded/);
});

maintenanceTest('optional inventory mutation cannot survive a stable CI reread', async () => {
  const first = await verifyCI(maintenanceFixture().input);
  const second = await verifyCI(maintenanceFixture(job => ({ ...job, completed_at: CREATED })).input);
  assert.throws(() => assertUnchangedCI(first, second), /jobs or logical epoch changed/);
});

module.exports = { maintenanceFixture, maintenanceSource, simulateMaintenanceBackend };
