'use strict';

const crypto = require('node:crypto');
const { gunzipSync } = require('node:zlib');
const { TextDecoder } = require('node:util');
const { testables: tracker } = require('./inline_suppression_tracker.js');

// These bounds are policy in the trusted checkout, never candidate inputs.
const MAX_ENTRIES = 10000;
const MAX_BLOB_BYTES = 2 * 1024 * 1024;
const MAX_TOTAL_BYTES = 32 * 1024 * 1024;
const MAX_ARCHIVE_BYTES = 40 * 1024 * 1024;
const MAX_TAR_BYTES = 64 * 1024 * 1024;
const SHA = /^[a-f0-9]{40}$/;
const utf8 = new TextDecoder('utf-8', { fatal: true });
// Ordinary Markdown/legal documents are treated as documentation. Arbitrary
// text/golden/testdata paths do not establish inert fixture provenance.
const DOCUMENT_DATA = /(?:\.md|(?:^|\/)LICENSE)$/i;
const CONFIG_FILE = /(?:^|\/)(?:\.[^/]+rc(?:\.[^/]+)?|[^/]*(?:config|lint|sonar|coverage|golangci|gosec)[^/]*|Makefile|\.env|pyproject\.toml|package\.json)$/i;
const SUSPECT_MARKER = /\b(?:no(?:lint|sec|sonar|qa)|(?:eslint|oxlint)-disable|ts-(?:ignore|expect-error|nocheck)|shellcheck[ \t]+(?:disable|source)|(?:istanbul|c8|v8)[ \t]+ignore|node:coverage[ \t]+(?:ignore|disable)|coverage[ \t]*:[ \t]*ignore|pragma[ \t]*(?::[ \t]*no[ \t]+(?:cover|branch)|warning[ \t]+disable|(?:GCC|clang)[ \t]+diagnostic[ \t]+ignored)|nullable[ \t]+disable|(?:ruff|flake8)[ \t]*:[ \t]*noqa|(?:pylint|swiftlint|revive)[ \t]*:[ \t]*disable|(?:type|pyright|lint)[ \t]*:[ \t]*(?:file-)?ignore|(?:biome|deno-lint)-ignore|Suppress(?:Warnings|Message)?|noinspection)\b|#\s*\[\s*(?:allow|expect)\s*\(/i;

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

function tarString(buffer) {
  const zero = buffer.indexOf(0);
  const value = zero < 0 ? buffer : buffer.subarray(0, zero);
  if (zero >= 0 && buffer.subarray(zero).some((byte) => byte !== 0)) hold('malformed tar string');
  try {
    return utf8.decode(value);
  } catch {
    return hold('tar path is not UTF-8');
  }
}

function tarNumber(buffer) {
  const raw = buffer.toString('ascii').replace(/\0.*$/, '').trim();
  if (!/^[0-7]+$/.test(raw)) hold('unsupported tar numeric encoding');
  const value = Number.parseInt(raw, 8);
  if (!Number.isSafeInteger(value)) hold('oversized tar number');
  return value;
}

function readHeader(header) {
  const checksum = tarNumber(header.subarray(148, 156));
  const actual = header.reduce((sum, byte, index) => sum + (index >= 148 && index < 156 ? 32 : byte), 0);
  if (checksum !== actual) hold('tar header checksum mismatch');
  if (header.subarray(257, 263).toString('ascii') !== 'ustar\0') hold('unsupported tar format');
  const prefix = tarString(header.subarray(345, 500));
  const name = tarString(header.subarray(0, 100));
  return {
    path: prefix ? `${prefix}/${name}` : name,
    size: tarNumber(header.subarray(124, 136)),
    type: header[156] === 0 ? '0' : String.fromCharCode(header[156]),
    link: tarString(header.subarray(157, 257)),
  };
}

function readPax(body, global, headSHA) {
  if (body.length > 65536) hold('oversized tar metadata');
  const fields = new Map();
  let offset = 0;
  while (offset < body.length) {
    const space = body.indexOf(32, offset);
    const lengthText = body.subarray(offset, space).toString('ascii');
    if (space < offset || !/^[1-9][0-9]*$/.test(lengthText)) hold('malformed tar metadata record');
    const length = Number(lengthText);
    if (!Number.isSafeInteger(length) || length <= space - offset + 1 || offset + length > body.length || body[offset + length - 1] !== 10) {
      hold('truncated tar metadata record');
    }
    const record = utf8.decode(body.subarray(space + 1, offset + length - 1));
    const equals = record.indexOf('=');
    const key = record.slice(0, equals);
    const value = record.slice(equals + 1);
    const allowed = global ? ['comment'] : ['path', 'mtime', 'atime', 'ctime'];
    if (equals < 1 || !allowed.includes(key) || fields.has(key)) hold('unsupported or duplicate tar metadata');
    if (key === 'comment' && value !== headSHA) hold('archive commit differs from audited head');
    if (key !== 'path' && key !== 'comment' && !/^-?[0-9]+(?:\.[0-9]+)?$/.test(value)) hold('invalid tar timestamp');
    fields.set(key, value);
    offset += length;
  }
  return fields;
}

function archiveBuffer(data) {
  if (Buffer.isBuffer(data)) return data;
  if (data instanceof ArrayBuffer) return Buffer.from(data);
  if (data instanceof Uint8Array) return Buffer.from(data.buffer, data.byteOffset, data.byteLength);
  return hold('archive response is missing binary data');
}

function inflateArchive(data) {
  const compressed = archiveBuffer(data);
  if (compressed.length > MAX_ARCHIVE_BYTES) hold('compressed archive exceeds audit limit');
  try {
    return gunzipSync(compressed, { maxOutputLength: MAX_TAR_BYTES });
  } catch {
    return hold('invalid gzip archive or decompressed archive exceeds audit limit');
  }
}

function verifyArchiveFile(path, body, entries, seen, files) {
  const entry = entries.get(path);
  if (!entry || entry.type !== 'blob' || seen.has(path)) hold(`unexpected or duplicate archive file ${path}`);
  const digest = crypto.createHash('sha1').update(`blob ${body.length}\0`).update(body).digest('hex');
  if (entry.size !== body.length || entry.sha !== digest) hold(`incomplete or mismatched immutable blob ${path}`);
  seen.add(path);
  files.push({ ...entry, body });
}

function archiveItem(tar, offset) {
  const item = readHeader(tar.subarray(offset, offset + 512));
  const end = offset + 512 + item.size;
  if (end > tar.length || item.size > MAX_BLOB_BYTES) hold('truncated or oversized archive entry');
  const next = offset + 512 + Math.ceil(item.size / 512) * 512;
  return { ...item, body: tar.subarray(offset + 512, end), next };
}

function acceptMetadata(item, state, headSHA) {
  if (state.pax || (item.type === 'g' && (state.globalSeen || state.prefix))) hold('misplaced tar metadata');
  const fields = readPax(item.body, item.type === 'g', headSHA);
  if (item.type === 'g') state.globalSeen = true;
  else state.pax = fields;
}

function acceptArchiveItem(item, state, entries, headSHA) {
  if (item.link) hold('tar links are unsupported');
  if (item.type === 'g' || item.type === 'x') {
    acceptMetadata(item, state, headSHA);
    return;
  }
  const archivePath = (state.pax?.get('path') ?? item.path).replace(/\/$/, '');
  state.pax = undefined;
  if (!validPath(archivePath)) hold('unsafe archive path');
  state.prefix ??= archivePath.split('/')[0];
  if (archivePath !== state.prefix && !archivePath.startsWith(`${state.prefix}/`)) hold('inconsistent archive root');
  const path = archivePath.slice(state.prefix.length + 1);
  if (item.type === '5') {
    if (item.body.length || (path && entries.get(path)?.type !== 'tree')) hold(`unexpected archive directory ${archivePath}`);
  } else if (item.type === '0' && path) {
    verifyArchiveFile(path, item.body, entries, state.seen, state.files);
  } else {
    hold(`unsupported archive entry type ${item.type}`);
  }
}

function verifyArchiveEnding(tar, offset, state, entries) {
  if (tar.length - offset < 1024 || tar.subarray(offset).some((byte) => byte !== 0) || state.pax) hold('malformed tar ending');
  const blobCount = [...entries.values()].filter((entry) => entry.type === 'blob').length;
  if (state.files.length !== blobCount) hold('archive omits tracked files');
}

function readArchive(data, entries, headSHA) {
  const tar = inflateArchive(data);
  const state = { seen: new Set(), files: [], globalSeen: false };
  let offset = 0;
  let count = 0;
  while (offset + 512 <= tar.length) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) {
      verifyArchiveEnding(tar, offset, state, entries);
      return state.files;
    }
    count += 1;
    if (count > MAX_ENTRIES * 2 + 2) hold('too many archive entries');
    const item = archiveItem(tar, offset);
    offset = item.next;
    acceptArchiveItem(item, state, entries, headSHA);
  }
  return hold('archive is missing its complete ending');
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
    const match = /^[ \t]*cat[ \t]+<<[ \t]*(['"])([A-Za-z_][A-Za-z0-9_]*)\1[ \t]*$/.exec(line);
    if (match) delimiter = match[2];
    return line;
  });
  if (delimiter) hold('unterminated literal heredoc containing audit input');
  return masked.join('\n');
}

function normalizeMarkers(content) {
  return content
    .replace(/\b(?:shellcheck[ \t]+(?:disable|source)|(?:istanbul|c8|v8)[ \t]+ignore|node:coverage[ \t]+(?:ignore|disable)|pragma[ \t]*:[ \t]*no[ \t]+branch|(?:pylint|swiftlint|revive)[ \t]*:[ \t]*disable|(?:type|pyright|lint)[ \t]*:[ \t]*(?:file-)?ignore|(?:ruff|flake8)[ \t]*:[ \t]*noqa|(?:biome|deno-lint)-ignore|oxlint-disable|ts-nocheck|noinspection)\b/gi, 'nolint')
    .replace(/\b(?:nolint|nosec|noqa|eslint-disable|ts-ignore|ts-expect-error)[\w-]*/gi, 'nolint')
    .replace(/\bnosonar[\w-]*/gi, 'NOSONAR')
    .replace(/@(?:[A-Za-z]+:)?(?:SuppressWarnings|SuppressMessage|Suppress)\b/g, '//nolint')
    .replace(/\[(?:System\.Diagnostics\.CodeAnalysis\.)?SuppressMessage\b/g, '/*nolint')
    .replace(/#[ \t]*(?:pragma[ \t]+(?:warning[ \t]+disable|(?:GCC|clang)[ \t]+diagnostic[ \t]+ignored)|nullable[ \t]+disable)\b/gi, '//nolint')
    .replace(/#\s*\[\s*(?:allow|expect)\s*\(/g, '/*nolint(')
    .replace(/^(\s*)\*[ \t]+(?=nolint\b)/gm, '$1/* ');
}

function configFinding(path, content) {
  if (tracker.isSourceFile(path) && !/\.(?:ya?ml)$/i.test(path) && !/(?:eslint|biome|oxlint)[^/]*\.(?:[cm]?js|ts)$/i.test(path)) return undefined;
  if (!CONFIG_FILE.test(path) && !/\.(?:properties|toml|ini|cfg)$/i.test(path)) return undefined;
  const lines = content.split('\n');
  const pattern = /(?:sonar\.(?:[^=\s]*exclusions|issue\.ignore)[^=\s]*[ \t]*[=:]|\b(?:exclude[-_]rules|exclude[-_]lines|exclude[-_]patterns|skip[-_](?:files|dirs)|disable[-_]error[-_]code|exclusions|omit|ignores)["']?[ \t]*[=:]|^[ \t]*disable[ \t]*=|\bGOSEC_EXCLUDE_RULES[ \t]*[?:+]?=[ \t]*\S)/i;
  const lintConfig = /(?:eslint|biome|oxlint)/i.test(path) || /"eslintConfig"\s*:/.test(content);
  const disabledRule = /["'][^"'\n]+["'][ \t]*:[ \t]*(?:["']off["']|0)(?:[,\s}\]]|$)/i;
  const index = lines.findIndex((line) => !/^[ \t]*(?:#|;|\/\/)/.test(line) &&
    (pattern.test(line) || (lintConfig && disabledRule.test(line))));
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
  if (SUSPECT_MARKER.test(literal)) auditLiteralExecution(content, start, literal, path, shell, quote);
  return end;
}

function auditLiteralExecution(content, start, literal, path, shell, quote) {
  const prefix = content.slice(0, start).split('\n').at(-1);
  const interpreter = /\b(?:(?:ba|k|z)?sh|python[0-9.]*|ruby)[ \t]+-c[ \t]*$|\bnode[ \t]+-[ep][ \t]*$/;
  if (shell && interpreter.test(prefix)) hold(`suppression in executable interpreter string in ${path}:${content.slice(0, start).split('\n').length}`);
  if (shell && quote === '"' && /\$\(|`/.test(literal)) hold(`possible suppression in executable string interpolation in ${path}; syntax requires review`);
  if (quote !== '`' || /\.go$/i.test(path) || !literal.includes('${')) return;
  const matches = [...literal.matchAll(/\$\{([^{}]*)\}/g)];
  const residue = literal.replace(/\$\{[^{}]*\}/g, '');
  const active = matches.some((match) => supplementalMarkers(match[1], 'expression.js', false).length > 0);
  if (residue.includes('${') || active) hold(`possible suppression in executable string interpolation in ${path}; syntax requires review`);
}

function directiveInComment(comment) {
  // Sonar recognizes its marker anywhere in a comment, including prose.
  if (/\bNOSONAR\b/i.test(comment)) return true;
  const normalized = normalizeMarkers(comment);
  return /^(?:\/\/|\/\*+|#)[ \t]*@?nolint\b|^[ \t]*\*[ \t]*nolint\b/m.test(normalized);
}

function commentAt(content, cursor, prefixes, shell) {
  const prefix = prefixes.find((candidate) => content.startsWith(candidate, cursor));
  if (!prefix) return undefined;
  if (prefix === '#' && shell && cursor > 0 && !/[\s;|&()]/.test(content[cursor - 1])) return undefined;
  const terminator = prefix === '/*' ? '*/' : '\n';
  const index = content.indexOf(terminator, cursor + prefix.length);
  return index < 0 ? content.length : index + (prefix === '/*' ? 2 : 0);
}

// Supplement the inherited diff lexer's quote heuristic with a bounded walk:
// quotes inside comments cannot start strings, and ordinary JS/Go quotes do
// not cross physical lines. Only closed literals are accepted as data.
function supplementalMarkers(content, path, shell) {
  const normalized = normalizeMarkers(content);
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
  if (!SUSPECT_MARKER.test(content)) return [];
  const source = tracker.isSourceFile(path) || shell;
  if (!source) return [{ file: path, line: 1, reason: 'possible suppression in unsupported syntax requires review' }];
  const prepared = shell ? maskLiteralHeredocs(content) : content;
  const normalized = normalizeMarkers(prepared);
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
  const archive = await github.rest.repos.downloadTarballArchive({ owner, repo, ref: headSHA });
  const files = readArchive(archive.data, entries, headSHA);
  const findings = files.flatMap(scanFile);
  if (findings.length) {
    const detail = findings.slice(0, 8).map((finding) => `${finding.file}:${finding.line}: ${finding.reason}`).join('; ');
    hold(`${findings.length} finding(s) at ${headSHA}: ${detail}`);
  }
  return { headSHA, trustedPolicySHA, treeSHA, count: 0, filesScanned: files.length, findings: [], apiRequests: 3 };
}

module.exports = { verifySuppressions };
module.exports.testables = { validateTree, readArchive, scanFile, MAX_BLOB_BYTES, MAX_TOTAL_BYTES, MAX_TAR_BYTES };
