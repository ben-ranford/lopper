'use strict';
const assert = require('node:assert/strict');
const test = require('node:test');
const { verifyReviews } = require('./queue_me_reviews');

function harness(pages) {
  const calls = [];
  const github = { graphql: async (_query, input) => {
    calls.push(input);
    const data = pages[calls.length - 1];
    if (data instanceof Error) throw data;
    return { repository: { pullRequest: { headRefOid: 'head', baseRefOid: 'base', ...data } } };
  } };
  return { calls, args: { github, owner: 'owner', repo: 'repo', pullNumber: 1, headSHA: 'head', baseSHA: 'base' } };
}
function page(nodes = [], { total = nodes.length, next = false, cursor = null } = {}) {
  return { reviewThreads: { totalCount: total, nodes, pageInfo: { hasNextPage: next, endCursor: cursor } } };
}
const resolved = (id) => ({ id, isResolved: true });

test('review evidence enumerates every page and binds head/base', async () => {
  const h = harness([page([resolved('a')], { total: 2, next: true, cursor: 'a' }), page([resolved('b')], { total: 2 })]);
  assert.deepEqual(await verifyReviews(h.args), { total: 2, unresolved: 0, headSHA: 'head', baseSHA: 'base' });
  assert.deepEqual(h.calls.map(c => c.cursor), [null, 'a']);
});

test('outdated unresolved thread on page two blocks admission', async () => {
  const h = harness([page([resolved('a')], { total: 2, next: true, cursor: 'a' }), page([{ id: 'b', isResolved: false, isOutdated: true }], { total: 2 })]);
  await assert.rejects(verifyReviews(h.args), /unresolved/);
});

test('empty complete review inventory is valid', async () => {
  assert.equal((await verifyReviews(harness([page()]).args)).unresolved, 0);
});

test('review connection failures never become a zero count', async (t) => {
  const cases = [
    ['head drift', [{ headRefOid: 'other', ...page() }]],
    ['base drift', [{ baseRefOid: 'other', ...page() }]],
    ['missing connection', [{}]],
    ['null node', [page([null])]],
    ['unknown resolution', [page([{ id: 'a' }])]],
    ['duplicate thread', [page([resolved('a'), resolved('a')])]],
    ['partial last page', [page([], { total: 1 })]],
    ['missing cursor', [page([resolved('a')], { total: 2, next: true })]],
    ['empty page with more', [page([], { total: 2, next: true, cursor: 'a' })]],
    ['total drift', [page([resolved('a')], { total: 2, next: true, cursor: 'a' }), page([resolved('b')], { total: 3 })]],
    ['repeated cursor', [page([resolved('a')], { total: 3, next: true, cursor: 'a' }), page([resolved('b')], { total: 3, next: true, cursor: 'a' })]],
    ['API failure', [new Error('API unavailable')]],
  ];
  for (const [name, pages] of cases) await t.test(name, async () => {
    await assert.rejects(verifyReviews(harness(pages).args));
  });
});
