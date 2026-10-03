'use strict';

// Only load this module and its provenance adapter from the protected snapshot.
// CI artifacts and job names remain untrusted evidence, never executable code.
const https = require('node:https');

const WORKFLOW_PATH = '.github/workflows/ci.yml';
const MAX_ARCHIVE_BYTES = 8 * 1024 * 1024;
const TIMEOUT_MS = 15000;
const PAGE_SIZE = 100;
const LOCATOR_PREFIX = 'suppression-artifact-';

class EvidenceError extends Error {}

function requireEvidence(condition, message) {
  if (!condition) throw new EvidenceError(`Reuse suppression: ${message}`);
}

function positiveID(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function inputs(context, snapshot) {
  const fields = ['version', 'repository', 'repository_id', 'head_repository_id', 'pull_number', 'base', 'head', 'base_ref'];
  requireEvidence(snapshot && Object.keys(snapshot).length === fields.length && fields.every((key) => Object.hasOwn(snapshot, key)), 'invalid snapshot fields');
  requireEvidence(snapshot.version === 1, 'invalid snapshot version');
  const repo = context?.repo;
  requireEvidence(['owner', 'repo'].every((key) => typeof repo?.[key] === 'string' && /^[A-Za-z0-9_.-]+$/.test(repo[key])), 'invalid repository');
  requireEvidence(snapshot.repository === `${repo.owner}/${repo.repo}`, 'repository mismatch');
  requireEvidence(['repository_id', 'head_repository_id', 'pull_number'].every((key) => positiveID(snapshot[key])), 'invalid snapshot IDs');
  requireEvidence(['head', 'base'].every((key) => typeof snapshot[key] === 'string' && /^[a-f0-9]{40}$/.test(snapshot[key])), 'invalid snapshot commits');
  requireEvidence(typeof snapshot.base_ref === 'string' && snapshot.base_ref.length > 0, 'invalid base ref');
  return { repo: { owner: repo.owner, repo: repo.repo }, snapshot: Object.freeze({ ...snapshot }) };
}

async function collectPages(method, parameters, key, maximum) {
  const result = [];
  let expected;
  for (let page = 1; page <= Math.ceil(maximum / PAGE_SIZE); page += 1) {
    const { data } = await method({ ...parameters, per_page: PAGE_SIZE, page });
    requireEvidence(Number.isSafeInteger(data?.total_count) && data.total_count >= 0 && data.total_count <= maximum, 'invalid or excessive result count');
    expected ??= data.total_count;
    requireEvidence(expected === data.total_count, 'result count changed during pagination');
    const entries = data[key];
    requireEvidence(Array.isArray(entries) && entries.length <= PAGE_SIZE, 'invalid result page');
    result.push(...entries);
    requireEvidence(result.length <= expected, 'inconsistent result count');
    if (result.length === expected) return result;
    requireEvidence(entries.length === PAGE_SIZE, 'incomplete result page');
  }
  throw new EvidenceError('Reuse suppression: pagination limit reached');
}

function matchingPull(run, snapshot) {
  const pulls = run.pull_requests;
  requireEvidence(Array.isArray(pulls) && pulls.length > 0, 'producer has no pull request association');
  const matches = pulls.filter((pull) => pull.number === snapshot.pull_number);
  requireEvidence(matches.length <= 1, 'ambiguous pull request association');
  return matches[0];
}

function assertRunIdentity(run, workflow, snapshot) {
  requireEvidence(positiveID(run?.id) && positiveID(run.run_attempt), 'invalid producer ID or attempt');
  requireEvidence(run.workflow_id === workflow.id && run.path === WORKFLOW_PATH && run.name === 'ci', 'producer workflow mismatch');
  requireEvidence(run.event === 'pull_request' && run.head_sha === snapshot.head, 'producer event or head mismatch');
  requireEvidence(run.repository?.id === snapshot.repository_id && run.head_repository?.id === snapshot.head_repository_id, 'producer repository mismatch');
}

function assertSelectedRun(run, workflow, snapshot) {
  assertRunIdentity(run, workflow, snapshot);
  const pull = matchingPull(run, snapshot);
  requireEvidence(pull?.head?.sha === snapshot.head && pull.base?.sha === snapshot.base && pull.base?.ref === snapshot.base_ref &&
    pull.head?.repo?.id === snapshot.head_repository_id && pull.base?.repo?.id === snapshot.repository_id, 'producer pull request snapshot mismatch');
  requireEvidence(run.status === 'completed' && run.conclusion === 'success', 'latest producer is not successful');
}

async function latestProducer(github, repo, snapshot) {
  const { data: workflow } = await github.rest.actions.getWorkflow({ ...repo, workflow_id: 'ci.yml' });
  requireEvidence(positiveID(workflow?.id) && workflow.path === WORKFLOW_PATH && workflow.name === 'ci', 'unexpected CI workflow');
  // GitHub caps filtered workflow-run searches at 1,000 results. Fail if the
  // complete set cannot be inspected rather than falling back to older evidence.
  const runs = await collectPages(github.rest.actions.listWorkflowRuns, { ...repo, workflow_id: 'ci.yml', head_sha: snapshot.head, event: 'pull_request' }, 'workflow_runs', 1000);
  let selected;
  const seen = new Set();
  for (const run of runs) {
    assertRunIdentity(run, workflow, snapshot);
    requireEvidence(!seen.has(run.id), 'duplicate producer run');
    seen.add(run.id);
    if (!selected || run.id > selected.id) selected = run;
  }
  requireEvidence(selected, 'no producer for this pull request');
  const { data: current } = await github.rest.actions.getWorkflowRun({ ...repo, run_id: selected.id });
  requireEvidence(current?.id === selected.id && current.run_attempt === selected.run_attempt, 'producer changed during selection');
  assertSelectedRun(current, workflow, snapshot);
  return current;
}

function locatorID(job, run) {
  requireEvidence(typeof job.name === 'string', 'invalid job name');
  if (!job.name.startsWith(LOCATOR_PREFIX)) return undefined;
  const match = /^suppression-artifact-([1-9]\d*)$/.exec(job.name);
  const id = match && Number(match[1]);
  requireEvidence(positiveID(id), 'invalid artifact locator');
  requireEvidence(job.run_id === run.id && job.run_attempt === run.run_attempt && job.head_sha === run.head_sha, 'locator producer mismatch');
  requireEvidence(job.status === 'completed' && job.conclusion === 'success', 'artifact locator is not successful');
  return id;
}

async function artifactLocator(github, repo, run) {
  const jobs = await collectPages(github.rest.actions.listJobsForWorkflowRunAttempt, { ...repo, run_id: run.id, attempt_number: run.run_attempt }, 'jobs', 10000);
  const locators = jobs.map((job) => locatorID(job, run)).filter((id) => id !== undefined);
  requireEvidence(locators.length === 1, 'missing or ambiguous artifact locator');
  return locators[0];
}

function signedArchiveURL(location) {
  let url;
  try {
    url = new URL(location);
  } catch {
    throw new EvidenceError('Reuse suppression: invalid artifact redirect');
  }
  const allowedHost = ['.blob.core.windows.net', '.githubusercontent.com'].some((suffix) => url.hostname.endsWith(suffix));
  requireEvidence(url.protocol === 'https:' && allowedHost && !url.username && !url.password && !url.port && !url.hash, 'untrusted artifact redirect');
  return url;
}

function readArchive(response, resolve, reject) {
  const declared = response.headers['content-length'];
  if (declared !== undefined && (!/^\d+$/.test(declared) || Number(declared) > MAX_ARCHIVE_BYTES)) {
    response.destroy();
    reject(new EvidenceError('Reuse suppression: excessive archive size'));
    return;
  }
  const chunks = [];
  let size = 0;
  response.on('data', (chunk) => {
    size += chunk.length;
    if (size > MAX_ARCHIVE_BYTES) {
      response.destroy();
      reject(new EvidenceError('Reuse suppression: excessive archive size'));
      return;
    }
    chunks.push(chunk);
  });
  response.on('end', () => {
    if (size === 0 || (declared !== undefined && size !== Number(declared))) {
      reject(new EvidenceError('Reuse suppression: incomplete archive'));
    } else {
      resolve(Buffer.concat(chunks, size));
    }
  });
}

function requestArtifact(url, headers, redirect, get = https.get) {
  return new Promise((resolve, reject) => {
    const request = get(url, { headers, timeout: TIMEOUT_MS }, (response) => {
      response.on('error', () => reject(new EvidenceError('Reuse suppression: artifact response failed')));
      response.on('aborted', () => reject(new EvidenceError('Reuse suppression: artifact response aborted')));
      if (redirect && response.statusCode === 302) {
        const location = response.headers.location;
        resolve(location);
        response.destroy();
      } else if (!redirect && response.statusCode === 200) {
        readArchive(response, resolve, reject);
      } else {
        response.destroy();
        reject(new EvidenceError('Reuse suppression: unexpected artifact response'));
      }
    });
    request.on('error', () => reject(new EvidenceError('Reuse suppression: artifact request failed')));
    request.on('timeout', () => request.destroy(new Error('artifact request timeout')));
    // An absolute deadline also bounds peers that keep an idle timeout alive.
    const deadline = setTimeout(() => {
      request.destroy();
      reject(new EvidenceError('Reuse suppression: artifact deadline exceeded'));
    }, TIMEOUT_MS);
    request.on('close', () => clearTimeout(deadline));
  });
}

async function downloadArchive(repo, artifactId, token, request = requestArtifact) {
  requireEvidence(typeof token === 'string' && token.length > 0 && !/[\r\n]/.test(token), 'missing artifact token');
  const url = new URL(`https://api.github.com/repos/${encodeURIComponent(repo.owner)}/${encodeURIComponent(repo.repo)}/actions/artifacts/${artifactId}/zip`);
  const location = await request(url, { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'User-Agent': 'lopper-reuse-verifier', 'X-GitHub-Api-Version': '2022-11-28' }, true);
  // Never forward the API credential to artifact storage, nor follow a second
  // redirect. A signed URL is data and is not included in any diagnostic.
  return request(signedArchiveURL(location), { 'User-Agent': 'lopper-reuse-verifier' }, false);
}

function receiptFor(snapshot, run, artifactId) {
  return { headSHA: snapshot.head, baseSHA: snapshot.base, runId: run.id, runAttempt: run.run_attempt, artifactId, suppressionCount: 0 };
}

function assertReceipt(receipt, expected) {
  const keys = Object.keys(expected);
  requireEvidence(receipt && Object.keys(receipt).length === keys.length && keys.every((key) => receipt[key] === expected[key]), 'adapter receipt mismatch');
}

// Dependency substitution exists only in this private test factory. Workflow
// callers cannot choose an adapter or downloader through their input object.
function createVerifier(loadVerifier = () => require('./suppression_provenance.js'), download = downloadArchive) {
  return async function verifyReuseSuppression({ github, context, snapshot: supplied, token }) {
    try {
      const { repo, snapshot } = inputs(context, supplied);
      const run = await latestProducer(github, repo, snapshot);
      const artifactId = await artifactLocator(github, repo, run);
      const archive = await download(repo, artifactId, token);
      const expected = { repoId: snapshot.repository_id, headRepoId: snapshot.head_repository_id, pullNumber: snapshot.pull_number, headSHA: snapshot.head, baseSHA: snapshot.base, runId: run.id, runAttempt: run.run_attempt };
      const receipt = await loadVerifier()({ github, context: { repo }, expected, artifactId, archive });
      assertReceipt(receipt, receiptFor(snapshot, run, artifactId));
      const current = await latestProducer(github, repo, snapshot);
      requireEvidence(current.id === run.id && current.run_attempt === run.run_attempt, 'producer superseded during verification');
      requireEvidence(await artifactLocator(github, repo, current) === artifactId, 'artifact locator changed');
      return receipt;
    } catch (error) {
      if (error instanceof EvidenceError) throw error;
      throw new EvidenceError('Reuse suppression: evidence could not be verified');
    }
  };
}

module.exports = createVerifier();
module.exports.testables = { createVerifier, collectPages, signedArchiveURL, requestArtifact, downloadArchive, MAX_ARCHIVE_BYTES };
