'use strict';

const SONAR_ORIGIN = 'https://sonarcloud.io';
const PROJECT = 'ben-ranford_lopper';
const REPOSITORY = 'ben-ranford/lopper';
const PAGE_SIZE = 500;
const MAX_PAGES = 20;
const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const REQUEST_TIMEOUT_MS = 10000;
const AUDIT_TIMEOUT_MS = 60000;

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

async function responseJSON(response) {
  requireEvidence(response?.ok === true && response.redirected === false,
    'the public API returned an error or redirect.');
  requireEvidence(/^application\/json(?:;|$)/i.test(response.headers?.get('content-type') || ''),
    'the public API returned a non-JSON response.');
  const length = response.headers.get('content-length');
  requireEvidence(length === null || (/^\d+$/.test(length) && Number(length) <= MAX_RESPONSE_BYTES),
    'the public API response exceeds its size limit.');
  requireEvidence(typeof response.body?.getReader === 'function',
    'the public API returned an unreadable response.');
  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    let next = await reader.read();
    while (!next.done) {
      requireEvidence(next.value instanceof Uint8Array, 'the public API returned invalid response data.');
      size += next.value.byteLength;
      requireEvidence(size <= MAX_RESPONSE_BYTES, 'the public API response exceeds its size limit.');
      chunks.push(next.value);
      next = await reader.read();
    }
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
  const buffer = Buffer.concat(chunks, size);
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(buffer));
}

function publicAPI(fetchImpl) {
  const deadline = Date.now() + AUDIT_TIMEOUT_MS;
  return async (endpoint, parameters) => {
    const remaining = deadline - Date.now();
    requireEvidence(remaining > 0, 'the audit exceeded its time limit; retry after analysis completes.');
    const url = new URL(`/api/${endpoint}`, SONAR_ORIGIN);
    url.search = new URLSearchParams(parameters).toString();
    const controller = new AbortController();
    let timer;
    try {
      const timeout = new Promise((_, reject) => {
        timer = setTimeout(() => {
          controller.abort();
          reject(pause('the public API timed out; retry after analysis completes.'));
        }, Math.min(REQUEST_TIMEOUT_MS, remaining));
      });
      const request = Promise.resolve(fetchImpl(url.toString(), {
        signal: controller.signal,
        redirect: 'error',
        credentials: 'omit',
        cache: 'no-store',
        headers: { Accept: 'application/json' },
      })).then(responseJSON);
      return await Promise.race([request, timeout]);
    } catch (error) {
      if (error?.queuePauseMessage) {
        throw error;
      }
      throw pause('the public API request failed or returned malformed data.');
    } finally {
      clearTimeout(timer);
      controller.abort();
    }
  };
}

function assertScope({ owner, repo, pullNumber, headSHA, baseRef }) {
  requireEvidence(`${owner}/${repo}` === REPOSITORY,
    'this policy supports only the configured repository.');
  requireEvidence(Number.isSafeInteger(pullNumber) && pullNumber > 0 &&
    typeof headSHA === 'string' && /^[a-f0-9]{40}$/.test(headSHA) &&
    typeof baseRef === 'string' && baseRef.length > 0 && baseRef.length <= 255,
  'the expected pull request identity is invalid.');
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

async function completedTask(api, pullNumber, analyzed) {
  // This documented endpoint is project-wide. An undocumented pullRequest
  // parameter does not narrow it; an unrelated latest task must pause the queue.
  const result = await api('ce/component', { component: PROJECT });
  requireEvidence(Array.isArray(result?.queue), 'the processing queue inventory is missing.');
  requireEvidence(result.queue.length === 0, 'Sonar processing is pending; wait for the project queue to empty.');
  const current = result.current;
  requireEvidence(current?.pullRequest === String(pullNumber),
    'the public latest task is missing or belongs to another pull request. Rerun this pull request in the existing Sonar UI after its checks are ready.');
  requireEvidence(current.componentKey === PROJECT && current.componentId === analyzed.componentId &&
    current.type === 'REPORT' && identifier(current.id) && identifier(current.analysisId),
  'the latest processing task identity is incomplete or does not match.');
  requireEvidence(current.status === 'SUCCESS', 'the latest processing task has not completed successfully.');
  const times = [analyzed.analysisDate, current.submittedAt, current.startedAt, current.executedAt].map(timestamp);
  requireEvidence(times.every((value, index) => index === 0 || value >= times[index - 1]),
    'the processing task timestamps do not match the analyzed head.');
  return {
    taskId: current.id,
    analysisId: current.analysisId,
    componentId: current.componentId,
    submittedAt: current.submittedAt,
    startedAt: current.startedAt,
    executedAt: current.executedAt,
  };
}

async function qualityGate(api, analysisId) {
  const result = await api('qualitygates/project_status', { analysisId });
  const gate = result?.projectStatus;
  requireEvidence(gate?.status === 'OK' && gate.ignoredConditions === false &&
    Array.isArray(gate.conditions) && gate.conditions.length > 0 &&
    gate.conditions.every((condition) => condition?.status === 'OK'),
  'the exact analysis quality gate is missing, failing, or has ignored conditions.');
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
      return total;
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

async function verifySonar({ owner, repo, pullNumber, headSHA, baseRef, fetchImpl = fetch }) {
  assertScope({ owner, repo, pullNumber, headSHA, baseRef });
  const api = publicAPI(fetchImpl);
  const analyzed = await analyzedPull(api, pullNumber, headSHA, baseRef);
  const task = await completedTask(api, pullNumber, analyzed);
  await qualityGate(api, task.analysisId);
  const fixedIssueCount = await fixedIssues(api, pullNumber);
  await zeroHotspots(api, pullNumber);
  const afterAnalysis = await analyzedPull(api, pullNumber, headSHA, baseRef);
  const afterTask = await completedTask(api, pullNumber, afterAnalysis);
  requireEvidence(JSON.stringify(analyzed) === JSON.stringify(afterAnalysis) &&
    JSON.stringify(task) === JSON.stringify(afterTask),
  'analysis evidence changed during the audit; wait for a stable analysis and retry.');
  return {
    project: PROJECT,
    repository: REPOSITORY,
    pullNumber,
    ...analyzed,
    ...task,
    qualityGate: 'OK',
    activeOrWaivedIssues: 0,
    fixedIssueCount,
    hotspots: 0,
  };
}

module.exports = { verifySonar };
