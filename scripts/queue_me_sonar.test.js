'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verifySonar } = require('./queue_me_sonar.js');

const PROJECT = 'ben-ranford_lopper';
const HEAD = 'a'.repeat(40);
const INPUT = {
  owner: 'ben-ranford', repo: 'lopper', pullNumber: 42, headSHA: HEAD, baseRef: 'main',
};

function analysis() {
  return {
    key: '42',
    base: 'main',
    target: 'main',
    url: 'https://github.com/ben-ranford/lopper/pull/42',
    commit: { sha: HEAD },
    analysisDate: '2026-09-30T15:20:35+0000',
    pullRequestUuidV1: 'pr-component',
  };
}

function task() {
  return {
    id: 'task-42',
    type: 'REPORT',
    componentId: 'pr-component',
    componentKey: PROJECT,
    analysisId: 'analysis-42',
    status: 'SUCCESS',
    pullRequest: '42',
    submittedAt: '2026-09-30T15:21:18+0000',
    startedAt: '2026-09-30T15:21:18+0000',
    executedAt: '2026-09-30T15:21:21+0000',
  };
}

function fixedIssue(index) {
  return { key: `issue-${index}`, project: PROJECT, pullRequest: '42', issueStatus: 'FIXED' };
}

function pageBody(items, page, field) {
  const result = {
    paging: { pageIndex: page, pageSize: 500, total: items.length },
    [field]: items.slice((page - 1) * 500, page * 500),
  };
  if (field === 'issues') {
    Object.assign(result, { total: items.length, p: page, ps: 500 });
  }
  return result;
}

function harness({ pulls = [analysis()], current = task(), queue = [], issues = [], hotspots = [], transform } = {}) {
  const requests = [];
  const counts = new Map();
  const fetchImpl = async (rawURL, options) => {
    const url = new URL(rawURL);
    requests.push({ url, options });
    const endpoint = url.pathname;
    const count = (counts.get(endpoint) || 0) + 1;
    counts.set(endpoint, count);
    let body;
    switch (endpoint) {
      case '/api/project_pull_requests/list': body = { pullRequests: pulls }; break;
      case '/api/ce/component': body = { queue, current }; break;
      case '/api/qualitygates/project_status': body = {
        projectStatus: { status: 'OK', ignoredConditions: false, conditions: [{ status: 'OK' }] },
      }; break;
      case '/api/issues/search': body = pageBody(issues, Number(url.searchParams.get('p')), 'issues'); break;
      case '/api/hotspots/search': body = pageBody(hotspots, Number(url.searchParams.get('p')), 'hotspots'); break;
      default: assert.fail(`Unexpected API path ${endpoint}`);
    }
    const responseBody = transform ? transform(structuredClone(body), endpoint, count) : body;
    return new Response(JSON.stringify(responseBody), { headers: { 'content-type': 'application/json' } });
  };
  return { requests, fetchImpl, run: (overrides = {}) => verifySonar({ ...INPUT, fetchImpl, ...overrides }) };
}

async function rejectsAudit(options, pattern) {
  await assert.rejects(harness(options).run(), (error) => {
    assert.equal(error.queuePauseMessage, error.message);
    assert.match(error.message, pattern);
    return true;
  });
}

test('returns serializable exact-head analysis evidence without GitHub API access', async () => {
  const audit = harness({ issues: [fixedIssue(1)] });
  const evidence = await audit.run({ github: { get rest() { throw new Error('must not read credentials'); } } });
  assert.deepEqual(JSON.parse(JSON.stringify(evidence)), {
    project: PROJECT, repository: 'ben-ranford/lopper', pullNumber: 42,
    headSHA: HEAD, baseRef: 'main', analysisDate: analysis().analysisDate,
    componentId: 'pr-component', taskId: 'task-42', analysisId: 'analysis-42',
    submittedAt: task().submittedAt, startedAt: task().startedAt, executedAt: task().executedAt,
    qualityGate: 'OK', activeOrWaivedIssues: 0, fixedIssueCount: 1, hotspots: 0,
  });
  for (const { url, options } of audit.requests) {
    assert.equal(url.origin, 'https://sonarcloud.io');
    assert.equal(options.redirect, 'error');
    assert.equal(options.credentials, 'omit');
    assert.equal(options.cache, 'no-store');
    assert.deepEqual(options.headers, { Accept: 'application/json' });
    assert.ok(options.signal instanceof AbortSignal);
  }
  const gate = audit.requests.find(({ url }) => url.pathname === '/api/qualitygates/project_status');
  assert.equal(gate.url.search, '?analysisId=analysis-42');
  const issueRequest = audit.requests.find(({ url }) => url.pathname === '/api/issues/search');
  assert.deepEqual([...issueRequest.url.searchParams.keys()], ['componentKeys', 'pullRequest', 'ps', 'p']);
  const ce = audit.requests.find(({ url }) => url.pathname === '/api/ce/component');
  assert.equal(ce.url.search, `?component=${PROJECT}`);
});

test('audits complete issue pagination including historical fixed findings', async () => {
  const audit = harness({ issues: Array.from({ length: 501 }, (_, index) => fixedIssue(index)) });
  assert.equal((await audit.run()).fixedIssueCount, 501);
  assert.equal(audit.requests.filter(({ url }) => url.pathname === '/api/issues/search').length, 2);
});

for (const issueStatus of ['OPEN', 'CONFIRMED', 'ACCEPTED', 'FALSE_POSITIVE', 'UNKNOWN', undefined]) {
  test(`rejects ${String(issueStatus)} findings even with an OK gate`, async () => {
    await rejectsAudit({ issues: [{ ...fixedIssue(1), issueStatus }] }, /active, accepted, false-positive/);
  });
}

test('rejects a waived finding on a later issue page', async () => {
  const issues = Array.from({ length: 501 }, (_, index) => fixedIssue(index));
  issues[500].issueStatus = 'ACCEPTED';
  await rejectsAudit({ issues }, /active, accepted, false-positive/);
});

for (const status of ['TO_REVIEW', 'REVIEWED', 'UNKNOWN']) {
  test(`rejects ${status} hotspots including reviews marked safe`, async () => {
    await rejectsAudit({ hotspots: [{ key: 'hotspot', status, resolution: 'SAFE' }] }, /security hotspots remain/);
  });
}

test('rejects stale heads, wrong project/base, missing and duplicate analyses', async (t) => {
  const cases = [
    ['wrong head', [{ ...analysis(), commit: { sha: 'b'.repeat(40) } }], /current pull request head/],
    ['missing head', [{ ...analysis(), commit: {} }], /current pull request head/],
    ['wrong base', [{ ...analysis(), base: 'other' }], /base identity/],
    ['wrong target', [{ ...analysis(), target: 'other' }], /base identity/],
    ['wrong repository', [{ ...analysis(), url: 'https://github.com/attacker/lopper/pull/42' }], /base identity/],
    ['missing component', [{ ...analysis(), pullRequestUuidV1: undefined }], /base identity/],
    ['missing', [], /missing or ambiguous/],
    ['duplicate', [analysis(), analysis()], /missing or ambiguous/],
  ];
  for (const [name, pulls, pattern] of cases) {
    await t.test(name, () => rejectsAudit({ pulls }, pattern));
  }
});

test('requires exact configured repository and valid expected identity before fetching', async () => {
  for (const overrides of [{ owner: 'other' }, { repo: 'other' }, { pullNumber: 0 },
    { pullNumber: '42' }, { headSHA: 'wrong' }, { baseRef: '' }]) {
    const audit = harness();
    await assert.rejects(audit.run(overrides), /Sonar audit paused/);
    assert.equal(audit.requests.length, 0);
  }
});

test('latest processing task must identify a successful candidate analysis', async (t) => {
  const cases = [
    ['pending', { queue: [{ id: 'queued' }] }, /processing is pending/],
    ['missing queue', { queue: null }, /queue inventory/],
    ['missing task', { current: null }, /Rerun this pull request/],
    ['another PR', { current: { ...task(), pullRequest: '43' } }, /Rerun this pull request/],
    ['main branch', { current: { ...task(), pullRequest: undefined } }, /Rerun this pull request/],
    ['wrong project', { current: { ...task(), componentKey: 'other_project' } }, /task identity/],
    ['wrong component', { current: { ...task(), componentId: 'other' } }, /task identity/],
    ['missing analysis', { current: { ...task(), analysisId: undefined } }, /task identity/],
    ['failed', { current: { ...task(), status: 'FAILED' } }, /not completed successfully/],
    ['cancelled', { current: { ...task(), status: 'CANCELED' } }, /not completed successfully/],
    ['in progress', { current: { ...task(), status: 'IN_PROGRESS' } }, /not completed successfully/],
    ['unknown', { current: { ...task(), status: undefined } }, /not completed successfully/],
  ];
  for (const [name, options, pattern] of cases) {
    await t.test(name, () => rejectsAudit(options, pattern));
  }
});

test('requires task timestamps ordered after the scanner analysis start', async (t) => {
  const cases = [
    ['missing', { ...task(), submittedAt: undefined }],
    ['invalid', { ...task(), submittedAt: 'not a date' }],
    ['task predates analysis', { ...task(), submittedAt: '2026-09-30T15:19:18+0000' }],
    ['start predates submit', { ...task(), startedAt: '2026-09-30T15:21:17+0000' }],
    ['execute predates start', { ...task(), executedAt: '2026-09-30T15:21:17+0000' }],
  ];
  for (const [name, current] of cases) {
    await t.test(name, () => rejectsAudit({ current }, /timestamps/));
  }
});

test('rejects missing, pending, failing, or waived quality gates', async (t) => {
  const gates = [undefined, { status: 'NONE' }, { status: 'WARN' }, { status: 'ERROR' },
    { status: 'OK', ignoredConditions: true, conditions: [{ status: 'OK' }] },
    { status: 'OK', conditions: [{ status: 'OK' }] },
    { status: 'OK', ignoredConditions: false, conditions: [] },
    { status: 'OK', ignoredConditions: false, conditions: [{ status: 'NO_VALUE' }] }];
  for (const [index, gate] of gates.entries()) {
    await t.test(String(index), () => rejectsAudit({
      transform: (body, endpoint) => endpoint.includes('qualitygates') ? { projectStatus: gate } : body,
    }, /quality gate/));
  }
});

test('rejects truncated, oversized, repeated, or unstable issue pages', async (t) => {
  const issues = Array.from({ length: 501 }, (_, index) => fixedIssue(index));
  const cases = [
    ['truncated', (body) => ({ ...body, issues: [] })],
    ['oversized', (body) => ({ ...body, paging: { ...body.paging, total: 10001 } })],
    ['wrong page', (body) => ({ ...body, paging: { ...body.paging, pageIndex: 0 } })],
    ['metadata', (body) => ({ ...body, total: 0 })],
    ['total drift', (body, count) => count === 2 ? { ...body, paging: { ...body.paging, total: 500 } } : body],
    ['duplicate key', (body, count) => count === 2 ? { ...body, issues: [fixedIssue(0)] } : body],
    ['wrong PR', (body) => ({ ...body, issues: body.issues.map((issue) => ({ ...issue, pullRequest: '43' })) })],
    ['wrong project', (body) => ({ ...body, issues: body.issues.map((issue) => ({ ...issue, project: 'other' })) })],
  ];
  for (const [name, alter] of cases) {
    await t.test(name, () => rejectsAudit({
      issues, transform: (body, endpoint, count) => endpoint.includes('issues/search') ? alter(body, count) : body,
    }, /inventory|pagination/));
  }
});

test('rejects analysis and processing changes during evidence collection', async (t) => {
  const cases = [
    ['analysis date', '/api/project_pull_requests/list', (body) => {
      body.pullRequests[0].analysisDate = '2026-09-30T15:20:36+0000'; return body;
    }],
    ['analysis head', '/api/project_pull_requests/list', (body) => {
      body.pullRequests[0].commit.sha = 'b'.repeat(40); return body;
    }],
    ['task id', '/api/ce/component', (body) => { body.current.id = 'new-task'; return body; }],
    ['analysis id', '/api/ce/component', (body) => { body.current.analysisId = 'new-analysis'; return body; }],
    ['new pending task', '/api/ce/component', (body) => { body.queue.push({ id: 'new-task' }); return body; }],
    ['new failed task', '/api/ce/component', (body) => { body.current.status = 'FAILED'; return body; }],
    ['other latest task', '/api/ce/component', (body) => { body.current.pullRequest = '43'; return body; }],
  ];
  for (const [name, target, alter] of cases) {
    await t.test(name, () => rejectsAudit({
      transform: (body, endpoint, count) => endpoint === target && count === 2 ? alter(body) : body,
    }, /Sonar audit paused/));
  }
});

test('network and malformed response errors do not reflect untrusted error bodies', async (t) => {
  const responses = [
    () => { throw new Error('sensitive transport detail'); },
    () => new Response('sensitive response detail', { status: 500 }),
    () => new Response('{malformed', { headers: { 'content-type': 'application/json' } }),
    () => new Response('{}', { headers: { 'content-type': 'text/html' } }),
    () => new Response('{}', { headers: { 'content-type': 'application/json', 'content-length': '2097153' } }),
    () => new Response(' '.repeat(2097153), { headers: { 'content-type': 'application/json' } }),
    () => ({ ok: true, redirected: true }),
    () => new Response('null', { headers: { 'content-type': 'application/json' } }),
  ];
  for (const [index, fetchImpl] of responses.entries()) {
    await t.test(String(index), () => assert.rejects(verifySonar({ ...INPUT, fetchImpl }), (error) => {
      assert.match(error.message, /Sonar audit paused/);
      assert.doesNotMatch(error.message, /sensitive/);
      assert.ok(error.message.length < 300);
      return true;
    }));
  }
});

test('bounds an API that never resolves with a timeout and abort signal', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let signal;
  const promise = verifySonar({ ...INPUT, fetchImpl: (_url, options) => {
    signal = options.signal;
    return new Promise(() => {
      // Deliberately unresolved transport exercises the request deadline.
    });
  } });
  const rejection = assert.rejects(promise, /timed out/);
  t.mock.timers.tick(10000);
  await rejection;
  assert.equal(signal.aborted, true);
});

test('bounds the total audit even when every individual response is timely', async (t) => {
  let now = 0;
  t.mock.method(Date, 'now', () => now);
  const audit = harness();
  const fetchImpl = (...args) => {
    now += 11000;
    return audit.fetchImpl(...args);
  };
  await assert.rejects(audit.run({ fetchImpl }), /audit exceeded its time limit/);
  assert.equal(audit.requests.length, 6);
});

test('bounds a response body that stalls after headers arrive', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const fetchImpl = () => new Response(new ReadableStream({
    pull() {
      return new Promise(() => {
        // Deliberately unresolved body exercises the response deadline.
      });
    },
  }), { headers: { 'content-type': 'application/json' } });
  const promise = verifySonar({ ...INPUT, fetchImpl });
  const rejection = assert.rejects(promise, /timed out/);
  await Promise.resolve();
  t.mock.timers.tick(10000);
  await rejection;
});

test('rejects incomplete hotspot paging instead of treating it as zero', async () => {
  await rejectsAudit({ transform: (body, endpoint) => endpoint.includes('hotspots/search')
    ? { ...body, paging: { ...body.paging, total: 1 } } : body,
  }, /truncated or inconsistent/);
});
