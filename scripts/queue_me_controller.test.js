'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const runController = require('./queue_me_controller.js');
const { testables } = runController;

function makePull(number, overrides = {}) {
  return {
    number,
    node_id: `PR_${number}`,
    labels: [{ name: 'queue-me' }],
    draft: false,
    maintainer_can_modify: true,
    base: {
      repo: {
        name: 'lopper',
        owner: { login: 'octo' },
      },
    },
    head: {
      sha: `head-${number}`,
      repo: { full_name: 'octo/lopper' },
    },
    ...overrides,
  };
}

function makeHarness(options = {}) {
  const pulls = options.pulls || [];
  const eventPull = options.eventPull;
  const allPulls = eventPull && !pulls.some((pull) => pull.number === eventPull.number)
    ? [...pulls, eventPull]
    : pulls;
  const states = new Map(
    allPulls.map((pull) => [
      pull.number,
      {
        id: pull.node_id,
        number: pull.number,
        headRefOid: pull.head.sha,
        isDraft: pull.draft,
        mergeable: 'MERGEABLE',
        mergeStateStatus: 'BLOCKED',
        autoMergeRequest: null,
        ...(options.initialStates?.[pull.number] || {}),
      },
    ]),
  );
  const comments = new Map();
  const calls = {
    armed: [],
    comments: [],
    createdLabels: [],
    disabled: [],
    merged: [],
    notices: [],
    rebased: [],
  };
  const repository = { default_branch: 'main', full_name: 'octo/lopper' };

  const github = {
    rest: {
      issues: {
        getLabel: async () => {
          if (options.labelMissing) {
            const error = new Error('label missing');
            error.status = 404;
            throw error;
          }
        },
        createLabel: async (input) => {
          calls.createdLabels.push(input.name);
        },
        listComments: async () => {},
        createComment: async (input) => {
          const comment = { id: calls.comments.length + 1, body: input.body, user: { type: 'Bot' } };
          comments.set(input.issue_number, [comment]);
          calls.comments.push({ number: input.issue_number, body: input.body });
        },
        updateComment: async (input) => {
          calls.comments.push({ number: undefined, body: input.body });
        },
      },
      pulls: {
        list: async () => {},
      },
      repos: {
        get: async () => ({ data: repository }),
        getBranch: async () => ({ data: { commit: { sha: 'base-sha' } } }),
        compareCommitsWithBasehead: async () => ({
          data: { status: options.comparisonStatus || 'ahead' },
        }),
      },
    },
    paginate: async (_method, input) => {
      if (input.issue_number) {
        return comments.get(input.issue_number) || [];
      }
      return pulls;
    },
    graphql: async (query, variables) => {
      if (query.includes('QueuePullState($owner')) {
        return { repository: { pullRequest: states.get(variables.number) } };
      }
      if (query.includes('QueuePullStateByID')) {
        return {
          node: [...states.values()].find((state) => state.id === variables.pullRequestId),
        };
      }
      if (query.includes('DisableQueueAutoMerge')) {
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        state.autoMergeRequest = null;
        calls.disabled.push(state.number);
        return { disablePullRequestAutoMerge: { pullRequest: { number: state.number } } };
      }
      if (query.includes('RebaseQueuedPull')) {
        if (options.rebaseError) {
          throw options.rebaseError;
        }
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        state.headRefOid = options.rebasedHead || `rebased-${state.number}`;
        calls.rebased.push(state.number);
        return { updatePullRequestBranch: { pullRequest: state } };
      }
      if (query.includes('ArmQueueAutoMerge')) {
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        state.autoMergeRequest = { enabledAt: 'now', mergeMethod: 'SQUASH' };
        calls.armed.push(state.number);
        return { enablePullRequestAutoMerge: { pullRequest: state } };
      }
      if (query.includes('MergeQueuedPull')) {
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        calls.merged.push(state.number);
        return { mergePullRequest: { pullRequest: { number: state.number, merged: true } } };
      }
      throw new Error(`unexpected GraphQL operation: ${query}`);
    },
  };

  const payload = eventPull
    ? {
        action: options.action || 'labeled',
        label: { name: 'queue-me' },
        pull_request: eventPull,
      }
    : {};
  return {
    args: {
      github,
      context: {
        repo: { owner: 'octo', repo: 'lopper' },
        eventName: eventPull ? 'pull_request_target' : 'workflow_dispatch',
        payload,
      },
      core: {
        notice: (message) => calls.notices.push(message),
      },
    },
    calls,
  };
}

test('sortQueuedPulls uses deterministic ascending PR numbers', () => {
  const sorted = testables.sortQueuedPulls([{ number: 42 }, { number: 7 }, { number: 19 }]);
  assert.deepEqual(sorted.map((pull) => pull.number), [7, 19, 42]);
});

test('hasLabel accepts REST label objects and string labels', () => {
  assert.equal(testables.hasLabel({ labels: [{ name: 'queue-me' }] }, 'queue-me'), true);
  assert.equal(testables.hasLabel({ labels: ['queue-me'] }, 'queue-me'), true);
  assert.equal(testables.hasLabel({ labels: [{ name: 'other' }] }, 'queue-me'), false);
  assert.equal(testables.hasLabel({}, 'queue-me'), false);
});

test('isBranchCurrent accepts only ancestor-preserving compare states', () => {
  assert.equal(testables.isBranchCurrent('ahead'), true);
  assert.equal(testables.isBranchCurrent('identical'), true);
  assert.equal(testables.isBranchCurrent('behind'), false);
  assert.equal(testables.isBranchCurrent('diverged'), false);
});

test('status helpers bound untrusted API text', () => {
  assert.equal(testables.shortSHA('1234567890abcdef'), '1234567890');
  assert.equal(testables.shortSHA(undefined), 'unknown');
  const sanitized = testables.safeError(new Error('bad `branch`'));
  assert.equal(sanitized, "bad 'branch'");
  assert.equal(testables.safeError('x'.repeat(1300)).length, 1200);
});

test('controller creates the queue label and exits cleanly for an empty queue', async () => {
  const harness = makeHarness({ labelMissing: true });

  await runController(harness.args);

  assert.deepEqual(harness.calls.createdLabels, ['queue-me']);
  assert.equal(harness.calls.notices.length, 1);
  assert.match(harness.calls.notices[0], /No open main pull requests/);
});

test('controller disables followers and arms only the oldest numbered pull request', async () => {
  const leader = makePull(10);
  const follower = makePull(20);
  const harness = makeHarness({
    pulls: [follower, leader],
    eventPull: follower,
    initialStates: {
      20: { autoMergeRequest: { enabledAt: 'before', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.disabled, [20]);
  assert.deepEqual(harness.calls.armed, [10]);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(
    harness.calls.comments.find((comment) => comment.number === 20).body,
    /Queued behind #10/,
  );
  assert.match(
    harness.calls.comments.find((comment) => comment.number === 10).body,
    /Squash auto-merge is armed/,
  );
});

test('controller rebases a stale leader and merges it when repository rules are satisfied', async () => {
  const leader = makePull(10);
  const harness = makeHarness({
    pulls: [leader],
    comparisonStatus: 'behind',
    initialStates: {
      10: { mergeStateStatus: 'CLEAN' },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.rebased, [10]);
  assert.deepEqual(harness.calls.merged, [10]);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /Rebased/);
  assert.match(harness.calls.comments[0].body, /GitHub squash-merged it/);
});

test('removing queue-me disables auto-merge and leaves an empty queue green', async () => {
  const pull = makePull(10, { labels: [] });
  const harness = makeHarness({
    eventPull: pull,
    action: 'unlabeled',
    initialStates: {
      10: { autoMergeRequest: { enabledAt: 'before', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.disabled, [10]);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /automatic merge is disabled/);
  assert.equal(harness.calls.notices.length, 1);
});

test('drafts and unmodifiable forks pause before rebase or auto-merge', async (t) => {
  const cases = [
    { name: 'draft', pull: makePull(10, { draft: true }), message: /still a draft/ },
    {
      name: 'fork',
      pull: makePull(10, {
        head: { sha: 'fork-head', repo: { full_name: 'contributor/lopper' } },
        maintainer_can_modify: false,
      }),
      message: /maintainers cannot rebase this fork branch/,
    },
  ];

  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      const harness = makeHarness({ pulls: [scenario.pull] });
      await runController(harness.args);
      assert.deepEqual(harness.calls.rebased, []);
      assert.deepEqual(harness.calls.armed, []);
      assert.match(harness.calls.comments[0].body, scenario.message);
    });
  }
});

test('a rebase conflict pauses the queue with a bounded status message', async () => {
  const leader = makePull(10);
  const harness = makeHarness({
    pulls: [leader],
    comparisonStatus: 'behind',
    rebaseError: new Error('conflict in `workflow`'),
  });

  await assert.rejects(runController(harness.args), /conflict/);

  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /could not rebase/);
  assert.match(harness.calls.comments[0].body, /conflict in 'workflow'/);
});
