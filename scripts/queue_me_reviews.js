'use strict';

function checkedConnection(pull, headSHA, baseSHA, expectedTotal) {
  if (pull?.headRefOid !== headSHA || pull?.baseRefOid !== baseSHA) {
    throw new Error('Review evidence does not match the current head and base.');
  }
  const connection = pull.reviewThreads;
  if (!Number.isSafeInteger(connection?.totalCount) || connection.totalCount < 0 ||
      !Array.isArray(connection.nodes) || typeof connection.pageInfo?.hasNextPage !== 'boolean') {
    throw new Error('Review thread evidence is incomplete.');
  }
  if (expectedTotal !== undefined && connection.totalCount !== expectedTotal) {
    throw new Error('Review thread inventory changed during pagination.');
  }
  return connection;
}

function collectResolvedThreads(nodes, ids) {
  for (const thread of nodes) {
    if (typeof thread?.id !== 'string' || !thread.id || ids.has(thread.id) ||
        typeof thread.isResolved !== 'boolean') {
      throw new Error('Review thread evidence is malformed or duplicated.');
    }
    ids.add(thread.id);
    if (!thread.isResolved) throw new Error('An unresolved review thread blocks the queue.');
  }
}

// Each page includes the immutable pair being reviewed. A partial connection is
// never equivalent to zero open conversations, including outdated threads.
async function verifyReviews({ github, owner, repo, pullNumber, headSHA, baseSHA }) {
  let cursor = null;
  let expectedTotal;
  const ids = new Set();
  const cursors = new Set();
  for (let page = 0; page < 100; page += 1) {
    const result = await github.graphql(
      `query QueueReviewThreads($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
        repository(owner: $owner, name: $repo) {
          pullRequest(number: $number) {
            headRefOid baseRefOid
            reviewThreads(first: 100, after: $cursor) {
              totalCount nodes { id isResolved }
              pageInfo { hasNextPage endCursor }
            }
          }
        }
      }`,
      { owner, repo, number: pullNumber, cursor },
    );
    const pull = result?.repository?.pullRequest;
    const connection = checkedConnection(pull, headSHA, baseSHA, expectedTotal);
    expectedTotal ??= connection.totalCount;
    collectResolvedThreads(connection.nodes, ids);
    if (!connection.pageInfo.hasNextPage) {
      if (ids.size !== expectedTotal) throw new Error('Review thread inventory is incomplete.');
      return { total: ids.size, unresolved: 0, headSHA, baseSHA };
    }
    const next = connection.pageInfo.endCursor;
    if (!connection.nodes.length || typeof next !== 'string' || !next || cursors.has(next)) {
      throw new Error('Review thread pagination is incomplete or repeated.');
    }
    cursors.add(next);
    cursor = next;
  }
  throw new Error('Review thread pagination exceeded the audit limit.');
}

module.exports = { verifyReviews };
