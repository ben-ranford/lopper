'use strict';

const { TextDecoder } = require('node:util');
const { testables: tracker } = require('./inline_suppression_tracker.js');

// These bounds are policy in the trusted checkout, never candidate inputs.
const MAX_ENTRIES = 10000;
const MAX_BLOB_BYTES = 2 * 1024 * 1024;
const MAX_TOTAL_BYTES = 32 * 1024 * 1024;
const MAX_BATCH_BLOBS = 50;
const MAX_BATCH_BYTES = 4 * 1024 * 1024;
const SHA = /^[a-f0-9]{40}$/;
const utf8 = new TextDecoder('utf-8', { fatal: true });
// Ordinary Markdown/legal documents are treated as documentation. Arbitrary
// text/golden/testdata paths do not establish inert fixture provenance.
const DOCUMENT_DATA = /(?:\.md|(?:^|\/)LICENSE)$/i;
const CONFIG_NAMES = new Set(['makefile', '.env', 'pyproject.toml', 'package.json']);
const CONFIG_WORDS = ['config', 'lint', 'sonar', 'coverage', 'golangci', 'gosec'];
const EXTRA_MARKER_PATTERNS = [
  /\bshellcheck[ \t]+(?:disable|source)\b/gi,
  /\b(?:istanbul|c8|v8)[ \t]+ignore\b/gi,
  /\bnode:coverage[ \t]+(?:ignore|disable)\b/gi,
  /\bpragma[ \t]*:[ \t]*no[ \t]+branch\b/gi,
  /\b(?:pylint|swiftlint|revive)[ \t]*:[ \t]*disable\b/gi,
  /\b(?:type|pyright|lint)[ \t]*:[ \t]*(?:file-)?ignore\b/gi,
  /\b(?:ruff|flake8)[ \t]*:[ \t]*noqa\b/gi,
  /\b(?:biome|deno-lint)-ignore\b/gi,
  /\b(?:oxlint-disable|ts-nocheck|noinspection)\b/gi,
  /\bgosec[ \t]*:[ \t]*disable\b/gi,
];
const SUSPECT_PATTERNS = [
  /\bno(?:lint|sec|sonar|qa)\b/i,
  /\b(?:eslint|oxlint)-disable\b/i,
  /\beslint[ \t\r\n]+/i,
  /\bts-(?:ignore|expect-error|nocheck)\b/i,
  /\bcoverage[ \t]*:[ \t]*ignore\b/i,
  /\bpragma[ \t]*:[ \t]*no[ \t]+(?:cover|branch)\b/i,
  /\bpragma[ \t]*warning[ \t]+disable\b/i,
  /\bpragma[ \t]*(?:GCC|clang)[ \t]+diagnostic[ \t]+ignored\b/i,
  /\bnullable[ \t]+disable\b/i,
  /\bSuppress(?:Warnings|Message)?\b/i,
  /#\s*\[\s*(?:allow|expect)\s*\(/i,
];
const LINE_MARKER = ['//', 'nolint'].join('');
const BLOCK_MARKER = ['/*', 'nolint'].join('');

function hasSuspectMarker(content) {
  return SUSPECT_PATTERNS.some((pattern) => pattern.test(content)) ||
    EXTRA_MARKER_PATTERNS.some((pattern) => content.search(pattern) !== -1);
}

function isConfigFile(path) {
  const name = path.split('/').at(-1).toLowerCase();
  return CONFIG_NAMES.has(name) || /^\.[^/]+rc(?:\.[^/]+)?$/.test(name) ||
    CONFIG_WORDS.some((word) => name.includes(word));
}

function hold(message) {
  const error = new Error(`Suppression audit held: ${message}`);
  error.queuePauseMessage = error.message;
  throw error;
}

function validPath(path) {
  return typeof path === 'string' && path.length > 0 && Buffer.byteLength(path) <= 1024 &&
    !/[\x00-\x1f\x7f\\]/.test(path) && !path.split('/').some((part) => !part || part === '.' || part === '..');
}

function assertSHA(sha, label) {
  if (!SHA.test(sha ?? '')) hold(`missing or malformed ${label} SHA`);
}

function validateTreeEntry(entry, entries) {
  if (!validPath(entry.path) || entries.has(entry.path)) hold('malformed or duplicate Git tree path');
  assertSHA(entry.sha, `tree entry ${entry.path}`);
  const regular = entry.type === 'blob' && ['100644', '100755'].includes(entry.mode);
  const directory = entry.type === 'tree' && entry.mode === '040000';
  if (!regular && !directory) hold(`unsupported Git object ${entry.path} (${entry.mode}); symlinks/submodules cannot be audited`);
  if (regular && (!Number.isSafeInteger(entry.size) || entry.size < 0 || entry.size > MAX_BLOB_BYTES)) {
    hold(`missing size or oversized blob ${entry.path}`);
  }
}

function validateTreeParents(entries) {
  for (const path of entries.keys()) {
    const parts = path.split('/');
    while (parts.length > 1) {
      parts.pop();
      if (entries.get(parts.join('/'))?.type !== 'tree') hold(`missing parent tree for ${path}`);
    }
  }
}

function validateTree(data, treeSHA) {
  if (data?.sha !== treeSHA || data.truncated !== false || !Array.isArray(data.tree)) {
    hold('complete exact-head Git tree is unavailable or truncated');
  }
  if (data.tree.length > MAX_ENTRIES) hold(`tree exceeds ${MAX_ENTRIES} entries`);
  const entries = new Map();
  let bytes = 0;
  for (const entry of data.tree) {
    validateTreeEntry(entry, entries);
    if (entry.type === 'blob') bytes += entry.size;
    entries.set(entry.path, entry);
  }
  if (bytes > MAX_TOTAL_BYTES) hold(`tree exceeds ${MAX_TOTAL_BYTES} content bytes`);
  validateTreeParents(entries);
  return entries;
}

function blobBatches(entries) {
  const unique = new Map(entries.filter((entry) => entry.type === 'blob').map((entry) => [entry.sha, entry]));
  const batches = [];
  let batch = [];
  let bytes = 0;
  for (const entry of unique.values()) {
    if (batch.length && (batch.length >= MAX_BATCH_BLOBS || bytes + entry.size > MAX_BATCH_BYTES)) {
      batches.push(batch);
      batch = [];
      bytes = 0;
    }
    batch.push(entry);
    bytes += entry.size;
  }
  if (batch.length) batches.push(batch);
  return batches;
}

function blobQuery(batch) {
  const fields = batch.map((entry, index) => `b${index}: object(oid: "${entry.sha}") {
    __typename ... on Blob { oid byteSize isBinary isTruncated text }
  }`).join('\n');
  return `query($owner: String!, $repo: String!) {
    repository(owner: $owner, name: $repo) { nameWithOwner ${fields} }
  }`;
}

function assertBlob(blob, entry) {
  if (blob?.__typename !== 'Blob' || blob.oid !== entry.sha || blob.byteSize !== entry.size) {
    hold(`missing or mismatched immutable blob ${entry.path}`);
  }
  if (blob.isTruncated !== false || typeof blob.isBinary !== 'boolean') {
    hold(`truncated or indeterminate blob ${entry.path}`);
  }
}

function textBlobBody(blob, entry) {
  if (typeof blob.text !== 'string') hold(`missing text for immutable blob ${entry.path}`);
  const body = Buffer.from(blob.text, 'utf8');
  if (body.length !== entry.size || utf8.decode(body) !== blob.text) {
    return undefined;
  }
  return body;
}

async function restBlobBody({ github, owner, repo, entry }) {
  const { data } = await github.rest.git.getBlob({ owner, repo, file_sha: entry.sha });
  if (data?.sha !== entry.sha || data.size !== entry.size || data.encoding !== 'base64' || typeof data.content !== 'string') {
    hold(`missing or mismatched REST blob ${entry.path}`);
  }
  if (data.content.length > MAX_BLOB_BYTES * 2) hold(`oversized REST blob response ${entry.path}`);
  const encoded = data.content.replaceAll('\r', '').replaceAll('\n', '');
  const body = Buffer.from(encoded, 'base64');
  if (body.length !== entry.size || body.toString('base64') !== encoded) hold(`invalid REST blob encoding ${entry.path}`);
  return body;
}

async function readBlobBody({ github, owner, repo, entry, blob }) {
  assertBlob(blob, entry);
  if (blob.isBinary && blob.text !== null) hold(`inconsistent binary blob ${entry.path}`);
  if (!blob.isBinary) {
    const body = textBlobBody(blob, entry);
    if (body !== undefined) return { body, apiRequests: 0 };
  }
  // Binary data and transformed text require the immutable base64 representation.
  const body = await restBlobBody({ github, owner, repo, entry });
  return { body, apiRequests: 1 };
}

async function fetchBlobs({ github, owner, repo, entries }) {
  const bodies = new Map();
  let apiRequests = 0;
  await blobBatches([...entries.values()]).reduce(async (previousBatch, batch) => {
    await previousBatch;
    const response = await github.graphql(blobQuery(batch), { owner, repo });
    apiRequests += 1;
    if (response?.errors?.length || response?.repository?.nameWithOwner?.toLowerCase() !== `${owner}/${repo}`.toLowerCase()) {
      hold('missing repository or GraphQL errors while reading immutable blobs');
    }
    await batch.reduce(async (previousBlob, entry, index) => {
      await previousBlob;
      const blob = response.repository[`b${index}`];
      const read = await readBlobBody({ github, owner, repo, entry, blob });
      bodies.set(entry.sha, read.body);
      apiRequests += read.apiRequests;
    }, Promise.resolve());
  }, Promise.resolve());
  const files = [...entries.values()].filter((entry) => entry.type === 'blob').map((entry) => ({ ...entry, body: bodies.get(entry.sha) }));
  return { files, apiRequests };
}

function shellFile(path, content) {
  return /\.(?:ba|k|z)?sh$/i.test(path) || path.startsWith('.githooks/') || /^#![^\n]*\b(?:ba|k|z)?sh\b/.test(content);
}

// Only a literal heredoc delivered directly to cat is proven inert here.
// Interpreter input, substitutions, pipes, expansions, and complex delimiters
// retain their marker text and therefore cannot silently become exemptions.
function maskLiteralHeredocs(content) {
  let delimiter;
  const lines = content.split('\n');
  const masked = lines.map((line) => {
    if (delimiter) {
      if (line === delimiter) delimiter = undefined;
      return '';
    }
    const match = /^[ \t]*cat[ \t]+<<[ \t]*(['"])([A-Za-z_]\w*)\1[ \t]*$/.exec(line);
    if (match) delimiter = match[2];
    return line;
  });
  if (delimiter) hold('unterminated literal heredoc containing audit input');
  return masked.join('\n');
}

function normalizeMarkers(content, path = '') {
  let normalized = /\.go$/i.test(path) ? content.replace(/#nosec\b/gi, 'NOSONAR') : content;
  for (const pattern of EXTRA_MARKER_PATTERNS) normalized = normalized.replace(pattern, 'nolint');
  normalized = normalized
    .replace(/\b(?:nolint|nosec|noqa|eslint-disable|ts-ignore|ts-expect-error)[\w-]*/gi, 'nolint')
    .replace(/\bnosonar[\w-]*/gi, 'NOSONAR')
    .replace(/@(?:[A-Za-z]+:)?(?:SuppressWarnings|SuppressMessage|Suppress)\b/g, LINE_MARKER)
    .replace(/\[(?:System\.Diagnostics\.CodeAnalysis\.)?SuppressMessage\b/g, BLOCK_MARKER)
    .replace(/#[ \t]*pragma[ \t]+warning[ \t]+disable\b/gi, LINE_MARKER)
    .replace(/#[ \t]*pragma[ \t]+(?:GCC|clang)[ \t]+diagnostic[ \t]+ignored\b/gi, LINE_MARKER)
    .replace(/#[ \t]*nullable[ \t]+disable\b/gi, LINE_MARKER)
    .replace(/#\s*\[\s*(?:allow|expect)\s*\(/g, `${BLOCK_MARKER}(`)
    .replace(/^([ \t]*)\*[ \t]+(?=nolint\b)/gm, '$1/* ');
  return normalized;
}

function skipSpace(content, start) {
  let cursor = start;
  while (cursor < content.length && /\s/.test(content[cursor])) cursor += 1;
  return cursor;
}

function disabledValueAt(content, start) {
  let cursor = skipSpace(content, start);
  if (content[cursor] === '[') cursor = skipSpace(content, cursor + 1);
  const value = ['"off"', "'off'", 'off', '0'].find((candidate) => content.startsWith(candidate, cursor));
  if (value === undefined) return false;
  const next = content[cursor + value.length];
  return next === undefined || /[,\s}\]]/.test(next);
}

function disabledRuleLine(content) {
  for (let cursor = 0; cursor < content.length; cursor += 1) {
    if (content[cursor] === ':' && disabledValueAt(content, cursor + 1)) {
      return content.slice(0, cursor).split('\n').length - 1;
    }
  }
  return -1;
}

function configDirective(line) {
  const key = line.split(/[=:]/, 1)[0].trim().toLowerCase();
  if (key.startsWith('sonar.') && (key.includes('exclusions') || key.startsWith('sonar.issue.ignore'))) return true;
  const patterns = [
    /\bexclude[-_](?:rules|lines|patterns)["']?[ \t]*[=:]/i,
    /\b(?:skip[-_](?:files|dirs)|disable[-_]error[-_]code)["']?[ \t]*[=:]/i,
    /\b(?:exclude|exclusions|omit|ignores?)["']?[ \t]*[=:]/i,
    /^[ \t]*disable[ \t]*[=:]/i,
    /\bGOSEC_EXCLUDE_RULES[ \t]*[?:+]?=[ \t]*\S/i,
  ];
  return patterns.some((pattern) => pattern.test(line));
}

function configFinding(path, content) {
  if (tracker.isSourceFile(path) && !/\.(?:ya?ml)$/i.test(path) && !/(?:eslint|biome|oxlint)[^/]*\.(?:[cm]?js|ts)$/i.test(path)) return undefined;
  if (!isConfigFile(path) && !/\.(?:properties|toml|ini|cfg)$/i.test(path)) return undefined;
  const lines = content.split('\n');
  const lintConfig = /(?:eslint|biome|oxlint)/i.test(path) || /"eslintConfig"\s*:/.test(content);
  const directiveLine = lines.findIndex((line) => !/^[ \t]*(?:#|;|\/\/)/.test(line) && configDirective(line));
  const ruleLine = lintConfig ? disabledRuleLine(content) : -1;
  const locations = [directiveLine, ruleLine].filter((line) => line >= 0);
  const index = locations.length ? Math.min(...locations) : -1;
  return index < 0 ? undefined : { file: path, line: index + 1, reason: 'analysis exclusion/disable configuration requires policy review; fixture exclusions are not automatically waived' };
}

function quoteEnd(content, start, delimiter, multiline, escapes) {
  let cursor = start + delimiter.length;
  while (cursor < content.length) {
    if (content.startsWith(delimiter, cursor)) return cursor + delimiter.length;
    if (!multiline && content[cursor] === '\n') return undefined;
    if (escapes && content[cursor] === '\\') cursor += 1;
    cursor += 1;
  }
  return undefined;
}

function literalEnd(content, start, path, shell) {
  const quote = content[start];
  if (!['"', "'", '`'].includes(quote)) return undefined;
  if (quote === '`' && shell) return undefined;
  const pythonTriple = /\.py$/i.test(path) && content.startsWith(quote.repeat(3), start);
  const delimiter = pythonTriple ? quote.repeat(3) : quote;
  const multiline = pythonTriple || shell || quote === '`';
  const escapes = !(shell && quote === "'") && !(quote === '`' && /\.go$/i.test(path));
  const end = quoteEnd(content, start, delimiter, multiline, escapes);
  if (end === undefined) return undefined;
  const literal = content.slice(start, end);
  if (hasSuspectMarker(literal)) auditLiteralExecution(content, start, literal, path, shell, quote);
  return end;
}

function interpreterCommand(prefix) {
  const tokens = prefix.trim().split(/\s+/);
  const options = [];
  while (tokens.at(-1)?.startsWith('-')) options.push(tokens.pop());
  const command = (tokens.pop() ?? '').split('/').at(-1);
  const interpreters = [
    { command: /^(?:ba|k|z)?sh$/, flags: 'c', long: [] },
    { command: /^python[\d.]*$/, flags: 'c', long: [] },
    { command: /^ruby$/, flags: 'e', long: [] },
    { command: /^node$/, flags: 'ep', long: ['--eval', '--print'] },
  ];
  return interpreters.some((interpreter) => interpreter.command.test(command) &&
    options.some((option) => interpreter.long.includes(option) ||
      (/^-[A-Za-z]+$/.test(option) && [...interpreter.flags].some((flag) => option.includes(flag)))));
}

function auditLiteralExecution(content, start, literal, path, shell, quote) {
  const prefix = content.slice(0, start).split('\n').at(-1);
  if (shell && interpreterCommand(prefix)) hold(`suppression in executable interpreter string in ${path}:${content.slice(0, start).split('\n').length}`);
  if (shell && quote === '"' && /\$\(|`/.test(literal)) hold(`possible suppression in executable string interpolation in ${path}; syntax requires review`);
  if (quote !== '`' || /\.go$/i.test(path) || !literal.includes('${')) return;
  const matches = [...literal.matchAll(/\$\{([^{}]*)\}/g)];
  const residue = literal.replace(/\$\{[^{}]*\}/g, '');
  const active = matches.some((match) => supplementalMarkers(match[1], 'expression.js', false).length > 0);
  if (residue.includes('${') || active) hold(`possible suppression in executable string interpolation in ${path}; syntax requires review`);
}

function inlineRuleColons(content) {
  const colons = [];
  let depth = 0;
  let cursor = 0;
  while (cursor < content.length) {
    const character = content[cursor];
    if (['"', "'"].includes(character)) {
      cursor = quoteEnd(content, cursor, character, true, true) ?? content.length;
      continue;
    }
    if (depth === 0 && content.startsWith('--', cursor)) break;
    if (character === ':' && depth === 0) colons.push(cursor);
    if ('[{'.includes(character)) depth += 1;
    if (']}'.includes(character)) depth -= 1;
    cursor += 1;
  }
  return colons;
}

function inlineESLintDisable(comment) {
  const prefix = /^\/\*+\s*eslint\s+/.exec(comment);
  if (!prefix) return false;
  const content = comment.slice(prefix[0].length, -2);
  return inlineRuleColons(content).some((cursor) => disabledValueAt(content, cursor + 1));
}

function directiveInComment(comment) {
  // Sonar recognizes its marker anywhere in a comment, including prose.
  if (/\bNOSONAR\b/i.test(comment) || inlineESLintDisable(comment)) return true;
  const normalized = normalizeMarkers(comment);
  return /^(?:\/\/|\/\*+|#)\s*@?nolint\b|^[ \t]*\*[ \t]*nolint\b/m.test(normalized);
}

function commentAt(content, cursor, prefixes, shell) {
  const prefix = prefixes.find((candidate) => content.startsWith(candidate, cursor));
  if (!prefix) return undefined;
  if (prefix === '#' && shell && cursor > 0 && !/[\s;|&()]/.test(content[cursor - 1])) return undefined;
  const terminator = prefix === '/*' ? '*/' : '\n';
  const index = content.indexOf(terminator, cursor + prefix.length);
  if (index < 0) return content.length;
  return prefix === '/*' ? index + 2 : index;
}

// Supplement the inherited diff lexer's quote heuristic with a bounded walk:
// quotes inside comments cannot start strings, and ordinary JS/Go quotes do
// not cross physical lines. Only closed literals are accepted as data.
function supplementalMarkers(content, path, shell) {
  const normalized = normalizeMarkers(content, path);
  const prefixes = tracker.commentPrefixesFor(shell ? `${path}.sh` : path);
  const lines = [];
  let cursor = 0;
  while (cursor < normalized.length) {
    const commentEnd = commentAt(normalized, cursor, prefixes, shell);
    if (commentEnd !== undefined) {
      const comment = normalized.slice(cursor, commentEnd);
      if (directiveInComment(comment)) lines.push(normalized.slice(0, cursor).split('\n').length);
      cursor = commentEnd;
      continue;
    }
    cursor = literalEnd(normalized, cursor, path, shell) ?? cursor + 1;
  }
  return lines;
}

function supportedBinaryData(path, body) {
  const signatures = [
    [/\.png$/i, Buffer.from('89504e470d0a1a0a', 'hex')],
    [/\.gif$/i, Buffer.from('GIF87a')],
    [/\.gif$/i, Buffer.from('GIF89a')],
    [/\.jpe?g$/i, Buffer.from([0xff, 0xd8, 0xff])],
    [/\.ico$/i, Buffer.from([0, 0, 1, 0])],
    [/\.woff$/i, Buffer.from('wOFF')],
    [/\.woff2$/i, Buffer.from('wOF2')],
    [/\.ttf$/i, Buffer.from([0, 1, 0, 0])],
  ];
  if (!signatures.some(([extension, signature]) => extension.test(path) && body.subarray(0, signature.length).equals(signature))) return false;
  if (body.includes(0)) return true;
  try {
    utf8.decode(body);
    return false;
  } catch {
    return true;
  }
}

function scanFile(entry) {
  const { path, body, mode } = entry;
  if (supportedBinaryData(path, body) && mode !== '100755') return [];
  let content;
  try {
    content = utf8.decode(body);
  } catch {
    return hold(`unsupported non-UTF-8 content ${path}`);
  }
  if (content.includes('\0')) hold(`unsupported NUL-containing content ${path}`);
  const shell = shellFile(path, content);
  if (DOCUMENT_DATA.test(path) && mode !== '100755' && !shell) return [];
  const config = configFinding(path, content);
  if (config) return [config];
  if (!hasSuspectMarker(content)) return [];
  const source = tracker.isSourceFile(path) || shell;
  if (!source) return [{ file: path, line: 1, reason: 'possible suppression in unsupported syntax requires review' }];
  const prepared = shell ? maskLiteralHeredocs(content) : content;
  const normalized = normalizeMarkers(prepared, path);
  const scannerPath = shell ? `${path}.sh` : path;
  const lines = new Set([...tracker.scanFullFileMarkers(normalized, scannerPath), ...supplementalMarkers(prepared, path, shell)]);
  return [...lines].map((line) => ({ file: path, line, reason: 'active or ambiguous analysis suppression (tracking metadata does not waive it)' }));
}

async function verifySuppressions({ github, owner, repo, headSHA, trustedPolicySHA }) {
  assertSHA(headSHA, 'candidate head');
  assertSHA(trustedPolicySHA, 'trusted policy');
  const commit = await github.rest.git.getCommit({ owner, repo, commit_sha: headSHA });
  if (commit.data?.sha !== headSHA) hold('commit lookup did not return the exact candidate head');
  const treeSHA = commit.data?.tree?.sha;
  assertSHA(treeSHA, 'candidate tree');
  const tree = await github.rest.git.getTree({ owner, repo, tree_sha: treeSHA, recursive: '1' });
  const entries = validateTree(tree.data, treeSHA);
  const { files, apiRequests } = await fetchBlobs({ github, owner, repo, entries });
  const findings = files.flatMap(scanFile);
  if (findings.length) {
    const detail = findings.slice(0, 8).map((finding) => `${finding.file}:${finding.line}: ${finding.reason}`).join('; ');
    hold(`${findings.length} finding(s) at ${headSHA}: ${detail}`);
  }
  return { headSHA, trustedPolicySHA, treeSHA, count: 0, filesScanned: files.length, findings: [], apiRequests: apiRequests + 2 };
}

module.exports = { verifySuppressions };
module.exports.testables = { validateTree, fetchBlobs, blobBatches, scanFile, MAX_BLOB_BYTES, MAX_TOTAL_BYTES, MAX_BATCH_BLOBS, MAX_BATCH_BYTES };
