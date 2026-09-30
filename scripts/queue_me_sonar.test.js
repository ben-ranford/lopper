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

function associatedPull() {
  return {
    number: 42, url: 'https://api.github.com/repos/ben-ranford/lopper/pulls/42',
    head: { sha: HEAD },
    base: { ref: 'main', sha: 'b'.repeat(40), repo: { url: 'https://api.github.com/repos/ben-ranford/lopper' } },
  };
}

function app() {
  return { id: 12526, slug: 'sonarqubecloud', owner: { id: 545988, login: 'SonarSource' } };
}

function check() {
  return {
    id: 80, check_suite: { id: 70 }, name: 'SonarCloud Code Analysis', app: app(),
    head_sha: HEAD, url: 'https://api.github.com/repos/ben-ranford/lopper/check-runs/80',
    details_url: 'https://sonarcloud.io/dashboard?id=ben-ranford_lopper&pullRequest=42',
    status: 'completed', conclusion: 'success', started_at: '2026-09-30T15:20:35Z',
    completed_at: '2026-09-30T15:21:21Z', pull_requests: [associatedPull()],
  };
}

function suite() {
  return {
    id: 70, app: app(), head_sha: HEAD,
    repository: { full_name: 'ben-ranford/lopper', private: false },
    status: 'completed', conclusion: 'success', created_at: '2026-09-30T15:20:00Z',
    updated_at: '2026-09-30T15:21:22Z', pull_requests: [associatedPull()],
  };
}

function fork() {
  return {
    number: 42, url: 'https://api.github.com/repos/ben-ranford/lopper/pulls/42', state: 'open', draft: false,
    head: { sha: HEAD, ref: 'feature', repo: { id: 2, fork: true, full_name: 'contributor/lopper',
      url: 'https://api.github.com/repos/contributor/lopper' } },
    base: { sha: 'b'.repeat(40), ref: 'main', repo: { id: 1, full_name: 'ben-ranford/lopper', private: false,
      url: 'https://api.github.com/repos/ben-ranford/lopper' } },
  };
}

function forkHarness(options = {}) {
  return harness({ checks: [{ ...check(), pull_requests: [] }], suites: [{ ...suite(), pull_requests: [] }], ...options });
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

function harness({ pulls = [analysis()], checks = [check()], suites = [suite()], forkPull = fork(), issues = [], hotspots = [], transform } = {}) {
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
      case '/repos/ben-ranford/lopper/pulls/42': body = forkPull; break;
      case '/api/project_pull_requests/list': body = { pullRequests: pulls }; break;
      case `/repos/ben-ranford/lopper/commits/${HEAD}/check-runs`:
        body = { total_count: checks.length, check_runs: checks.slice((Number(url.searchParams.get('page')) - 1) * 100,
          Number(url.searchParams.get('page')) * 100) }; break;
      case `/repos/ben-ranford/lopper/commits/${HEAD}/check-suites`:
        body = { total_count: suites.length, check_suites: suites.slice((Number(url.searchParams.get('page')) - 1) * 100,
          Number(url.searchParams.get('page')) * 100) }; break;
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

test('returns exact-head evidence from public APIs without reading credentials', async () => {
  const audit = harness({ issues: [fixedIssue(1)] });
  const evidence = await audit.run({ github: { get rest() { throw new Error('must not read credentials'); } } });
  assert.deepEqual(JSON.parse(JSON.stringify(evidence)), {
    project: PROJECT, repository: 'ben-ranford/lopper', pullNumber: 42,
    headSHA: HEAD, baseRef: 'main', analysisDate: analysis().analysisDate,
    componentId: 'pr-component', checkRunId: 80, suiteId: 70,
    checkStartedAt: Date.parse(check().started_at), checkCompletedAt: Date.parse(check().completed_at),
    createdAt: suite().created_at, updatedAt: suite().updated_at,
    qualityGate: 'OK', activeOrWaivedIssues: 0, fixedIssueCount: 1, hotspots: 0,
  });
  for (const { url, options } of audit.requests) {
    assert.ok(['https://sonarcloud.io', 'https://api.github.com'].includes(url.origin));
    assert.equal(options.redirect, 'error');
    assert.equal(options.credentials, 'omit');
    assert.equal(options.cache, 'no-store');
    assert.equal(options.headers.Authorization, undefined);
    assert.ok(options.signal instanceof AbortSignal);
  }
  const gate = audit.requests.find(({ url }) => url.pathname === '/api/qualitygates/project_status');
  assert.equal(gate.url.search, `?projectKey=${PROJECT}&pullRequest=42`);
  const issueRequest = audit.requests.find(({ url }) => url.pathname === '/api/issues/search');
  assert.deepEqual([...issueRequest.url.searchParams.keys()], ['componentKeys', 'pullRequest', 'ps', 'p']);
  assert.equal(audit.requests.some(({ url }) => url.pathname.includes('/ce/')), false);
  const runs = audit.requests.find(({ url }) => url.pathname.endsWith('/check-runs'));
  assert.equal(runs.url.searchParams.get('filter'), 'all');
  assert.equal(runs.url.searchParams.get('app_id'), '12526');
});

test('audits complete issue pagination including historical fixed findings', async () => {
  const audit = harness({ issues: Array.from({ length: 501 }, (_, index) => fixedIssue(index)) });
  assert.equal((await audit.run()).fixedIssueCount, 501);
  assert.equal(audit.requests.filter(({ url }) => url.pathname === '/api/issues/search').length, 4);
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
    { pullNumber: '42' }, { headSHA: 'wrong' }, { baseRef: '' }, { baseSHA: 42 }]) {
    const audit = harness();
    await assert.rejects(audit.run(overrides), /Sonar audit paused/);
    assert.equal(audit.requests.length, 0);
  }
});

test('requires a successful exact-head trusted check and completed suite', async (t) => {
  const cases = [
    ['missing run', { checks: [] }, /check is missing/],
    ['untrusted app', { checks: [{ ...check(), app: { ...app(), id: 1 } }] }, /untrusted/],
    ['wrong slug', { checks: [{ ...check(), app: { ...app(), slug: 'imposter' } }] }, /untrusted/],
    ['wrong name', { checks: [{ ...check(), name: 'other' }] }, /untrusted/],
    ['wrong head', { checks: [{ ...check(), head_sha: 'c'.repeat(40) }] }, /untrusted/],
    ['wrong URL', { checks: [{ ...check(), url: 'https://example.com' }] }, /untrusted/],
    ['wrong PR', { checks: [{ ...check(), pull_requests: [{ ...associatedPull(), number: 43 }] }] }, /association/],
    ['foreign details', { checks: [{ ...check(), details_url: 'https://example.com' }] }, /untrusted/],
    ['pending', { checks: [{ ...check(), status: 'queued', started_at: null }] }, /pending/],
    ['failed', { checks: [{ ...check(), conclusion: 'failure' }] }, /did not succeed/],
    ['waived', { checks: [{ ...check(), conclusion: 'neutral' }] }, /did not succeed/],
    ['missing suite', { suites: [] }, /suite is missing/],
    ['rerun requested', { suites: [{ ...suite(), status: 'queued', conclusion: null }] }, /rerequested/],
    ['failed suite', { suites: [{ ...suite(), conclusion: 'failure' }] }, /did not succeed/],
    ['foreign suite', { suites: [{ ...suite(), repository: { full_name: 'other/repo', private: false } }] }, /untrusted/],
    ['stale analysis', { pulls: [{ ...analysis(), analysisDate: '2026-09-30T15:20:34+0000' }] }, /timestamps do not match/],
    ['missing date', { checks: [{ ...check(), started_at: null }] }, /timestamps/],
    ['completion before start', { checks: [{ ...check(), completed_at: '2026-09-30T15:19:00Z' }] }, /timestamps/],
  ];
  for (const [name, options, pattern] of cases) {
    await t.test(name, () => rejectsAudit(options, pattern));
  }
});

test('binds check association to expected base SHA when supplied', async () => {
  await harness().run({ baseSHA: 'b'.repeat(40) });
  await assert.rejects(harness().run({ baseSHA: 'c'.repeat(40) }), /base commit association/);
});

function olderCheck(overrides = {}) {
  return { ...check(), id: 79, url: 'https://api.github.com/repos/ben-ranford/lopper/check-runs/79',
    started_at: '2026-09-30T15:18:00Z', completed_at: '2026-09-30T15:19:00Z', ...overrides };
}

test('latest attempt takes precedence over an earlier result', async () => {
  await harness({ checks: [olderCheck({ conclusion: 'failure' }), check()] }).run();
  await rejectsAudit({ checks: [olderCheck(), { ...check(), conclusion: 'failure' }] }, /did not succeed/);
  await rejectsAudit({ checks: [check(), olderCheck({ status: 'queued', started_at: null })] }, /pending/);
  await rejectsAudit({ checks: [check(), olderCheck({ completed_at: '2026-09-30T15:22:00Z' })] }, /overlaps/);
  await rejectsAudit({ checks: [check(), olderCheck({ started_at: check().started_at, completed_at: check().completed_at })] }, /ambiguous/);
});

test('a newer suite without a matching run blocks a retained success', async () => {
  await rejectsAudit({ suites: [suite(), { ...suite(), id: 71, created_at: '2026-09-30T15:21:22Z' }] }, /newer or ambiguous/);
});

test('documented empty fork associations require two matching live canonical PR reads', async () => {
  const audit = forkHarness();
  assert.equal((await audit.run({ baseSHA: 'b'.repeat(40) })).checkRunId, 80);
  const githubRequests = audit.requests.filter(({ url }) => url.origin === 'https://api.github.com');
  assert.equal(githubRequests.length, 6);
  assert.equal(githubRequests.filter(({ url }) => url.pathname.endsWith('/pulls/42')).length, 2);
});

test('empty association fallback rejects nonforks, missing identities, and ineligible PRs', async (t) => {
  const cases = [
    ['nonfork', (pull) => { pull.head.repo.fork = false; }],
    ['missing fork', (pull) => { delete pull.head.repo; }],
    ['same id', (pull) => { pull.head.repo.id = pull.base.repo.id; }],
    ['same repository', (pull) => { pull.head.repo.full_name = 'ben-ranford/lopper'; }],
    ['wrong head', (pull) => { pull.head.sha = 'c'.repeat(40); }],
    ['wrong number', (pull) => { pull.number = 43; }],
    ['wrong URL', (pull) => { pull.url = 'https://example.com'; }],
    ['closed', (pull) => { pull.state = 'closed'; }],
    ['draft', (pull) => { pull.draft = true; }],
    ['foreign base', (pull) => { pull.base.repo.full_name = 'other/repo'; }],
    ['wrong base ref', (pull) => { pull.base.ref = 'other'; }],
    ['wrong base SHA', (pull) => { pull.base.sha = 'c'.repeat(40); }],
  ];
  for (const [name, mutate] of cases) {
    await t.test(name, async () => {
      const pull = fork(); mutate(pull);
      await assert.rejects(forkHarness({ forkPull: pull }).run({ baseSHA: 'b'.repeat(40) }), /Sonar audit paused/);
    });
  }
});

test('fallback never accepts missing or nonempty incorrect check associations', async () => {
  for (const pull_requests of [undefined, null, {}, [{ ...associatedPull(), number: 43 }]]) {
    const audit = forkHarness({ checks: [{ ...check(), pull_requests }] });
    await assert.rejects(audit.run(), /association/);
  }
});

test('fork evidence must retain head, base and source repository through the audit', async (t) => {
  const cases = [
    ['head', (pull) => { pull.head.sha = 'c'.repeat(40); }],
    ['base', (pull) => { pull.base.sha = 'c'.repeat(40); }],
    ['head ref', (pull) => { pull.head.ref = 'other'; }],
    ['source id', (pull) => { pull.head.repo.id = 3; }],
    ['source name', (pull) => {
      pull.head.repo.full_name = 'another/lopper'; pull.head.repo.url = 'https://api.github.com/repos/another/lopper';
    }],
  ];
  for (const [name, mutate] of cases) {
    await t.test(name, () => assert.rejects(forkHarness({ transform: (body, endpoint, count) => {
      if (endpoint.endsWith('/pulls/42') && count === 2) { mutate(body); }
      return body;
    } }).run(), /Sonar audit paused/));
  }
});

test('historical failed attempts can retain an earlier base commit', async () => {
  const oldAssociation = associatedPull();
  oldAssociation.base.sha = 'c'.repeat(40);
  const oldRun = olderCheck({ conclusion: 'failure', check_suite: { id: 69 }, pull_requests: [oldAssociation] });
  const oldSuite = { ...suite(), id: 69, conclusion: 'failure', created_at: '2026-09-30T15:17:00Z',
    pull_requests: [oldAssociation] };
  const audit = harness({ checks: [oldRun, check()], suites: [oldSuite, suite()] });
  await audit.run({ baseSHA: 'b'.repeat(40) });
});

test('independent sibling analyses never require a project-wide CE task', async () => {
  const audit = harness();
  await audit.run();
  assert.equal(audit.requests.some(({ url }) => url.pathname.includes('/ce/')), false);
});

test('fully paginates checks and suites before selecting a trusted attempt', async () => {
  const checks = Array.from({ length: 100 }, (_, index) => olderCheck({ id: 1000 + index,
    url: `https://api.github.com/repos/ben-ranford/lopper/check-runs/${1000 + index}`,
    started_at: new Date(Date.parse('2026-09-30T15:00:00Z') + index * 1000).toISOString(),
  }));
  const suites = Array.from({ length: 100 }, (_, index) => ({ id: index + 1000, app: { id: 1 } }));
  const audit = harness({ checks: [...checks, check()], suites: [...suites, suite()] });
  assert.equal((await audit.run()).checkRunId, 80);
  assert.equal(audit.requests.filter(({ url }) => url.pathname.endsWith('/check-runs')).length, 4);
  assert.equal(audit.requests.filter(({ url }) => url.pathname.endsWith('/check-suites')).length, 4);
});

test('rejects malformed, duplicate, truncated, or drifting GitHub pages', async (t) => {
  const cases = [
    ['missing', (body) => ({ ...body, check_runs: undefined })],
    ['truncated', (body) => ({ ...body, total_count: 2 })],
    ['oversized', (body) => ({ ...body, total_count: 1001 })],
    ['duplicate', (body) => ({ ...body, total_count: 2, check_runs: [check(), check()] })],
  ];
  for (const [name, change] of cases) {
    await t.test(name, () => rejectsAudit({
      transform: (body, endpoint) => endpoint.endsWith('/check-runs') ? change(body) : body,
    }, /inventory/));
  }
});

test('rejects pagination totals changing on a later GitHub page', async () => {
  const suites = Array.from({ length: 101 }, (_, index) => ({ id: index + 1000, app: { id: 1 } }));
  await rejectsAudit({ suites, transform: (body, endpoint, count) => {
    if (endpoint.endsWith('/check-suites') && count === 2) {
      return { total_count: 102, check_suites: [...body.check_suites, { id: 2000, app: { id: 1 } }] };
    }
    return body;
  } }, /changed during pagination/);
});

test('public rate limits pause without a credential fallback', async () => {
  for (const status of [403, 429]) {
    await assert.rejects(verifySonar({ ...INPUT, fetchImpl: () => new Response('private detail', { status }) }),
      /rate limit; retry/);
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

test('rejects evidence changes during collection', async (t) => {
  const cases = [
    ['analysis date', '/api/project_pull_requests/list', (body) => {
      body.pullRequests[0].analysisDate = '2026-09-30T15:20:36+0000'; return body;
    }],
    ['analysis head', '/api/project_pull_requests/list', (body) => {
      body.pullRequests[0].commit.sha = 'b'.repeat(40); return body;
    }],
    ['new failure', `/repos/ben-ranford/lopper/commits/${HEAD}/check-runs`, (body) => {
      body.check_runs[0].conclusion = 'failure'; return body;
    }],
    ['new rerun', `/repos/ben-ranford/lopper/commits/${HEAD}/check-suites`, (body) => {
      body.check_suites[0].status = 'queued'; return body;
    }],
    ['new finding', '/api/issues/search', () => pageBody([{ ...fixedIssue(1), issueStatus: 'ACCEPTED' }], 1, 'issues')],
    ['history changed', '/api/issues/search', () => pageBody([fixedIssue(1)], 1, 'issues')],
    ['hotspot appeared', '/api/hotspots/search', () => pageBody([{ key: 'new' }], 1, 'hotspots')],
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
