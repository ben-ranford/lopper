'use strict';

const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { IncomingMessage } = require('node:http');
const { PassThrough } = require('node:stream');
const test = require('node:test');
const verifyReuseSuppression = require('./reuse_suppression.js');
const { createVerifier, collectPages, signedArchiveURL, requestArtifact, downloadArchive, MAX_ARCHIVE_BYTES } = verifyReuseSuppression.testables;

const HEAD = 'a'.repeat(40);
const BASE = 'b'.repeat(40);
const SNAPSHOT = { version: 1, repository: 'owner/repo', repository_id: 12, head_repository_id: 34, pull_number: 56, head: HEAD, base: BASE, base_ref: 'main' };
const REPO = { owner: 'owner', repo: 'repo' };
const WORKFLOW = { id: 78, path: '.github/workflows/ci.yml', name: 'ci' };

function makeRun(id = 100) {
  return {
    id, run_attempt: 2, workflow_id: WORKFLOW.id, path: WORKFLOW.path, name: 'ci', event: 'pull_request', head_sha: HEAD,
    repository: { id: 12 }, head_repository: { id: 34 }, status: 'completed', conclusion: 'success',
    pull_requests: [{ number: 56, head: { sha: HEAD, repo: { id: 34 } }, base: { sha: BASE, ref: 'main', repo: { id: 12 } } }],
  };
}

function makeJob(artifact = 200, run = makeRun()) {
  return { name: `suppression-artifact-${artifact}`, run_id: run.id, run_attempt: run.run_attempt, head_sha: HEAD, status: 'completed', conclusion: 'success' };
}

function receipt(run = makeRun(), artifact = 200) {
  return { headSHA: HEAD, baseSHA: BASE, runId: run.id, runAttempt: run.run_attempt, artifactId: artifact, suppressionCount: 0 };
}

function harness() {
  const state = { run: makeRun(), jobs: [makeJob()], workflow: { ...WORKFLOW }, runs: undefined, calls: [], adapterCalls: [] };
  const github = { rest: { actions: {
    getWorkflow: async () => ({ data: state.workflow }),
    listWorkflowRuns: async (parameters) => {
      state.calls.push({ method: 'runs', ...parameters });
      const runs = state.runs ?? [state.run];
      return { data: { total_count: runs.length, workflow_runs: runs.slice((parameters.page - 1) * 100, parameters.page * 100) } };
    },
    getWorkflowRun: async (parameters) => {
      state.calls.push({ method: 'run', ...parameters });
      return { data: state.run };
    },
    listJobsForWorkflowRunAttempt: async (parameters) => {
      state.calls.push({ method: 'jobs', ...parameters });
      return { data: { total_count: state.jobs.length, jobs: state.jobs.slice((parameters.page - 1) * 100, parameters.page * 100) } };
    },
  } } };
  state.arguments = { github, context: { repo: REPO }, snapshot: { ...SNAPSHOT }, token: 'test-token' };
  state.download = async (repo, artifactId, token) => {
    state.calls.push({ method: 'download', repo, artifactId, token });
    return Buffer.from('archive');
  };
  state.adapter = async (input) => {
    state.adapterCalls.push(input);
    return receipt();
  };
  state.verify = () => createVerifier(() => state.adapter, state.download)(state.arguments);
  return state;
}

test('binds the adapter to the exact run attempt and locator, then rechecks both', async () => {
  const fixture = harness();
  assert.deepEqual(await fixture.verify(), receipt());
  const input = fixture.adapterCalls[0];
  assert.deepEqual(input.expected, { repoId: 12, headRepoId: 34, pullNumber: 56, headSHA: HEAD, baseSHA: BASE, runId: 100, runAttempt: 2 });
  assert.equal(input.artifactId, 200);
  assert.equal(input.archive.toString(), 'archive');
  assert.deepEqual(fixture.calls.filter((call) => call.method === 'jobs').map((call) => call.attempt_number), [2, 2]);
  assert.equal(fixture.calls.filter((call) => call.method === 'runs').length, 2);
});

test('chooses the newest run regardless of API order without an older-green fallback', async () => {
  const fixture = harness();
  fixture.runs = [makeRun(99), fixture.run, makeRun(98)];
  fixture.run.conclusion = 'failure';
  await assert.rejects(fixture.verify(), /latest producer is not successful/);
  assert.equal(fixture.adapterCalls.length, 0);
});

test('does not fall back when the newest same-head run belongs to another pull', async () => {
  const fixture = harness();
  fixture.runs = [makeRun(99), fixture.run];
  fixture.run.pull_requests[0].number = 57;
  await assert.rejects(fixture.verify(), /snapshot mismatch/);
});

const invalidRunCases = [
  ['workflow ID', (run) => { run.workflow_id = 79; }],
  ['workflow path', (run) => { run.path = '.github/workflows/other.yml'; }],
  ['workflow name', (run) => { run.name = 'other'; }],
  ['event', (run) => { run.event = 'push'; }],
  ['head', (run) => { run.head_sha = BASE; }],
  ['repository', (run) => { run.repository.id = 13; }],
  ['head repository', (run) => { run.head_repository.id = 35; }],
  ['attempt', (run) => { run.run_attempt = 0; }],
  ['missing association', (run) => { run.pull_requests = []; }],
  ['ambiguous association', (run) => { run.pull_requests.push(run.pull_requests[0]); }],
  ['stale base', (run) => { run.pull_requests[0].base.sha = HEAD; }],
  ['base ref', (run) => { run.pull_requests[0].base.ref = 'other'; }],
  ['pull head', (run) => { run.pull_requests[0].head.sha = BASE; }],
  ['pull repository', (run) => { run.pull_requests[0].base.repo.id = 13; }],
  ['pull head repository', (run) => { run.pull_requests[0].head.repo.id = 35; }],
  ['pending', (run) => { run.status = 'in_progress'; }],
];
for (const [name, mutate] of invalidRunCases) {
  test(`rejects producer ${name}`, async () => {
    const fixture = harness();
    mutate(fixture.run);
    await assert.rejects(fixture.verify(), /Reuse suppression:/);
    assert.equal(fixture.adapterCalls.length, 0);
  });
}

test('rejects invalid workflow identity, missing and duplicate runs', async () => {
  for (const change of [
    (state) => { state.workflow.path = 'other'; },
    (state) => { state.workflow.id = 0; },
    (state) => { state.runs = []; },
    (state) => { state.runs = [state.run, state.run]; },
    (state) => { state.runs = [makeRun(101)]; },
  ]) {
    const fixture = harness();
    change(fixture);
    await assert.rejects(fixture.verify(), /Reuse suppression:/);
  }
});

test('rejects malformed snapshots before any API access', async () => {
  for (const change of [
    (snapshot) => { snapshot.extra = true; },
    (snapshot) => { delete snapshot.version; },
    (snapshot) => { snapshot.version = 2; },
    (snapshot) => { snapshot.repository = 'elsewhere/repo'; },
    (snapshot) => { snapshot.head = 'abc'; },
    (snapshot) => { snapshot.pull_number = -1; },
    (snapshot) => { snapshot.base_ref = ''; },
  ]) {
    const fixture = harness();
    change(fixture.arguments.snapshot);
    await assert.rejects(fixture.verify(), /Reuse suppression:/);
    assert.equal(fixture.calls.length, 0);
  }
});

test('rejects malformed repository context', async () => {
  const fixture = harness();
  fixture.arguments.context = { repo: { owner: '../owner', repo: 'repo' } };
  await assert.rejects(fixture.verify(), /invalid repository/);
});

const invalidLocatorCases = [
  ['missing', (state) => { state.jobs = []; }],
  ['duplicate', (state) => { state.jobs.push(makeJob(201)); }],
  ['malformed', (state) => { state.jobs[0].name += '-tail'; }],
  ['unsafe ID', (state) => { state.jobs[0].name = `suppression-artifact-${Number.MAX_SAFE_INTEGER + 1}`; }],
  ['zero', (state) => { state.jobs[0].name = 'suppression-artifact-0'; }],
  ['unnamed', (state) => { delete state.jobs[0].name; }],
  ['other run', (state) => { state.jobs[0].run_id += 1; }],
  ['other attempt', (state) => { state.jobs[0].run_attempt += 1; }],
  ['other head', (state) => { state.jobs[0].head_sha = BASE; }],
  ['failed', (state) => { state.jobs[0].conclusion = 'failure'; }],
  ['pending', (state) => { state.jobs[0].status = 'queued'; }],
];
for (const [name, mutate] of invalidLocatorCases) {
  test(`rejects ${name} artifact locator`, async () => {
    const fixture = harness();
    mutate(fixture);
    await assert.rejects(fixture.verify(), /Reuse suppression:/);
    assert.equal(fixture.adapterCalls.length, 0);
  });
}

test('fully paginates producer and job lists', async () => {
  const fixture = harness();
  fixture.run = makeRun(150);
  fixture.runs = Array.from({ length: 150 }, (_, index) => makeRun(index + 1));
  fixture.jobs = [...Array.from({ length: 100 }, () => ({ name: 'other-job' })), makeJob(200, fixture.run)];
  fixture.adapter = async () => receipt(fixture.run);
  assert.deepEqual(await fixture.verify(), receipt(fixture.run));
  assert.equal(fixture.calls.filter((call) => call.method === 'jobs' && call.page === 2).length, 2);
  assert.equal(fixture.calls.filter((call) => call.method === 'runs' && call.page === 2).length, 2);
});

test('rejects truncated, growing, oversized or malformed paginated responses', async () => {
  const first = { total_count: 101, items: Array(100).fill(0) };
  const invalid = [
    [{ total_count: 1001, items: [] }],
    [{ total_count: 1, items: [] }],
    [{ total_count: 0, items: [0] }],
    [{ total_count: 1, items: {} }],
    [{ total_count: -1, items: [] }],
    [{ total_count: 100, items: Array(101).fill(0) }],
    [first, { total_count: 102, items: [0, 0] }],
  ];
  for (const pages of invalid) {
    await assert.rejects(collectPages(async ({ page }) => ({ data: pages[page - 1] }), {}, 'items', 1000), /Reuse suppression:/);
  }
});

test('inspects all 1,000 permitted workflow results', async () => {
  let calls = 0;
  const result = await collectPages(async () => {
    calls += 1;
    return { data: { total_count: 1000, items: Array(100).fill(0) } };
  }, {}, 'items', 1000);
  assert.equal(result.length, 1000);
  assert.equal(calls, 10);
});

test('rejects changed producer, retried attempt and locator after the adapter', async () => {
  for (const change of [
    (state) => { state.run = makeRun(101); },
    (state) => { state.run.run_attempt += 1; },
    (state) => { state.jobs = [makeJob(201)]; },
  ]) {
    const fixture = harness();
    fixture.adapter = async () => {
      change(fixture);
      return receipt();
    };
    await assert.rejects(fixture.verify(), /Reuse suppression:/);
  }
});

test('rejects mismatched or extended adapter receipts', async () => {
  for (const output of [{ ...receipt(), suppressionCount: 1 }, { ...receipt(), extra: true }, { ...receipt(), runId: 99 }, undefined]) {
    const fixture = harness();
    fixture.adapter = async () => output;
    await assert.rejects(fixture.verify(), /adapter receipt mismatch/);
  }
});

test('normalizes API, adapter, loader and download errors without leaking secrets', async () => {
  for (const failure of ['api', 'adapter', 'download', 'loader']) {
    const fixture = harness();
    const fail = () => { throw new Error('secret-token or signed-url'); };
    if (failure === 'api') fixture.arguments.github.rest.actions.getWorkflow = fail;
    if (failure === 'adapter') fixture.adapter = fail;
    if (failure === 'download') fixture.download = fail;
    const verify = failure === 'loader' ? createVerifier(fail, fixture.download) : createVerifier(() => fixture.adapter, fixture.download);
    await assert.rejects(verify(fixture.arguments), { message: 'Reuse suppression: evidence could not be verified' });
  }
});

test('production export validates inputs without accepting injected workflow dependencies', async () => {
  const fixture = harness();
  fixture.arguments.snapshot = null;
  fixture.arguments.download = () => { assert.fail('workflow may not inject downloader'); };
  await assert.rejects(verifyReuseSuppression(fixture.arguments), /invalid snapshot fields/);
});

test('accepts only credential-free HTTPS artifact storage redirects', () => {
  for (const host of ['results.blob.core.windows.net', 'artifact.githubusercontent.com']) {
    assert.equal(signedArchiveURL(`https://${host}/archive?signature=value`).hostname, host);
  }
  for (const url of [undefined, 'invalid', 'http://results.blob.core.windows.net/a', 'https://githubusercontent.com/a', 'https://evilgithubusercontent.com/a', 'https://api.github.com/a', 'https://u:p@results.blob.core.windows.net/a', 'https://results.blob.core.windows.net:444/a', 'https://results.blob.core.windows.net/a#fragment', 'https://results.blob.core.windows.net.evil.example/a']) {
    assert.throws(() => signedArchiveURL(url), /Reuse suppression:/);
  }
});

test('sends authorization only to the fixed API origin', async () => {
  const calls = [];
  const request = async (url, headers, redirect) => {
    calls.push({ url, headers, redirect });
    return redirect ? 'https://results.blob.core.windows.net/archive?signature=value' : Buffer.from('archive');
  };
  assert.equal((await downloadArchive(REPO, 200, 'private-token', request)).toString(), 'archive');
  assert.equal(calls[0].url.href, 'https://api.github.com/repos/owner/repo/actions/artifacts/200/zip');
  assert.equal(calls[0].headers.Authorization, 'Bearer private-token');
  assert.equal(calls[1].headers.Authorization, undefined);
  assert.equal(calls[1].redirect, false);
  for (const token of ['', 'bad\nheader']) await assert.rejects(downloadArchive(REPO, 200, token, request), /missing artifact token/);
});

function fakeGet({ status = 200, headers = {}, chunks = [Buffer.from('archive')], event, requestEvent } = {}) {
  return (url, options, callback) => {
    assert.equal(options.timeout, 15000);
    const request = new EventEmitter();
    request.destroy = (error) => {
      if (error) request.emit('error', error);
      request.emit('close');
    };
    queueMicrotask(() => {
      if (requestEvent) {
        request.emit(requestEvent);
        request.destroy();
        return;
      }
      const response = new EventEmitter();
      response.statusCode = status;
      response.headers = headers;
      response.destroy = () => request.destroy();
      callback(response);
      if (event) response.emit(event);
      else {
        for (const chunk of chunks) response.emit('data', chunk);
        response.emit('end');
      }
      request.destroy();
    });
    return request;
  };
}

test('reads bounded archives and handles the single API redirect', async () => {
  const url = new URL('https://example.com/');
  assert.equal((await requestArtifact(url, {}, false, fakeGet())).toString(), 'archive');
  assert.equal((await requestArtifact(url, {}, false, fakeGet({ headers: { 'content-length': '7' } }))).length, 7);
  assert.equal(await requestArtifact(url, {}, true, fakeGet({ status: 302, headers: { location: 'https://storage.invalid' } })), 'https://storage.invalid');
});

test('settles a native API redirect before disposing its response', async () => {
  function nativeResponseGet(statusCode, headers, disposeAfterCallback = false) {
    return (url, options, callback) => {
      const request = new EventEmitter();
      request.destroy = (error) => {
        if (error) request.emit('error', error);
        request.emit('close');
      };
      queueMicrotask(() => {
        const response = new IncomingMessage(new PassThrough());
        response.statusCode = statusCode;
        response.headers = headers;
        callback(response);
        if (disposeAfterCallback) response.destroy();
        request.emit('close');
      });
      return request;
    };
  }

  const url = new URL('https://api.github.com/');
  const location = 'https://results.blob.core.windows.net/archive?signature=value';
  assert.equal(await requestArtifact(url, {}, true, nativeResponseGet(302, { location })), location);
  await assert.rejects(
    requestArtifact(url, {}, false, nativeResponseGet(200, {}, true)),
    /Reuse suppression: artifact response aborted/,
  );
});

test('rejects failed responses, further redirects, truncated and oversized bodies', async () => {
  const cases = [
    { status: 403 }, { status: 302 }, { chunks: [] },
    { headers: { 'content-length': 'wrong' } },
    { headers: { 'content-length': '8' } },
    { headers: { 'content-length': String(MAX_ARCHIVE_BYTES + 1) } },
    { chunks: [Buffer.alloc(MAX_ARCHIVE_BYTES), Buffer.from('x')] },
    { event: 'error' }, { event: 'aborted' }, { requestEvent: 'error' }, { requestEvent: 'timeout' },
  ];
  for (const input of cases) {
    await assert.rejects(requestArtifact(new URL('https://example.com/'), {}, false, fakeGet(input)), /Reuse suppression:/);
  }
  await assert.rejects(requestArtifact(new URL('https://example.com/'), {}, true, fakeGet()), /unexpected artifact response/);
});

test('bounds a connection that never emits a response', async (context) => {
  context.mock.timers.enable({ apis: ['setTimeout'] });
  const get = () => {
    const request = new EventEmitter();
    request.destroy = () => request.emit('close');
    return request;
  };
  const result = requestArtifact(new URL('https://example.com/'), {}, false, get);
  context.mock.timers.tick(15000);
  await assert.rejects(result, /deadline exceeded/);
});
