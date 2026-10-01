'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { createPublicAPI } = require('./queue_me_public_api');

function pause(message) {
  const error = new Error(`CI audit paused: ${message}`);
  error.queuePauseMessage = error.message;
  return error;
}

function harness() {
  const requests = [];
  const api = createPublicAPI({ pause, fetchImpl: async (url, options) => {
    requests.push({ url: new URL(url), options });
    return new Response('{"ok":true}', { headers: { 'content-type': 'application/json' } });
  } });
  return { api, requests };
}

test('shared public transport fixes both origins and GitHub repository without credentials', async () => {
  const { api, requests } = harness();
  assert.deepEqual(await api('actions/workflows/ci.yml/runs', { branch: 'feature/example' }, true), { ok: true });
  assert.deepEqual(await api('qualitygates/project_status', { projectKey: 'ben-ranford_lopper' }), { ok: true });
  assert.equal(requests[0].url.origin, 'https://api.github.com');
  assert.equal(requests[0].url.pathname, '/repos/ben-ranford/lopper/actions/workflows/ci.yml/runs');
  assert.equal(requests[0].url.searchParams.get('branch'), 'feature/example');
  assert.equal(requests[1].url.origin, 'https://sonarcloud.io');
  assert.equal(requests[1].url.pathname, '/api/qualitygates/project_status');
  assert.deepEqual(requests[0].options.headers,
    { Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' });
  assert.deepEqual(requests[1].options.headers, { Accept: 'application/json' });
  for (const { options } of requests) {
    assert.equal(options.credentials, 'omit');
    assert.equal(options.redirect, 'error');
    assert.equal(options.cache, 'no-store');
    assert.ok(options.signal instanceof AbortSignal);
  }
});

test('endpoint paths cannot escape fixed API origins or repository scope', async () => {
  const { api, requests } = harness();
  const endpoints = [undefined, '', '/', '//example.com', 'https://example.com', '../other', 'actions/../../other',
    '%2e%2e/other', 'actions/%2fother', 'actions\\other', 'actions?query=value', 'actions#fragment',
    'actions//runs', 'actions/./runs', 'x'.repeat(1025)];
  for (const endpoint of endpoints) {
    await assert.rejects(api(endpoint, {}, true), /CI audit paused:.*configured scope/);
    await assert.rejects(api(endpoint, {}), /CI audit paused:.*configured scope/);
  }
  await assert.rejects(api('actions/runs', {}, 'https://example.com'), /configured scope/);
  assert.equal(requests.length, 0);
});

test('caller error policy labels failures without reflecting transport details', async () => {
  const api = createPublicAPI({ pause, fetchImpl: () => { throw new Error('private transport value'); } });
  await assert.rejects(api('actions/runs', {}, true), (error) => {
    assert.equal(error.queuePauseMessage, error.message);
    assert.match(error.message, /^CI audit paused:/);
    assert.doesNotMatch(error.message, /Sonar|private transport value/);
    return true;
  });
});

test('rate limits preserve the caller error prefix and actionable pause', async () => {
  for (const status of [403, 429]) {
    const api = createPublicAPI({ pause, fetchImpl: () => new Response('private response value', { status }) });
    await assert.rejects(api('actions/runs', {}, true), /^Error: CI audit paused:.*rate limit; retry/);
  }
});

test('shared transport rejects redirects, invalid UTF-8, and oversized streamed JSON', async () => {
  const responses = [
    () => ({ ok: true, redirected: true }),
    () => new Response(Uint8Array.of(0xff), { headers: { 'content-type': 'application/json' } }),
    () => new Response(' '.repeat(2097153), { headers: { 'content-type': 'application/json' } }),
  ];
  for (const fetchImpl of responses) {
    const api = createPublicAPI({ pause, fetchImpl });
    await assert.rejects(api('actions/runs', {}, true), /CI audit paused:/);
  }
});
