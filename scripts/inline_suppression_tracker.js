'use strict';

const crypto = require('node:crypto');

const MAX_CHANGED_FILES = 3000;
const MAX_RECORDS = 100;
const MARKER_PATTERN =
  /(^|\s)((\/\/|\/\*+|#)\s*(@?(no(sec|sonar|lint|qa)|eslint-disable(-next-line|-line)?|ts-(ignore|expect-error)|pragma:\s*no\s+cover|coverage:\s*ignore)))([^A-Za-z0-9_-]|$)/iu;
const SOURCE_FILE_PATTERN =
  /^(?:\.githooks\/|.*\.(?:go|sh|bash|zsh|ksh|py|rb|php|js|jsx|cjs|mjs|ts|tsx|java|kt|kts|swift|rs|c|cc|cpp|cxx|h|hpp|hh|cs|ya?ml))$/u;

function metadataValue(content, keyPattern) {
  const match = new RegExp(`(^|[\\s;,])(${keyPattern})\\s*[:=]\\s*([^;]+)`, 'iu').exec(content);
  return match ? match[3].trim() : '';
}

function fingerprintFor(file, content) {
  return crypto.createHash('sha256').update(`${file}\n${content}`).digest('hex');
}

function validateString(value, label, maxBytes) {
  if (
    typeof value !== 'string' ||
    value.length === 0 ||
    Buffer.byteLength(value, 'utf8') > maxBytes ||
    /[\u0000-\u0008\u000a-\u001f\u007f]/u.test(value)
  ) {
    throw new Error(`Invalid inline suppression ${label}.`);
  }
  return value;
}

function validateFile(file) {
  validateString(file, 'file', 512);
  if (file.startsWith('/') || file.split('/').includes('..')) {
    throw new Error('Invalid inline suppression file path.');
  }
  return file;
}

function sourceURLFor({ serverURL, owner, repo, headSHA, file, line }) {
  return `${serverURL || 'https://github.com'}/${owner}/${repo}/blob/${headSHA}/${file}#L${line}`;
}

function escapeFence(value) {
  return value.replaceAll('```', '``\u200b`');
}

function addSuppression(records, { file, line, content, context, headSHA }) {
  validateFile(file);
  if (!Number.isInteger(line) || line < 1 || line > 1000000) {
    throw new Error('Invalid inline suppression line.');
  }
  validateString(content, 'content', 4096);

  const rationale = validateString(metadataValue(content, 'rationale|reason'), 'rationale', 1024);
  const owner = validateString(metadataValue(content, 'owner'), 'owner', 256);
  const removeWhen = validateString(
    metadataValue(content, 'remove-when|removal-condition|removal'),
    'remove_when',
    1024,
  );
  const fingerprint = fingerprintFor(file, content);
  if (records.has(fingerprint)) {
    return;
  }
  records.set(fingerprint, {
    fingerprint,
    file,
    line,
    source: sourceURLFor({
      serverURL: process.env.GITHUB_SERVER_URL,
      owner: context.repo.owner,
      repo: context.repo.repo,
      headSHA,
      file,
      line,
    }),
    content,
    rationale,
    owner,
    remove_when: removeWhen,
  });
}

function scanPatch(records, { file, patch, context, headSHA }) {
  validateFile(file);
  if (typeof patch !== 'string') {
    throw new Error(`Inline suppression diff patch is unavailable for ${file}; refusing to publish tracking mutations.`);
  }

  let line = 0;
  for (const rawLine of patch.split('\n')) {
    if (rawLine.startsWith('@@ ')) {
      const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(rawLine);
      if (!hunk) {
        throw new Error(`Unable to parse inline suppression diff hunk for ${file}.`);
      }
      line = Number.parseInt(hunk[1], 10);
      continue;
    }
    if (rawLine.startsWith('+++')) {
      continue;
    }
    if (rawLine.startsWith('+')) {
      const content = rawLine.slice(1);
      if (MARKER_PATTERN.test(content)) {
        addSuppression(records, { file, line, content, context, headSHA });
      }
      line += 1;
      continue;
    }
    if (!rawLine.startsWith('-') && !rawLine.startsWith('\\')) {
      line += 1;
    }
  }
}

async function changedFileCount({ github, context, pull }) {
  if (Number.isInteger(pull.changed_files)) {
    return pull.changed_files;
  }
  const response = await github.rest.pulls.get({
    owner: context.repo.owner,
    repo: context.repo.repo,
    pull_number: pull.number,
  });
  if (!Number.isInteger(response.data.changed_files)) {
    throw new Error('Pull request changed file count is unavailable; refusing to publish tracking mutations.');
  }
  return response.data.changed_files;
}

function assertMutablePull(pull) {
  if (!pull?.number || !pull?.base?.sha || !pull?.head?.sha) {
    throw new Error('Pull request base and head SHAs are required to recompute inline suppression records.');
  }
  if (pull.state && pull.state !== 'open') {
    throw new Error(`Pull request #${pull.number} is ${pull.state}; refusing to publish tracking mutations.`);
  }
}

async function recomputeSuppressionRecords({ github, context }) {
  const pull = context.payload.pull_request;
  assertMutablePull(pull);
  const count = await changedFileCount({ github, context, pull });
  if (count > MAX_CHANGED_FILES) {
    throw new Error(`Pull request changed file count ${count} exceeds the ${MAX_CHANGED_FILES}-file trusted diff limit; refusing to publish tracking mutations.`);
  }

  const files = await github.paginate(github.rest.pulls.listFiles, {
    owner: context.repo.owner,
    repo: context.repo.repo,
    pull_number: pull.number,
    per_page: 100,
  });
  if (files.length !== count) {
    throw new Error(`Trusted inline suppression recomputation saw ${files.length} changed files but GitHub reports ${count}; refusing to publish tracking mutations.`);
  }

  const records = new Map();
  for (const file of files) {
    if (!['added', 'modified'].includes(file.status) || !SOURCE_FILE_PATTERN.test(file.filename)) {
      continue;
    }
    scanPatch(records, {
      file: file.filename,
      patch: file.patch,
      context,
      headSHA: pull.head.sha,
    });
  }
  if (records.size > MAX_RECORDS) {
    throw new Error(`Inline suppression records exceed the ${MAX_RECORDS}-record publication limit.`);
  }
  return records;
}

function trackingBody(record) {
  return [
    `<!-- lopper-inline-suppression:${record.fingerprint} -->`,
    '',
    '## Inline analysis suppression tracking',
    '',
    `- Location: \`${record.file}:${record.line}\``,
    `- Source: ${record.source}`,
    `- Rationale: ${record.rationale}`,
    `- Owner: ${record.owner}`,
    `- Removal condition: ${record.remove_when}`,
    '',
    'Source line:',
    '',
    '```text',
    escapeFence(record.content),
    '```',
  ].join('\n');
}

async function upsertTrackingIssue({ github, context, record }) {
  const marker = `lopper-inline-suppression:${record.fingerprint}`;
  const title = `ci: track inline suppression in ${record.file}:${record.line}`;
  const results = await github.rest.search.issuesAndPullRequests({
    q: `repo:${context.repo.owner}/${context.repo.repo} is:issue is:open ${marker}`,
    per_page: 2,
  });
  const existing = results.data.items.find((item) => !item.pull_request);
  if (existing) {
    await github.rest.issues.update({
      owner: context.repo.owner,
      repo: context.repo.repo,
      issue_number: existing.number,
      title,
      body: trackingBody(record),
    });
    return { action: 'updated', number: existing.number };
  }
  const created = await github.rest.issues.create({
    owner: context.repo.owner,
    repo: context.repo.repo,
    title,
    body: trackingBody(record),
  });
  return { action: 'opened', number: created.data.number };
}

async function trackInlineSuppressions({ github, context, core }) {
  const records = await recomputeSuppressionRecords({ github, context });
  if (records.size === 0) {
    core.info('No inline suppression records were produced.');
    return;
  }

  for (const record of records.values()) {
    const result = await upsertTrackingIssue({ github, context, record });
    const verb = result.action === 'updated' ? 'Updated' : 'Opened';
    core.info(`${verb} inline suppression tracking issue #${result.number} for ${record.file}:${record.line}.`);
  }
}

module.exports = trackInlineSuppressions;
module.exports.testables = {
  MAX_CHANGED_FILES,
  addSuppression,
  escapeFence,
  fingerprintFor,
  recomputeSuppressionRecords,
  scanPatch,
  trackingBody,
  validateFile,
};
