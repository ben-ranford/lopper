'use strict';

// Queue pause regressions: a CI/reuse audit refusal pauses only its candidate,
// and the advance job merges nothing but the prepared ticket in queue order.
const assert = require('node:assert/strict');
const test = require('node:test');
const runController = require('../../queue_me_controller.js');
const reuse = require('../../queue_me_reuse.js');

const identity = { name: 'ben-ranford', email: '84072202+ben-ranford@users.noreply.github.com' };

function pull(number) {
  return {
    number, node_id: `PR_${number}`, state: 'open', labels: [{ name: 'queue-me' }], draft: false,
    base: { ref: 'main', repo: { name: 'lopper', owner: { login: 'octo' } } },
    head: { sha: `head-${number}`, ref: `queue-me-${number}`, repo: { full_name: 'octo/lopper' } },
  };
}

function harness(numbers) {
  const pulls = numbers.map(pull);
  const states = new Map(pulls.map(item => [item.number, {
    id: item.node_id, number: item.number, baseRefName: 'main', baseRefOid: 'base-sha', headRefOid: item.head.sha,
    isDraft: false, state: 'OPEN', mergeable: 'MERGEABLE', mergeStateStatus: 'CLEAN', autoMergeRequest: null,
  }]));
  const byID = id => [...states.values()].find(state => state.id === id);
  const comments = new Map();
  const calls = { armed: [], merged: [], evidence: [], notices: [] };
  const github = {
    rest: {
      issues: {
        getLabel: async () => {},
        createComment: async ({ issue_number: number, body }) => { comments.set(number, [{ id: number, body, user: { type: 'Bot' } }]); },
        updateComment: async ({ comment_id: id, body }) => { comments.get(id)[0].body = body; },
      },
      pulls: { get: async ({ pull_number: number }) => ({ data: pulls.find(item => item.number === number) }) },
      repos: {
        get: async () => ({ data: { default_branch: 'main', full_name: 'octo/lopper' } }),
        getBranch: async () => ({ data: { commit: { sha: 'base-sha' } } }),
        compareCommitsWithBasehead: async () => ({ data: { status: 'ahead', total_commits: 1, commits: [{
          sha: 'head-commit', commit: { author: identity, committer: identity },
          author: { login: 'ben-ranford', type: 'User' }, committer: { login: 'ben-ranford', type: 'User' } }] } }),
      },
    },
    paginate: async (_method, input) => input.issue_number ? comments.get(input.issue_number) || [] : pulls,
    graphql: async (query, variables) => {
      if (query.includes('QueuePullState($owner')) return { repository: { pullRequest: { ...states.get(variables.number) } } };
      if (query.includes('QueuePullStateByID')) return { node: { ...byID(variables.pullRequestId) } };
      if (query.includes('DisableQueueAutoMerge')) {
        byID(variables.pullRequestId).autoMergeRequest = null;
        return { disablePullRequestAutoMerge: { pullRequest: { autoMergeRequest: null } } };
      }
      if (query.includes('ArmQueueAutoMerge')) {
        calls.armed.push(byID(variables.pullRequestId).number);
        return { enablePullRequestAutoMerge: { pullRequest: byID(variables.pullRequestId) } };
      }
      if (query.includes('MergeQueuedPull')) {
        calls.merged.push(byID(variables.pullRequestId).number);
        return { mergePullRequest: { pullRequest: { merged: true } } };
      }
      throw new Error(`unexpected GraphQL operation: ${query}`);
    },
  };
  const args = {
    github, context: { repo: { owner: 'octo', repo: 'lopper' }, eventName: 'workflow_dispatch', payload: {} },
    core: { notice: message => calls.notices.push(message) }, trustedPolicySHA: 'base-sha',
    verifyEvidence: async input => { calls.evidence.push(input.pullNumber); return { headSHA: input.headSHA, baseSHA: input.baseSHA }; },
    revalidateEvidence: async () => {},
  };
  return { args, calls, comment: number => comments.get(number)?.[0]?.body || '' };
}

function refuse(t, numbers, message = 'CI audit paused: candidate changes trusted workflow source .github/workflows/ci.yml.') {
  t.mock.method(reuse, 'prepareCandidate', async input => {
    if (numbers.includes(input.pullNumber)) {
      const error = new Error(message);
      error.queuePauseMessage = message;
      throw error;
    }
    return { ticket: { snapshot: { pull_number: input.pullNumber }, intent: {} }, readiness: { state: 'READY', reasons: [] } };
  });
}

const paused = /Queue paused: the CI or reuse audit refused this pull request[^]*`CI audit paused: candidate changes trusted workflow source/;

test('a refused leader is paused and prepare selects its follower', async t => {
  const h = harness([10, 20]);
  refuse(t, [10]);
  const ticket = await runController.prepareQueue(h.args);
  assert.deepEqual(ticket, { snapshot: { pull_number: 20 }, intent: {}, predecessors: [10] });
  assert.match(h.comment(10), paused);
  assert.match(h.comment(10), /continue with the next queued pull request/);
  assert.deepEqual(h.calls.armed, []);
  assert.deepEqual(h.calls.merged, []);
});

test('a refused last queued pull is paused without a ticket', async t => {
  const h = harness([10]);
  refuse(t, [10]);
  assert.equal(await runController.prepareQueue(h.args), undefined);
  assert.match(h.comment(10), paused);
  assert.doesNotMatch(h.comment(10), /next queued pull request/);
  assert.deepEqual(h.calls.merged, []);
});

test('a non-pause preparation error still fails the queue run', async t => {
  const h = harness([10, 20]);
  t.mock.method(reuse, 'prepareCandidate', async () => { throw new Error('controller fault'); });
  await assert.rejects(runController.prepareQueue(h.args), /controller fault/);
});

test('advance verifies and merges only the prepared ticket and keeps the leader pause', async t => {
  const h = harness([10, 20]);
  refuse(t, [10]);
  const ticket = await runController.prepareQueue(h.args);
  const leader = h.comment(10);
  t.mock.restoreAll();
  await runController({ ...h.args, queueProof: { ticket } });
  assert.deepEqual(h.calls.evidence, [20]);
  assert.deepEqual(h.calls.merged, [20]);
  assert.equal(h.calls.armed.includes(10), false);
  assert.equal(h.comment(10), leader);
});

test('advance merges nothing when the queue ahead of the ticket changed or the ticket is malformed', async () => {
  for (const ticket of [
    { snapshot: { pull_number: 20 }, intent: {}, predecessors: [] },
    { snapshot: { pull_number: 20 }, intent: {}, predecessors: [10, 15] },
    { snapshot: { pull_number: 20 }, intent: {} },
    { snapshot: { pull_number: 30 }, intent: {}, predecessors: [10, 20] },
    { snapshot: { pull_number: '20' }, intent: {}, predecessors: [10] },
    { intent: {}, predecessors: [10] },
  ]) {
    const h = harness([10, 20]);
    await runController({ ...h.args, queueProof: { ticket } });
    assert.deepEqual(h.calls.evidence, []);
    assert.deepEqual(h.calls.merged, []);
    assert.deepEqual(h.calls.armed, []);
    assert.match(h.calls.notices.join('\n'), /nothing was merged/);
  }
});

test('a newly queued lower-numbered pull blocks the prepared ticket', async t => {
  const prepared = harness([10, 20]);
  refuse(t, [10]);
  const ticket = await runController.prepareQueue(prepared.args);
  t.mock.restoreAll();
  const h = harness([5, 10, 20]);
  await runController({ ...h.args, queueProof: { ticket } });
  assert.deepEqual(h.calls.merged, []);
  assert.deepEqual(h.calls.evidence, []);
});

test('candidate-controlled pause text stays inert in the status comment', async t => {
  const h = harness([10]);
  refuse(t, [10], 'CI audit paused: job `x`\n## @ben-ranford **owned** [link](https://evil.example)');
  await runController.prepareQueue(h.args);
  const body = h.comment(10);
  const code = body.indexOf('`');
  assert.equal((body.match(/`/g) || []).length, 2);
  assert.match(body.slice(code), /^`CI audit paused: job 'x' ## @ben-ranford \*\*owned\*\* \[link\]\(https:\/\/evil\.example\)`$/);
  assert.doesNotMatch(body.slice(0, code), /@ben-ranford/);
});
