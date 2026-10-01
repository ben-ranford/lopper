'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { collectCIIntent } = require('./queue_me_ci_intent');

const HEAD = 'a'.repeat(40);
const BASE = 'b'.repeat(40);
const at = (seconds) => `2026-09-30T00:00:${String(seconds).padStart(2, '0')}Z`;
const iso = (seconds) => at(seconds).replace('Z', '.000Z');
const label = (id, seconds, name = 'queue-me', type = 'LabeledEvent') =>
  ({ id, createdAt: at(seconds), __typename: type, label: name === null ? null : { name } });
const event = (id, seconds, type) => ({ id, createdAt: at(seconds), __typename: type });

function page(nodes = [label('queued', 2)], overrides = {}) {
  return {
    number: 7, state: 'OPEN', isDraft: false, headRefOid: HEAD, baseRefOid: BASE,
    baseRefName: 'main', createdAt: at(0), lastEditedAt: null,
    labels: { nodes: [{ name: 'queue-me' }], pageInfo: { hasNextPage: false } },
    timelineItems: { nodes, pageInfo: { hasNextPage: false, endCursor: null } },
    ...overrides,
  };
}

function continuation(nodes, cursor) {
  return page(nodes, {
    timelineItems: { nodes, pageInfo: { hasNextPage: true, endCursor: cursor } },
  });
}

function harness(pages, overrides = {}) {
  const calls = [];
  const github = { graphql: async (query, variables) => {
    calls.push({ query, ...variables });
    const response = pages[calls.length - 1];
    if (response instanceof Error) throw response;
    return { repository: { pullRequest: response } };
  } };
  return { calls, input: { github, owner: 'owner', repo: 'repo', pullNumber: 7,
    headSHA: HEAD, baseSHA: BASE, baseRef: 'main', ...overrides } };
}

function rejects(pages, expression = /CI intent paused:/, overrides = {}) {
  return assert.rejects(collectCIIntent(harness(pages, overrides).input), (error) => {
    assert.match(error.message, expression);
    assert.equal(error.queuePauseMessage, error.message);
    return true;
  });
}

test('CI intent binds the live pair and a known queue label generation', async () => {
  const h = harness([page()]);
  assert.deepEqual(await collectCIIntent(h.input), {
    headSHA: HEAD, baseSHA: BASE, baseRef: 'main', createdAt: iso(0), lastEditedAt: null,
    labels: ['queue-me'], pullNumber: 7, queueLabel: 'queue-me', ciNotBefore: iso(2),
    queueEventId: 'queued', queueEventAt: iso(2), metadataEventId: 'queued',
    metadataEventAt: iso(2), eventCount: 1,
  });
  assert.deepEqual(h.calls.map(({ owner, repo, number, cursor }) => ({ owner, repo, number, cursor })),
    [{ owner: 'owner', repo: 'repo', number: 7, cursor: null }]);
  assert.doesNotMatch(h.calls[0].query, /updatedAt|IssueComment|ISSUE_COMMENT/);
});

test('queue removal cancels intent and readdition creates a new generation', async () => {
  const history = [label('old', 1), label('removed', 2, 'queue-me', 'UnlabeledEvent')];
  await rejects([page(history)], /latest label-add/);
  const evidence = await collectCIIntent(harness([page([...history, label('new', 3)])]).input);
  assert.equal(evidence.queueEventId, 'new');
  assert.equal(evidence.ciNotBefore, iso(3));
});

test('other labels, title, lifecycle and base/head events advance CI freshness', async (t) => {
  const changes = [
    label('other-add', 3, 'enhancement'), label('other-remove', 3, 'other', 'UnlabeledEvent'),
    ...['RenamedTitleEvent', 'ReadyForReviewEvent', 'ReopenedEvent', 'BaseRefChangedEvent',
      'HeadRefForcePushedEvent'].map((type) => event(type, 3, type)),
  ];
  for (const change of changes) await t.test(change.id, async () => {
    const result = await collectCIIntent(harness([page([label('queued', 2), change])]).input);
    assert.equal(result.queueEventId, 'queued');
    assert.equal(result.metadataEventId, change.id);
    assert.equal(result.ciNotBefore, iso(3));
  });
});

test('body edits advance freshness while explicit never-edited null is supported', async () => {
  const result = await collectCIIntent(harness([page(undefined, { lastEditedAt: at(5) })]).input);
  assert.equal(result.ciNotBefore, iso(5));
  assert.equal(result.metadataEventId, 'queued');
  assert.equal(result.lastEditedAt, iso(5));
});

test('bot comment updates do not invalidate otherwise identical intent snapshots', async () => {
  const first = await collectCIIntent(harness([page(undefined, { updatedAt: at(3) })]).input);
  const second = await collectCIIntent(harness([page(undefined, { updatedAt: at(40) })]).input);
  assert.deepEqual(first, second);
});

test('all filtered pages are read without trusting the unfiltered total count', async () => {
  const first = continuation([label('queued', 2)], 'cursor1');
  first.timelineItems.totalCount = 17;
  const second = page([event('title', 3, 'RenamedTitleEvent')]);
  second.timelineItems.totalCount = 18;
  const h = harness([first, second]);
  const result = await collectCIIntent(h.input);
  assert.equal(result.ciNotBefore, iso(3));
  assert.equal(result.eventCount, 2);
  assert.deepEqual(h.calls.map((call) => call.cursor), [null, 'cursor1']);
});

test('same-second events preserve timeline order and distinct intent identity', async () => {
  const result = await collectCIIntent(harness([page([
    label('old', 2), label('remove', 2, 'queue-me', 'UnlabeledEvent'), label('new', 2),
  ])]).input);
  assert.equal(result.queueEventId, 'new');
  assert.equal(result.metadataEventId, 'new');
});

test('label ordering does not change the deterministic snapshot', async () => {
  const first = continuation([label('queued', 2)], 'cursor1');
  first.labels.nodes.push({ name: 'enhancement' });
  const second = page([event('title', 3, 'RenamedTitleEvent')]);
  second.labels.nodes.unshift({ name: 'enhancement' });
  const result = await collectCIIntent(harness([first, second]).input);
  assert.deepEqual(result.labels, ['enhancement', 'queue-me']);
});

test('deleted historical labels are bounded by a later known queue generation', async () => {
  const result = await collectCIIntent(harness([page([label('deleted-label', 1, null), label('queued', 2)])]).input);
  assert.equal(result.ciNotBefore, iso(2));
  await rejects([page([label('queued', 2), label('unknown-later', 3, null)])], /deleted-label history/);
});

test('CI intent accepts a configured queue label without assuming its name', async () => {
  const h = harness([page([label('queue', 2, 'merge-intent')], {
    labels: { nodes: [{ name: 'merge-intent' }], pageInfo: { hasNextPage: false } },
  })], { queueLabel: 'merge-intent' });
  assert.equal((await collectCIIntent(h.input)).queueLabel, 'merge-intent');
});

test('unknown, missing and malformed live metadata fails closed', async (t) => {
  const missingEdit = page(); delete missingEdit.lastEditedAt;
  const cases = [
    ['missing PR', null], ['wrong PR', page(undefined, { number: 8 })],
    ['closed', page(undefined, { state: 'CLOSED' })], ['draft', page(undefined, { isDraft: true })],
    ['missing draft state', page(undefined, { isDraft: undefined })],
    ['head drift', page(undefined, { headRefOid: 'c'.repeat(40) })],
    ['base drift', page(undefined, { baseRefOid: 'c'.repeat(40) })],
    ['retargeted', page(undefined, { baseRefName: 'release' })],
    ['null creation', page(undefined, { createdAt: null })], ['missing edit', missingEdit],
    ['undefined edit', page(undefined, { lastEditedAt: undefined })],
    ['numeric edit', page(undefined, { lastEditedAt: 5 })],
    ['invalid edit', page(undefined, { lastEditedAt: 'not-a-date' })],
    ['impossible date', page(undefined, { lastEditedAt: '2026-09-31T00:00:05Z' })],
    ['edit before creation', page(undefined, { lastEditedAt: '2026-09-29T00:00:00Z' })],
    ['label removed', page(undefined, { labels: { nodes: [], pageInfo: { hasNextPage: false } } })],
    ['missing labels', page(undefined, { labels: undefined })],
    ['partial labels', page(undefined, { labels: { nodes: [{ name: 'queue-me' }], pageInfo: { hasNextPage: true } } })],
    ['malformed label', page(undefined, { labels: { nodes: [null], pageInfo: { hasNextPage: false } } })],
    ['duplicate labels', page(undefined, { labels: { nodes: [{ name: 'queue-me' }, { name: 'queue-me' }], pageInfo: { hasNextPage: false } } })],
  ];
  for (const [name, candidate] of cases) await t.test(name, () => rejects([candidate]));
});

test('malformed event inventories never establish valid intent', async (t) => {
  const missingLabel = label('queue', 2); delete missingLabel.label;
  const cases = [
    ['no queue event', page([])], ['unknown event', page([event('comment', 2, 'IssueComment')])],
    ['null event', page([null])], ['missing ID', page([label(undefined, 2)])],
    ['missing timestamp', page([{ ...label('queue', 2), createdAt: null }])],
    ['duplicate event', page([label('queue', 2), label('queue', 2)])],
    ['events out of order', page([label('queue', 3), event('title', 2, 'RenamedTitleEvent')])],
    ['event before creation', page([label('queue', 1)], { createdAt: at(2) })],
    ['missing historical label', page([missingLabel])],
    ['malformed historical label', page([{ ...label('queue', 2), label: {} }])],
    ['missing timeline', page(undefined, { timelineItems: undefined })],
    ['missing page state', page(undefined, { timelineItems: { nodes: [label('queue', 2)] } })],
    ['oversized page', page(Array.from({ length: 101 }, (_, index) => label(String(index), 2)))],
    ['missing cursor', continuation([label('queue', 2)], null)],
    ['empty intermediate page', continuation([], 'cursor')],
  ];
  for (const [name, candidate] of cases) await t.test(name, () => rejects([candidate]));
});

test('page drift and duplicate or repeated continuation evidence fail closed', async (t) => {
  const first = () => continuation([label('queued', 2)], 'cursor1');
  const cases = [
    ['body edit', page([event('title', 3, 'RenamedTitleEvent')], { lastEditedAt: at(3) })],
    ['creation changed', page([event('title', 3, 'RenamedTitleEvent')], { createdAt: at(1) })],
    ['labels changed', page([event('title', 3, 'RenamedTitleEvent')], { labels: {
      nodes: [{ name: 'queue-me' }, { name: 'new' }], pageInfo: { hasNextPage: false },
    } })],
    ['head changed', page([], { headRefOid: 'c'.repeat(40) })],
    ['base changed', page([], { baseRefOid: 'c'.repeat(40) })],
    ['duplicate ID', page([label('queued', 2)])],
    ['repeated cursor', continuation([event('title', 3, 'RenamedTitleEvent')], 'cursor1')],
    ['event order reversed', page([event('title', 1, 'RenamedTitleEvent')])],
  ];
  for (const [name, second] of cases) await t.test(name, () => rejects([first(), second]));
});

test('page limits and API failures are actionable holds', async () => {
  const pages = Array.from({ length: 100 }, (_, index) => continuation([label(`event-${index}`, 2)], `cursor-${index}`));
  await rejects(pages, /pagination exceeded/);
  await rejects([new Error('upstream transport details')], /metadata could not be read completely/);
});

test('invalid expected identities fail before requesting metadata', async () => {
  for (const overrides of [{ pullNumber: 0 }, { headSHA: 'missing' }, { baseSHA: '' }, { baseRef: '' }, { queueLabel: '' }]) {
    const h = harness([page()], overrides);
    await assert.rejects(collectCIIntent(h.input), /identity is invalid/);
    assert.equal(h.calls.length, 0);
  }
});
