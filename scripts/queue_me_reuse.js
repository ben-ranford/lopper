'use strict';

const path = require('node:path');
const { spawnSync } = require('node:child_process');

const SNAPSHOT_KEYS = ['version', 'repository', 'repository_id', 'head_repository_id',
  'pull_number', 'base', 'head', 'base_ref'];

function requireEvidence(condition, message) {
  if (!condition) throw new Error(`Queue reuse paused: ${message}`);
}

function exactFields(value, fields) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).length === fields.length && fields.every((key) => Object.hasOwn(value, key));
}

function validateTicket(ticket, input) {
  requireEvidence(exactFields(ticket, ['snapshot', 'intent']), 'missing or malformed prepared snapshot.');
  const s = ticket.snapshot;
  requireEvidence(exactFields(s, SNAPSHOT_KEYS) && s.version === 1 &&
    s.repository === `${input.owner}/${input.repo}` && s.repository === 'ben-ranford/lopper' &&
    s.repository_id === 1155023607 && Number.isSafeInteger(s.head_repository_id) && s.head_repository_id > 0 &&
    s.pull_number === input.pullNumber && s.head === input.headSHA && s.base === input.baseSHA &&
    s.base_ref === input.baseRef && s.base_ref === 'main' && s.base === input.trustedPolicySHA,
  'prepared snapshot does not match this candidate and protected policy.');
  requireEvidence(ticket.intent && typeof ticket.intent === 'object' && !Array.isArray(ticket.intent),
    'missing prepared queue intent.');
  return s;
}

async function prepareCandidate(input) {
  const { data: pull } = await input.github.rest.pulls.get({
    owner: input.owner, repo: input.repo, pull_number: input.pullNumber,
  });
  const { collectCIIntent } = require('./queue_me_ci_intent');
  const ticket = {
    snapshot: {
      version: 1, repository: `${input.owner}/${input.repo}`,
      repository_id: pull.base?.repo?.id, head_repository_id: pull.head?.repo?.id,
      pull_number: pull.number, base: pull.base?.sha, head: pull.head?.sha, base_ref: pull.base?.ref,
    },
    intent: await collectCIIntent(input),
  };
  validateTicket(ticket, input);
  requireEvidence(pull.state === 'open' && pull.draft === false, 'candidate is no longer open and ready.');
  return ticket;
}

function runSharedValidator(document, token) {
  requireEvidence(typeof token === 'string' && token.length > 0 && !/\s/.test(token), 'missing read-only API token.');
  const result = spawnSync('/usr/bin/python3', ['-E', '-S', '-B', path.join(__dirname, 'queue_me_reuse.py'), 'validate'], {
    input: JSON.stringify(document), encoding: 'utf8', maxBuffer: 1024 * 1024, timeout: 120000,
    // Never pass the queue App token, action inputs, Git config or output-file
    // paths to the shared validator. It executes no candidate programs.
    env: { PATH: '/usr/bin:/bin', GH_TOKEN: token },
  });
  requireEvidence(!result.error && result.status === 0, 'shared protected validation failed.');
  const evidence = JSON.parse(result.stdout);
  requireEvidence(exactFields(evidence, ['reviews', 'producer']) && Array.isArray(evidence.reviews) &&
    evidence.producer && typeof evidence.producer === 'object', 'malformed shared live evidence.');
  return evidence;
}

function createVerifier(validate = runSharedValidator) {
  return async function verifySharedReuse(input, ciEvidence) {
    const proof = input.queueProof;
    requireEvidence(proof?.analysisOutcome === 'success' && proof.suppressionOutcome === 'success',
      'both protected read-only jobs must succeed.');
    const snapshot = validateTicket(proof.ticket, input);
    requireEvidence(JSON.stringify(proof.ticket.intent) === JSON.stringify(ciEvidence.intent),
      'queue intent changed after selection.');
    const receipt = proof.suppression;
    const workflow = ciEvidence.ci?.workflows?.find((item) => item.workflowId === 232814257);
    requireEvidence(workflow && receipt && workflow.runId === receipt.runId &&
      workflow.runAttempt === receipt.runAttempt && workflow.artifactId === receipt.artifactId,
    'CI locator and shared suppression receipt disagree.');
    return validate({ snapshot, analysis: proof.analysis, suppression: receipt }, proof.readToken);
  };
}

module.exports = { prepareCandidate, verifySharedReuse: createVerifier() };
module.exports.testables = { createVerifier, validateTicket, runSharedValidator };
