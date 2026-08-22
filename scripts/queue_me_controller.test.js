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
      ref: 'main',
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

function makeComparisonCommit(sha = 'head-commit', overrides = {}) {
  const identity = {
    name: 'ben-ranford',
    email: '84072202+ben-ranford@users.noreply.github.com',
  };
  const { commit: commitOverrides = {}, ...rest } = overrides;
  return {
    sha,
    commit: {
      author: { ...identity },
      committer: { ...identity },
      ...commitOverrides,
    },
    author: { login: 'ben-ranford', type: 'User' },
    committer: { login: 'ben-ranford', type: 'User' },
    ...rest,
  };
}

function makeHarness(options = {}) {
  const pulls = options.pulls || [];
  const eventPull = options.eventPull;
  const branchSHAs = options.branchSHAs || ['base-sha'];
  const allPulls = eventPull && !pulls.some((pull) => pull.number === eventPull.number)
    ? [...pulls, eventPull]
    : pulls;
  const states = new Map(
    allPulls.map((pull) => [
      pull.number,
      {
        id: pull.node_id,
        number: pull.number,
        baseRefName: pull.base.ref,
        baseRefOid: branchSHAs[0],
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
    armExpectedHeads: [],
    branchReads: [],
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
          for (const [number, issueComments] of comments) {
            const existing = issueComments.find((comment) => comment.id === input.comment_id);
            if (existing) {
              existing.body = input.body;
              calls.comments.push({ number, body: input.body });
              return;
            }
          }
          throw new Error(`unknown comment ${input.comment_id}`);
        },
      },
      pulls: {
        list: async () => {},
      },
      repos: {
        get: async () => ({ data: repository }),
        getBranch: async () => {
          const sha = branchSHAs[Math.min(calls.branchReads.length, branchSHAs.length - 1)];
          calls.branchReads.push(sha);
          return { data: { commit: { sha } } };
        },
        compareCommitsWithBasehead: async (input) => {
          if (options.comparisonError) {
            throw options.comparisonError;
          }
          calls.comparisons = calls.comparisons || [];
          calls.comparisons.push(input);
          if (options.comparisonPages) {
            const page = options.comparisonPages[(input.page || 1) - 1];
            if (!page) {
              return {
                data: {
                  status: options.comparisonStatus || 'ahead',
                  commits: [],
                  total_commits: options.totalCommits ?? 0,
                },
              };
            }
            return {
              data: {
                status: page.status || options.comparisonStatus || 'ahead',
                commits: page.commits || [],
                total_commits: page.totalCommits ?? options.totalCommits ?? page.commits?.length ?? 0,
              },
            };
          }
          const commits = options.comparisonCommits || [makeComparisonCommit()];
          return {
            data: {
              status: options.comparisonStatus || 'ahead',
              commits,
              total_commits: options.totalCommits ?? commits.length,
            },
          };
        },
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
        const state = states.get(variables.number);
        if (calls.branchReads.length >= 2 && options.stateAfterFinalBranchRead?.[variables.number]) {
          Object.assign(state, options.stateAfterFinalBranchRead[variables.number]);
        }
        return { repository: { pullRequest: { ...state } } };
      }
      if (query.includes('QueuePullStateByID')) {
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        return {
          node: { ...state },
        };
      }
      if (query.includes('DisableQueueAutoMerge')) {
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        state.autoMergeRequest = null;
        calls.disabled.push(state.number);
        return { disablePullRequestAutoMerge: { pullRequest: { number: state.number } } };
      }
      if (query.includes('RebaseQueuedPull')) {
        calls.rebased.push(variables.pullRequestId);
        throw new Error('queue must not call updatePullRequestBranch');
      }
      if (query.includes('ArmQueueAutoMerge')) {
        const state = [...states.values()].find((value) => value.id === variables.pullRequestId);
        calls.armExpectedHeads.push(variables.expectedHeadOid);
        if (options.armErrorHead) {
          state.headRefOid = options.armErrorHead;
        }
        if (options.armError) {
          throw options.armError;
        }
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
        sender: options.sender || { login: 'octocat', type: 'User' },
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
      queueAppSlug: options.queueAppSlug,
    },
    calls,
    pulls,
  };
}

function commentsFor(harness, number) {
  return harness.calls.comments
    .filter((comment) => comment.number === number)
    .map((comment) => comment.body)
    .at(-1) || '';
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
  const sanitized = testables.safeError(new Error('bad `branch`\r\ntry again'));
  assert.equal(sanitized, "bad 'branch' try again");
  assert.equal(testables.safeError('x'.repeat(1300)).length, 1200);
});

test('identity audit failure reports bound failure count and text length', () => {
  const failures = Array.from(
    { length: 25 },
    (_, index) => `commit-${index}: bad \`metadata\`\n${'x'.repeat(1000)}`,
  );

  const message = testables.queueIdentityFailureMessage(failures);

  assert.match(message, /Found 25 failing commits; showing 10:/);
  assert.match(message, /15 additional commit identity failures omitted/);
  assert.match(message, /commit-0: bad 'metadata' x/);
  assert.doesNotMatch(message, /commit-10:/);
  assert.doesNotMatch(message, /[`\r\n]/);
  assert.ok(message.length < 3500, `message length ${message.length} must stay bounded`);
});

test('sticky queue status comments are safely truncated before GitHub updates', () => {
  const body = testables.truncateCommentBody('x'.repeat(70000));

  assert.equal(body.length, 60000);
  assert.match(body, /Status message truncated to fit GitHub comment limits/);
});

test('commit identity audit accepts only canonical user committer identity', () => {
  assert.doesNotThrow(() =>
    testables.assertCanonicalCommitIdentity({
      commits: [makeComparisonCommit('good-commit')],
      total_commits: 1,
    }),
  );

  assert.doesNotThrow(() =>
    testables.assertCanonicalCommitIdentity({
      commits: [
        makeComparisonCommit('same-linked-user', {
          commit: {
            author: {
              name: 'Ben Ranford',
              email: '84072202+ben-ranford@users.noreply.github.com',
            },
            committer: {
              name: 'ben-ranford',
              email: '84072202+ben-ranford@users.noreply.github.com',
            },
          },
          author: { login: 'ben-ranford', type: 'User' },
          committer: { login: 'ben-ranford', type: 'User' },
        }),
      ],
      total_commits: 1,
    }),
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [
          makeComparisonCommit('matching-raw-missing-links', {
            author: null,
            committer: null,
          }),
        ],
        total_commits: 1,
      }),
    /cannot prove canonical author and committer GitHub identity/,
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [
          makeComparisonCommit('matching-raw-one-missing-link', {
            committer: null,
          }),
        ],
        total_commits: 1,
      }),
    /cannot prove canonical author and committer GitHub identity/,
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [
          makeComparisonCommit('linked-user-mismatch', {
            author: { login: 'ben-ranford', type: 'User' },
            committer: { login: 'other-user', type: 'User' },
          }),
        ],
        total_commits: 1,
      }),
    /author and committer identities differ/,
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [
          makeComparisonCommit('bot-commit', {
            commit: {
              committer: {
                name: 'lopper-queue-controller[bot]',
                email: '123+lopper-queue-controller[bot]@users.noreply.github.com',
              },
            },
            committer: { login: 'lopper-queue-controller[bot]', type: 'Bot' },
          }),
        ],
        total_commits: 1,
      }),
    /committer is a bot identity/,
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [
          makeComparisonCommit('linked-author-bot-type', {
            author: { login: 'neutral-linked-author', type: 'Bot' },
            committer: { login: 'neutral-linked-author', type: 'User' },
          }),
        ],
        total_commits: 1,
      }),
    /author is a bot identity/,
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [
          makeComparisonCommit('linked-committer-bot-type', {
            author: { login: 'neutral-linked-committer', type: 'User' },
            committer: { login: 'neutral-linked-committer', type: 'bOt' },
          }),
        ],
        total_commits: 1,
      }),
    /committer is a bot identity/,
  );

  assert.throws(
    () =>
      testables.assertCanonicalCommitIdentity({
        commits: [makeComparisonCommit('partial')],
        total_commits: 2,
      }),
    /cannot prove canonical author and committer identity/,
  );
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
  assert.deepEqual(harness.calls.armExpectedHeads, ['head-10']);
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

test('queue refresh updates a stale follower position after the leader advances', async () => {
  const formerLeader = makePull(3);
  const currentLeader = makePull(5);
  const follower = makePull(8);
  const harness = makeHarness({
    pulls: [formerLeader, currentLeader, follower],
    eventPull: follower,
  });

  await runController(harness.args);
  assert.match(commentsFor(harness, 8), /Queued behind #3/);
  assert.equal(commentsFor(harness, 5), '');

  harness.pulls.splice(0, harness.pulls.length, currentLeader, follower);
  harness.args.context.eventName = 'push';
  harness.args.context.payload = {};

  await runController(harness.args);

  assert.match(commentsFor(harness, 8), /Queued behind #5/);
  assert.doesNotMatch(commentsFor(harness, 8), /Queued behind #3/);
});

test('controller pauses a stale leader before GitHub can rewrite committers', async () => {
  const leader = makePull(10);
  const harness = makeHarness({
    pulls: [leader],
    comparisonStatus: 'diverged',
    initialStates: {
      10: { mergeStateStatus: 'CLEAN' },
    },
    queueAppSlug: 'lopper-queue-controller',
  });

  await assert.rejects(runController(harness.args), /will not call GitHub branch update/);

  assert.deepEqual(harness.calls.rebased, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /rewrites PR commits/);
  assert.match(harness.calls.comments[0].body, /lopper-queue-controller\[bot\]/);
  assert.match(harness.calls.comments[0].body, /queue-me` will retry/);
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

test('drafts and stale fork branches pause before branch update or auto-merge', async (t) => {
  const cases = [
    { name: 'draft', pull: makePull(10, { draft: true }), message: /still a draft/ },
    {
      name: 'stale fork',
      pull: makePull(10, {
        head: { sha: 'fork-head', repo: { full_name: 'contributor/lopper' } },
      }),
      options: { comparisonStatus: 'behind' },
      message: /will not call GitHub branch update/,
      rejects: true,
    },
  ];

  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      const harness = makeHarness({ pulls: [scenario.pull], ...scenario.options });
      if (scenario.rejects) {
        await assert.rejects(runController(harness.args), scenario.message);
      } else {
        await runController(harness.args);
      }
      assert.deepEqual(harness.calls.rebased, []);
      assert.deepEqual(harness.calls.armed, []);
      assert.match(harness.calls.comments[0].body, scenario.message);
    });
  }
});

test('a current fork branch can arm auto-merge without a branch update', async () => {
  const fork = makePull(10, {
    head: { sha: 'fork-head', repo: { full_name: 'contributor/lopper' } },
  });
  const harness = makeHarness({ pulls: [fork], comparisonStatus: 'ahead' });

  await runController(harness.args);

  assert.deepEqual(harness.calls.rebased, []);
  assert.deepEqual(harness.calls.armed, [10]);
  assert.deepEqual(harness.calls.armExpectedHeads, ['fork-head']);
  assert.match(harness.calls.comments[0].body, /Squash auto-merge is armed/);
});

test('controller audits canonical commits across paginated compare results', async () => {
  const commits = Array.from({ length: 251 }, (_, index) =>
    makeComparisonCommit(`canonical-${index}`),
  );
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonPages: [
      { status: 'ahead', commits: commits.slice(0, 100), totalCommits: 251 },
      { status: 'behind', commits: commits.slice(100, 200), totalCommits: 251 },
      { status: 'behind', commits: commits.slice(200), totalCommits: 251 },
    ],
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.comparisons.map((input) => input.page), [1, 2, 3]);
  assert.deepEqual(harness.calls.comparisons.map((input) => input.per_page), [100, 100, 100]);
  assert.deepEqual(harness.calls.armed, [10]);
  assert.match(harness.calls.comments[0].body, /passed the PR-unique commit identity audit/);
});

test('controller bounds comparison pagination before auditing commit identity', async () => {
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonPages: [
      {
        status: 'ahead',
        commits: Array.from({ length: 100 }, (_, index) => makeComparisonCommit(`canonical-${index}`)),
        totalCommits: 501,
      },
    ],
  });

  await assert.rejects(runController(harness.args), /501 PR-unique commits exceeds the 500-commit audit limit/);

  assert.deepEqual(harness.calls.comparisons.map((input) => input.page), [1]);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(harness.calls.comments[0].body, /500-commit audit limit/);
});

test('controller fails identity audit for noncanonical commits on later compare pages', async () => {
  const canonical = Array.from({ length: 100 }, (_, index) =>
    makeComparisonCommit(`canonical-${index}`),
  );
  const botCommit = makeComparisonCommit('bot-rewrite-later-page', {
    commit: {
      committer: {
        name: 'lopper-queue-controller[bot]',
        email: '123+lopper-queue-controller[bot]@users.noreply.github.com',
      },
    },
    committer: { login: 'lopper-queue-controller[bot]', type: 'Bot' },
  });
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonPages: [
      { status: 'ahead', commits: canonical, totalCommits: 101 },
      { status: 'ahead', commits: [botCommit], totalCommits: 101 },
    ],
  });

  await assert.rejects(runController(harness.args), /Queue identity audit failed/);

  assert.deepEqual(harness.calls.comparisons.map((input) => input.page), [1, 2]);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(harness.calls.comments[0].body, /bot-rewrit/);
  assert.match(harness.calls.comments[0].body, /committer is a bot identity/);
});

test('controller pauses identity failures with bounded count context', async () => {
  const failingCommits = Array.from({ length: 125 }, (_, index) =>
    makeComparisonCommit(`bot-rewrite-${index}`, {
      commit: {
        committer: {
          name: 'lopper-queue-controller[bot]',
          email: '123+lopper-queue-controller[bot]@users.noreply.github.com',
        },
      },
      committer: { login: 'lopper-queue-controller[bot]', type: 'Bot' },
    }),
  );
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonStatus: 'ahead',
    comparisonCommits: failingCommits,
  });

  await assert.rejects(runController(harness.args), /Queue identity audit failed/);

  const comment = harness.calls.comments[0].body;
  assert.ok(comment.length <= 60000, `comment length ${comment.length} must fit GitHub limits`);
  assert.match(comment, /Found 125 failing commits; showing 10:/);
  assert.match(comment, /115 additional commit identity failures omitted/);
  assert.match(comment, /bot-rewrit/);
  assert.doesNotMatch(comment, /bot-rewrite-10/);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
});

test('controller fails identity audit before arming a bot-committed leader', async () => {
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonStatus: 'ahead',
    comparisonCommits: [
      makeComparisonCommit('bot-rewrite', {
        commit: {
          committer: {
            name: 'lopper-queue-controller[bot]',
            email: '123+lopper-queue-controller[bot]@users.noreply.github.com',
          },
        },
        committer: { login: 'lopper-queue-controller[bot]', type: 'Bot' },
      }),
    ],
  });

  await assert.rejects(runController(harness.args), /Queue identity audit failed/);

  assert.deepEqual(harness.calls.rebased, []);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(harness.calls.comments[0].body, /PR-unique commits/);
  assert.match(harness.calls.comments[0].body, /committer is a bot identity/);
});

test('a comparison failure pauses the queue with a bounded status message', async () => {
  const leader = makePull(10);
  const harness = makeHarness({
    pulls: [leader],
    comparisonStatus: 'behind',
    comparisonError: new Error('compare failed in `workflow`'),
  });

  await assert.rejects(runController(harness.args), /compare failed/);

  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /identity audit/);
  assert.match(harness.calls.comments[0].body, /compare failed in 'workflow'/);
});

test('controller pauses when the default branch moves before auto-merge is armed', async () => {
  const harness = makeHarness({
    pulls: [makePull(10)],
    branchSHAs: ['base-sha', 'new-base-sha'],
  });

  await assert.rejects(runController(harness.args), /Default branch main moved/);

  assert.deepEqual(harness.calls.branchReads, ['base-sha', 'new-base-sha']);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(harness.calls.comments[0].body, /Default branch main moved/);
});

test('controller never merges an unverified head after auto-merge arming races a push', async () => {
  const harness = makeHarness({
    pulls: [makePull(10)],
    armError: new Error('expected head mismatch'),
    armErrorHead: 'pushed-head',
  });

  await assert.rejects(runController(harness.args), /Pull request head moved/);

  assert.deepEqual(harness.calls.armExpectedHeads, ['head-10']);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(harness.calls.comments[0].body, /Pull request head moved/);
});

test('controller revalidates baseRefName and baseRefOid immediately before auto-merge or merge', async (t) => {
  const cases = [
    {
      name: 'retargeted base pauses before auto-merge',
      harness: makeHarness({
        pulls: [makePull(10)],
        stateAfterFinalBranchRead: {
          10: { baseRefName: 'release' },
        },
      }),
      message: /Pull request base changed from main to release/,
    },
    {
      name: 'base tip drift pauses before merge',
      harness: makeHarness({
        pulls: [makePull(10)],
        stateAfterFinalBranchRead: {
          10: { baseRefOid: '1234567890abcdef', mergeStateStatus: 'CLEAN' },
        },
      }),
      message: /Pull request base main moved from base-sha to 1234567890/,
    },
  ];

  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      await assert.rejects(runController(scenario.harness.args), scenario.message);

      assert.deepEqual(scenario.harness.calls.armed, []);
      assert.deepEqual(scenario.harness.calls.merged, []);
      assert.match(scenario.harness.calls.comments[0].body, scenario.message);
    });
  }
});

test('changing a queued pull request away from main disables auto-merge', async () => {
  const pull = makePull(10);
  pull.base.ref = 'release';
  const harness = makeHarness({
    eventPull: pull,
    action: 'edited',
    initialStates: {
      10: { autoMergeRequest: { enabledAt: 'before', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.disabled, [10]);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /base changed to `release`/);
  assert.equal(harness.calls.notices.length, 1);
});

test('non-default-base queue events disable auto-merge', async (t) => {
  for (const action of ['labeled', 'auto_merge_enabled']) {
    await t.test(action, async () => {
      const pull = makePull(10);
      pull.base.ref = 'release';
      const harness = makeHarness({
        eventPull: pull,
        action,
        initialStates: {
          10: { autoMergeRequest: { enabledAt: 'before', mergeMethod: 'SQUASH' } },
        },
      });

      await runController(harness.args);

      assert.deepEqual(harness.calls.disabled, [10]);
      assert.deepEqual(harness.calls.armed, []);
      assert.match(harness.calls.comments[0].body, /base changed to `release`/);
      assert.equal(harness.calls.notices.length, 1);
    });
  }
});

test('a non-default-base pause comment is not replaced by a queue position', async () => {
  const leader = makePull(10);
  const releasePull = makePull(20);
  releasePull.base.ref = 'release';
  const harness = makeHarness({
    pulls: [leader],
    eventPull: releasePull,
    action: 'labeled',
    initialStates: {
      20: { autoMergeRequest: { enabledAt: 'manual', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  const eventComments = harness.calls.comments.filter(
    (comment) => comment.number === 20 || comment.number === undefined,
  );
  assert.deepEqual(harness.calls.disabled, [20]);
  assert.equal(eventComments.length, 1);
  assert.match(eventComments[0].body, /base changed to `release`/);
  assert.doesNotMatch(eventComments[0].body, /Queued behind/);
  assert.deepEqual(harness.calls.armed, [10]);
});

test('manually enabling auto-merge on a follower restores queue ordering', async () => {
  const leader = makePull(10);
  const follower = makePull(20);
  const harness = makeHarness({
    pulls: [leader, follower],
    eventPull: follower,
    action: 'auto_merge_enabled',
    initialStates: {
      20: { autoMergeRequest: { enabledAt: 'manual', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.disabled, [20]);
  assert.deepEqual(harness.calls.armed, [10]);
});

test("the queue App's leader auto-merge event does not trigger a disable-enable loop", async () => {
  const leader = makePull(10);
  const harness = makeHarness({
    pulls: [leader],
    eventPull: leader,
    action: 'auto_merge_enabled',
    queueAppSlug: 'queue-app',
    sender: { login: 'queue-app[bot]', type: 'Bot' },
    initialStates: {
      10: { autoMergeRequest: { enabledAt: 'controller', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.disabled, []);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.notices[0], /Ignoring the queue App's auto-merge event/);
});
