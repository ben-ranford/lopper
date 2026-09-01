'use strict';

const COMMENT_MARKER = '<!-- queue-me-controller -->';
const DEFAULT_QUEUE_LABEL = 'queue-me';
const MAX_GITHUB_COMMENT_BODY_LENGTH = 60000;
const MAX_QUEUE_IDENTITY_FAILURES = 10;
const MAX_QUEUE_IDENTITY_FAILURE_LENGTH = 240;
const MAX_QUEUE_IDENTITY_COMMITS = 500;
const COMMENT_TRUNCATION_NOTICE = '\n\n_Status message truncated to fit GitHub comment limits._';

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

function safeCommentText(value, maxLength) {
  return String(value).replace(/[\r\n]+/g, ' ').replaceAll('`', "'").slice(0, maxLength);
}

function truncateCommentBody(body) {
  if (body.length <= MAX_GITHUB_COMMENT_BODY_LENGTH) {
    return body;
  }
  return `${body.slice(0, MAX_GITHUB_COMMENT_BODY_LENGTH - COMMENT_TRUNCATION_NOTICE.length)}${COMMENT_TRUNCATION_NOTICE}`;
}

function queuePauseError(message) {
  const error = new Error(message);
  error.queuePauseMessage = message;
  return error;
}

function isBotIdentity(identity) {
  const type = String(identity?.type || '').trim().toLowerCase();
  const login = String(identity?.login || '').toLowerCase();
  const name = String(identity?.name || '').toLowerCase();
  const email = String(identity?.email || '').toLowerCase();
  return (
    type === 'bot' ||
    login.endsWith('[bot]') ||
    name.endsWith('[bot]') ||
    email.includes('[bot]@') ||
    email === 'noreply@github.com'
  );
}

function githubLogin(identity) {
  const login = String(identity?.login || '').trim().toLowerCase();
  if (!login || isBotIdentity(identity)) {
    return '';
  }
  return login;
}

function sameCommitIdentity(commit) {
  const authorLogin = githubLogin(commit?.author);
  const committerLogin = githubLogin(commit?.committer);
  return authorLogin !== '' && authorLogin === committerLogin;
}

function commitIdentityFailure(commit) {
  const author = commit?.commit?.author || {};
  const committer = commit?.commit?.committer || {};
  const authorName = String(author.name || '').trim();
  const authorEmail = String(author.email || '').trim();
  const committerName = String(committer.name || '').trim();
  const committerEmail = String(committer.email || '').trim();
  const sha = shortSHA(commit?.sha);

  if (!authorName || !authorEmail || !committerName || !committerEmail) {
    return `${sha}: author and committer metadata must both be present`;
  }
  if (isBotIdentity(commit?.author) || isBotIdentity(author)) {
    return `${sha}: author is a bot identity`;
  }
  if (isBotIdentity(commit?.committer) || isBotIdentity(committer)) {
    return `${sha}: committer is a bot identity`;
  }
  const authorLogin = githubLogin(commit?.author);
  const committerLogin = githubLogin(commit?.committer);
  if (!authorLogin || !committerLogin) {
    return `${sha}: cannot prove canonical author and committer GitHub identity`;
  }
  if (!sameCommitIdentity(commit)) {
    return `${sha}: author and committer identities differ`;
  }
  return '';
}

function queueIdentityFailureMessage(failures) {
  const shownFailures = failures
    .slice(0, MAX_QUEUE_IDENTITY_FAILURES)
    .map((failure) => safeCommentText(failure, MAX_QUEUE_IDENTITY_FAILURE_LENGTH));
  const omitted = failures.length - shownFailures.length;
  let omittedSummary = '';
  if (omitted > 0) {
    const omittedFailureNoun = omitted === 1 ? 'failure' : 'failures';
    omittedSummary = `; ${omitted} additional commit identity ${omittedFailureNoun} omitted`;
  }
  return `Queue identity audit failed: PR-unique commits must use the same canonical user author and committer identity. Found ${failures.length} failing commit${failures.length === 1 ? '' : 's'}; showing ${shownFailures.length}: ${shownFailures.join('; ')}${omittedSummary}.`;
}

function assertCanonicalCommitIdentity(comparison) {
  const commits = comparison?.commits || [];
  if (comparison?.total_commits > commits.length) {
    throw queuePauseError(
      `Queue identity audit failed: GitHub returned ${commits.length} of ${comparison.total_commits} PR-unique commits, so the queue cannot prove canonical author and committer identity.`,
    );
  }
  const failures = commits.map(commitIdentityFailure).filter(Boolean);
  if (failures.length > 0) {
    throw queuePauseError(queueIdentityFailureMessage(failures));
  }
  if (comparison?.total_commits > MAX_QUEUE_IDENTITY_COMMITS) {
    throw queuePauseError(
      `Queue identity audit failed: ${comparison.total_commits} PR-unique commits exceeds the ${MAX_QUEUE_IDENTITY_COMMITS}-commit audit limit.`,
    );
  }
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

async function syncStatusComment(
  github,
  owner,
  repo,
  number,
  body,
  { createIfMissing = true } = {},
) {
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
  const nextBody = truncateCommentBody(`${COMMENT_MARKER}\n${body}`);
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

function followerStatusBody(leaderNumber, { conflictSkipped = false } = {}) {
  const orderingSummary = conflictSkipped
    ? 'Earlier queued pull requests that are not yet eligible are retried after their branches or the base branch change.'
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

async function verifyHeadForQueue(
  github,
  pull,
  defaultBranchSHA,
) {
  const comparisonRequest = {
    owner: pull.base.repo.owner.login,
    repo: pull.base.repo.name,
    basehead: `${defaultBranchSHA}...${pull.head.sha}`,
    per_page: 100,
  };
  const { data: firstComparison } = await github.rest.repos.compareCommitsWithBasehead({
    ...comparisonRequest,
    page: 1,
  });
  if (firstComparison.total_commits > MAX_QUEUE_IDENTITY_COMMITS) {
    throw queuePauseError(
      `Queue identity audit failed: ${firstComparison.total_commits} PR-unique commits exceeds the ${MAX_QUEUE_IDENTITY_COMMITS}-commit audit limit.`,
    );
  }
  const commits = [...(firstComparison.commits || [])];
  let page = 2;
  while (commits.length < firstComparison.total_commits) {
    const { data: comparisonPage } = await github.rest.repos.compareCommitsWithBasehead({
      ...comparisonRequest,
      page,
    });
    page += 1;
    commits.push(...(comparisonPage.commits || []));
    if (!comparisonPage.commits?.length) {
      break;
    }
  }
  const comparison = { ...firstComparison, commits };
  assertCanonicalCommitIdentity(comparison);
  if (isBranchCurrent(comparison.status)) {
    return { headSHA: pull.head.sha, needsCurrentBase: false };
  }
  return { headSHA: pull.head.sha, needsCurrentBase: true };
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

function isQueueAppAutoMergeEvent({ context, queueAppSlug }) {
  return (
    context.eventName === 'pull_request_target' &&
    context.payload.action === 'auto_merge_enabled' &&
    Boolean(queueAppSlug) &&
    context.payload.sender?.login === `${queueAppSlug}[bot]`
  );
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
    const queueSummary = `Head \`${shortSHA(update.headSHA)}\` already contains current \`${defaultBranch}\` and passed the PR-unique commit identity audit.`;
    const mergeSummary = result === 'merged'
      ? 'All repository requirements were satisfied, so GitHub squash-merged it.'
      : 'Squash auto-merge is armed and will wait for the repository ruleset.';
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\n${queueSummary}\n\n${mergeSummary}`,
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
  queueAppSlug,
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
  let update;
  try {
    update = await verifyHeadForQueue(github, candidate, defaultBranchSHA);
  } catch (error) {
    const pauseMessage = error?.queuePauseMessage ||
      `GitHub could not compare this pull request with \`${defaultBranch}\` for the queue identity audit.`;
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\nQueue paused: ${pauseMessage}\n\n\`${safeError(error)}\``,
    );
    throw error;
  }
  if (update.needsCurrentBase) {
    const queueCommitter = queueAppSlug ? `${queueAppSlug}[bot]` : 'the queue App bot';
    const retrySummary = hasFollower
      ? 'The queue will continue with the next queued pull request. This pull request will be retried after a clean identity audit.'
      : 'This pull request will be retried after a clean identity audit.';
    const message = `this pull request branch does not contain current \`${defaultBranch}\`. The queue will not call GitHub branch update because it rewrites PR commits with \`${queueCommitter}\` as committer. Push a history that contains current \`${defaultBranch}\` while preserving canonical author and committer identity. ${retrySummary}`;
    await syncStatusComment(
      github,
      owner,
      repo,
      candidate.number,
      `## Queue status\n\nQueue paused: ${message}`,
    );
    return true;
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

  if (isQueueAppAutoMergeEvent({ context, queueAppSlug })) {
    core.notice(`Ignoring the queue App's auto-merge event for #${eventPull.number}.`);
    return;
  }

  const eventQueueEntry = eventPull && queued.find((pull) => pull.number === eventPull.number);
  const { data: branch } = await github.rest.repos.getBranch({
    owner,
    repo,
    branch: defaultBranch,
  });

  for (const [index, candidate] of queued.entries()) {
    await syncFollowerStatuses({
      github,
      owner,
      repo,
      followers: queued.slice(index + 1),
      leaderNumber: candidate.number,
      eventQueueEntry,
      eventAction: context.payload.action,
      conflictSkipped: index > 0,
      disableFollowers: index === 0,
    });
    const shouldAdvance = await advanceQueuedPull({
      github,
      owner,
      repo,
      candidate,
      defaultBranch,
      defaultBranchSHA: branch.commit.sha,
      queueAppSlug,
      isFirst: index === 0,
      hasFollower: index + 1 < queued.length,
    });
    if (!shouldAdvance) {
      return;
    }
  }

  core.notice('Every queued pull request is waiting for a clean queue identity audit after a base branch update.');
}

module.exports = runController;
module.exports.testables = {
  assertCanonicalCommitIdentity,
  commitIdentityFailure,
  hasLabel,
  isBranchCurrent,
  isBotIdentity,
  isQueueAppAutoMergeEvent,
  labelName,
  queueIdentityFailureMessage,
  safeError,
  shortSHA,
  sortQueuedPulls,
  truncateCommentBody,
  verifyHeadForQueue,
};
