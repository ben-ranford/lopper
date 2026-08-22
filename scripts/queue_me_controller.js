'use strict';

const COMMENT_MARKER = '<!-- queue-me-controller -->';
const CONFLICT_BLOCK_RE = /<!-- queue-me-conflict-block head=([^\s>]+) base=([^\s>]+) -->/;
const DEFAULT_QUEUE_LABEL = 'queue-me';

function labelName(label) {
  return typeof label === 'string' ? label : label?.name;
}

function hasLabel(pull, queueLabel) {
  return (pull.labels || []).some((label) => labelName(label) === queueLabel);
}

function sortQueuedPulls(pulls) {
  return [...pulls].sort((left, right) => left.number - right.number);
}

function isBranchCurrent(comparisonStatus) {
  return comparisonStatus === 'ahead' || comparisonStatus === 'identical';
}

function shortSHA(sha) {
  return typeof sha === 'string' ? sha.slice(0, 10) : 'unknown';
}

function safeError(error) {
  const message = error instanceof Error ? error.message : String(error);
  return message.replace(/[\r\n]+/g, ' ').replaceAll('`', "'").slice(0, 1200);
}

function isMergeConflict(error) {
  return /\bconflict(?:s|ed|ing)?\b|\bpull request is not mergeable\b/i.test(safeError(error));
}

function conflictBlockMarker(headSHA, baseSHA) {
  return `<!-- queue-me-conflict-block head=${headSHA} base=${baseSHA} -->`;
}

function parseConflictBlock(body) {
  if (typeof body !== 'string') {
    return null;
  }
  const match = body.match(CONFLICT_BLOCK_RE);
  if (!match) {
    return null;
  }
  return { headSHA: match[1], baseSHA: match[2] };
}

async function ensureQueueLabel(github, owner, repo, queueLabel) {
  try {
    await github.rest.issues.getLabel({ owner, repo, name: queueLabel });
  } catch (error) {
    if (error?.status !== 404) {
      throw error;
    }
    await github.rest.issues.createLabel({
      owner,
      repo,
      name: queueLabel,
      color: '1D76DB',
      description: 'Rebase and squash-merge automatically in deterministic PR order',
    });
  }
}

async function pullState(github, owner, repo, number) {
  const result = await github.graphql(
    `query QueuePullState($owner: String!, $repo: String!, $number: Int!) {
      repository(owner: $owner, name: $repo) {
        pullRequest(number: $number) {
          id
          number
          baseRefName
          baseRefOid
          headRefOid
          isDraft
          mergeable
          mergeStateStatus
          autoMergeRequest {
            enabledAt
            mergeMethod
          }
        }
      }
    }`,
    { owner, repo, number },
  );
  return result.repository.pullRequest;
}

function assertExpectedBaseState(state, expectedBaseRefName, expectedBaseRefOid) {
  if (state.baseRefName !== expectedBaseRefName) {
    throw new Error(
      `Pull request base changed from ${expectedBaseRefName} to ${state.baseRefName || 'unknown'} while advancing the queue.`,
    );
  }
  if (state.baseRefOid !== expectedBaseRefOid) {
    throw new Error(
      `Pull request base ${expectedBaseRefName} moved from ${shortSHA(expectedBaseRefOid)} to ${shortSHA(state.baseRefOid)} while advancing the queue.`,
    );
  }
}

async function statusComment(github, owner, repo, number) {
  const comments = await github.paginate(github.rest.issues.listComments, {
    owner,
    repo,
    issue_number: number,
    per_page: 100,
  });
  return comments.find(
    (comment) =>
      comment.user?.type === 'Bot' &&
      typeof comment.body === 'string' &&
      comment.body.includes(COMMENT_MARKER),
  );
}

async function syncStatusComment(
  github,
  owner,
  repo,
  number,
  body,
  { createIfMissing = true } = {},
) {
  const existing = await statusComment(github, owner, repo, number);
  const nextBody = `${COMMENT_MARKER}\n${body}`;
  if (existing?.body === nextBody) {
    return;
  }
  if (existing) {
    await github.rest.issues.updateComment({
      owner,
      repo,
      comment_id: existing.id,
      body: nextBody,
    });
    return;
  }
  if (!createIfMissing) {
    return;
  }
  await github.rest.issues.createComment({
    owner,
    repo,
    issue_number: number,
    body: nextBody,
  });
}

async function isBlockedOnSameHead(github, owner, repo, pull, defaultBranchSHA) {
  const existing = await statusComment(github, owner, repo, pull.number);
  const block = parseConflictBlock(existing?.body);
  return block?.headSHA === pull.head.sha && block.baseSHA === defaultBranchSHA;
}

async function firstEligibleQueueIndex(github, owner, repo, queued, defaultBranchSHA) {
  for (const [index, pull] of queued.entries()) {
    if (!(await isBlockedOnSameHead(github, owner, repo, pull, defaultBranchSHA))) {
      return index;
    }
  }
  return queued.length;
}

function followerStatusBody(leaderNumber, { conflictSkipped = false } = {}) {
  const orderingSummary = conflictSkipped
    ? 'Earlier queued pull requests with rebase conflicts are retried after their branches change.'
    : 'Pull requests advance in ascending number order.';
  return `## Queue status\n\nQueued behind #${leaderNumber}. ${orderingSummary}`;
}

async function syncFollowerStatuses({
  github,
  owner,
  repo,
  followers,
  leaderNumber,
  eventQueueEntry,
  eventAction,
  conflictSkipped = false,
  disableFollowers = false,
}) {
  for (const follower of followers) {
    if (disableFollowers) {
      await disableAutoMerge(github, owner, repo, follower.number);
    }
    await syncStatusComment(
      github,
      owner,
      repo,
      follower.number,
      followerStatusBody(leaderNumber, { conflictSkipped }),
      {
        createIfMissing:
          eventQueueEntry?.number === follower.number && eventAction === 'labeled',
      },
    );
  }
}

async function disableAutoMerge(github, owner, repo, number) {
  const state = await pullState(github, owner, repo, number);
  if (!state?.autoMergeRequest) {
    return;
  }
  await github.graphql(
    `mutation DisableQueueAutoMerge($pullRequestId: ID!) {
      disablePullRequestAutoMerge(input: { pullRequestId: $pullRequestId }) {
        pullRequest { number }
      }
    }`,
    { pullRequestId: state.id },
  );
}

async function rebaseOntoDefault(
  github,
  pull,
  defaultBranchSHA,
  { canUpdateBranch = true } = {},
) {
  const { data: comparison } = await github.rest.repos.compareCommitsWithBasehead({
    owner: pull.base.repo.owner.login,
    repo: pull.base.repo.name,
    basehead: `${defaultBranchSHA}...${pull.head.sha}`,
  });
  if (isBranchCurrent(comparison.status)) {
    return { headSHA: pull.head.sha, rebased: false };
  }
  if (!canUpdateBranch) {
    return { headSHA: pull.head.sha, rebased: false, needsManualRebase: true };
  }
  const result = await github.graphql(
    `mutation RebaseQueuedPull($pullRequestId: ID!, $expectedHeadOid: GitObjectID!) {
      updatePullRequestBranch(input: {
        pullRequestId: $pullRequestId
        expectedHeadOid: $expectedHeadOid
        updateMethod: REBASE
      }) {
        pullRequest {
          headRefOid
          number
        }
      }
    }`,
    {
      pullRequestId: pull.node_id,
      expectedHeadOid: pull.head.sha,
    },
  );
  return {
    headSHA: result.updatePullRequestBranch.pullRequest.headRefOid,
    rebased: true,
  };
}

async function mergeNow(github, pullRequestId, expectedHeadOid) {
  return github.graphql(
    `mutation MergeQueuedPull($pullRequestId: ID!, $expectedHeadOid: GitObjectID!) {
      mergePullRequest(input: {
        pullRequestId: $pullRequestId
        expectedHeadOid: $expectedHeadOid
        mergeMethod: SQUASH
      }) {
        pullRequest { number merged mergedAt }
      }
    }`,
    { pullRequestId, expectedHeadOid },
  );
}

async function armAutoMerge(github, pullRequestId, expectedHeadOid) {
  return github.graphql(
    `mutation ArmQueueAutoMerge($pullRequestId: ID!, $expectedHeadOid: GitObjectID!) {
      enablePullRequestAutoMerge(input: {
        pullRequestId: $pullRequestId
        expectedHeadOid: $expectedHeadOid
        mergeMethod: SQUASH
      }) {
        pullRequest {
          number
          autoMergeRequest { enabledAt mergeMethod }
        }
      }
    }`,
    { pullRequestId, expectedHeadOid },
  );
}

async function armOrMerge(github, state, { expectedBaseRefName, expectedBaseRefOid }) {
  assertExpectedBaseState(state, expectedBaseRefName, expectedBaseRefOid);
  if (state.autoMergeRequest) {
    return 'armed';
  }
  if (state.mergeable === 'MERGEABLE' && state.mergeStateStatus === 'CLEAN') {
    await mergeNow(github, state.id, state.headRefOid);
    return 'merged';
  }
  try {
    await armAutoMerge(github, state.id, state.headRefOid);
    return 'armed';
  } catch (error) {
    const refreshed = await pullStateByID(github, state.id);
    if (refreshed.headRefOid !== state.headRefOid) {
      throw new Error(
        `Pull request head moved from ${shortSHA(state.headRefOid)} to ${shortSHA(refreshed.headRefOid)} while arming auto-merge.`,
      );
    }
    assertExpectedBaseState(refreshed, expectedBaseRefName, expectedBaseRefOid);
    if (refreshed.mergeable === 'MERGEABLE' && refreshed.mergeStateStatus === 'CLEAN') {
      await mergeNow(github, refreshed.id, state.headRefOid);
      return 'merged';
    }
    throw error;
  }
}

async function pullStateByID(github, pullRequestId) {
  const result = await github.graphql(
    `query QueuePullStateByID($pullRequestId: ID!) {
      node(id: $pullRequestId) {
        ... on PullRequest {
          id
          number
          baseRefName
          baseRefOid
          headRefOid
          mergeable
          mergeStateStatus
          autoMergeRequest { enabledAt mergeMethod }
        }
      }
    }`,
    { pullRequestId },
  );
  return result.node;
}

async function reconcileEventPull({
  github,
  context,
  owner,
  repo,
  queueLabel,
  defaultBranch,
  eventPull,
}) {
  if (!eventPull || context.eventName !== 'pull_request_target') {
    return;
  }
  if (
    context.payload.action === 'unlabeled' &&
    context.payload.label?.name === queueLabel
  ) {
    await disableAutoMerge(github, owner, repo, eventPull.number);
    await syncStatusComment(
      github,
      owner,
      repo,
      eventPull.number,
      `## Queue status\n\nRemoved from \`${queueLabel}\`; automatic merge is disabled.`,
    );
    return;
  }
  if (!hasLabel(eventPull, queueLabel) || eventPull.base?.ref === defaultBranch) {
    return;
  }
  await disableAutoMerge(github, owner, repo, eventPull.number);
  await syncStatusComment(
    github,
    owner,
    repo,
    eventPull.number,
    `## Queue status\n\nQueue paused: the base changed to \`${eventPull.base?.ref || 'unknown'}\`. Automatic merge is disabled because \`${queueLabel}\` pull requests must target \`${defaultBranch}\`.`,
  );
}

function isQueueAppAutoMergeEvent({ context, eventPull, queueAppSlug }) {
  return (
    context.eventName === 'pull_request_target' &&
    context.payload.action === 'auto_merge_enabled' &&
    queueAppSlug &&
    context.payload.sender?.login === `${queueAppSlug}[bot]`
  );
}

async function rebaseQueuedPull({
  github,
  owner,
  repo,
  candidate,
  defaultBranch,
  defaultBranchSHA,
  canUpdateBranch,
  hasFollower,
}) {
  try {
    return await rebaseOntoDefault(github, candidate, defaultBranchSHA, { canUpdateBranch });
  } catch (error) {
    if (!isMergeConflict(error)) {
      await syncStatusComment(
        github,
        owner,
        repo,
        candidate.number,
        `## Queue status\n\nQueue paused: GitHub could not rebase this pull request onto \`${defaultBranch}\`.\n\n\`${safeError(error)}\``,
      );
      throw error;
    }
    const retrySummary = hasFollower
      ? 'The queue will continue with the next queued pull request. This pull request will be retried after its branch or the base branch changes.'
      : 'This pull request will be retried after its branch or the base branch changes.';
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\nGitHub could not rebase this pull request onto \`${defaultBranch}\` because of merge conflicts. ${retrySummary}\n\n${conflictBlockMarker(candidate.head.sha, defaultBranchSHA)}\n\n\`${safeError(error)}\``,
    );
    return null;
  }
}

async function armOrMergeQueuedPull({
  github,
  owner,
  repo,
  candidate,
  defaultBranch,
  defaultBranchSHA,
  update,
}) {
  try {
    const { data: latestBranch } = await github.rest.repos.getBranch({
      owner,
      repo,
      branch: defaultBranch,
    });
    if (latestBranch.commit.sha !== defaultBranchSHA) {
      throw new Error(
        `Default branch ${defaultBranch} moved from ${shortSHA(defaultBranchSHA)} to ${shortSHA(latestBranch.commit.sha)} while advancing the queue.`,
      );
    }
    const state = await pullState(github, owner, repo, candidate.number);
    if (state.headRefOid !== update.headSHA) {
      throw new Error(
        `Pull request head moved from ${shortSHA(update.headSHA)} to ${shortSHA(state.headRefOid)} while advancing the queue.`,
      );
    }
    const result = await armOrMerge(github, state, {
      expectedBaseRefName: defaultBranch,
      expectedBaseRefOid: defaultBranchSHA,
    });
    const rebaseSummary = update.rebased
      ? `Rebased \`${shortSHA(candidate.head.sha)}\` to \`${shortSHA(update.headSHA)}\` on current \`${defaultBranch}\`.`
      : `Head \`${shortSHA(update.headSHA)}\` already contains current \`${defaultBranch}\`.`;
    const mergeSummary = result === 'merged'
      ? 'All repository requirements were satisfied, so GitHub squash-merged it.'
      : 'Squash auto-merge is armed and will wait for the repository ruleset.';
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\n${rebaseSummary}\n\n${mergeSummary}`,
    );
  } catch (error) {
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\nQueue paused while enabling or completing squash auto-merge.\n\n\`${safeError(error)}\``,
    );
    throw error;
  }
}

async function advanceQueuedPull({
  github,
  owner,
  repo,
  candidate,
  defaultBranch,
  defaultBranchSHA,
  canUpdateBranch,
  isFirst,
  hasFollower,
}) {
  if (isFirst) {
    await disableAutoMerge(github, owner, repo, candidate.number);
  }
  if (candidate.draft) {
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      '## Queue status\n\nQueue paused: the oldest eligible queued pull request is still a draft.',
    );
    return false;
  }
  const update = await rebaseQueuedPull({
    github,
    owner,
    repo,
    candidate,
    defaultBranch,
    defaultBranchSHA,
    canUpdateBranch,
    hasFollower,
  });
  if (!update) {
    return true;
  }
  if (update.needsManualRebase) {
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\nQueue paused: this fork branch does not contain current \`${defaultBranch}\`, and the repository-scoped queue App cannot update it. Rebase the fork branch manually; the queue will retry after the push.`,
    );
    return false;
  }
  await armOrMergeQueuedPull({
    github,
    owner,
    repo,
    candidate,
    defaultBranch,
    defaultBranchSHA,
    update,
  });
  return false;
}

async function runController({
  github,
  context,
  core,
  queueAppSlug = process.env.QUEUE_APP_SLUG,
}) {
  const queueLabel = process.env.QUEUE_LABEL || DEFAULT_QUEUE_LABEL;
  const { owner, repo } = context.repo;
  await ensureQueueLabel(github, owner, repo, queueLabel);

  const { data: repository } = await github.rest.repos.get({ owner, repo });
  const defaultBranch = repository.default_branch;
  const eventPull = context.payload.pull_request;
  await reconcileEventPull({
    github,
    context,
    owner,
    repo,
    queueLabel,
    defaultBranch,
    eventPull,
  });

  const pulls = await github.paginate(github.rest.pulls.list, {
    owner,
    repo,
    state: 'open',
    base: defaultBranch,
    sort: 'created',
    direction: 'asc',
    per_page: 100,
  });
  const queued = sortQueuedPulls(pulls.filter((pull) => hasLabel(pull, queueLabel)));
  if (queued.length === 0) {
    core.notice(`No open ${defaultBranch} pull requests carry the ${queueLabel} label.`);
    return;
  }

  if (isQueueAppAutoMergeEvent({ context, eventPull, queueAppSlug })) {
    core.notice(`Ignoring the queue App's auto-merge event for #${eventPull.number}.`);
    return;
  }
  const eventQueueEntry = eventPull && queued.find((pull) => pull.number === eventPull.number);
  const { data: branch } = await github.rest.repos.getBranch({
    owner,
    repo,
    branch: defaultBranch,
  });
  const activeIndex = await firstEligibleQueueIndex(github, owner, repo, queued, branch.commit.sha);
  if (activeIndex >= queued.length) {
    core.notice('Every queued pull request is waiting for a branch update after a rebase conflict.');
    return;
  }
  await syncFollowerStatuses({
    github,
    owner,
    repo,
    followers: queued.slice(activeIndex + 1),
    leaderNumber: queued[activeIndex].number,
    eventQueueEntry,
    eventAction: context.payload.action,
    conflictSkipped: activeIndex > 0,
    disableFollowers: true,
  });
  for (let index = activeIndex; index < queued.length; index += 1) {
    const candidate = queued[index];
    const shouldAdvance = await advanceQueuedPull({
      github,
      owner,
      repo,
      candidate,
      defaultBranch,
      defaultBranchSHA: branch.commit.sha,
      canUpdateBranch: candidate.head.repo?.full_name === repository.full_name,
      isFirst: index === 0,
      hasFollower: index + 1 < queued.length,
    });
    if (!shouldAdvance) {
      return;
    }
    const nextCandidate = queued[index + 1];
    if (nextCandidate) {
      await syncFollowerStatuses({
        github,
        owner,
        repo,
        followers: queued.slice(index + 2),
        leaderNumber: nextCandidate.number,
        eventQueueEntry,
        eventAction: context.payload.action,
        conflictSkipped: true,
      });
    }
  }

  core.notice('Every queued pull request is waiting for a branch update after a rebase conflict.');
}

module.exports = runController;
module.exports.testables = {
  hasLabel,
  isBranchCurrent,
  isMergeConflict,
  isQueueAppAutoMergeEvent,
  labelName,
  parseConflictBlock,
  safeError,
  shortSHA,
  sortQueuedPulls,
};
