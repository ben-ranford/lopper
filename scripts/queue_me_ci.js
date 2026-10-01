'use strict';

const { createPublicAPI } = require('./queue_me_public_api');

const REPOSITORY = 'ben-ranford/lopper';
const REPOSITORY_ID = 1155023607;
const API_ROOT = `https://api.github.com/repos/${REPOSITORY}`;
const WEB_ROOT = `https://github.com/${REPOSITORY}`;
const SHA = /^[a-f0-9]{40}$/;
const PAGE_SIZE = 100;
const MAX_ITEMS = 1000;
const SOURCE_PATHS = ['.github/workflows/ci.yml', '.github/workflows/ci-tests.yml', '.github/workflows/windows-runtime.yml'];
const CI_JOBS = {
  'verify-checks': 'ubuntu-latest',
  'publish-pr-reports': 'ubuntu-latest',
  'verification checks (rolling)': 'ubuntu-latest',
  'verify-tests / tests': 'ubuntu-latest',
  'verify-rolling-tests / tests': 'ubuntu-latest',
  'regression-proof-windows': 'windows-latest',
  verify: 'ubuntu-latest',
  'verify (rolling)': 'ubuntu-latest',
  'os-smoke (ubuntu-latest)': 'ubuntu-latest',
  'os-smoke (macos-26)': 'macos-26',
  'vscode-smoke (ubuntu-latest)': 'ubuntu-latest',
  'vscode-smoke (macos-26)': 'macos-26',
  'homebrew-tap-verify': 'ubuntu-latest',
};
const WORKFLOWS = [
  { id: 232814257, name: 'ci', path: SOURCE_PATHS[0], jobs: CI_JOBS, intentFloor: true },
  { id: 354077369, name: 'windows runtime', path: SOURCE_PATHS[2], jobs: { 'runtime-cancellation': 'windows-latest' }, intentFloor: false },
];

function pause(message) {
  const error = new Error(`CI audit paused: ${message}`);
  error.queuePauseMessage = error.message;
  return error;
}

function requireEvidence(condition, message) {
  if (!condition) throw pause(message);
}

function positive(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function timestamp(value) {
  requireEvidence(typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value), 'missing or malformed CI timestamp.');
  const result = Date.parse(value);
  requireEvidence(Number.isFinite(result), 'invalid CI timestamp.');
  return result;
}

function assertInput(input) {
  requireEvidence(`${input.owner}/${input.repo}` === REPOSITORY && positive(input.pullNumber), 'unsupported repository or pull request identity.');
  requireEvidence([input.headSHA, input.baseSHA, input.trustedPolicySHA].every((value) => typeof value === 'string' && SHA.test(value)), 'invalid expected commit identity.');
  requireEvidence(input.trustedPolicySHA === input.baseSHA && input.baseRef === 'main', 'CI policy must run from the current protected base.');
  timestamp(input.ciNotBefore);
}

function fullRepository(repository) {
  return repository?.id === REPOSITORY_ID && repository.full_name === REPOSITORY && repository.url === API_ROOT;
}

function validHeadRepository(repository) {
  return positive(repository?.id) && typeof repository.full_name === 'string' &&
    /^[\w.-]+\/[\w.-]+$/.test(repository.full_name) &&
    repository.url === `https://api.github.com/repos/${repository.full_name}`;
}

async function livePull(input) {
  const { data } = await input.github.rest.pulls.get({ owner: input.owner, repo: input.repo, pull_number: input.pullNumber });
  requireEvidence(data?.number === input.pullNumber && positive(data.id) && data.state === 'open' && data.draft === false, 'the pull request is no longer open and ready.');
  requireEvidence(data.head?.sha === input.headSHA && data.base?.sha === input.baseSHA && data.base?.ref === input.baseRef, 'the live pull request head or base changed.');
  requireEvidence(validHeadRepository(data.head.repo) && fullRepository(data.base.repo), 'unknown pull request repository association.');
  requireEvidence(typeof data.head.ref === 'string' && data.head.ref.length > 0 && data.head.ref.length <= 255, 'missing pull request head branch.');
  return { id: data.id, headRef: data.head.ref, headRepo: data.head.repo };
}

function appendPage(data, key, collected, total, seen) {
  requireEvidence(Number.isSafeInteger(data?.total_count) && data.total_count >= 0 && data.total_count < MAX_ITEMS, 'missing, oversized, or API-capped CI inventory.');
  requireEvidence(total === undefined || total === data.total_count, 'CI inventory changed during pagination.');
  requireEvidence(Array.isArray(data[key]) && data[key].length === Math.min(PAGE_SIZE, data.total_count - collected.length), 'incomplete CI inventory page.');
  for (const item of data[key]) {
    requireEvidence(positive(item?.id) && !seen.has(item.id), 'missing or duplicate CI inventory identity.');
    seen.add(item.id);
    collected.push(item);
  }
  return data.total_count;
}

async function inventory(api, endpoint, parameters, key, total, collected = [], seen = new Set()) {
  const page = collected.length / PAGE_SIZE + 1;
  const data = await api(endpoint, { ...parameters, per_page: PAGE_SIZE, page }, true);
  const expected = appendPage(data, key, collected, total, seen);
  if (collected.length === expected) return collected;
  return inventory(api, endpoint, parameters, key, expected, collected, seen);
}

function assertRunEnvelope(run, workflow, input) {
  requireEvidence(positive(run.id) && positive(run.run_number) && positive(run.run_attempt), 'invalid logical workflow run identity.');
  requireEvidence(run.workflow_id === workflow.id && run.path === workflow.path && run.name === workflow.name && run.event === 'pull_request', 'unexpected workflow source or event.');
  requireEvidence(run.head_sha === input.headSHA && fullRepository(run.repository) && validHeadRepository(run.head_repository), 'workflow run repository or head mismatch.');
  requireEvidence(run.url === `${API_ROOT}/actions/runs/${run.id}` && run.html_url === `${WEB_ROOT}/actions/runs/${run.id}`, 'noncanonical workflow run URL.');
  requireEvidence(timestamp(run.updated_at) >= timestamp(run.created_at), 'workflow run timestamp order is invalid.');
}

function matchingHead(run, pull) {
  return run.head_branch === pull.headRef && run.head_repository.id === pull.headRepo.id &&
    run.head_repository.full_name === pull.headRepo.full_name && run.head_repository.url === pull.headRepo.url;
}

function recordedRepository(repository, expected) {
  return repository?.id === expected.id && repository.url === expected.url && repository.name === expected.full_name.split('/')[1];
}

function associations(run, input) {
  requireEvidence(Array.isArray(run.pull_requests) && run.pull_requests.length <= 100, 'missing or oversized recorded pull request associations.');
  const numbers = new Set();
  for (const association of run.pull_requests) {
    requireEvidence(positive(association?.id) && positive(association.number) && association.url === `${API_ROOT}/pulls/${association.number}`, 'malformed recorded pull request association.');
    requireEvidence(!numbers.has(association.number), 'duplicate recorded pull request association.');
    numbers.add(association.number);
  }
  return run.pull_requests.filter((association) => association.number === input.pullNumber);
}

function assertRecordedPull(run, input, pull) {
  const matching = associations(run, input);
  requireEvidence(matching.length === 1, 'missing or ambiguous recorded pull request association.');
  const association = matching[0];
  requireEvidence(association?.id === pull.id && association.number === input.pullNumber && association.url === `${API_ROOT}/pulls/${input.pullNumber}`, 'workflow run belongs to another pull request.');
  requireEvidence(association.head?.sha === input.headSHA && association.head?.ref === pull.headRef && recordedRepository(association.head.repo, pull.headRepo), 'recorded pull request head does not match.');
  requireEvidence(association.base?.sha === input.baseSHA && association.base?.ref === input.baseRef && recordedRepository(association.base.repo, { id: REPOSITORY_ID, url: API_ROOT, full_name: REPOSITORY }), 'recorded pull request base does not match.');
}

function assertSuccessfulRun(run, workflow, input, pull) {
  assertRunEnvelope(run, workflow, input);
  requireEvidence(matchingHead(run, pull), 'workflow run head repository or branch mismatch.');
  assertRecordedPull(run, input, pull);
  requireEvidence(run.status === 'completed' && run.conclusion === 'success', `latest ${workflow.name} run ${run.id} is ${run.status}/${run.conclusion}; wait for a successful current run.`);
  requireEvidence(!workflow.intentFloor || timestamp(run.created_at) > timestamp(input.ciNotBefore), 'wait for a new CI run after the current queue or metadata intent; same-second ordering is ambiguous.');
}

async function latestRun(api, workflow, input, pull) {
  const runs = await inventory(api, `actions/workflows/${workflow.id}/runs`, { event: 'pull_request', head_sha: input.headSHA }, 'workflow_runs');
  requireEvidence(runs.length > 0, `missing ${workflow.name} pull request run for the exact head.`);
  const numbers = new Set();
  const candidates = [];
  for (const run of runs) {
    assertRunEnvelope(run, workflow, input);
    requireEvidence(!numbers.has(run.run_number), 'duplicate logical workflow run number.');
    numbers.add(run.run_number);
    const matching = associations(run, input);
    if (matching.length || (run.pull_requests.length === 0 && matchingHead(run, pull))) candidates.push(run);
  }
  requireEvidence(candidates.length > 0, `missing ${workflow.name} run associated with this pull request.`);
  const latest = candidates.reduce((selected, run) => run.run_number > selected.run_number ? run : selected, candidates[0]);
  assertSuccessfulRun(latest, workflow, input, pull);
  return latest;
}

function assertJobIdentity(job, run, workflow, input, pull) {
  requireEvidence(job.run_id === run.id && positive(job.run_attempt) && job.run_attempt <= run.run_attempt, 'job belongs to another run or attempt.');
  requireEvidence(job.head_sha === input.headSHA && job.head_branch === pull.headRef && job.workflow_name === workflow.name, 'job head, branch, or workflow mismatch.');
  requireEvidence(job.url === `${API_ROOT}/actions/jobs/${job.id}` && job.run_url === run.url && job.html_url === `${WEB_ROOT}/actions/runs/${run.id}/job/${job.id}`, 'noncanonical job URL.');
  requireEvidence(Object.hasOwn(workflow.jobs, job.name), `unknown job ${String(job.name)} in ${workflow.name}; review the trusted job manifest.`);
}

function assertSuccessfulJob(job, workflow) {
  requireEvidence(job.status === 'completed' && job.conclusion === 'success', `${workflow.name} job ${job.name} is ${job.status}/${job.conclusion}.`);
  requireEvidence(Array.isArray(job.labels) && job.labels.length === 1 && job.labels[0] === workflow.jobs[job.name], `unexpected runner labels for ${job.name}.`);
  requireEvidence(positive(job.runner_id) && job.runner_group_id === 0 && typeof job.runner_name === 'string' && job.runner_name.length > 0, `unknown hosted runner for ${job.name}.`);
  requireEvidence(timestamp(job.completed_at) >= timestamp(job.started_at) && timestamp(job.started_at) >= timestamp(job.created_at), `invalid job timestamps for ${job.name}.`);
}

function selectedJobs(jobs, run, workflow, input, pull) {
  // Partial reruns retain successful jobs from earlier attempts of this run only.
  const latest = new Map();
  const attempts = new Set();
  for (const job of jobs) {
    assertJobIdentity(job, run, workflow, input, pull);
    const key = `${job.name}:${job.run_attempt}`;
    requireEvidence(!attempts.has(key), 'duplicate job name in one run attempt.');
    attempts.add(key);
    if (!latest.has(job.name) || latest.get(job.name).run_attempt < job.run_attempt) latest.set(job.name, job);
  }
  requireEvidence(Object.keys(workflow.jobs).every((name) => latest.has(name)), `missing expected ${workflow.name} job or matrix member.`);
  requireEvidence([...latest.values()].some((job) => job.run_attempt === run.run_attempt), 'current run attempt has no job evidence.');
  return [...latest.keys()].sort((left, right) => left.localeCompare(right, 'en')).map((name) => {
    const job = latest.get(name);
    assertSuccessfulJob(job, workflow);
    return { name, id: job.id, runAttempt: job.run_attempt, runnerLabel: job.labels[0] };
  });
}

function sourceReader(input) {
  const cache = new Map();
  return (ref, path) => {
    const key = `${ref}:${path}`;
    if (!cache.has(key)) {
      cache.set(key, input.github.rest.repos.getContent({ owner: input.owner, repo: input.repo, path, ref }).then(({ data }) => {
        requireEvidence(data?.type === 'file' && data.path === path && typeof data.sha === 'string' && SHA.test(data.sha), `missing immutable workflow source ${path}.`);
        return data.sha;
      }));
    }
    return cache.get(key);
  };
}

async function protectedSources(read, input) {
  return Promise.all(SOURCE_PATHS.map(async (path) => {
    const [trusted, candidate] = await Promise.all([read(input.trustedPolicySHA, path), read(input.headSHA, path)]);
    requireEvidence(trusted === candidate, `candidate changes trusted workflow source ${path}; policy review and protected-base integration are required.`);
    return { path, sha: trusted };
  }));
}

function referencedMerge(run, input) {
  requireEvidence(Array.isArray(run.referenced_workflows) && run.referenced_workflows.length === 1, 'missing or unexpected reusable workflow source.');
  const reference = run.referenced_workflows[0];
  requireEvidence(typeof reference?.sha === 'string' && SHA.test(reference.sha) && reference.ref === `refs/pull/${input.pullNumber}/merge`, 'reusable workflow is not bound to this pull request merge commit.');
  requireEvidence(reference.path === `${REPOSITORY}/${SOURCE_PATHS[1]}@${reference.sha}`, 'reusable workflow path or immutable source mismatch.');
  return reference.sha;
}

async function assertMergeSources(run, input, read, sources) {
  const mergeSHA = referencedMerge(run, input);
  const { data } = await input.github.rest.git.getCommit({ owner: input.owner, repo: input.repo, commit_sha: mergeSHA });
  requireEvidence(data?.sha === mergeSHA && Array.isArray(data.parents) && data.parents.length === 2 && data.parents[0]?.sha === input.baseSHA && data.parents[1]?.sha === input.headSHA, 'reusable workflow merge commit does not have the exact base and head parents.');
  await Promise.all(sources.map(async ({ path, sha }) => {
    requireEvidence(await read(mergeSHA, path) === sha, `merge workflow source ${path} differs from protected policy.`);
  }));
  return mergeSHA;
}

function runSnapshot(run) {
  return JSON.stringify({
    id: run.id, runNumber: run.run_number, runAttempt: run.run_attempt,
    createdAt: run.created_at, updatedAt: run.updated_at,
    references: run.referenced_workflows,
  });
}

async function verifyWorkflow(api, workflow, input, pull, read, sources) {
  const run = await latestRun(api, workflow, input, pull);
  const jobs = await inventory(api, `actions/runs/${run.id}/jobs`, { filter: 'all' }, 'jobs');
  const selected = selectedJobs(jobs, run, workflow, input, pull);
  let mergeSHA = null;
  if (workflow.intentFloor) mergeSHA = await assertMergeSources(run, input, read, sources);
  else requireEvidence(Array.isArray(run.referenced_workflows) && run.referenced_workflows.length === 0, 'unexpected Windows reusable workflow source.');
  const current = await api(`actions/runs/${run.id}`, {}, true);
  assertSuccessfulRun(current, workflow, input, pull);
  requireEvidence(runSnapshot(current) === runSnapshot(run), 'workflow run or attempt changed while auditing jobs.');
  return { workflowId: workflow.id, runId: run.id, runNumber: run.run_number, runAttempt: run.run_attempt, mergeSHA, jobs: selected };
}

async function verifyCI(input) {
  // The caller repeats this snapshot with fresh intent before its guarded merge;
  // a selected-run reread alone cannot detect a newly created run generation.
  assertInput(input);
  const pull = await livePull(input);
  const api = createPublicAPI({ pause, fetchImpl: input.fetchImpl });
  const read = sourceReader(input);
  const sources = await protectedSources(read, input);
  const workflows = [];
  await WORKFLOWS.reduce(async (previous, workflow) => {
    await previous;
    workflows.push(await verifyWorkflow(api, workflow, input, pull, read, sources));
  }, Promise.resolve());
  return { headSHA: input.headSHA, baseSHA: input.baseSHA, trustedPolicySHA: input.trustedPolicySHA, ciNotBefore: input.ciNotBefore, sources, workflows };
}

module.exports = { verifyCI };
module.exports.testables = { WORKFLOWS };
