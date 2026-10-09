'use strict';

// This adds the actual committed-parent guard around the unchanged strict
// Sonar/App/quality verifier. It publishes no status and receives no token.
const { verifySonar } = require('./queue_me_sonar');
const { createPublicAPI } = require('./queue_me_public_api');

function pause(message) {
  return new Error(`Sonar maintenance remains pending: ${message}`);
}

function validTime(value) {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})$/.test(value) &&
    Number.isFinite(Date.parse(value));
}

function validateHistory(rows) {
  const ids = new Set();
  for (const [index, row] of rows.entries()) {
    if (typeof row.key !== 'string' || !row.key || ids.has(row.key) || !validTime(row.date) ||
        (index > 0 && Date.parse(rows[index - 1].date) <= Date.parse(row.date))) {
      throw pause('parent history is malformed, ambiguous or unordered');
    }
    ids.add(row.key);
  }
}

async function parentBaseline(input) {
  const api = createPublicAPI({ pause, fetchImpl: input.fetchImpl ?? fetch });
  const project = 'ben-ranford_lopper';
  const branches = await api('project_branches/list', { project });
  const rows = branches?.branches?.filter(row => row.name === input.baseRef);
  if (!Array.isArray(rows) || rows.length !== 1 || rows[0].type !== 'LONG') {
    throw pause('actual target lacks one independent LONG baseline');
  }
  const history = await api('project_analyses/search', { project, branch: input.baseRef, p: '1', ps: '1000' });
  const latest = history?.analyses;
  if (!Array.isArray(latest) || latest.length < 1 || latest.length > 1000 || history.paging?.pageIndex !== 1 ||
      !Number.isSafeInteger(history.paging?.total) || history.paging.total !== latest.length ||
      latest[0].revision !== input.baseSHA || typeof latest[0].key !== 'string' || !latest[0].key ||
      !validTime(latest[0].date)) {
    throw pause('latest branch analysis does not identify the actual committed base');
  }
  validateHistory(latest);
  return { branch: input.baseRef, revision: input.baseSHA, analysisId: latest[0].key, date: latest[0].date };
}

async function verifyMaintenanceAnalysis(input) {
  if (!/^[a-f0-9]{40}$/.test(input.baseSHA ?? '')) throw pause('exact base revision is required');
  const before = await parentBaseline(input);
  const child = await verifySonar(input);
  if (Date.parse(child.analysisDate) <= Date.parse(before.date)) throw pause('child analysis predates its parent baseline');
  const after = await parentBaseline(input);
  if (JSON.stringify(before) !== JSON.stringify(after)) throw pause('parent baseline changed during child verification');
  return { parent: before, child };
}

module.exports = { verifyMaintenanceAnalysis, parentBaseline };
