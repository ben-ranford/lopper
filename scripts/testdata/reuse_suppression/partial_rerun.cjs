'use strict';

// Partial CI reruns carry the successful producer jobs and their artifact into
// a later attempt. The protected verifier must accept that exact carried-over
// evidence and still reject artifacts outside the producing jobs' window.
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const { spawnSync } = require('node:child_process');
const test = require('node:test');
const { gitBlobIdentity } = require('../../queue_me_git_object.js');
const verifySuppressionProvenance = require('../../suppression_provenance.js');
const { createVerifier } = require('../../reuse_suppression.js').testables;

const HEAD = 'a'.repeat(40);
const BASE = 'b'.repeat(40);
const DETECTOR_PATH = 'scripts/inline_suppression_tracker.js';
const detector = Buffer.from('module.exports.testables = { recomputeSuppressionRecords: async () => ({ records: new Map() }) };\n');
const repo = { owner: 'ben-ranford', repo: 'lopper' };

function reportArchive() {
  const script = `
import io, sys, zipfile
output = io.BytesIO()
with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
    info = zipfile.ZipInfo('inline-suppressions.json')
    info.create_system = 3
    info.external_attr = 0o100600 << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    archive.writestr(info, '{"schema": "lopper-inline-suppressions-v1", "suppressions": []}')
sys.stdout.buffer.write(output.getvalue())
`;
  const result = spawnSync('/usr/bin/python3', ['-I', '-c', script], { timeout: 10000, env: { PATH: '/usr/bin:/bin' } });
  assert.equal(result.status, 0, result.stderr?.toString());
  return result.stdout;
}

// Attempt 2 reran only a failed job: its run started after the carried-over
// verify-checks job produced the artifact and the locator job exposed it.
function partialRerun({ artifactCreatedAt = '2026-10-19T11:07:41Z' } = {}) {
  const archive = reportArchive();
  const pull = { number: 1829, state: 'open', changed_files: 1,
    head: { sha: HEAD, repo: { id: 22, full_name: 'ben-ranford/lopper' } },
    base: { sha: BASE, ref: 'main', repo: { id: 11, full_name: 'ben-ranford/lopper' } } };
  const workflow = { id: 44, path: '.github/workflows/ci.yml', name: 'ci' };
  const run = { id: 33, workflow_id: 44, path: workflow.path, name: 'ci', event: 'pull_request',
    status: 'completed', conclusion: 'success', head_sha: HEAD, run_attempt: 2,
    repository: { id: 11 }, head_repository: { id: 22 }, pull_requests: [structuredClone(pull)],
    run_started_at: '2026-10-19T11:27:45Z', updated_at: '2026-10-19T11:39:19Z' };
  const carried = { run_id: 33, run_attempt: 2, head_sha: HEAD, status: 'completed', conclusion: 'success' };
  const jobs = [{ ...carried, name: 'verify-checks', started_at: '2026-10-19T10:45:16Z', completed_at: '2026-10-19T11:07:59Z' },
    { ...carried, name: 'suppression-artifact-55', started_at: '2026-10-19T11:08:01Z', completed_at: '2026-10-19T11:08:03Z' },
    { ...carried, name: 'os-smoke (ubuntu-latest)', started_at: '2026-10-19T11:27:50Z', completed_at: '2026-10-19T11:39:10Z' }];
  const artifact = { id: 55, name: 'pr-report-inputs-1829', expired: false, size_in_bytes: archive.length,
    created_at: artifactCreatedAt, digest: `sha256:${createHash('sha256').update(archive).digest('hex')}`,
    workflow_run: { id: 33, head_sha: HEAD, repository_id: 11, head_repository_id: 22 } };
  const github = {
    rest: {
      repos: {
        get: async () => ({ data: { id: 11, full_name: 'ben-ranford/lopper', default_branch: 'main' } }),
        getContent: async (input) => {
          assert.equal(input.path, DETECTOR_PATH);
          assert.equal(input.ref, BASE);
          return { data: { type: 'file', path: DETECTOR_PATH, encoding: 'base64', content: detector.toString('base64'),
            size: detector.length, sha: gitBlobIdentity(detector) } };
        },
      },
      pulls: { get: async () => ({ data: structuredClone(pull) }), listFiles: async () => { throw new Error('unused'); } },
      git: { getRef: async () => ({ data: { object: { sha: BASE } } }), getBlob: async () => { throw new Error('unused'); } },
      actions: {
        getWorkflow: async () => ({ data: workflow }),
        listWorkflowRuns: async () => ({ data: { total_count: 1, workflow_runs: [structuredClone(run)] } }),
        getWorkflowRun: async () => ({ data: structuredClone(run) }),
        listJobsForWorkflowRunAttempt: async (input) => {
          assert.equal(input.attempt_number, 2);
          return { data: { total_count: jobs.length, jobs: structuredClone(jobs) } };
        },
        getArtifact: async () => ({ data: structuredClone(artifact) }),
      },
    },
    paginate: async () => [],
  };
  const verify = createVerifier(() => verifySuppressionProvenance, async (_repo, id) => {
    assert.equal(id, 55);
    return archive;
  });
  return () => verify({ github, context: { repo }, token: 'test', snapshot: {
    version: 2, policy_source: BASE, repository: 'ben-ranford/lopper', repository_id: 11, head_repository_id: 22,
    pull_number: 1829, head: HEAD, base: BASE, base_ref: 'main' } });
}

test('carried-over producer evidence verifies on the partial rerun attempt', async () => {
  assert.deepEqual(await partialRerun()(), { version: 2, policySHA: BASE, headSHA: HEAD, baseSHA: BASE,
    runId: 33, runAttempt: 2, artifactId: 55, suppressionCount: 0 });
});

test('artifacts outside the listed producer and locator jobs fail with the exact reason', async () => {
  await Promise.all(['2026-10-19T10:45:15Z', '2026-10-19T11:08:04Z', '2026-10-19T11:30:00Z'].map(artifactCreatedAt =>
    assert.rejects(partialRerun({ artifactCreatedAt })(),
      { message: 'Suppression provenance: artifact is outside the selected producer attempt' })));
});
