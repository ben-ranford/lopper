'use strict';

const crypto = require('node:crypto');

const MAX_CHANGED_FILES = 3000;
const MAX_RECORDS = 100;
const COMMENT_PREFIXES = ['//', '/*', '#'];
const NO_MARKERS = new Set(['nosec', 'nosonar', 'nolint', 'noqa']);
const ESLINT_MARKERS = new Set(['eslint-disable', 'eslint-disable-next-line', 'eslint-disable-line']);
const TS_MARKERS = new Set(['ts-ignore', 'ts-expect-error']);
const TRACKED_FILE_STATUSES = new Set(['added', 'modified', 'renamed']);
const TRUSTED_TRACKER_LOGINS = new Set(['github-actions[bot]']);
const SOURCE_EXTENSIONS = new Set([
  'bash',
  'c',
  'cc',
  'cjs',
  'cpp',
  'cs',
  'cxx',
  'go',
  'h',
  'hh',
  'hpp',
  'java',
  'js',
  'jsx',
  'ksh',
  'kt',
  'kts',
  'mjs',
  'php',
  'py',
  'rb',
  'rs',
  'sh',
  'swift',
  'ts',
  'tsx',
  'yaml',
  'yml',
  'zsh',
]);

function isWhitespace(char) {
  return char === ' ' || char === '\t' || char === '\n' || char === '\r' || char === '\f' || char === '\v';
}

function isMetadataBoundary(char) {
  return char === undefined || isWhitespace(char) || char === ';' || char === ',';
}

function isCommentBoundary(char) {
  return char === undefined || isWhitespace(char);
}

function isMarkerBoundary(char) {
  if (char === undefined) {
    return true;
  }
  const code = char.codePointAt(0);
  const digit = code >= 48 && code <= 57;
  const upper = code >= 65 && code <= 90;
  const lower = code >= 97 && code <= 122;
  return !(digit || upper || lower || char === '_' || char === '-');
}

function metadataValue(content, keys) {
  const lowerContent = content.toLowerCase();
  for (let index = 0; index < content.length; index += 1) {
    if (!isMetadataBoundary(content[index - 1])) {
      continue;
    }
    const key = keys.find((candidate) => lowerContent.startsWith(candidate, index));
    if (!key) {
      continue;
    }
    let cursor = index + key.length;
    while (isWhitespace(content[cursor])) {
      cursor += 1;
    }
    if (content[cursor] !== ':' && content[cursor] !== '=') {
      continue;
    }
    cursor += 1;
    while (isWhitespace(content[cursor])) {
      cursor += 1;
    }
    let end = cursor;
    while (end < content.length && content[end] !== ';') {
      end += 1;
    }
    return content.slice(cursor, end).trim();
  }
  return '';
}

function fingerprintFor(file, content) {
  return crypto.createHash('sha256').update(`${file}\n${content}`).digest('hex');
}

function validateString(value, label, maxBytes) {
  if (
    typeof value !== 'string' ||
    value.length === 0 ||
    Buffer.byteLength(value, 'utf8') > maxBytes ||
    hasDisallowedControl(value)
  ) {
    throw new TypeError(`Invalid inline suppression ${label}.`);
  }
  return value;
}

function validateFile(file) {
  validateString(file, 'file', 512);
  if (file.startsWith('/') || file.split('/').includes('..')) {
    throw new TypeError('Invalid inline suppression file path.');
  }
  return file;
}

function hasDisallowedControl(value) {
  for (const char of value) {
    const code = char.codePointAt(0);
    if (code !== 9 && (code < 32 || code === 127)) {
      return true;
    }
  }
  return false;
}

function fileExtension(file) {
  const name = file.split('/').pop() || '';
  const dot = name.lastIndexOf('.');
  return dot === -1 ? '' : name.slice(dot + 1).toLowerCase();
}

function isSourceFile(file) {
  return file.startsWith('.githooks/') || SOURCE_EXTENSIONS.has(fileExtension(file));
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
    throw new RangeError('Invalid inline suppression line.');
  }
  validateString(content, 'content', 4096);

  const rationale = validateString(metadataValue(content, ['rationale', 'reason']), 'rationale', 1024);
  const owner = validateString(metadataValue(content, ['owner']), 'owner', 256);
  const removeWhen = validateString(
    metadataValue(content, ['remove-when', 'removal-condition', 'removal']),
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

function markerAt(content, index, marker) {
  return content.startsWith(marker, index) && isMarkerBoundary(content[index + marker.length]);
}

function spacedMarkerAt(content, index, first, second) {
  if (!content.startsWith(first, index)) {
    return false;
  }
  let cursor = index + first.length;
  if (!isWhitespace(content[cursor])) {
    return false;
  }
  while (isWhitespace(content[cursor])) {
    cursor += 1;
  }
  return markerAt(content, cursor, second);
}

function colonMarkerAt(content, index, label, value) {
  if (!content.startsWith(label, index)) {
    return false;
  }
  let cursor = index + label.length;
  while (isWhitespace(content[cursor])) {
    cursor += 1;
  }
  if (content[cursor] !== ':') {
    return false;
  }
  cursor += 1;
  while (isWhitespace(content[cursor])) {
    cursor += 1;
  }
  if (value === 'no cover') {
    return spacedMarkerAt(content, cursor, 'no', 'cover');
  }
  return markerAt(content, cursor, value);
}

function hasNamedMarker(content, index, markers) {
  for (const marker of markers) {
    if (markerAt(content, index, marker)) {
      return true;
    }
  }
  return false;
}

function hasMarkerAfterCommentPrefix(content, index) {
  let cursor = index;
  while (isWhitespace(content[cursor])) {
    cursor += 1;
  }
  if (content[cursor] === '@') {
    cursor += 1;
  }

  const lowerContent = content.toLowerCase();
  return (
    hasNamedMarker(lowerContent, cursor, NO_MARKERS) ||
    hasNamedMarker(lowerContent, cursor, ESLINT_MARKERS) ||
    hasNamedMarker(lowerContent, cursor, TS_MARKERS) ||
    colonMarkerAt(lowerContent, cursor, 'coverage', 'ignore') ||
    colonMarkerAt(lowerContent, cursor, 'pragma', 'no cover')
  );
}

function markerStartAfterPrefix(content, index, prefix) {
  let cursor = index + prefix.length;
  if (prefix === '/*') {
    while (content[cursor] === '*') {
      cursor += 1;
    }
  }
  return cursor;
}

function hasInlineSuppressionMarker(content) {
  for (let index = 0; index < content.length; index += 1) {
    if (!isCommentBoundary(content[index - 1])) {
      continue;
    }
    const prefix = COMMENT_PREFIXES.find((candidate) => content.startsWith(candidate, index));
    if (prefix && hasMarkerAfterCommentPrefix(content, markerStartAfterPrefix(content, index, prefix))) {
      return true;
    }
  }
  return false;
}

function parseHunkStart(rawLine, file) {
  const plusIndex = rawLine.indexOf(' +');
  if (!rawLine.startsWith('@@ ') || plusIndex === -1) {
    throw new SyntaxError(`Unable to parse inline suppression diff hunk for ${file}.`);
  }
  let cursor = plusIndex + 2;
  const start = cursor;
  while (rawLine[cursor] >= '0' && rawLine[cursor] <= '9') {
    cursor += 1;
  }
  if (cursor === start || (rawLine[cursor] !== ',' && rawLine[cursor] !== ' ')) {
    throw new SyntaxError(`Unable to parse inline suppression diff hunk for ${file}.`);
  }
  return Number.parseInt(rawLine.slice(start, cursor), 10);
}

function scanPatch(records, { file, patch, context, headSHA }) {
  validateFile(file);
  if (typeof patch !== 'string') {
    throw new TypeError(`Inline suppression diff patch is unavailable for ${file}; refusing to publish tracking mutations.`);
  }

  let line = 0;
  for (const rawLine of patch.split('\n')) {
    if (rawLine.startsWith('@@ ')) {
      line = parseHunkStart(rawLine, file);
      continue;
    }
    if (rawLine.startsWith('+++')) {
      continue;
    }
    if (rawLine.startsWith('+')) {
      const content = rawLine.slice(1);
      if (hasInlineSuppressionMarker(content)) {
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
    throw new TypeError('Pull request changed file count is unavailable; refusing to publish tracking mutations.');
  }
  return response.data.changed_files;
}

async function fetchCurrentPull({ github, context, pull }) {
  const response = await github.rest.pulls.get({
    owner: context.repo.owner,
    repo: context.repo.repo,
    pull_number: pull.number,
  });
  return response.data;
}

function assertMutablePull(pull) {
  if (!pull?.number || !pull?.base?.sha || !pull?.head?.sha) {
    throw new TypeError('Pull request base and head SHAs are required to recompute inline suppression records.');
  }
  if (pull.state && pull.state !== 'open') {
    throw new RangeError(`Pull request #${pull.number} is ${pull.state}; refusing to publish tracking mutations.`);
  }
}

function assertCurrentHeadMatchesEvent({ eventPull, currentPull }) {
  if (!currentPull?.head?.sha) {
    throw new TypeError('Current pull request head SHA is unavailable; refusing to publish tracking mutations.');
  }
  if (currentPull.head.sha !== eventPull.head.sha) {
    throw new RangeError(
      `Pull request #${eventPull.number} head changed from event SHA ${eventPull.head.sha} to ${currentPull.head.sha}; refusing to use stale inline suppression diff records.`,
    );
  }
}

function assertTrustedFileCount(count) {
  if (count > MAX_CHANGED_FILES) {
    throw new RangeError(`Pull request changed file count ${count} exceeds the ${MAX_CHANGED_FILES}-file trusted diff limit; refusing to publish tracking mutations.`);
  }
}

async function listChangedFiles({ github, context, pull, expectedCount }) {
  const files = await github.paginate(github.rest.pulls.listFiles, {
    owner: context.repo.owner,
    repo: context.repo.repo,
    pull_number: pull.number,
    per_page: 100,
  });
  if (files.length !== expectedCount) {
    throw new RangeError(`Trusted inline suppression recomputation saw ${files.length} changed files but GitHub reports ${expectedCount}; refusing to publish tracking mutations.`);
  }
  return files;
}

function collectSuppressionRecords({ files, context, pull }) {
  const records = new Map();
  for (const file of files) {
    if (!TRACKED_FILE_STATUSES.has(file.status) || !isSourceFile(file.filename)) {
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
    throw new RangeError(`Inline suppression records exceed the ${MAX_RECORDS}-record publication limit.`);
  }
  return records;
}

async function recomputeSuppressionRecords({ github, context }) {
  const eventPull = context.payload.pull_request;
  assertMutablePull(eventPull);
  const pull = await fetchCurrentPull({ github, context, pull: eventPull });
  assertMutablePull(pull);
  assertCurrentHeadMatchesEvent({ eventPull, currentPull: pull });
  const count = await changedFileCount({ github, context, pull });
  assertTrustedFileCount(count);
  const files = await listChangedFiles({ github, context, pull, expectedCount: count });
  const refreshedPull = await fetchCurrentPull({ github, context, pull: eventPull });
  assertMutablePull(refreshedPull);
  assertCurrentHeadMatchesEvent({ eventPull, currentPull: refreshedPull });
  if (refreshedPull.changed_files !== count) {
    throw new RangeError('Pull request changed file count drifted while recomputing inline suppression records; refusing to publish tracking mutations.');
  }
  return collectSuppressionRecords({ files, context, pull });
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

function issueBodyIncludesMarker(issue, marker) {
  return typeof issue.body === 'string' && issue.body.includes(`<!-- ${marker} -->`);
}

function issueHasTrustedTrackerOwner(issue) {
  return issue.user?.type === 'Bot' && TRUSTED_TRACKER_LOGINS.has(issue.user?.login);
}

function isTrustedTrackingIssue(issue, marker) {
  return (
    issue &&
    !issue.pull_request &&
    Number.isInteger(issue.number) &&
    issueHasTrustedTrackerOwner(issue) &&
    issueBodyIncludesMarker(issue, marker)
  );
}

async function upsertTrackingIssue({ github, context, record }) {
  const marker = `lopper-inline-suppression:${record.fingerprint}`;
  const title = `ci: track inline suppression in ${record.file}:${record.line}`;
  const searchTrackingIssue = async () => github.rest.search.issuesAndPullRequests({
    q: `repo:${context.repo.owner}/${context.repo.repo} is:issue is:open ${marker}`,
    per_page: 2,
  });
  const findTrustedExisting = async () => {
    const results = await searchTrackingIssue();
    return results.data.items.find((item) => isTrustedTrackingIssue(item, marker));
  };
  const existing = await findTrustedExisting();
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
  let created;
  try {
    created = await github.rest.issues.create({
      owner: context.repo.owner,
      repo: context.repo.repo,
      title,
      body: trackingBody(record),
    });
  } catch (error) {
    const concurrentExisting = await findTrustedExisting();
    if (concurrentExisting) {
      await github.rest.issues.update({
        owner: context.repo.owner,
        repo: context.repo.repo,
        issue_number: concurrentExisting.number,
        title,
        body: trackingBody(record),
      });
      return { action: 'updated', number: concurrentExisting.number };
    }
    throw error;
  }
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
  hasInlineSuppressionMarker,
  isSourceFile,
  isTrustedTrackingIssue,
  metadataValue,
  parseHunkStart,
  recomputeSuppressionRecords,
  scanPatch,
  trackingBody,
  validateFile,
};
