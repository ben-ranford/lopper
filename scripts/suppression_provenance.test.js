'use strict';

const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const test = require('node:test');
const { inflateRawSync } = require('node:zlib');
const verifySuppressionProvenance = require('./suppression_provenance.js');
const verifyReuseSuppression = require('./reuse_suppression.js');

const HEAD = 'a'.repeat(40);
const BASE = 'b'.repeat(40);
const OTHER = 'c'.repeat(40);
const DETECTOR_PATH = 'scripts/inline_suppression_tracker.js';
const detectorSource = fs.readFileSync(path.join(__dirname, 'inline_suppression_tracker.js'));
const emptyReport = JSON.stringify({ schema: 'lopper-inline-suppressions-v1', suppressions: [] });

function zip(entries) {
  const script = String.raw`
import io, json, sys, zipfile
output = io.BytesIO()
with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
    for entry in json.load(sys.stdin):
        info = zipfile.ZipInfo(entry['name'])
        info.create_system = 3
        info.external_attr = entry.get('mode', 0o100600) << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        archive.writestr(info, entry['content'] * entry.get('repeat', 1))
sys.stdout.buffer.write(output.getvalue())
`;
  const input = JSON.stringify(entries);
  assert(Buffer.byteLength(input) <= 4096, 'ZIP fixtures must keep Python stdin small; expand repeated content in Python');
  const result = spawnSync('/usr/bin/python3', ['-I', '-c', script], {
    input, maxBuffer: 1024 * 1024,
    timeout: 10000, killSignal: 'SIGKILL', env: { PATH: '/usr/bin:/bin' },
  });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stderr?.toString());
  return result.stdout;
}

function reportZip(content = emptyReport, extras = []) {
  return zip([{ name: 'inline-suppressions.json', content }, ...extras]);
}

const validArchive = reportZip();

function harness(options = {}) {
  const expected = {
    repoId: 11, headRepoId: 22, pullNumber: 1750,
    policySHA: BASE, headSHA: HEAD, baseSHA: BASE, runId: 33, runAttempt: 2,
  };
  const context = { repo: { owner: 'owner', repo: 'lopper' } };
  const pull = {
    number: expected.pullNumber, state: 'open', changed_files: 1,
    head: { sha: HEAD, repo: { id: 22, full_name: 'fork/lopper' } },
    base: { sha: BASE, ref: 'main', repo: { id: 11, full_name: 'owner/lopper' } },
  };
  const run = {
    id: 33, workflow_id: 44, path: '.github/workflows/ci.yml', name: 'ci',
    event: 'pull_request', status: 'completed', conclusion: 'success',
    head_sha: HEAD, run_attempt: 2,
    repository: { id: 11 }, head_repository: { id: 22 },
    pull_requests: [structuredClone(pull)],
    run_started_at: '2026-09-30T12:00:00Z', updated_at: '2026-09-30T12:40:00Z',
  };
  const workflow = { id: 44, path: '.github/workflows/ci.yml', name: 'ci' };
  const archive = options.archive ?? validArchive;
  const artifact = {
    id: 55, name: 'pr-report-inputs-1750', expired: false,
    size_in_bytes: archive.length, created_at: '2026-09-30T12:35:00Z',
    digest: `sha256:${createHash('sha256').update(archive).digest('hex')}`,
    workflow_run: { id: 33, head_sha: HEAD, repository_id: 11, head_repository_id: 22 },
  };
  const detector = {
    type: 'file', path: DETECTOR_PATH, encoding: 'base64',
    content: detectorSource.toString('base64'), size: detectorSource.length, sha: createHash('sha1').update(`blob ${detectorSource.length}\0`).update(detectorSource).digest('hex'),
  };
  const files = options.files ?? [{
    filename: 'clean.go', status: 'added', additions: 1, deletions: 0, patch: '@@ -0,0 +1 @@\n+package clean\n',
  }];
  pull.changed_files = files.length;
  const calls = { pulls: 0, runs: 0, content: [], pagination: 0 };
  const github = {
    rest: {
      actions: {
        getWorkflow: async (input) => {
          assert.deepEqual(input, { ...context.repo, workflow_id: 'ci.yml' });
          return { data: structuredClone(workflow) };
        },
        getWorkflowRun: async (input) => {
          assert.deepEqual(input, { ...context.repo, run_id: expected.runId });
          calls.runs++;
          const value = structuredClone(run);
          options.onRun?.(value, calls.runs);
          return { data: value };
        },
        getArtifact: async (input) => {
          assert.deepEqual(input, { ...context.repo, artifact_id: 55 });
          return { data: structuredClone(artifact) };
        },
      },
      pulls: {
        get: async (input) => {
          assert.deepEqual(input, { ...context.repo, pull_number: expected.pullNumber });
          calls.pulls++;
          const value = structuredClone(pull);
          options.onPull?.(value, calls.pulls);
          return { data: value };
        },
        listFiles: async () => { throw new Error('listFiles must be paginated'); },
      },
      repos: {
        getContent: async (input) => {
          calls.content.push(input);
          if (input.path === DETECTOR_PATH) {
            assert.equal(input.ref, BASE, 'detector source must come from immutable base');
            return { data: structuredClone(detector) };
          }
          assert.equal(input.ref, HEAD, 'PR source is only read at the pinned head');
          return { data: { type: 'file', encoding: 'base64', content: Buffer.from(options.headContent ?? '').toString('base64') } };
        },
      },
      git: { getBlob: async () => { throw new Error('unexpected blob fetch'); } },
    },
    paginate: async (method, input) => {
      assert.equal(method, github.rest.pulls.listFiles);
      assert.deepEqual(input, { ...context.repo, pull_number: expected.pullNumber, per_page: 100 });
      calls.pagination++;
      return structuredClone(files);
    },
  };
  return { expected, context, pull, run, workflow, artifact, detector, calls,
    args: { github, context, expected, artifactId: 55, archive } };
}

test('accepts late production on the exact completed attempt and recomputes with the real base detector', async () => {
  const fixture = harness();
  assert.deepEqual(await verifySuppressionProvenance(fixture.args), {
    version: 2, policySHA: BASE, headSHA: HEAD, baseSHA: BASE, runId: 33, runAttempt: 2, artifactId: 55, suppressionCount: 0,
  });
  assert.equal(fixture.calls.runs, 2);
  assert.equal(fixture.calls.pulls, 4);
  assert.equal(fixture.calls.pagination, 1);
  assert.deepEqual(fixture.calls.content, [{ ...fixture.context.repo, path: DETECTOR_PATH, ref: BASE }]);
  assert.equal(Object.keys(require.cache).filter((filename) => filename.includes('lopper-suppression-provenance-')).length, 0);
});

test('rejects wrong or regressed producer identity before loading trusted code', async (t) => {
  const changes = {
    'wrong workflow ID': (f) => { f.run.workflow_id++; },
    'wrong workflow name': (f) => { f.workflow.name = 'other'; },
    'wrong workflow path': (f) => { f.workflow.path = '.github/workflows/other.yml'; },
    'wrong run path': (f) => { f.run.path = '.github/workflows/other.yml'; },
    'wrong run name': (f) => { f.run.name = 'other'; },
    'wrong run ID': (f) => { f.run.id++; },
    'wrong event': (f) => { f.run.event = 'workflow_dispatch'; },
    'failed run': (f) => { f.run.conclusion = 'failure'; },
    'cancelled run': (f) => { f.run.conclusion = 'cancelled'; },
    'running producer': (f) => { f.run.status = 'in_progress'; },
    'different head': (f) => { f.run.head_sha = OTHER; },
    'older attempt': (f) => { f.run.run_attempt--; },
    'malformed attempt': (f) => { f.run.run_attempt = '3'; },
    'wrong repository': (f) => { f.run.repository.id++; },
    'wrong fork repository': (f) => { f.run.head_repository.id++; },
    'missing PR association': (f) => { f.run.pull_requests = []; },
    'ambiguous PR association': (f) => { f.run.pull_requests.push(f.run.pull_requests[0]); },
    'different PR association': (f) => { f.run.pull_requests[0].number++; },
    'different associated base': (f) => { f.run.pull_requests[0].base.sha = OTHER; },
    'different associated base ref': (f) => { f.run.pull_requests[0].base.ref = 'other'; },
    'different associated head repository': (f) => { f.run.pull_requests[0].head.repo.id++; },
    'current PR closed': (f) => { f.pull.state = 'closed'; },
    'current PR head changed': (f) => { f.pull.head.sha = OTHER; },
    'current PR base changed': (f) => { f.pull.base.sha = OTHER; },
    'current PR fork changed': (f) => { f.pull.head.repo.id++; },
  };
  for (const [name, change] of Object.entries(changes)) {
    await t.test(name, async () => {
      const fixture = harness(); change(fixture);
      await assert.rejects(verifySuppressionProvenance(fixture.args), (error) => {
        assert.match(error.message, /Suppression provenance:/);
        assert.equal(verifySuppressionProvenance.isDeferred(error), false);
        return true;
      });
      assert.equal(fixture.calls.content.length, 0);
    });
  }
});

test('rejects artifacts outside the selected run and attempt', async (t) => {
  const changes = {
    'wrong ID': (a) => { a.id++; },
    'wrong name': (a) => { a.name = 'pr-report-inputs-1'; },
    expired: (a) => { a.expired = true; },
    'wrong run': (a) => { a.workflow_run.id++; },
    'wrong head': (a) => { a.workflow_run.head_sha = OTHER; },
    'wrong target repository': (a) => { a.workflow_run.repository_id++; },
    'wrong fork repository': (a) => { a.workflow_run.head_repository_id++; },
    'old retry artifact': (a) => { a.created_at = '2026-09-30T11:59:59Z'; },
    'artifact after completion': (a) => { a.created_at = '2026-09-30T12:40:01Z'; },
    'missing timestamp': (a) => { delete a.created_at; },
    'wrong digest': (a) => { a.digest = `sha256:${'0'.repeat(64)}`; },
    'missing digest': (a) => { delete a.digest; },
    'different archive size': (a) => { a.size_in_bytes++; },
    oversized: (a) => { a.size_in_bytes = 8 * 1024 * 1024 + 1; },
  };
  for (const [name, change] of Object.entries(changes)) {
    await t.test(name, async () => {
      const fixture = harness(); change(fixture.artifact);
      await assert.rejects(verifySuppressionProvenance(fixture.args), /Suppression provenance:/);
      assert.equal(fixture.calls.content.length, 0);
    });
  }
});

test('validates immutable expected inputs before any API reads', async (t) => {
  for (const [field, value] of [['repoId', 0], ['headRepoId', -1], ['pullNumber', '1750'], ['runId', NaN],
    ['runAttempt', 1.2], ['headSHA', 'main'], ['baseSHA', HEAD.slice(0, 7)], ['sourceRef', 'main']]) {
    await t.test(field, async () => {
      const fixture = harness(); fixture.expected[field] = value;
      await assert.rejects(verifySuppressionProvenance(fixture.args), /Suppression provenance:/);
      assert.equal(fixture.calls.runs, 0);
    });
  }
});

test('rejects missing, malformed, unsafe or oversized archives without extracting files', async (t) => {
  const oversizedReport = zip([{ name: 'inline-suppressions.json', content: ' ', repeat: 131073 }]);
  // Check the local ZIP header and inflate independently so a compact descriptor
  // cannot silently turn the decompressed-size regression into another JSON error.
  assert.equal(oversizedReport.readUInt32LE(0), 0x04034b50);
  assert.equal(oversizedReport.readUInt16LE(8), 8);
  assert.equal(oversizedReport.readUInt32LE(22), 131073);
  const contentStart = 30 + oversizedReport.readUInt16LE(26) + oversizedReport.readUInt16LE(28);
  const compressed = oversizedReport.subarray(contentStart, contentStart + oversizedReport.readUInt32LE(18));
  assert.deepEqual(inflateRawSync(compressed), Buffer.alloc(131073, ' '));
  const archives = {
    'not ZIP': Buffer.from('not an archive'),
    'archive too large': Buffer.alloc(8 * 1024 * 1024 + 1),
    'missing report': zip([{ name: 'lopper-base-outcome.txt', content: 'success' }]),
    'empty report': reportZip(''),
    'wrong schema': reportZip('{"schema":"other","suppressions":[]}'),
    'wrong records type': reportZip('{"schema":"lopper-inline-suppressions-v1","suppressions":{}}'),
    'nonempty records': reportZip('{"schema":"lopper-inline-suppressions-v1","suppressions":[{}]}'),
    'duplicate JSON member': reportZip('{"schema":"lopper-inline-suppressions-v1","suppressions":[{}],"suppressions":[]}'),
    'oversized decompressed report': oversizedReport,
    traversal: reportZip(emptyReport, [{ name: '../detector.cjs', content: 'throw new Error("executed")' }]),
    'unknown root file': reportZip(emptyReport, [{ name: 'detector.cjs', content: 'throw new Error("executed")' }]),
    'duplicate report': reportZip(emptyReport, [{ name: 'inline-suppressions.json', content: emptyReport }]),
    symlink: zip([{ name: 'inline-suppressions.json', content: emptyReport, mode: 0o120777 }]),
    directory: zip([{ name: 'inline-suppressions.json', content: emptyReport, mode: 0o040700 }]),
  };
  for (const [name, archive] of Object.entries(archives)) {
    await t.test(name, async () => {
      const fixture = harness({ archive });
      await assert.rejects(verifySuppressionProvenance(fixture.args), /Suppression provenance:/);
      assert.equal(fixture.calls.content.length, 0);
    });
  }
});

test('rejects invalid trusted detector source metadata', async (t) => {
  const changes = {
    'wrong path': (d) => { d.path = 'head-controlled.js'; },
    'non-file': (d) => { d.type = 'symlink'; },
    'wrong encoding': (d) => { d.encoding = 'none'; },
    'invalid base64': (d) => { d.content = '!invalid!'; },
    'invalid source size': (d) => { d.size++; },
    'oversized source': (d) => { d.content = 'a'.repeat(2 * 1024 * 1024 + 1); },
    'missing blob identity': (d) => { delete d.sha; },
  };
  for (const [name, change] of Object.entries(changes)) {
    await t.test(name, async () => {
      const fixture = harness(); change(fixture.detector);
      await assert.rejects(verifySuppressionProvenance(fixture.args), /Suppression provenance:/);
      assert.equal(fixture.calls.pagination, 0);
    });
  }
});

test('stack recomputation loads protected tracker while hostile parent remains data', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'lopper-stacked-tracker-'));
  const markerPath = path.join(directory, 'parent-executed');
  try {
    const marker = 'no' + 'lint';
    const line = `var unsafe = 1 //${marker} rationale=temporary; owner=@owner; remove-when=resolved`;
    const fixture = harness({ headContent: `package main\n${line}\n`, files: [{
      filename: 'main.go', status: 'added', additions: 2, deletions: 0,
      patch: `@@ -0,0 +1,2 @@\n+package main\n+${line}\n`,
    }] });
    fixture.expected.policySHA = OTHER;
    const original = fixture.args.github.rest.repos.getContent;
    const selected = [];
    fixture.args.github.rest.repos.getContent = async (input) => {
      if (input.path !== DETECTOR_PATH) return original(input);
      selected.push(input.ref);
      if (input.ref === OTHER) return { data: structuredClone(fixture.detector) };
      assert.equal(input.ref, BASE);
      const hostile = Buffer.from(`require('node:fs').writeFileSync(${JSON.stringify(markerPath)}, 'executed'); module.exports.testables = {recomputeSuppressionRecords: async () => ({records: new Map()})};`);
      return { data: { ...fixture.detector, content: hostile.toString('base64'), size: hostile.length,
        sha: createHash('sha1').update(`blob ${hostile.length}\0`).update(hostile).digest('hex') } };
    };
    await assert.rejects(verifySuppressionProvenance(fixture.args), /trusted diff contains inline suppressions/);
    assert.deepEqual(selected, [OTHER]);
    assert.equal(fs.existsSync(markerPath), false);
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('real trusted detector finds an omitted suppression despite clean artifact', async () => {
  const marker = 'no' + 'lint';
  const line = `var unsafe = 1 //${marker} rationale=temporary; owner=@owner; remove-when=resolved`;
  const headContent = `package main\n${line}\n`;
  const fixture = harness({ headContent, files: [{ filename: 'main.go', status: 'added', additions: 2,
    deletions: 0, patch: `@@ -0,0 +1,2 @@\n+package main\n+${line}\n` }] });
  await assert.rejects(verifySuppressionProvenance(fixture.args), /trusted diff contains inline suppressions/);
  assert.equal(fixture.calls.pagination, 1);
  assert.equal(fixture.calls.content[1].ref, HEAD);
});

test('fails closed when head/base drift during recomputation or final refresh', async (t) => {
  for (const call of [2, 3, 4]) {
    for (const side of ['head', 'base']) {
      await t.test(`${side} at pull read ${call}`, async () => {
        const fixture = harness({ onPull: (pull, count) => { if (count === call) pull[side].sha = OTHER; } });
        await assert.rejects(verifySuppressionProvenance(fixture.args), /changed from event SHA|base changed|head or base changed/);
      });
    }
  }
});

test('defers an authenticated newer attempt observed during recomputation', async () => {
  const fixture = harness({ onRun: (run, count) => { if (count === 2) run.run_attempt++; } });
  await assert.rejects(verifySuppressionProvenance(fixture.args), (error) => {
    assert.equal(verifySuppressionProvenance.isDeferred(error), true);
    assert.equal(error.reason, 'superseded');
    assert.deepEqual(error.producer, { id: 33, run_attempt: 3, workflow_id: 44, status: 'completed', conclusion: 'success' });
    assert.equal(Object.isFrozen(error.producer), true);
    return true;
  });
});

test('defers only newer attempts in known nonterminal states after authenticating identities', async () => {
  for (const status of ['queued', 'in_progress', 'waiting', 'pending', 'requested']) {
    const fixture = harness();
    Object.assign(fixture.run, { run_attempt: 3, status, conclusion: null });
    await assert.rejects(verifySuppressionProvenance(fixture.args), (error) => {
      assert.equal(verifySuppressionProvenance.isDeferred(error), true);
      assert.equal(error.reason, 'superseded');
      assert.deepEqual(error.producer, { id: 33, run_attempt: 3, workflow_id: 44, status, conclusion: null });
      return true;
    });
    assert.equal(fixture.calls.content.length, 0);
  }
  for (const mutate of [
    (fixture) => { fixture.run.name = 'other'; },
    (fixture) => { fixture.workflow.name = 'other'; },
    (fixture) => { fixture.run.head_sha = OTHER; },
    (fixture) => { fixture.run.repository.id++; },
    (fixture) => { fixture.run.pull_requests = []; },
    (fixture) => { fixture.run.pull_requests.push(fixture.run.pull_requests[0]); },
    (fixture) => { fixture.pull.base.sha = OTHER; },
    (fixture) => { fixture.run.status = 'unknown'; },
    (fixture) => { fixture.run.conclusion = 'success'; },
    (fixture) => { fixture.run.status = 'completed'; fixture.run.conclusion = 'failure'; fixture.run.run_attempt++; },
  ]) {
    const fixture = harness();
    Object.assign(fixture.run, { run_attempt: 3, status: 'queued', conclusion: null });
    mutate(fixture);
    await assert.rejects(verifySuppressionProvenance(fixture.args), (error) => {
      assert.equal(verifySuppressionProvenance.isDeferred(error), false);
      return true;
    });
  }
  assert.equal(verifySuppressionProvenance.isDeferred({ reason: 'pending', producer: { id: 33, run_attempt: 2 } }), false);
});

test('the adapter rejects same-attempt nonterminal state at initial and final reads', async () => {
  for (const raceRead of [1, 2]) {
    for (const status of ['queued', 'in_progress', 'waiting', 'pending', 'requested']) {
      const fixture = harness({ onRun: (run, count) => {
        if (count === raceRead) Object.assign(run, { status, conclusion: null });
      } });
      await assert.rejects(verifySuppressionProvenance(fixture.args), (error) => {
        assert.equal(verifySuppressionProvenance.isDeferred(error), false);
        assert.match(error.message, /successful producer became nonterminal without a new attempt/);
        return true;
      });
    }
  }
});

test('malicious or unavailable artifacts remain hard failures when a newer attempt is pending', async () => {
  for (const [archive, mutate] of [
    [Buffer.from('not ZIP')],
    [reportZip('{"schema":"lopper-inline-suppressions-v1","suppressions":[{}]}')],
    [reportZip(emptyReport, [{ name: '../payload.js', content: 'untrusted' }])],
    [validArchive, (artifact) => { artifact.digest = 'sha256:invalid'; }],
    [validArchive, (artifact) => { artifact.expired = true; }],
    [validArchive, (artifact) => { delete artifact.created_at; }],
  ]) {
    const fixture = harness({ archive });
    Object.assign(fixture.run, { run_attempt: 3, status: 'queued', conclusion: null });
    mutate?.(fixture.artifact);
    await assert.rejects(verifySuppressionProvenance(fixture.args), (error) => {
      assert.equal(verifySuppressionProvenance.isDeferred(error), false);
      return true;
    });
  }
  const fixture = harness();
  Object.assign(fixture.run, { run_attempt: 3, status: 'queued', conclusion: null });
  fixture.args.github.rest.actions.getArtifact = async () => { throw new Error('artifact unavailable'); };
  await assert.rejects(verifySuppressionProvenance(fixture.args), /artifact unavailable/);
});

function verifyThroughOuter(fixture, listRuns) {
  const { github } = fixture.args;
  github.rest.repos.get = async () => ({ data: { id: 11, full_name: 'owner/lopper', default_branch: 'main' } });
  github.rest.git.getRef = async () => ({ data: { object: { sha: BASE } } });
  github.rest.actions.listWorkflowRuns = listRuns ?? (async () => ({ data: { total_count: 1, workflow_runs: [structuredClone(fixture.run)] } }));
  github.rest.actions.listJobsForWorkflowRunAttempt = async () => ({ data: { total_count: 1, jobs: [{
    name: 'suppression-artifact-55', run_id: 33, run_attempt: 2, head_sha: HEAD, status: 'completed', conclusion: 'success',
  }] } });
  const verify = verifyReuseSuppression.testables.createVerifier(() => verifySuppressionProvenance, async () => fixture.args.archive);
  return verify({ github, context: fixture.context, token: 'test', snapshot: {
    version: 2, policy_source: BASE, repository: 'owner/lopper', repository_id: 11, head_repository_id: 22,
    pull_number: 1750, head: HEAD, base: BASE, base_ref: 'main',
  } });
}

test('the real adapter initial and final attempt races produce authenticated outer deferrals', async () => {
  for (const raceRead of [2, 3]) {
    for (const status of ['queued', 'completed']) {
      const fixture = harness({ onRun: (run, count) => {
        if (count >= raceRead) Object.assign(run, { run_attempt: 3, status, conclusion: status === 'completed' ? 'success' : null });
      } });
      await assert.rejects(verifyThroughOuter(fixture), (error) => {
        assert.equal(verifyReuseSuppression.isDeferred(error), true);
        assert.equal(error.deferred.reason, 'superseded');
        assert.equal(error.deferred.runId, 33);
        assert.equal(error.deferred.runAttempt, 3);
        return true;
      });
    }
  }
});

test('the outer verifier distinguishes a pending observed epoch from an already successful one', async () => {
  for (const completesBeforeReadback of [false, true]) {
    const fixture = harness({ onRun: (run, count) => {
      if (count >= 2) {
        const completed = completesBeforeReadback && count >= 3;
        Object.assign(run, { run_attempt: 3, status: completed ? 'completed' : 'queued', conclusion: completed ? 'success' : null });
      }
    } });
    // Inventory has caught up on the outer reread, so pending classification
    // must use the adapter's observed readiness, not a stale-list shortcut.
    const list = () => ({ data: { total_count: 1, workflow_runs: [fixture.calls.runs >= 2 ?
      { ...structuredClone(fixture.run), run_attempt: 3, status: 'queued', conclusion: null } : structuredClone(fixture.run)] } });
    await assert.rejects(verifyThroughOuter(fixture, async () => list()), (error) => {
      assert.equal(verifyReuseSuppression.isDeferred(error), true);
      assert.equal(error.deferred.runAttempt, 3);
      return true;
    });
  }
  const contradictory = harness({ onRun: (run, count) => {
    if (count >= 2) Object.assign(run, { run_attempt: 3, status: count === 2 ? 'completed' : 'queued', conclusion: count === 2 ? 'success' : null });
  } });
  await assert.rejects(verifyThroughOuter(contradictory), (error) => {
    assert.equal(verifyReuseSuppression.isDeferred(error), false);
    assert.match(error.message, /successful producer became nonterminal without a new attempt/);
    return true;
  });
});

test('the outer verifier does not turn real adapter corruption or epoch regression into deferral', async () => {
  const corrupt = harness({ onRun: (run, count) => {
    if (count >= 2) Object.assign(run, { run_attempt: 3, status: 'queued', conclusion: null });
  } });
  corrupt.artifact.digest = 'sha256:invalid';
  await assert.rejects(verifyThroughOuter(corrupt), (error) => {
    assert.equal(verifyReuseSuppression.isDeferred(error), false);
    return true;
  });
  const regressed = harness({ onRun: (run, count) => {
    if (count === 2) Object.assign(run, { run_attempt: 3, status: 'queued', conclusion: null });
  } });
  await assert.rejects(verifyThroughOuter(regressed), (error) => {
    assert.equal(verifyReuseSuppression.isDeferred(error), false);
    assert.match(error.message, /producer epoch regressed/);
    return true;
  });
});

test('propagates missing evidence API errors instead of treating them as clean', async () => {
  const fixture = harness();
  fixture.args.github.rest.actions.getArtifact = async () => { throw new Error('artifact unavailable'); };
  await assert.rejects(verifySuppressionProvenance(fixture.args), /artifact unavailable/);
  assert.equal(fixture.calls.content.length, 0);
});

test('rejects unavailable trusted source without a checkout fallback', async () => {
  const fixture = harness();
  fixture.args.github.rest.repos.getContent = async (input) => {
    assert.equal(input.path, DETECTOR_PATH);
    assert.equal(input.ref, BASE);
    throw new Error('trusted source unavailable');
  };
  await assert.rejects(verifySuppressionProvenance(fixture.args), /trusted source unavailable/);
  assert.equal(fixture.calls.pagination, 0);
});

test('removes private source files and module cache if the trusted detector throws', async () => {
  const temporarySources = () => fs.readdirSync(os.tmpdir()).filter((name) => name.startsWith('lopper-suppression-provenance-')).sort();
  const before = temporarySources();
  const fixture = harness();
  const throwingSource = Buffer.from('module.exports = { testables: { recomputeSuppressionRecords: async () => { throw new Error("detector unavailable"); } } };');
  fixture.detector.content = throwingSource.toString('base64');
  fixture.detector.size = throwingSource.length;
  fixture.detector.sha = createHash('sha1').update(`blob ${throwingSource.length}\0`).update(throwingSource).digest('hex');
  await assert.rejects(verifySuppressionProvenance(fixture.args), /detector unavailable/);
  assert.deepEqual(temporarySources(), before);
  assert.equal(Object.keys(require.cache).filter((filename) => filename.includes('lopper-suppression-provenance-')).length, 0);
});
