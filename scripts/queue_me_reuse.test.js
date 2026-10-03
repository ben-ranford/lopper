'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { testables } = require('./queue_me_reuse');

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
