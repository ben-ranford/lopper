'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { testables, prepareCandidate } = require('./queue_me_reuse');

function fixture() {
  const snapshot = { version: 1, repository: 'ben-ranford/lopper', repository_id: 1155023607,
    head_repository_id: 1155023607, pull_number: 1772, base: 'a'.repeat(40), head: 'b'.repeat(40), base_ref: 'main' };
  const intent = { queueEventId: 'label', ciNotBefore: '2026-10-01T00:00:00Z' };
  return {
    input: { owner: 'ben-ranford', repo: 'lopper', pullNumber: 1772, headSHA: snapshot.head,
      baseSHA: snapshot.base, baseRef: 'main', trustedPolicySHA: snapshot.base,
      queueProof: { ticket: { snapshot, intent }, analysis: { detector_exit: 0 },
        suppression: { runId: 10, runAttempt: 2, artifactId: 20 },
        analysisOutcome: 'success', suppressionOutcome: 'success', readToken: 'read-only' } },
    ci: { intent, ci: { workflows: [{ workflowId: 232814257, runId: 10, runAttempt: 2, artifactId: 20 }] } },
  };
}

test('shared caller receives the exact trusted same-run outputs', async () => {
  const { input, ci } = fixture();
  const verify = testables.createVerifier((document, token) => {
    assert.deepEqual(document, { snapshot: input.queueProof.ticket.snapshot,
      analysis: input.queueProof.analysis, suppression: input.queueProof.suppression });
    assert.equal(token, 'read-only');
    return { reviews: [], producer: { id: 20 } };
  });
  assert.deepEqual(await verify(input, ci), { reviews: [], producer: { id: 20 } });
});

test('failed skipped canceled or absent upstream phases cannot validate', async () => {
  for (const field of ['analysisOutcome', 'suppressionOutcome']) {
    for (const outcome of ['failure', 'skipped', 'cancelled', undefined]) {
      const { input, ci } = fixture();
      input.queueProof[field] = outcome;
      await assert.rejects(testables.createVerifier(() => assert.fail('validation must not run'))(input, ci), /must succeed/);
    }
  }
});

test('different candidate policy identity or changed queue intent holds', async () => {
  for (const mutate of [
    f => { f.input.queueProof.ticket.snapshot.head = 'c'.repeat(40); },
    f => { f.input.queueProof.ticket.snapshot.base = 'c'.repeat(40); },
    f => { f.input.queueProof.ticket.snapshot.pull_number = 1802; },
    f => { f.input.trustedPolicySHA = 'c'.repeat(40); },
    f => { f.input.queueProof.ticket.extra = true; },
    f => { f.input.queueProof.ticket.intent = { queueEventId: 'newer' }; },
  ]) {
    const f = fixture(); mutate(f);
    await assert.rejects(testables.createVerifier(() => assert.fail('validation must not run'))(f.input, f.ci), /snapshot|intent/);
  }
});

test('receipt and independently audited current CI locator must agree', async () => {
  for (const field of ['runId', 'runAttempt', 'artifactId']) {
    const { input, ci } = fixture(); input.queueProof.suppression[field] += 1;
    await assert.rejects(testables.createVerifier(() => assert.fail('validation must not run'))(input, ci), /disagree/);
  }
});

test('shared validator rejection cannot turn into approval', async () => {
  const { input, ci } = fixture();
  await assert.rejects(testables.createVerifier(() => { throw new Error('forged receipt'); })(input, ci), /forged receipt/);
});

test('protected bridge deferral requires exact exit discriminator and same snapshot', async t => {
  const { input, ci } = fixture();
  const document = { snapshot: input.queueProof.ticket.snapshot };
  const result = { status: 75, stdout: JSON.stringify({ version: 1, kind: 'ci-deferred', ...document }) };
  const isDeferred = require('./queue_me_reuse').isDeferred;
  assert.throws(() => testables.validatorResult(result, document), isDeferred);
  for (const change of [{ status: 1 }, { status: 0 }, { error: new Error('timeout') },
    { stdout: '{}' }, { stdout: 'not json' },
    { stdout: JSON.stringify({ version: 1, kind: 'ci-deferred', snapshot: { ...document.snapshot, head: 'c'.repeat(40) } }) }]) {
    assert.throws(() => testables.validatorResult({ ...result, ...change }, document), error => !isDeferred(error));
  }
  t.mock.method(require('./queue_me_ci_intent'), 'collectCIIntent', async () => ci.intent);
  const verify = testables.createVerifier(() => testables.validatorResult(result, document));
  await assert.rejects(verify(input, ci), isDeferred);
  t.mock.method(require('./queue_me_ci_intent'), 'collectCIIntent', async () => ({ ...ci.intent, queueEventId: 'new' }));
  await assert.rejects(verify(input, ci), error => !isDeferred(error) && /intent changed/.test(error.message));
  assert.equal(isDeferred(Object.assign(new Error('waiting'), { code: 'ci-deferred' })), false);
});

test('preparation returns no ticket for cancelled or pending CI and recovers on fresh success', async t => {
  const { harness, run, CI_ID, WINDOWS_ID } = require('./testdata/queue_waiting/ci_fixture.cjs');
  const runs = [{ ...run(CI_ID), conclusion: 'cancelled', referenced_workflows: [] }, run(WINDOWS_ID)];
  const { input } = harness({ runs });
  const intent = { queueEventId: 'label', ciNotBefore: input.ciNotBefore };
  t.mock.method(require('./queue_me_ci_intent'), 'collectCIIntent', async () => intent);
  const blocked = await prepareCandidate(input);
  assert.equal(blocked.ticket, null);
  assert.equal(blocked.readiness.state, 'BLOCKED');
  runs.push({ ...run(CI_ID, 101), status: 'queued', conclusion: null, referenced_workflows: [] });
  const waiting = await prepareCandidate(input);
  assert.equal(waiting.ticket, null);
  assert.equal(waiting.readiness.state, 'WAITING');
  Object.assign(runs.at(-1), run(CI_ID, 101));
  const ready = await prepareCandidate(input);
  assert.equal(ready.readiness.state, 'READY');
  assert.equal(ready.ticket.snapshot.head, input.headSHA);
  assert.equal(ready.ticket.snapshot.base, input.baseSHA);
  assert.deepEqual(ready.ticket.intent, intent);
});

test('blocked readiness cannot hide an intent change during preparation', async t => {
  const { harness, run, CI_ID, WINDOWS_ID } = require('./testdata/queue_waiting/ci_fixture.cjs');
  const { input } = harness({ runs: [{ ...run(CI_ID), conclusion: 'cancelled' }, run(WINDOWS_ID)] });
  let reads = 0;
  t.mock.method(require('./queue_me_ci_intent'), 'collectCIIntent', async () => ({
    queueEventId: `label-${++reads}`, ciNotBefore: input.ciNotBefore,
  }));
  await assert.rejects(prepareCandidate(input), /intent changed/);
});
