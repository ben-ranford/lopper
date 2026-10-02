'use strict';

// This is an unactivated verifier, not a check publisher. Its source and expected
// snapshot must be supplied by an immutable trusted runner. Workflow ID/path
// identify the producer; they do not prove approval of PR-edited workflow code.
const { createHash } = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const WORKFLOW_PATH = '.github/workflows/ci.yml';
const DETECTOR_PATH = 'scripts/inline_suppression_tracker.js';
const MAX_ARCHIVE_BYTES = 8 * 1024 * 1024;
const MAX_SOURCE_BYTES = 1024 * 1024;
const MAX_REPORT_BYTES = 128 * 1024;

// Static, isolated stdlib program: inspect the zip in memory, never extract it.
const READ_REPORT = `
import io, json, stat, sys, zipfile
limits = {
    'coverage-package-failures.txt': 131072,
    'coverage-status.txt': 64,
    'coverage-total.txt': 64,
    'inline-suppressions.json': 131072,
    'lopper-base-outcome.txt': 64,
    'lopper-delta-outcome.txt': 64,
    'lopper-pr-comment.md': 1048576,
    'memory-bench-status.txt': 64,
    'memory-bench-summary.md': 1048576,
}
data = sys.stdin.buffer.read(8388609)
if len(data) > 8388608:
    raise ValueError('archive exceeds size limit')
with zipfile.ZipFile(io.BytesIO(data)) as archive:
    entries = archive.infolist()
    if len(entries) > len(limits):
        raise ValueError('too many archive entries')
    seen = set()
    report = None
    for entry in entries:
        name = entry.filename
        kind = stat.S_IFMT(entry.external_attr >> 16)
        if name not in limits or name in seen or name != entry.orig_filename:
            raise ValueError('unexpected or duplicate archive path')
        if entry.is_dir() or kind not in (0, stat.S_IFREG) or entry.flag_bits & 1:
            raise ValueError('archive entries must be unencrypted regular files')
        if entry.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED):
            raise ValueError('unsupported archive compression')
        seen.add(name)
        limit = limits[name]
        if entry.file_size > limit:
            raise ValueError('report exceeds size limit')
        with archive.open(entry) as source:
            content = source.read(limit + 1)
        if len(content) > limit or len(content) != entry.file_size:
            raise ValueError('invalid report size')
        if name == 'inline-suppressions.json':
            report = content.decode('utf-8', errors='strict')
    if report is None:
        raise ValueError('missing suppression report')
    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate JSON member')
            result[key] = value
        return result
    value = json.loads(report, object_pairs_hook=unique_object)
    if not isinstance(value, dict) or set(value) != {'schema', 'suppressions'}:
        raise ValueError('invalid suppression report object')
    if value['schema'] != 'lopper-inline-suppressions-v1':
        raise ValueError('invalid suppression report schema')
    if not isinstance(value['suppressions'], list) or value['suppressions']:
        raise ValueError('suppression report must contain zero records')
    sys.stdout.write(json.dumps(value))
`;

function requireCondition(condition, message) {
  if (!condition) throw new Error(`Suppression provenance: ${message}`);
}

function positiveID(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function trustedInputs(context, expected, artifactId, archive) {
  requireCondition(expected && typeof expected === 'object', 'expected snapshot is required');
  const ids = ['repoId', 'headRepoId', 'pullNumber', 'runId', 'runAttempt'];
  const fields = [...ids, 'headSHA', 'baseSHA'];
  requireCondition(Object.keys(expected).length === fields.length, 'unexpected snapshot fields');
  for (const field of ids) {
    requireCondition(positiveID(expected[field]), `invalid ${field}`);
  }
  for (const field of ['headSHA', 'baseSHA']) {
    requireCondition(typeof expected[field] === 'string' && /^[a-f0-9]{40}$/.test(expected[field]), `invalid ${field}`);
  }
  for (const field of ['owner', 'repo']) {
    requireCondition(typeof context?.repo?.[field] === 'string' && /^[A-Za-z0-9_.-]+$/.test(context.repo[field]), `invalid repository ${field}`);
  }
  requireCondition(positiveID(artifactId), 'invalid artifact ID');
  requireCondition(Buffer.isBuffer(archive) && archive.length > 0 && archive.length <= MAX_ARCHIVE_BYTES, 'invalid archive size');
  return { expected: Object.freeze({ ...expected }), repo: { owner: context.repo.owner, repo: context.repo.repo } };
}

function assertPull(pull, expected) {
  requireCondition(pull?.number === expected.pullNumber && pull.state === 'open', 'pull request is unavailable or closed');
  requireCondition(pull.head?.sha === expected.headSHA && pull.base?.sha === expected.baseSHA, 'pull request head or base changed');
  requireCondition(pull.head?.repo?.id === expected.headRepoId && pull.base?.repo?.id === expected.repoId, 'pull request repository mismatch');
}

function assertRun(run, workflow, expected) {
  requireCondition(workflow?.path === WORKFLOW_PATH && positiveID(workflow.id), 'unexpected CI workflow identity');
  requireCondition(run?.id === expected.runId && run.workflow_id === workflow.id && run.path === WORKFLOW_PATH, 'producer workflow mismatch');
  requireCondition(run.event === 'pull_request' && run.status === 'completed' && run.conclusion === 'success', 'producer is not a successful completed pull_request run');
  requireCondition(run.head_sha === expected.headSHA && run.run_attempt === expected.runAttempt, 'producer head or attempt changed');
  requireCondition(run.repository?.id === expected.repoId && run.head_repository?.id === expected.headRepoId, 'producer repository mismatch');
  const pulls = run.pull_requests;
  requireCondition(Array.isArray(pulls) && pulls.some((pull) => pull.number === expected.pullNumber &&
    pull.head?.sha === expected.headSHA && pull.base?.sha === expected.baseSHA &&
    pull.head?.repo?.id === expected.headRepoId && pull.base?.repo?.id === expected.repoId), 'producer has no exact pull request association');
}

function assertArtifact(artifact, producer, expected, artifactId, archive) {
  requireCondition(artifact?.id === artifactId && artifact.name === `pr-report-inputs-${expected.pullNumber}` && artifact.expired === false, 'missing, expired, or incorrectly named artifact');
  requireCondition(positiveID(artifact.size_in_bytes) && artifact.size_in_bytes <= MAX_ARCHIVE_BYTES && artifact.size_in_bytes === archive.length, 'invalid artifact size');
  const run = artifact.workflow_run;
  requireCondition(run?.id === expected.runId && run.head_sha === expected.headSHA &&
    run.repository_id === expected.repoId && run.head_repository_id === expected.headRepoId, 'artifact producer mismatch');
  // Artifacts lack run_attempt. Reject retained artifacts from older retries;
  // the caller must select evidence created during this exact producer attempt.
  const started = Date.parse(producer.run_started_at);
  const created = Date.parse(artifact.created_at);
  const completed = Date.parse(producer.updated_at);
  requireCondition(Number.isFinite(started) && Number.isFinite(created) && Number.isFinite(completed) &&
    created >= started && created <= completed, 'artifact is outside the selected producer attempt');
  const digest = `sha256:${createHash('sha256').update(archive).digest('hex')}`;
  requireCondition(artifact.digest === digest, 'archive digest mismatch');
}

function readReport(archive) {
  const result = spawnSync('/usr/bin/python3', ['-I', '-c', READ_REPORT], {
    input: archive,
    encoding: 'utf8',
    timeout: 10000,
    killSignal: 'SIGKILL',
    maxBuffer: MAX_REPORT_BYTES + 1024,
    env: { PATH: '/usr/bin:/bin' },
  });
  requireCondition(!result.error && result.status === 0, 'invalid suppression report archive');
  return JSON.parse(result.stdout);
}

function decodeDetector(data) {
  requireCondition(data?.type === 'file' && data.path === DETECTOR_PATH && data.encoding === 'base64', 'invalid trusted detector source');
  requireCondition(typeof data.content === 'string' && data.content.length <= MAX_SOURCE_BYTES * 2, 'invalid detector source size');
  const encoded = data.content.replaceAll('\n', '');
  const source = Buffer.from(encoded, 'base64');
  requireCondition(source.length > 0 && source.length <= MAX_SOURCE_BYTES && source.toString('base64') === encoded, 'invalid detector source encoding');
  requireCondition(typeof data.sha === 'string' && /^[a-f0-9]{40}$/.test(data.sha) && data.size === source.length, 'detector source metadata mismatch');
  return source;
}

function readOnlyClient(github) {
  return {
    paginate: github.paginate.bind(github),
    rest: {
      pulls: { get: github.rest.pulls.get, listFiles: github.rest.pulls.listFiles },
      repos: { getContent: github.rest.repos.getContent },
      git: { getBlob: github.rest.git.getBlob },
    },
  };
}

async function recomputeFromTrustedSource(github, repo, pull, expected) {
  const { data } = await github.rest.repos.getContent({ ...repo, path: DETECTOR_PATH, ref: expected.baseSHA });
  const source = decodeDetector(data);
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'lopper-suppression-provenance-'));
  const filename = path.join(directory, 'detector.cjs');
  let moduleID;
  try {
    fs.chmodSync(directory, 0o700);
    fs.writeFileSync(filename, source, { mode: 0o600, flag: 'wx' });
    // Temporary compatibility API: replace with a production export when the
    // tracker owner introduces one. Never resolve this module from a PR checkout.
    moduleID = require.resolve(filename);
    const recompute = require(moduleID).testables?.recomputeSuppressionRecords;
    requireCondition(typeof recompute === 'function', 'trusted detector API is unavailable');
    const result = await recompute({ github: readOnlyClient(github), context: { repo, payload: { pull_request: pull } } });
    requireCondition(result?.records instanceof Map && result.records.size === 0, 'trusted diff contains inline suppressions');
  } finally {
    if (moduleID) delete require.cache[moduleID];
    fs.rmSync(directory, { recursive: true, force: true });
  }
}

async function verifySuppressionProvenance({ github, context, expected: supplied, artifactId, archive }) {
  const { expected, repo } = trustedInputs(context, supplied, artifactId, archive);
  const [workflow, run, pull, artifact] = await Promise.all([
    github.rest.actions.getWorkflow({ ...repo, workflow_id: 'ci.yml' }),
    github.rest.actions.getWorkflowRun({ ...repo, run_id: expected.runId }),
    github.rest.pulls.get({ ...repo, pull_number: expected.pullNumber }),
    github.rest.actions.getArtifact({ ...repo, artifact_id: artifactId }),
  ]);
  assertPull(pull.data, expected);
  assertRun(run.data, workflow.data, expected);
  assertArtifact(artifact.data, run.data, expected, artifactId, archive);
  readReport(archive);
  await recomputeFromTrustedSource(github, repo, pull.data, expected);
  const [currentPull, currentRun] = await Promise.all([
    github.rest.pulls.get({ ...repo, pull_number: expected.pullNumber }),
    github.rest.actions.getWorkflowRun({ ...repo, run_id: expected.runId }),
  ]);
  assertPull(currentPull.data, expected);
  assertRun(currentRun.data, workflow.data, expected);
  return { headSHA: expected.headSHA, baseSHA: expected.baseSHA, runId: expected.runId, runAttempt: expected.runAttempt, artifactId, suppressionCount: 0 };
}

module.exports = verifySuppressionProvenance;
