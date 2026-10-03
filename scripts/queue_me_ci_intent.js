'use strict';

const PAGE_SIZE = 100;
const MAX_PAGES = 100;
const EVENT_TYPES = new Set([
  'LabeledEvent', 'UnlabeledEvent', 'RenamedTitleEvent', 'ReadyForReviewEvent',
  'ReopenedEvent', 'BaseRefChangedEvent', 'HeadRefForcePushedEvent',
]);
const QUERY = `query QueueCIIntent($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      number state isDraft headRefOid baseRefOid baseRefName createdAt lastEditedAt
      labels(first: 100) { nodes { name } pageInfo { hasNextPage } }
      timelineItems(first: 100, after: $cursor, itemTypes: [LABELED_EVENT,
        UNLABELED_EVENT, RENAMED_TITLE_EVENT, READY_FOR_REVIEW_EVENT,
        REOPENED_EVENT, BASE_REF_CHANGED_EVENT, HEAD_REF_FORCE_PUSHED_EVENT]) {
        nodes {
          __typename
          ... on LabeledEvent { id createdAt label { name } }
          ... on UnlabeledEvent { id createdAt label { name } }
          ... on RenamedTitleEvent { id createdAt }
          ... on ReadyForReviewEvent { id createdAt }
          ... on ReopenedEvent { id createdAt }
          ... on BaseRefChangedEvent { id createdAt }
          ... on HeadRefForcePushedEvent { id createdAt }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`;

function pause(message) {
  const error = new Error(`CI intent paused: ${message}`);
  error.queuePauseMessage = error.message;
  return error;
}

function requireEvidence(condition, message) {
  if (!condition) throw pause(message);
}

function boundedString(value) {
  return typeof value === 'string' && value.length > 0 && value.length <= 256;
}

function lexicalOrder(left, right) {
  if (left === right) return 0;
  return left < right ? -1 : 1;
}

function timestamp(value) {
  requireEvidence(typeof value === 'string' &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?Z$/.test(value),
  'a metadata timestamp is missing or malformed.');
  const milliseconds = Date.parse(value);
  requireEvidence(Number.isFinite(milliseconds), 'a metadata timestamp is invalid.');
  const normalized = new Date(milliseconds).toISOString();
  const expected = value.includes('.')
    ? value.replace(/\.(\d{1,3})Z$/, (_match, fraction) => `.${fraction.padEnd(3, '0')}Z`)
    : value.replace(/Z$/, '.000Z');
  requireEvidence(normalized === expected, 'a metadata timestamp is invalid.');
  return normalized;
}

function currentLabels(connection, queueLabel) {
  requireEvidence(Array.isArray(connection?.nodes) && connection.nodes.length <= PAGE_SIZE &&
    connection.pageInfo?.hasNextPage === false, 'current labels are missing or incomplete.');
  const labels = connection.nodes.map((label) => {
    requireEvidence(boundedString(label?.name), 'a current label is malformed.');
    return label.name;
  }).sort(lexicalOrder);
  requireEvidence(new Set(labels).size === labels.length, 'current labels contain duplicates.');
  requireEvidence(labels.includes(queueLabel), 'the queue label has been removed.');
  return labels;
}

function pullSnapshot(pull, expected) {
  requireEvidence(pull?.number === expected.pullNumber && pull.state === 'OPEN' &&
    pull.isDraft === false, 'the pull request is missing, closed, or a draft.');
  requireEvidence(pull.headRefOid === expected.headSHA && pull.baseRefOid === expected.baseSHA &&
    pull.baseRefName === expected.baseRef, 'the pull request head or base changed.');
  const createdAt = timestamp(pull.createdAt);
  requireEvidence(Object.hasOwn(pull, 'lastEditedAt'), 'the body-edit timestamp is missing.');
  const lastEditedAt = pull.lastEditedAt === null ? null : timestamp(pull.lastEditedAt);
  requireEvidence(lastEditedAt === null || lastEditedAt >= createdAt,
    'the body-edit timestamp precedes creation.');
  return {
    headSHA: pull.headRefOid, baseSHA: pull.baseRefOid, baseRef: pull.baseRefName,
    createdAt, lastEditedAt, labels: currentLabels(pull.labels, expected.queueLabel),
  };
}

function eventLabel(node) {
  if (!['LabeledEvent', 'UnlabeledEvent'].includes(node.__typename)) return undefined;
  requireEvidence(Object.hasOwn(node, 'label'), 'label history is incomplete.');
  if (node.label === null) return null;
  requireEvidence(boundedString(node.label?.name), 'label history is malformed.');
  return node.label.name;
}

function collectEvent(node, state, expected) {
  requireEvidence(EVENT_TYPES.has(node?.__typename) && boundedString(node.id) &&
    !state.ids.has(node.id), 'timeline evidence has an unknown, missing, or duplicate event.');
  const createdAt = timestamp(node.createdAt);
  requireEvidence(createdAt >= state.snapshot.createdAt && createdAt >= state.lastEventAt,
    'timeline timestamps are out of order.');
  const label = eventLabel(node);
  state.ids.add(node.id);
  state.lastEventAt = createdAt;
  state.latest = { id: node.id, createdAt, type: node.__typename, order: state.ids.size };
  if (label === expected.queueLabel) state.queue = state.latest;
  if (label === null) state.unknownLabelOrder = state.ids.size;
}

function timelineConnection(pull) {
  const connection = pull.timelineItems;
  requireEvidence(Array.isArray(connection?.nodes) && connection.nodes.length <= PAGE_SIZE &&
    typeof connection.pageInfo?.hasNextPage === 'boolean', 'timeline evidence is incomplete.');
  // GitHub's filtered totalCount includes other event types. Completion is
  // established by pageInfo and cursor progress, never by that unfiltered count.
  return connection;
}

function nextCursor(connection, state) {
  const cursor = connection.pageInfo.endCursor;
  requireEvidence(connection.nodes.length > 0 && boundedString(cursor) &&
    !state.cursors.has(cursor), 'timeline pagination is empty, missing, or repeated.');
  state.cursors.add(cursor);
  return cursor;
}

async function collectPages(github, expected, state, cursor = null, page = 0) {
  requireEvidence(page < MAX_PAGES, 'timeline pagination exceeded its audit limit.');
  const result = await github.graphql(QUERY, {
    owner: expected.owner, repo: expected.repo, number: expected.pullNumber, cursor,
  });
  const pull = result?.repository?.pullRequest;
  const snapshot = pullSnapshot(pull, expected);
  if (state.snapshot) {
    requireEvidence(JSON.stringify(snapshot) === JSON.stringify(state.snapshot),
      'pull request metadata or labels changed during pagination.');
  } else {
    state.snapshot = snapshot;
    state.lastEventAt = snapshot.createdAt;
  }
  const connection = timelineConnection(pull);
  for (const node of connection.nodes) collectEvent(node, state, expected);
  if (connection.pageInfo.hasNextPage) {
    return collectPages(github, expected, state, nextCursor(connection, state), page + 1);
  }
  return state;
}

function intentSnapshot(state, expected) {
  requireEvidence(state.queue?.type === 'LabeledEvent',
    'current queue intent has no complete, latest label-add event.');
  // A deleted unrelated historical label does not erase a later known intent.
  // An unknown label event after that intent cannot prove it was unrelated.
  requireEvidence(state.unknownLabelOrder < state.queue.order,
    'deleted-label history after queue intent is ambiguous; refresh the queue label intent.');
  const { createdAt, lastEditedAt } = state.snapshot;
  const ciNotBefore = [createdAt, lastEditedAt || createdAt, state.latest.createdAt].sort(lexicalOrder).at(-1);
  return {
    ...state.snapshot, pullNumber: expected.pullNumber, queueLabel: expected.queueLabel,
    ciNotBefore, queueEventId: state.queue.id, queueEventAt: state.queue.createdAt,
    metadataEventId: state.latest.id, metadataEventAt: state.latest.createdAt,
    eventCount: state.ids.size,
  };
}

async function collectCIIntent({ github, owner, repo, pullNumber, headSHA, baseSHA,
  baseRef, queueLabel = 'queue-me' }) {
  const expected = { owner, repo, pullNumber, headSHA, baseSHA, baseRef, queueLabel };
  requireEvidence([owner, repo, baseRef, queueLabel].every(boundedString) &&
    Number.isSafeInteger(pullNumber) && pullNumber > 0 &&
    /^[a-f\d]{40}$/.test(headSHA) && /^[a-f\d]{40}$/.test(baseSHA),
  'the expected pull request identity is invalid.');
  try {
    const state = await collectPages(github, expected,
      { ids: new Set(), cursors: new Set(), unknownLabelOrder: 0 });
    return intentSnapshot(state, expected);
  } catch (error) {
    if (error?.queuePauseMessage) throw error;
    throw pause('GitHub metadata could not be read completely; retry when it is available.');
  }
}

module.exports = { collectCIIntent };
