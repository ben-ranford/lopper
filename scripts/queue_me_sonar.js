'use strict';

const { createPublicAPI } = require('./queue_me_public_api');

const SONAR_ORIGIN = 'https://sonarcloud.io';
const PROJECT = 'ben-ranford_lopper';
const REPOSITORY = 'ben-ranford/lopper';
const GITHUB_ORIGIN = 'https://api.github.com';
const SONAR_APP_ID = 12526;
const SONAR_CHECK = 'SonarCloud Code Analysis';
const GITHUB_PAGE_SIZE = 100;
const MAX_GITHUB_ITEMS = 1000;
const PAGE_SIZE = 500;
const MAX_PAGES = 20;

function pause(message) {
  const error = new Error(`Sonar audit paused: ${message}`);
  error.queuePauseMessage = error.message;
  return error;
}

function requireEvidence(condition, message) {
  if (!condition) {
    throw pause(message);
  }
}

function identifier(value) {
  return typeof value === 'string' && value.length > 0 && value.length <= 200;
}

function timestamp(value) {
  requireEvidence(typeof value === 'string' &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})$/.test(value),
  'missing or malformed analysis timestamps.');
  const result = Date.parse(value);
  requireEvidence(Number.isFinite(result), 'missing or malformed analysis timestamps.');
  return result;
}

function assertScope({ owner, repo, pullNumber, headSHA, baseRef, baseSHA }) {
  requireEvidence(`${owner}/${repo}` === REPOSITORY,
    'this policy supports only the configured repository.');
  requireEvidence(Number.isSafeInteger(pullNumber) && pullNumber > 0 &&
    typeof headSHA === 'string' && /^[a-f0-9]{40}$/.test(headSHA) &&
    typeof baseRef === 'string' && baseRef.length > 0 && baseRef.length <= 255,
  'the expected pull request identity is invalid.');
  requireEvidence(baseSHA === undefined || (typeof baseSHA === 'string' && /^[a-f0-9]{40}$/.test(baseSHA)),
    'the expected base commit identity is invalid.');
}

async function analyzedPull(api, pullNumber, headSHA, baseRef) {
  const result = await api('project_pull_requests/list', { project: PROJECT });
  requireEvidence(Array.isArray(result?.pullRequests), 'the pull request analysis inventory is missing.');
  const matches = result.pullRequests.filter((pull) => pull?.key === String(pullNumber));
  requireEvidence(matches.length === 1, 'the exact pull request analysis is missing or ambiguous.');
  const pull = matches[0];
  requireEvidence(pull.commit?.sha === headSHA, 'the analyzed commit is not the current pull request head.');
  requireEvidence(pull.base === baseRef && pull.target === baseRef &&
    pull.url === `https://github.com/${REPOSITORY}/pull/${pullNumber}` &&
    identifier(pull.pullRequestUuidV1), 'the analysis repository or base identity does not match.');
  timestamp(pull.analysisDate);
  return {
    headSHA,
    baseRef,
    analysisDate: pull.analysisDate,
    componentId: pull.pullRequestUuidV1,
  };
}

function trustedApp(app) {
  return app?.id === SONAR_APP_ID && app.slug === 'sonarqubecloud' &&
    app.owner?.login === 'SonarSource' && app.owner?.id === 545988;
}

function associatedPull(pulls, expected) {
  requireEvidence(Array.isArray(pulls), 'the check pull request association is missing.');
  if (pulls.length === 0) {
    requireEvidence(expected.forkProof, 'the empty check association requires a verified live fork pull request.');
    return;
  }
  const matches = pulls.filter((pull) => pull?.number === expected.pullNumber);
  requireEvidence(matches.length === 1, 'the check pull request association is missing or ambiguous.');
  const pull = matches[0];
  const repositoryURL = `${GITHUB_ORIGIN}/repos/${REPOSITORY}`;
  requireEvidence(pull.url === `${repositoryURL}/pulls/${expected.pullNumber}` &&
    pull.head?.sha === expected.headSHA && pull.base?.ref === expected.baseRef &&
    pull.base?.repo?.url === repositoryURL,
  'the check repository, head, or base association does not match.');
  requireEvidence(expected.baseSHA === undefined || pull.base.sha === expected.baseSHA,
    'the check base commit association does not match.');
}

async function githubInventory(api, headSHA, field, options = {}) {
  const { parameters = {}, page = 1, prior = [], expectedTotal } = options;
  const endpoint = field === 'check_runs' ? 'check-runs' : 'check-suites';
  const result = await api(`commits/${headSHA}/${endpoint}`, {
    ...parameters, per_page: String(GITHUB_PAGE_SIZE), page: String(page),
  }, true);
  const items = result?.[field];
  const total = result?.total_count;
  requireEvidence(Array.isArray(items) && Number.isSafeInteger(total) && total >= 0 &&
    total <= MAX_GITHUB_ITEMS && items.length === Math.min(GITHUB_PAGE_SIZE, total - prior.length),
  'the GitHub check inventory is missing, truncated, or exceeds its pagination limit.');
  requireEvidence(expectedTotal === undefined || total === expectedTotal,
    'the GitHub check inventory changed during pagination.');
  const all = [...prior, ...items];
  const ids = new Set(all.map((item) => item?.id));
  requireEvidence(ids.size === all.length && all.every((item) => Number.isSafeInteger(item?.id) && item.id > 0),
    'the GitHub check inventory contains missing or duplicate identities.');
  if (all.length === total) {
    return all;
  }
  return githubInventory(api, headSHA, field, { parameters, page: page + 1, prior: all, expectedTotal: total });
}

function normalizedRun(run, expected) {
  requireEvidence(trustedApp(run.app) && run.name === SONAR_CHECK && run.head_sha === expected.headSHA &&
    run.url === `${GITHUB_ORIGIN}/repos/${REPOSITORY}/check-runs/${run.id}` &&
    run.details_url === `${SONAR_ORIGIN}/dashboard?id=${PROJECT}&pullRequest=${expected.pullNumber}` &&
    Number.isSafeInteger(run.check_suite?.id), 'the Sonar check identity is missing or untrusted.');
  associatedPull(run.pull_requests, { ...expected, baseSHA: undefined });
  // Pending attempts supersede a retained success even before started_at exists.
  requireEvidence(run.status === 'completed', 'a Sonar check is pending; wait for it to finish.');
  const started = timestamp(run.started_at);
  const completed = timestamp(run.completed_at);
  requireEvidence(completed >= started, 'the Sonar check timestamps are inconsistent.');
  return { id: run.id, suiteId: run.check_suite.id, started, completed, conclusion: run.conclusion };
}

function latestSuccessfulRun(runs, expected, analyzed) {
  requireEvidence(runs.length > 0, 'the trusted exact-head Sonar check is missing.');
  const normalized = runs.map((run) => normalizedRun(run, expected));
  normalized.sort((left, right) => right.started - left.started);
  const latest = normalized[0];
  requireEvidence(normalized.slice(1).every((run) => run.started < latest.started && run.completed <= latest.completed),
    'the latest Sonar check attempt is ambiguous or overlaps another completion.');
  associatedPull(runs.find((run) => run.id === latest.id).pull_requests, expected);
  requireEvidence(latest.conclusion === 'success', 'the latest Sonar check did not succeed.');
  requireEvidence(latest.started === timestamp(analyzed.analysisDate),
    'the successful Sonar check and analyzed commit timestamps do not match.');
  return latest;
}

function checkedSuite(suites, run, expected) {
  requireEvidence(suites.every((suite) => Number.isSafeInteger(suite.app?.id) && suite.app.id > 0),
    'the GitHub suite inventory has missing app identity.');
  const trusted = suites.filter((suite) => suite.app.id === SONAR_APP_ID);
  const selected = trusted.find((suite) => suite.id === run.suiteId);
  requireEvidence(selected, 'the Sonar check suite is missing.');
  const selectedCreated = timestamp(selected.created_at);
  for (const suite of trusted) {
    requireEvidence(trustedApp(suite.app) && suite.head_sha === expected.headSHA &&
      suite.repository?.full_name === REPOSITORY && suite.repository.private === false,
    'the Sonar suite repository, head, or app identity is untrusted.');
    associatedPull(suite.pull_requests, { ...expected, baseSHA: undefined });
    // GitHub rerequest resets the suite without changing its old successful run.
    requireEvidence(suite.status === 'completed', 'a Sonar check suite is pending or was rerequested.');
    requireEvidence(suite.id === selected.id || timestamp(suite.created_at) < selectedCreated,
      'a newer or ambiguous Sonar check suite has no matching current analysis.');
  }
  associatedPull(selected.pull_requests, expected);
  requireEvidence(selected.conclusion === 'success', 'the current Sonar check suite did not succeed.');
  requireEvidence(selectedCreated <= run.started && timestamp(selected.updated_at) >= run.completed,
    'the Sonar suite timestamps are inconsistent.');
  return { suiteId: selected.id, createdAt: selected.created_at, updatedAt: selected.updated_at };
}

function forkRepository(repository, baseRepository) {
  requireEvidence(repository?.fork === true && Number.isSafeInteger(repository.id) && repository.id > 0 &&
    repository.id !== baseRepository.id && repository.full_name !== REPOSITORY &&
    typeof repository.full_name === 'string' && /^[\w.-]+\/[\w.-]+$/.test(repository.full_name) &&
    repository.url === `${GITHUB_ORIGIN}/repos/${repository.full_name}`,
  'the empty check association is not a verifiable fork repository.');
  return { id: repository.id, name: repository.full_name, url: repository.url };
}

async function liveFork(api, expected) {
  const pull = await api(`pulls/${expected.pullNumber}`, {}, true);
  requireEvidence(pull?.number === expected.pullNumber &&
    pull.url === `${GITHUB_ORIGIN}/repos/${REPOSITORY}/pulls/${expected.pullNumber}` &&
    pull.state === 'open' && pull.draft === false && pull.head?.sha === expected.headSHA &&
    typeof pull.head.ref === 'string' && pull.head.ref.length > 0 && pull.head.ref.length <= 255,
  'the live fork pull request identity, head, or eligibility changed.');
  const base = pull.base;
  requireEvidence(base?.ref === expected.baseRef && typeof base.sha === 'string' && /^[a-f0-9]{40}$/.test(base.sha) &&
    (expected.baseSHA === undefined || base.sha === expected.baseSHA),
  'the live fork pull request base changed.');
  const repository = base.repo;
  requireEvidence(repository?.full_name === REPOSITORY && repository.private === false &&
    repository.url === `${GITHUB_ORIGIN}/repos/${REPOSITORY}` &&
    Number.isSafeInteger(repository.id) && repository.id > 0,
  'the live fork pull request base repository is invalid.');
  return { headSHA: pull.head.sha, headRef: pull.head.ref, baseSHA: base.sha, baseRef: base.ref, baseRepositoryId: repository.id,
    headRepository: forkRepository(pull.head.repo, repository) };
}

function emptyAssociation(item) {
  return Array.isArray(item.pull_requests) && item.pull_requests.length === 0;
}

async function ensureForkProof(api, expected, runs, suites, state) {
  const empty = runs.some(emptyAssociation) ||
    suites.some((suite) => suite.app?.id === SONAR_APP_ID && emptyAssociation(suite));
  if (empty && !state.proof) {
    state.proof = await liveFork(api, expected);
  }
  return { ...expected, forkProof: state.proof };
}

async function githubCheck(api, expected, analyzed, forkState) {
  // List every suite, not just the trusted app: the runs endpoint silently limits
  // results to the newest 1000 suites. Our total bound proves it did not do so.
  const runs = await githubInventory(api, expected.headSHA, 'check_runs', {
    parameters: { filter: 'all', app_id: String(SONAR_APP_ID) },
  });
  const suites = await githubInventory(api, expected.headSHA, 'check_suites');
  const boundExpected = await ensureForkProof(api, expected, runs, suites, forkState);
  const run = latestSuccessfulRun(runs, boundExpected, analyzed);
  const suite = checkedSuite(suites, run, boundExpected);
  return { checkRunId: run.id, checkStartedAt: run.started, checkCompletedAt: run.completed, ...suite };
}

async function qualityGate(api, pullNumber) {
  const result = await api('qualitygates/project_status', { projectKey: PROJECT, pullRequest: String(pullNumber) });
  const gate = result?.projectStatus;
  requireEvidence(gate?.status === 'OK' && gate.ignoredConditions === false &&
    Array.isArray(gate.conditions) && gate.conditions.length > 0 &&
    gate.conditions.every((condition) => condition?.status === 'OK'),
  'the current pull request quality gate is missing, failing, or has ignored conditions.');
}

function pageInventory(result, field, page, expectedTotal) {
  const paging = result?.paging;
  const items = result?.[field];
  requireEvidence(Array.isArray(items) && paging?.pageIndex === page && paging.pageSize === PAGE_SIZE &&
    Number.isSafeInteger(paging.total) && paging.total >= 0 && paging.total <= PAGE_SIZE * MAX_PAGES,
  'the findings inventory is malformed or exceeds its pagination limit.');
  requireEvidence(expectedTotal === undefined || paging.total === expectedTotal,
    'the findings inventory changed during pagination.');
  const expectedLength = Math.min(PAGE_SIZE, paging.total - (page - 1) * PAGE_SIZE);
  requireEvidence(items.length === expectedLength, 'the findings inventory is truncated or inconsistent.');
  if (field === 'issues') {
    requireEvidence(result.total === paging.total && result.p === page && result.ps === PAGE_SIZE,
      'the issue pagination metadata is inconsistent.');
  }
  return { items, total: paging.total };
}

async function fixedIssues(api, pullNumber) {
  let total;
  const keys = new Set();
  for (let page = 1; page <= MAX_PAGES; page += 1) {
    // No status/resolution filter: waived and future unknown states must remain visible.
    const result = await api('issues/search', {
      componentKeys: PROJECT, pullRequest: String(pullNumber), ps: String(PAGE_SIZE), p: String(page),
    });
    const inventory = pageInventory(result, 'issues', page, total);
    total = inventory.total;
    for (const issue of inventory.items) {
      requireEvidence(identifier(issue?.key) && issue.project === PROJECT &&
        issue.pullRequest === String(pullNumber) && !keys.has(issue.key),
      'the issue inventory contains missing, duplicate, or mismatched identities.');
      requireEvidence(issue.issueStatus === 'FIXED',
        'active, accepted, false-positive, or unknown-status issues remain. Resolve every finding in code.');
      keys.add(issue.key);
    }
    if (keys.size === total) {
      return [...keys].sort((left, right) => left.localeCompare(right));
    }
  }
  throw pause('the issue inventory exceeds its pagination limit.');
}

async function zeroHotspots(api, pullNumber) {
  const result = await api('hotspots/search', {
    projectKey: PROJECT, pullRequest: String(pullNumber), ps: String(PAGE_SIZE), p: '1',
  });
  const inventory = pageInventory(result, 'hotspots', 1);
  requireEvidence(inventory.total === 0,
    'security hotspots remain, including reviewed or waived hotspots. Resolve every hotspot in code.');
}

async function currentFindings(api, pullNumber) {
  await qualityGate(api, pullNumber);
  const fixed = await fixedIssues(api, pullNumber);
  await zeroHotspots(api, pullNumber);
  return fixed;
}

async function verifySonar({ owner, repo, pullNumber, headSHA, baseRef, baseSHA, fetchImpl = fetch }) {
  const expected = { owner, repo, pullNumber, headSHA, baseRef, baseSHA };
  assertScope(expected);
  const api = createPublicAPI({ pause, fetchImpl });
  const forkState = {};
  const analyzed = await analyzedPull(api, pullNumber, headSHA, baseRef);
  const check = await githubCheck(api, expected, analyzed, forkState);
  const fixed = await currentFindings(api, pullNumber);
  const afterFixed = await currentFindings(api, pullNumber);
  const afterAnalysis = await analyzedPull(api, pullNumber, headSHA, baseRef);
  const afterCheck = await githubCheck(api, expected, afterAnalysis, forkState);
  if (forkState.proof) {
    const afterFork = await liveFork(api, expected);
    requireEvidence(JSON.stringify(forkState.proof) === JSON.stringify(afterFork),
      'the live fork pull request identity changed during the audit.');
  }
  requireEvidence(JSON.stringify(analyzed) === JSON.stringify(afterAnalysis) &&
    JSON.stringify(check) === JSON.stringify(afterCheck) && JSON.stringify(fixed) === JSON.stringify(afterFixed),
  'analysis evidence changed during the audit; wait for a stable analysis and retry.');
  return {
    project: PROJECT,
    repository: REPOSITORY,
    pullNumber,
    ...analyzed,
    ...check,
    qualityGate: 'OK',
    activeOrWaivedIssues: 0,
    fixedIssueCount: fixed.length,
    hotspots: 0,
  };
}

module.exports = { verifySonar };
