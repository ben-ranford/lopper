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
      ref: `queue-me-${number}`,
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

function makeRenovateCommit(sha = 'renovate-commit', overrides = {}) {
  const renovate = {
    login: 'renovate[bot]',
    type: 'Bot',
    id: 29139614,
  };
  const rawRenovate = {
    name: 'renovate[bot]',
    email: '29139614+renovate[bot]@users.noreply.github.com',
  };
  const { commit: commitOverrides = {}, ...rest } = overrides;
  return {
    sha,
    commit: {
      author: { ...rawRenovate },
      committer: { ...rawRenovate },
      verification: { verified: true, reason: 'valid' },
      ...commitOverrides,
    },
    author: { ...renovate },
    committer: { ...renovate },
    ...rest,
  };
}

function makeRenovateActivity(sha = 'renovate-commit', overrides = {}) {
  return {
    after: sha,
    actor: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
    activity_type: 'push',
    ref: 'refs/heads/queue-me-10',
    ...overrides,
  };
}

function makeQueueBranchUpdateCommit(sha = 'queue-update-commit', appSlug = 'lopper-queue-controller', overrides = {}) {
  const appLogin = `${appSlug}[bot]`;
  const { commit: commitOverrides = {}, ...rest } = overrides;
  return {
    sha,
    parents: [{ sha: 'pr-head' }, { sha: 'base-head' }],
    author: { login: appLogin, type: 'Bot', id: 123456 },
    committer: { login: 'web-flow', type: 'User', id: 19864447 },
    commit: {
      author: { name: appLogin, email: `123456+${appLogin}@users.noreply.github.com` },
      committer: { name: 'GitHub', email: 'noreply@github.com' },
      verification: { verified: true, reason: 'valid' },
      ...commitOverrides,
    },
    ...rest,
  };
}

function makeQueueBranchUpdateActivity(sha = 'queue-update-commit', appSlug = 'lopper-queue-controller', overrides = {}) {
  return {
    after: sha,
    actor: { login: `${appSlug}[bot]`, type: 'Bot', id: 123456 },
    activity_type: 'push',
    ref: 'refs/heads/queue-me-10',
    ...overrides,
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
  const fixtureCommits = [
    ...(options.comparisonCommits || []),
    ...Object.values(options.comparisonCommitsByNumber || {}).flat(),
    ...(options.comparisonPages || []).flatMap((page) => page.commits || []),
  ];
  const trustedRenovateCommits = fixtureCommits.filter(
    (commit) => commit?.author?.login === 'renovate[bot]' && commit?.author?.id === 29139614,
  );
  const trustedQueueUpdateCommits = fixtureCommits.filter((commit) =>
    options.queueAppSlug && commit?.author?.login === `${options.queueAppSlug}[bot]` &&
    commit?.parents?.length >= 2,
  );
  const calls = {
    activities: [],
    armed: [],
    armExpectedHeads: [],
    branchReads: [],
    comments: [],
    createdLabels: [],
    disabled: [],
    merged: [],
    notices: [],
    branchUpdates: [],
    rebased: [],
  };
  const repository = { default_branch: 'main', full_name: 'octo/lopper' };

  const github = {
    request: async (route, input) => {
      calls.activities.push({ route, input });
      if (options.activityError) throw options.activityError;
      const activities = options.activities || [
        ...trustedRenovateCommits.map((commit) => ({
          after: commit.sha,
          actor: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
          activity_type: 'push',
          ref: input.ref,
        })),
        ...trustedQueueUpdateCommits.map((commit) => ({
          after: commit.sha,
          actor: { login: `${options.queueAppSlug}[bot]`, type: 'Bot', id: 123456 },
          activity_type: 'push',
          ref: input.ref,
        })),
      ];
      return { data: activities };
    },
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
        updateBranch: async (input) => {
          calls.branchUpdates.push(input);
          if (options.branchUpdateError) throw options.branchUpdateError;
          return { status: 202, data: { message: 'Updating pull request branch.' } };
        },
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
          const matchedPull = allPulls.find((candidate) =>
            input.basehead.endsWith(`...${candidate.head.sha}`),
          );
          const perPullStatus = options.comparisonStatuses?.[matchedPull?.number];
          const perPullCommits = options.comparisonCommitsByNumber?.[matchedPull?.number];
          if (options.comparisonPages) {
            const page = options.comparisonPages[(input.page || 1) - 1];
            if (!page) {
              return {
                data: {
                  status: perPullStatus || options.comparisonStatus || 'ahead',
                  commits: [],
                  total_commits: options.totalCommits ?? 0,
                },
              };
            }
            return {
              data: {
                status: page.status || perPullStatus || options.comparisonStatus || 'ahead',
                commits: page.commits || [],
                total_commits: page.totalCommits ?? options.totalCommits ?? page.commits?.length ?? 0,
              },
            };
          }
          const commits = perPullCommits || options.comparisonCommits || [makeComparisonCommit()];
          return {
            data: {
              status: perPullStatus || options.comparisonStatus || 'ahead',
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
  const pulls = [{ number: 20 }, { number: 3 }, { number: 11 }];
  assert.deepEqual(testables.sortQueuedPulls(pulls).map((pull) => pull.number), [3, 11, 20]);
});

test('hasLabel accepts REST label objects and string labels', () => {
  assert.equal(testables.hasLabel({ labels: [{ name: 'queue-me' }] }, 'queue-me'), true);
  assert.equal(testables.hasLabel({ labels: ['queue-me'] }, 'queue-me'), true);
  assert.equal(testables.hasLabel({ labels: [{ name: 'other' }] }, 'queue-me'), false);
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
  const failures = Array.from({ length: 12 }, (_, index) => `${index}: identities differ`);
  const message = testables.queueIdentityFailureMessage(failures);
  assert.match(message, /Found 12 failing commits; showing 10:/);
  assert.match(message, /2 additional commit identity failures omitted/);
});

test('sticky queue status comments are safely truncated before GitHub updates', () => {
  const truncated = testables.truncateCommentBody('x'.repeat(60005));
  assert.ok(truncated.length <= 60000, `expected truncated length <= 60000, got ${truncated.length}`);
  assert.match(truncated, /truncated to fit GitHub comment limits/);
  assert.equal(testables.truncateCommentBody('short'), 'short');
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

test('commit identity audit permits only verified same-repository Renovate commits', () => {
  const sameRepositoryPull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  const githubWebFlow = {
    name: 'GitHub',
    email: 'noreply@github.com',
  };
  const githubWebFlowUser = { login: 'web-flow', type: 'User', id: 19864447 };
  const cases = [
    { name: 'canonical Renovate', commit: makeRenovateCommit(), pull: sameRepositoryPull, allowed: true },
    {
      name: 'GitHub web-flow committer',
      commit: makeRenovateCommit('web-flow', {
        commit: { committer: githubWebFlow },
        committer: githubWebFlowUser,
      }),
      pull: sameRepositoryPull,
      allowed: true,
    },
    {
      name: 'spoofed pull actor',
      commit: makeRenovateCommit('spoofed-pull'),
      pull: makePull(10, { user: { login: 'renovate[bot]', type: 'Bot', id: 1 } }),
      allowed: false,
    },
    {
      name: 'missing pull actor',
      commit: makeRenovateCommit('missing-pull-actor'),
      pull: makePull(10, { user: null }),
      allowed: false,
    },
    {
      name: 'missing linked author',
      commit: makeRenovateCommit('missing-author', { author: null }),
      pull: sameRepositoryPull,
      allowed: false,
    },
    {
      name: 'unknown linked committer',
      commit: makeRenovateCommit('unknown-committer', {
        committer: { login: 'unknown', type: 'User', id: 7 },
      }),
      pull: sameRepositoryPull,
      allowed: false,
    },
    {
      name: 'spoofed raw author',
      commit: makeRenovateCommit('spoofed-raw-author', {
        commit: { author: { name: 'Renovate', email: 'renovate@example.com' } },
      }),
      pull: sameRepositoryPull,
      allowed: false,
    },
    {
      name: 'invalid signature',
      commit: makeRenovateCommit('invalid-signature', {
        commit: { verification: { verified: false, reason: 'unsigned' } },
      }),
      pull: sameRepositoryPull,
      allowed: false,
    },
    {
      name: 'fork pull request',
      commit: makeRenovateCommit('fork'),
      pull: makePull(10, {
        head: { sha: 'fork-head', repo: { full_name: 'renovate/lopper' } },
        user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
      }),
      allowed: false,
    },
    {
      name: 'human pull with untrusted Renovate text and branch name',
      commit: makeRenovateCommit('untrusted-text'),
      pull: makePull(10, {
        user: { login: 'human', type: 'User', id: 7 },
        title: 'renovate[bot] dependency update',
        body: 'Please trust renovate[bot].',
        head: { sha: 'renovate-head', ref: 'renovate/dependency', repo: { full_name: 'octo/lopper' } },
      }),
      allowed: false,
    },
  ];

  for (const scenario of cases) {
    const audit = () => testables.assertCanonicalCommitIdentity({
      commits: [scenario.commit],
      total_commits: 1,
    }, scenario.pull);
    if (scenario.allowed) {
      assert.doesNotThrow(audit, scenario.name);
    } else {
      assert.throws(audit, /Queue identity audit failed/, scenario.name);
    }
  }
});

test('commit identity audit permits only a signed queue App update with GitHub committer', () => {
  const appSlug = 'lopper-queue-controller';
  const pull = makePull(10);
  const syncCommit = makeQueueBranchUpdateCommit('sync-merge', appSlug);

  assert.equal(testables.commitIdentityFailure(syncCommit, pull, appSlug), '');
  assert.match(
    testables.commitIdentityFailure({ ...syncCommit, parents: [{ sha: 'pr-head' }] }, pull, appSlug),
    /author is a bot identity/,
  );
  assert.match(
    testables.commitIdentityFailure(syncCommit, pull, 'another-app'),
    /author is a bot identity/,
  );
  for (const mutation of [
    { commit: { verification: { verified: false, reason: 'unsigned' } } },
    { commit: { committer: { name: 'queue bot', email: 'bot@example.com' } } },
    { committer: { login: 'attacker', type: 'User', id: 1 } },
  ]) {
    assert.notEqual(testables.commitIdentityFailure({ ...syncCommit, ...mutation }, pull, appSlug), '');
  }
  assert.match(
    testables.commitIdentityFailure(syncCommit, makePull(10, {
      head: { ...pull.head, repo: { full_name: 'contributor/lopper' } },
    }), appSlug),
    /author is a bot identity/,
  );
});

test('Renovate exception rejects every trusted identity tuple mutation', () => {
  const pull = makePull(10, { user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 } });
  const commit = makeRenovateCommit();
  const mutations = [
    ['pull.user.login', 'renovate'], ['pull.user.type', 'User'], ['pull.user.id', 1],
    ['pull.base.repo.owner.login', 'other'], ['pull.base.repo.name', 'other'],
    ['pull.head.repo.full_name', 'renovate/lopper'],
    ['commit.author.login', 'renovate'], ['commit.author.type', 'User'], ['commit.author.id', 1],
    ['commit.commit.author.name', 'Renovate'], ['commit.commit.author.email', 'renovate@example.com'],
    ['commit.commit.verification.verified', false], ['commit.commit.verification.reason', 'unsigned'],
    ['commit.committer.login', 'renovate'], ['commit.committer.type', 'User'], ['commit.committer.id', 1],
    ['commit.commit.committer.name', 'Renovate'], ['commit.commit.committer.email', 'renovate@example.com'],
  ];
  const mutate = (value, path, replacement) => {
    const copy = structuredClone(value);
    const keys = path.split('.');
    let target = copy;
    for (const key of keys.slice(0, -1)) target = target[key];
    target[keys.at(-1)] = replacement;
    return copy;
  };

  for (const [path, replacement] of mutations) {
    const isPull = path.startsWith('pull.');
    const mutatedPull = isPull ? mutate(pull, path.slice(5), replacement) : pull;
    const mutatedCommit = isPull ? commit : mutate(commit, path.slice(7), replacement);
    assert.throws(
      () => testables.assertCanonicalCommitIdentity({ commits: [mutatedCommit], total_commits: 1 }, mutatedPull),
      /Queue identity audit failed/,
      path,
    );
  }
  const webFlow = makeRenovateCommit('web-flow', {
    committer: { login: 'web-flow', type: 'User', id: 19864447 },
    commit: { committer: { name: 'GitHub', email: 'noreply@github.com' } },
  });
  for (const path of ['committer.login', 'committer.type', 'committer.id',
    'commit.committer.name', 'commit.committer.email', 'commit.verification']) {
    for (const replacement of [undefined, 'spoofed']) {
      assert.throws(
        () => testables.assertCanonicalCommitIdentity({
          commits: [mutate(webFlow, path, replacement)], total_commits: 1,
        }, pull),
        /Queue identity audit failed/,
        path,
      );
    }
  }

});

test('a Renovate pull accepts trusted commits but rejects mixed bad history', () => {
  const pull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  assert.throws(
    () => testables.assertCanonicalCommitIdentity({
      commits: [
        makeRenovateCommit('trusted'),
        makeComparisonCommit('bad-bot', {
          committer: { login: 'other-bot[bot]', type: 'Bot', id: 2 },
        }),
      ],
      total_commits: 2,
    }, pull),
    /Queue identity audit failed/,
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

test('controller requests a guarded base update for a stale same-repository leader', async () => {
  const leader = makePull(10);
  const harness = makeHarness({
    pulls: [leader],
    comparisonStatus: 'diverged',
    initialStates: {
      10: { mergeStateStatus: 'CLEAN' },
    },
    queueAppSlug: 'lopper-queue-controller',
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.branchUpdates, [{
    owner: 'octo', repo: 'lopper', pull_number: 10, expected_head_sha: 'head-10',
  }]);
  assert.deepEqual(harness.calls.rebased, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /GitHub is updating this pull request branch/);
  assert.match(harness.calls.comments[0].body, /before enabling auto-merge/);
});

test('controller audits the queue App merge commit before arming the updated head', async () => {
  const appSlug = 'lopper-queue-controller';
  const queueMerge = makeQueueBranchUpdateCommit('queue-base-update', appSlug);
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonCommits: [makeComparisonCommit(), queueMerge],
    queueAppSlug: appSlug,
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.armed, [10]);
  assert.match(harness.calls.comments[0].body, /passed the PR-unique commit identity audit/);
});

test('controller requires exact queue App branch activity before trusting its update commit', async (t) => {
  const appSlug = 'lopper-queue-controller';
  const queueMerge = makeQueueBranchUpdateCommit('queue-base-update', appSlug);
  const cases = [
    { name: 'missing activity', activities: [] },
    { name: 'wrong commit SHA', activities: [makeQueueBranchUpdateActivity('other-sha', appSlug)] },
    {
      name: 'spoofed actor',
      activities: [makeQueueBranchUpdateActivity(queueMerge.sha, appSlug, {
        actor: { login: 'attacker', type: 'User', id: 1 },
      })],
    },
    {
      name: 'wrong branch',
      activities: [makeQueueBranchUpdateActivity(queueMerge.sha, appSlug, { ref: 'refs/heads/other' })],
    },
    {
      name: 'wrong activity type',
      activities: [makeQueueBranchUpdateActivity(queueMerge.sha, appSlug, { activity_type: 'branch_deletion' })],
    },
    { name: 'activity API failure', activityError: new Error('activity unavailable') },
  ];

  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      const harness = makeHarness({
        pulls: [makePull(10)],
        comparisonCommits: [queueMerge],
        queueAppSlug: appSlug,
        ...scenario,
      });

      await runController(harness.args);

      assert.equal(harness.calls.activities.length, 1);
      assert.deepEqual(harness.calls.armed, []);
      assert.match(harness.calls.comments[0].body, /identity audit|provenance/i);
    });
  }
});

test('controller proves queue App update commit SHA from branch activity', async () => {
  const appSlug = 'lopper-queue-controller';
  const queueMerge = makeQueueBranchUpdateCommit('queue-base-update', appSlug);
  const harness = makeHarness({
    pulls: [makePull(10)],
    comparisonCommits: [queueMerge],
    queueAppSlug: appSlug,
    activities: [makeQueueBranchUpdateActivity(queueMerge.sha, appSlug)],
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.activities, [{
    route: 'GET /repos/{owner}/{repo}/activity',
    input: {
      owner: 'octo', repo: 'lopper', ref: 'refs/heads/queue-me-10', per_page: 100, direction: 'desc',
    },
  }]);
  assert.deepEqual(harness.calls.armed, [10]);
});

test('controller updates a stale same-repository Renovate pull after provenance audit', async () => {
  const renovatePull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  const harness = makeHarness({
    pulls: [renovatePull],
    comparisonStatus: 'behind',
    comparisonCommits: [makeRenovateCommit()],
    queueAppSlug: 'lopper-queue-controller',
  });

  await runController(harness.args);

  assert.equal(harness.calls.branchUpdates.length, 1);
  assert.deepEqual(harness.calls.rebased, []);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /GitHub is updating this pull request branch/);
});

test('controller does not update a stale fork branch', async () => {
  const forkPull = makePull(10, {
    head: { sha: 'head-10', ref: 'queue-me-10', repo: { full_name: 'contributor/lopper' } },
  });
  const harness = makeHarness({ pulls: [forkPull], comparisonStatus: 'behind' });

  await runController(harness.args);

  assert.deepEqual(harness.calls.branchUpdates, []);
  assert.match(harness.calls.comments[0].body, /belongs to a fork/);
});

test('controller reports a rejected branch update and does not arm auto-merge', async () => {
  const harness = makeHarness({
    pulls: [makePull(10)], comparisonStatus: 'behind',
    branchUpdateError: new Error('branch update failed'),
  });

  await runController(harness.args);

  assert.equal(harness.calls.branchUpdates.length, 1);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /Queue paused while updating/);
});

test('controller arms a verified same-repository Renovate pull without rewriting its branch', async () => {
  const renovatePull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  const harness = makeHarness({
    pulls: [renovatePull],
    comparisonCommits: [makeRenovateCommit()],
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.rebased, []);
  assert.deepEqual(harness.calls.armed, [10]);
  assert.match(harness.calls.comments[0].body, /passed the PR-unique commit identity audit/);
});

test('controller proves Renovate provenance with one bounded branch activity request', async () => {
  const renovatePull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  const harness = makeHarness({
    pulls: [renovatePull],
    comparisonCommits: [makeRenovateCommit()],
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.activities, [{
    route: 'GET /repos/{owner}/{repo}/activity',
    input: {
      owner: 'octo', repo: 'lopper', ref: 'refs/heads/queue-me-10', per_page: 100, direction: 'desc',
    },
  }]);
  assert.deepEqual(harness.calls.armed, [10]);
});

test('controller pauses Renovate provenance failures before auto-merge', async (t) => {
  const cases = [
    { name: 'spoofed actor', activities: [makeRenovateActivity('renovate-commit', { actor: { login: 'attacker', type: 'User', id: 1 } })] },
    { name: 'wrong commit SHA', activities: [makeRenovateActivity('other-sha')] },
    { name: 'missing activity SHA', activities: [makeRenovateActivity('renovate-commit', { after: null })] },
    { name: 'missing activity', activities: [] },
    { name: 'wrong branch ref', activities: [makeRenovateActivity('renovate-commit', { ref: 'refs/heads/other' })] },
    { name: 'wrong activity type', activities: [makeRenovateActivity('renovate-commit', { activity_type: 'branch_deletion' })] },
    { name: 'activity API failure', activityError: new Error('activity unavailable') },
  ];
  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      const renovatePull = makePull(10, {
        user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
      });
      const harness = makeHarness({
        pulls: [renovatePull], comparisonCommits: [makeRenovateCommit()], ...scenario,
      });

      await runController(harness.args);

      assert.equal(harness.calls.activities.length, 1);
      assert.deepEqual(harness.calls.armed, []);
      assert.match(harness.calls.comments[0].body, /provenance|identity audit/i);
    });
  }
});

test('controller accepts Renovate branch creation and force-push provenance', async (t) => {
  for (const activity_type of ['branch_creation', 'force_push']) {
    await t.test(activity_type, async () => {
      const renovatePull = makePull(10, {
        user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
      });
      const harness = makeHarness({
        pulls: [renovatePull],
        comparisonCommits: [makeRenovateCommit()],
        activities: [makeRenovateActivity('renovate-commit', { activity_type })],
      });

      await runController(harness.args);

      assert.deepEqual(harness.calls.armed, [10]);
    });
  }
});

test('controller does not read branch activity for canonical human commits', async () => {
  const harness = makeHarness({ pulls: [makePull(10)] });

  await runController(harness.args);

  assert.deepEqual(harness.calls.activities, []);
  assert.deepEqual(harness.calls.armed, [10]);
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
      message: /belongs to a fork/,
    },
  ];

  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      const harness = makeHarness({ pulls: [scenario.pull], ...scenario.options });
      await runController(harness.args);
      assert.deepEqual(harness.calls.rebased, []);
      if (scenario.name === 'stale fork') {
        assert.deepEqual(harness.calls.branchUpdates, []);
      }
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

test('a stale leader advances the queue to the next eligible pull request', async () => {
  const leader = makePull(10);
  const follower = makePull(20);
  const harness = makeHarness({
    pulls: [leader, follower],
    comparisonStatuses: { 10: 'behind', 20: 'ahead' },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.armed, [20]);
  assert.deepEqual(harness.calls.merged, []);
  assert.equal(harness.calls.branchUpdates[0].pull_number, 10);
  assert.match(commentsFor(harness, 10), /GitHub is updating this pull request branch/);
  assert.match(commentsFor(harness, 10), /next queued pull request/);
  assert.match(commentsFor(harness, 20), /Squash auto-merge is armed/);
});

test('a stale leader skip refreshes queued followers behind the selected eligible pull request', async () => {
  const blocked = makePull(10);
  const selected = makePull(20);
  const follower = makePull(30);
  const harness = makeHarness({
    pulls: [blocked, selected, follower],
    eventPull: follower,
    comparisonStatuses: { 10: 'behind', 20: 'ahead' },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.armed, [20]);
  assert.equal(harness.calls.branchUpdates[0].pull_number, 10);
  assert.match(commentsFor(harness, 10), /GitHub is updating this pull request branch/);
  assert.match(commentsFor(harness, 30), /Queued behind #20/);
  assert.match(commentsFor(harness, 30), /retried after their branches/);
});

test('a leader that fails the identity audit advances the queue to the next eligible pull request', async () => {
  const leader = makePull(10);
  const follower = makePull(20);
  const harness = makeHarness({
    pulls: [leader, follower],
    comparisonStatuses: { 10: 'ahead', 20: 'ahead' },
    comparisonCommitsByNumber: {
      10: [
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
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.armed, [20]);
  assert.match(commentsFor(harness, 10), /committer is a bot identity/);
  assert.match(commentsFor(harness, 10), /next queued pull request/);
  assert.match(commentsFor(harness, 20), /Squash auto-merge is armed/);
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

test("the queue App's auto-merge event for an advanced follower does not trigger a disable-enable loop", async () => {
  const leader = makePull(10);
  const follower = makePull(20);
  const harness = makeHarness({
    pulls: [leader, follower],
    eventPull: follower,
    action: 'auto_merge_enabled',
    queueAppSlug: 'queue-app',
    sender: { login: 'queue-app[bot]', type: 'Bot' },
    initialStates: {
      20: { autoMergeRequest: { enabledAt: 'controller', mergeMethod: 'SQUASH' } },
    },
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.disabled, []);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.notices[0], /Ignoring the queue App's auto-merge event/);
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

test('controller audits verified Renovate commits on every compare page', async () => {
  const renovatePull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  const harness = makeHarness({
    pulls: [renovatePull],
    comparisonPages: [
      { status: 'ahead', commits: [makeRenovateCommit('renovate-page-one')], totalCommits: 2 },
      { status: 'ahead', commits: [makeRenovateCommit('renovate-page-two')], totalCommits: 2 },
    ],
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.comparisons.map((input) => input.page), [1, 2]);
  assert.deepEqual(harness.calls.armed, [10]);
});

test('controller rejects an invalid Renovate commit on a later compare page', async () => {
  const renovatePull = makePull(10, {
    user: { login: 'renovate[bot]', type: 'Bot', id: 29139614 },
  });
  const harness = makeHarness({
    pulls: [renovatePull],
    comparisonPages: [
      { status: 'ahead', commits: [makeRenovateCommit('first')], totalCommits: 2 },
      {
        status: 'ahead', totalCommits: 2,
        commits: [makeRenovateCommit('invalid-later', {
          commit: { verification: { verified: true, reason: 'unknown_key' } },
        })],
      },
    ],
  });

  await runController(harness.args);

  assert.deepEqual(harness.calls.comparisons.map((input) => input.page), [1, 2]);
  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /invalid-la/);
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

  await runController(harness.args);

  assert.deepEqual(harness.calls.comparisons.map((input) => input.page), [1]);
  assert.deepEqual(harness.calls.armed, []);
  assert.deepEqual(harness.calls.merged, []);
  assert.match(harness.calls.comments[0].body, /500-commit audit limit/);
  assert.match(harness.calls.notices.at(-1), /waiting for a clean queue identity audit/);
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

  await runController(harness.args);

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

  await runController(harness.args);

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

  await runController(harness.args);

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

  await runController(harness.args);

  assert.deepEqual(harness.calls.armed, []);
  assert.match(harness.calls.comments[0].body, /identity audit/);
  assert.match(harness.calls.comments[0].body, /compare failed in 'workflow'/);
});
