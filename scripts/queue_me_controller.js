'use strict';

const COMMENT_MARKER = '<!-- queue-me-controller -->';
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

async function syncStatusComment(github, owner, repo, number, body) {
  const comments = await github.paginate(github.rest.issues.listComments, {
    owner,
    repo,
    issue_number: number,
    per_page: 100,
  });
  const existing = comments.find(
    (comment) =>
      comment.user?.type === 'Bot' &&
      typeof comment.body === 'string' &&
      comment.body.includes(COMMENT_MARKER),
  );
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
  await github.rest.issues.createComment({
    owner,
    repo,
    issue_number: number,
    body: nextBody,
  });
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

async function armOrMerge(github, state) {
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

function isQueueAppLeaderAutoMergeEvent({ context, eventPull, leader, queueAppSlug }) {
  return (
    context.eventName === 'pull_request_target' &&
    context.payload.action === 'auto_merge_enabled' &&
    eventPull?.number === leader.number &&
    queueAppSlug &&
    context.payload.sender?.login === `${queueAppSlug}[bot]`
  );
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

  const leader = queued[0];
  if (isQueueAppLeaderAutoMergeEvent({ context, eventPull, leader, queueAppSlug })) {
    core.notice(`Ignoring the queue App's auto-merge event for leader #${leader.number}.`);
    return;
  }
  for (const follower of queued.slice(1)) {
    await disableAutoMerge(github, owner, repo, follower.number);
  }
  const eventQueueEntry = eventPull && queued.find((pull) => pull.number === eventPull.number);
  if (
    eventQueueEntry &&
    eventQueueEntry.number !== leader.number &&
    context.payload.action === 'labeled'
  ) {
    await syncStatusComment(
      github,
      owner,
      repo,
      eventQueueEntry.number,
      `## Queue status\n\nQueued behind #${leader.number}. Pull requests advance in ascending number order.`,
    );
  }
  await disableAutoMerge(github, owner, repo, leader.number);
  if (leader.draft) {
    await syncStatusComment(
      github,
      owner,
      repo,
      leader.number,
      `## Queue status\n\nQueue paused: the oldest queued pull request is still a draft.`,
    );
    return;
  }
  const { data: branch } = await github.rest.repos.getBranch({
    owner,
    repo,
    branch: defaultBranch,
  });
  let update;
  try {
    update = await rebaseOntoDefault(github, leader, branch.commit.sha, {
      canUpdateBranch: leader.head.repo?.full_name === repository.full_name,
    });
  } catch (error) {
    await syncStatusComment(
      github,
      owner,
      repo,
      leader.number,
      `## Queue status\n\nQueue paused: GitHub could not rebase this pull request onto \`${defaultBranch}\`. Resolve the conflict and push the branch to retry.\n\n\`${safeError(error)}\``,
    );
    throw error;
  }
  if (update.needsManualRebase) {
    await syncStatusComment(
      github,
      owner,
      repo,
      leader.number,
      `## Queue status\n\nQueue paused: this fork branch does not contain current \`${defaultBranch}\`, and the repository-scoped queue App cannot update it. Rebase the fork branch manually; the queue will retry after the push.`,
    );
    return;
  }

  try {
    const state = await pullState(github, owner, repo, leader.number);
    if (state.headRefOid !== update.headSHA) {
      throw new Error(
        `Pull request head moved from ${shortSHA(update.headSHA)} to ${shortSHA(state.headRefOid)} while advancing the queue.`,
      );
    }
    const { data: latestBranch } = await github.rest.repos.getBranch({
      owner,
      repo,
      branch: defaultBranch,
    });
    if (latestBranch.commit.sha !== branch.commit.sha) {
      throw new Error(
        `Default branch ${defaultBranch} moved from ${shortSHA(branch.commit.sha)} to ${shortSHA(latestBranch.commit.sha)} while advancing the queue.`,
      );
    }
    const result = await armOrMerge(github, state);
    const rebaseSummary = update.rebased
      ? `Rebased \`${shortSHA(leader.head.sha)}\` to \`${shortSHA(update.headSHA)}\` on current \`${defaultBranch}\`.`
      : `Head \`${shortSHA(update.headSHA)}\` already contains current \`${defaultBranch}\`.`;
    const mergeSummary = result === 'merged'
      ? 'All repository requirements were satisfied, so GitHub squash-merged it.'
      : 'Squash auto-merge is armed and will wait for the repository ruleset.';
    await syncStatusComment(
      github,
      owner,
      repo,
      leader.number,
      `## Queue status\n\n${rebaseSummary}\n\n${mergeSummary}`,
    );
  } catch (error) {
    await syncStatusComment(
      github,
      owner,
      repo,
      leader.number,
      `## Queue status\n\nQueue paused while enabling or completing squash auto-merge.\n\n\`${safeError(error)}\``,
    );
    throw error;
  }
}

module.exports = runController;
module.exports.testables = {
  hasLabel,
  isBranchCurrent,
  labelName,
  safeError,
  shortSHA,
  sortQueuedPulls,
};
