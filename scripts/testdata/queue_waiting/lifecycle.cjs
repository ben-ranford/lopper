'use strict';

// Frozen proof support: every production import resolves from the worktree
// executing this file, including the historical regression-proof overlay.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const controller = require('../../queue_me_controller');
const reuse = require('../../queue_me_reuse');
const { harness, run, HEAD, BASE, REPO, CI_ID, WINDOWS_ID, ARTIFACT_ID } = require('./ci_fixture.cjs');
const workflow = JSON.parse(fs.readFileSync(process.env.QUEUE_WORKFLOW_FIXTURE, 'utf8'));

// Interpret only the workflow condition grammar exercised here. Workflow text
// is data: no part of the expression is passed to a JavaScript code evaluator.
function conditionTokens(source) {
  const wrapped = source.trim();
  assert.ok(wrapped.startsWith('${{') && wrapped.endsWith('}}'), 'condition wrapper');
  assert.ok(wrapped.length <= 4096, 'bounded condition length');
  const expression = wrapped.slice(3, -2).trim();
  const patterns = [/\s+/y, /needs\.[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)+/y,
    /'[^'\r\n]*'/y, /always\(\)|&&|\|\||==|!=|[!()]/y];
  const tokens = [];
  let offset = 0;
  while (offset < expression.length) {
    const match = conditionToken(patterns, expression, offset);
    assert.ok(match, `unsupported condition token at ${offset}`);
    offset += match[0].length;
    if (match[0].trim()) tokens.push(match[0]);
    assert.ok(tokens.length <= 256, 'bounded condition token count');
  }
  return tokens;
}

function conditionToken(patterns, expression, offset) {
  for (const pattern of patterns) {
    pattern.lastIndex = offset;
    const match = pattern.exec(expression);
    if (match) return match;
  }
  return null;
}

function conditionValue(token, needs) {
  if (token === 'always()') return true;
  if (token?.startsWith("'")) return token.slice(1, -1);
  assert.ok(token?.startsWith('needs.'), 'expected condition value');
  return token.split('.').slice(1).reduce((value, key) => {
    assert.ok(value && Object.hasOwn(value, key), 'unknown condition property');
    return value[key];
  }, needs);
}

function conditionOperation(operator, left, right) {
  switch (operator) {
    case '&&': return Boolean(left) && Boolean(right);
    case '||': return Boolean(left) || Boolean(right);
    case '==': return left === right;
    case '!=': return left !== right;
    default: throw new Error('unsupported condition operator');
  }
}

function evaluateCondition(source, needs) {
  const tokens = conditionTokens(source);
  let index = 0;
  function chain(next, operators) {
    let value = next();
    while (operators.includes(tokens[index])) {
      const operator = tokens[index++];
      value = conditionOperation(operator, value, next());
    }
    return value;
  }
  function primary() {
    const token = tokens[index++];
    if (token === '!') return !primary();
    if (token !== '(') return conditionValue(token, needs);
    const value = disjunction();
    assert.equal(tokens[index++], ')', 'balanced condition parentheses');
    return value;
  }
  const comparison = () => chain(primary, ['==', '!=']);
  const conjunction = () => chain(comparison, ['&&']);
  const disjunction = () => chain(conjunction, ['||']);
  const value = disjunction();
  assert.equal(index, tokens.length, 'complete condition expression');
  return Boolean(value);
}

function condition(name, needs) {
  return evaluateCondition(workflow.jobs[name].if, needs);
}

test('condition interpreter rejects executable or unsupported workflow text', () => {
  for (const expression of ["process.exit()", "always(); true", "needs.constructor.name == 'Object'",
    "always() + 1", "(always()", "always() always()", "'unterminated"] ) {
    assert.throws(() => evaluateCondition('${{ ' + expression + ' }}', {}));
  }
  assert.equal(evaluateCondition("${{ !('failed' == 'success') && (always() || 'a' != 'a') }}", {}), true);
  assert.equal(evaluateCondition("${{ always() && 'a' == 'b' || 'c' != 'c' }}", {}), false);
});

function queueHarness(runs) {
  const fixture = harness({ runs });
  const github = fixture.input.github;
  const calls = { disarmed: 0, merged: [], comments: [], proofs: 0 };
  const pull = { id: 300, number: 1777, node_id: 'PR_1777', state: 'open', draft: false,
    labels: [{ name: 'queue-me' }],
    head: { sha: HEAD, ref: 'feature', repo: { ...REPO } },
    base: { sha: BASE, ref: 'main', repo: { ...REPO, owner: { login: 'ben-ranford' } } } };
  const state = { id: pull.node_id, number: pull.number, state: 'OPEN', isDraft: false,
    headRefOid: HEAD, baseRefOid: BASE, baseRefName: 'main', mergeable: 'MERGEABLE',
    mergeStateStatus: 'CLEAN', autoMergeRequest: {} };
  let lastEditedAt = null;
  github.rest.pulls.get = async () => ({ data: structuredClone(pull) });
  github.rest.pulls.list = async () => {};
  github.rest.repos.get = async () => ({ data: { default_branch: 'main', full_name: REPO.full_name } });
  github.rest.repos.getBranch = async () => ({ data: { commit: { sha: BASE } } });
  github.rest.repos.compareCommitsWithBasehead = async () => ({ data: {
    status: 'ahead', total_commits: 1, commits: [{ sha: HEAD,
      author: { login: 'ben-ranford', type: 'User' }, committer: { login: 'ben-ranford', type: 'User' },
      commit: { author: { name: 'ben-ranford', email: '84072202+ben-ranford@users.noreply.github.com' },
        committer: { name: 'ben-ranford', email: '84072202+ben-ranford@users.noreply.github.com' } } }],
  } });
  github.rest.issues = {
    getLabel: async () => {}, listComments: async () => {},
    createComment: async ({ body }) => { calls.comments.push(body); },
  };
  github.paginate = async (_method, args) => args.issue_number ? [] : [pull];
  github.graphql = async (query, variables) => {
    if (query.includes('QueueCIIntent(')) return { repository: { pullRequest: {
      ...state, createdAt: '2026-10-01T00:00:00Z', lastEditedAt,
      labels: { nodes: pull.labels, pageInfo: { hasNextPage: false } },
      timelineItems: { nodes: [{ __typename: 'LabeledEvent', id: 'label1',
        createdAt: '2026-10-01T00:59:59Z', label: { name: 'queue-me' } }],
      pageInfo: { hasNextPage: false, endCursor: null } },
    } } };
    if (query.includes('QueuePullStateByID')) return { node: { ...state } };
    if (query.includes('QueuePullState(')) return { repository: { pullRequest: { ...state } } };
    if (query.includes('DisableQueueAutoMerge')) {
      calls.disarmed++;
      state.autoMergeRequest = null;
      return { disablePullRequestAutoMerge: { pullRequest: { number: 1777, autoMergeRequest: null } } };
    }
    if (query.includes('MergeQueuedPull')) {
      assert.equal(variables.expectedHeadOid, HEAD);
      calls.merged.push(HEAD);
      return { mergePullRequest: { pullRequest: { merged: true } } };
    }
    throw new Error(`Unexpected operation: ${query}`);
  };
  const fetchImpl = async (...args) => {
    assert.equal(calls.disarmed, 1, 'all retained merge requests must be disarmed before CI reads');
    return fixture.input.fetchImpl(...args);
  };
  return { calls, runs, state, fetchImpl, edit: () => { lastEditedAt = '2026-10-01T02:01:00Z'; },
    args: { github, context: { repo: { owner: 'ben-ranford', repo: 'lopper' },
      eventName: 'pull_request_target', payload: { action: 'labeled', pull_request: pull, label: { name: 'queue-me' } } },
    trustedPolicySHA: BASE, core: { notice: () => {} } } };
}

function proofAdapters(t, fixture) {
  // CI selection, full job audit, intent, receipt/locator agreement, controller
  // rereads and guarded merge are real. These adapters represent independently
  // tested external review/Sonar/detector services, not readiness decisions.
  t.mock.method(reuse, 'verifySharedReuse', reuse.testables.createVerifier(() => {
    fixture.calls.proofs++;
    return { reviews: [], producer: { id: ARTIFACT_ID } };
  }));
  for (const [file, method] of [['queue_me_suppressions', 'verifySuppressions'],
    ['queue_me_reviews', 'verifyReviews'], ['queue_me_sonar', 'verifySonar']]) {
    t.mock.method(require(`../../${file}`), method, async () => ({}));
  }
}

function queueProof(ticket) {
  return { ticket, analysis: { detector_exit: 0 },
    suppression: { runId: 100, runAttempt: 1, artifactId: ARTIFACT_ID },
    analysisOutcome: 'success', suppressionOutcome: 'success', readToken: 'read-only' };
}

function needsFor(ticket) {
  return { prepare: { result: 'success', outputs: { ticket: ticket ? JSON.stringify(ticket) : '' } },
    analyze: { result: 'success', outputs: {} },
    suppression: { result: 'success', outputs: { receipt: '', deferred: '', readiness: '' } } };
}

for (const initial of ['pending', 'registration', 'prior-intent']) {
  test(`queue lifecycle: ${initial} initial head event waits, completion performs strict CI and guarded merge`, async t => {
    const runs = initial === 'registration' ? [] : [run(CI_ID), run(WINDOWS_ID)];
    if (initial === 'pending') Object.assign(runs[0], { status: 'queued', conclusion: null, referenced_workflows: [] });
    if (initial === 'prior-intent') runs[0].created_at = '2026-10-01T00:59:59Z';
    const fixture = queueHarness(runs);
    t.mock.method(globalThis, 'fetch', fixture.fetchImpl);
    proofAdapters(t, fixture);
    const ticket = await controller.prepareQueue(fixture.args);
    assert.equal(ticket, null, 'initial CI scheduling must withhold the queue ticket');
    const needs = needsFor(ticket);
    for (const job of ['analyze', 'suppression', 'advance']) assert.equal(condition(job, needs), false, job);
    assert.equal(fixture.calls.proofs, 0);
    assert.deepEqual(fixture.calls.merged, []);
    assert.equal(fixture.state.autoMergeRequest, null);
    assert.match(fixture.calls.comments.join(), /Waiting for current CI/);

    // A main-associated workflow_run completion repeats real preparation; the
    // initial head event has created no failed proof/advance job to retain.
    runs.splice(0, runs.length, run(CI_ID), run(WINDOWS_ID));
    fixture.args.context = { ...fixture.args.context, eventName: 'workflow_run', payload: {} };
    const ready = await controller.prepareQueue(fixture.args);
    assert.equal(ready.snapshot.head, HEAD);
    const readyNeeds = needsFor(ready);
    assert.equal(condition('analyze', readyNeeds), true);
    assert.equal(condition('suppression', readyNeeds), true);
    readyNeeds.suppression.outputs.receipt = 'validated';
    assert.equal(condition('advance', readyNeeds), true);
    fixture.args.queueProof = queueProof(ready);
    await controller(fixture.args);
    assert.equal(fixture.calls.proofs, 3);
    assert.deepEqual(fixture.calls.merged, [HEAD]);
    assert.equal(fixture.state.autoMergeRequest, null);
  });
}

test('workflow skips final writer only for explicit successful scheduling deferral', () => {
  const needs = needsFor({ snapshot: {} });
  needs.suppression.outputs = { readiness: 'WAITING', deferred: '{"version":1}', receipt: '' };
  assert.equal(condition('advance', needs), false);
  for (const phase of ['analyze', 'suppression']) for (const result of ['failure', 'cancelled', 'skipped', '']) {
    const changed = structuredClone(needs); changed[phase].result = result;
    assert.equal(condition('advance', changed), true);
  }
  for (const readiness of ['', 'READY', 'unknown']) {
    const changed = structuredClone(needs); changed.suppression.outputs.readiness = readiness;
    assert.equal(condition('advance', changed), true);
  }
  const contradictory = structuredClone(needs); contradictory.suppression.outputs.receipt = 'receipt';
  assert.equal(condition('advance', contradictory), true);
});

test('intent drift after a real CI pending gate remains an error', async t => {
  const fixture = queueHarness([{ ...run(CI_ID), status: 'queued', conclusion: null }, run(WINDOWS_ID)]);
  t.mock.method(globalThis, 'fetch', async (...args) => {
    const result = await fixture.fetchImpl(...args);
    fixture.edit();
    return result;
  });
  await assert.rejects(controller.prepareQueue(fixture.args), /intent changed/);
  assert.deepEqual(fixture.calls.merged, []);
});


for (const deferredBoundary of [2, 3]) {
  test(`protected bridge deferral at shared boundary ${deferredBoundary} keeps final advance successful without merging`, async t => {
    const fixture = queueHarness([run(CI_ID), run(WINDOWS_ID)]);
    t.mock.method(globalThis, 'fetch', fixture.fetchImpl);
    proofAdapters(t, fixture);
    const ready = await controller.prepareQueue(fixture.args);
    fixture.args.queueProof = queueProof(ready);
    let validations = 0;
    t.mock.method(reuse, 'verifySharedReuse', reuse.testables.createVerifier(document => {
      validations++;
      if (validations === deferredBoundary) {
        return reuse.testables.validatorResult({ status: 75,
          stdout: JSON.stringify({ version: 1, kind: 'ci-deferred', snapshot: document.snapshot }) }, document);
      }
      return { reviews: [], producer: { id: ARTIFACT_ID } };
    }));
    await controller(fixture.args);
    assert.equal(validations, deferredBoundary);
    assert.deepEqual(fixture.calls.merged, []);
    assert.equal(fixture.state.autoMergeRequest, null);
    assert.match(fixture.calls.comments.join(), /Queue reuse waiting/);
  });
}
