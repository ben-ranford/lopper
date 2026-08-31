'use strict';

const crypto = require('node:crypto');

const MAX_CHANGED_FILES = 3000;
const MAX_RECORDS = 100;
// Recognizing every prefix in every language is wrong, not just permissive:
// Python only has "#" comments, so "//" in `value = numerator // noqa` is
// floor division, not a comment start, and treating it as one rejects
// valid code for missing suppression metadata it was never meant to carry.
const HASH_ONLY_EXTENSIONS = new Set(['bash', 'ksh', 'py', 'rb', 'sh', 'yaml', 'yml', 'zsh']);
const SLASH_STYLE_EXTENSIONS = new Set([
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
  'kt',
  'kts',
  'mjs',
  'rs',
  'swift',
  'ts',
  'tsx',
]);
// PHP alone among the covered languages supports both hash and slash-style
// comments.
const ALL_COMMENT_PREFIXES = ['//', '/*', '#'];
const NO_MARKERS = new Set(['nosec', 'nosonar', 'nolint', 'noqa']);
const ESLINT_MARKERS = new Set(['eslint-disable', 'eslint-disable-next-line', 'eslint-disable-line']);
const TS_MARKERS = new Set(['ts-ignore', 'ts-expect-error']);
const TRACKED_FILE_STATUSES = new Set(['added', 'modified', 'renamed', 'copied']);
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

// A comment delimiter needs no preceding whitespace in any of the covered
// languages -- a marker immediately following code with no space in
// between is still a valid suppression -- so requiring it caused every
// detector to miss such lines entirely. Excluding ":" keeps a URL scheme
// (`http://nolint...`) from matching; everything else, including
// alphanumerics and punctuation, is a valid preceding position. Whether the
// candidate position falls inside a string literal (e.g. `"Use //nolint"`)
// is handled separately by isInsideQuotedRegion, which tracks quote state
// rather than only inspecting the single preceding character -- checking
// only that character misses a marker preceded by ordinary text inside an
// otherwise-open string, such as `"Use //nolint to suppress"`.
function isCommentBoundary(char) {
  return char === undefined || char !== ':';
}

// Scans content[0, index) tracking single/double/backtick-quoted regions
// (with backslash-escaping) to determine whether `index` falls inside an
// unterminated string literal. This is a heuristic shared across common
// C-like/scripting comment and string syntax, not a full per-language
// lexer, but it is what the covered SOURCE_EXTENSIONS all agree on.
//
// Rust and C/C++ have no multi-character single-quoted strings: a leading
// "'" there is either a self-contained char literal ('x' or '\x'), a Rust
// lifetime ('static, 'a) that never closes, or a C++14 digit separator
// (1'000'000) that never closes either. Treating every "'" as a string
// delimiter would let one of these swallow the rest of the line, hiding a
// real suppression comment that follows it -- for example, a lifetime
// preceding a coverage-ignore marker, or a digit separator preceding a
// lint-suppression marker; for these languages, only the narrow
// char-literal shape opens (and immediately closes) a quoted region.
const NARROW_SINGLE_QUOTE_EXTENSIONS = new Set(['rs', 'c', 'cc', 'cpp', 'cxx', 'h', 'hh', 'hpp']);

// Runs the quote-tracking state machine across content[0, index), starting
// from `initialQuote` (the quote character already open when this line
// began, or undefined if none). Returns { quote, pastCommentStart }: `quote`
// is the quote character still open at `index` (or undefined) -- used
// as-is for within-line checks, so a well-formed quoted span later in a
// comment (e.g. a backtick-quoted example inside a "#" comment) still masks
// correctly. `pastCommentStart` reports whether a genuine, unquoted line
// comment delimiter was seen anywhere before `index`; callers computing the
// state to carry into the *next* line (content.length) must discard `quote`
// when this is true, since an apostrophe left dangling in comment prose
// (e.g. "// don't use this path") is not an unterminated string in code and
// must not corrupt the following line's own scan -- but discarding it
// unconditionally, rather than only for that carry-over, would also break
// masking of anything genuinely quoted later in the very same comment.
function quoteStateAt(content, index, file, initialQuote) {
  const narrowSingleQuoteLanguage = typeof file === 'string' && NARROW_SINGLE_QUOTE_EXTENSIONS.has(fileExtension(file));
  let quote = initialQuote;
  let pastCommentStart = false;
  for (let cursor = 0; cursor < index; cursor += 1) {
    const char = content[cursor];
    if (quote !== undefined) {
      if (char === '\\') {
        cursor += 1;
      } else if (char === quote) {
        quote = undefined;
      }
      continue;
    }
    if (char === "'" && narrowSingleQuoteLanguage) {
      if (content[cursor + 1] === '\\' && content[cursor + 3] === "'") {
        cursor += 3;
      } else if (content[cursor + 1] !== "'" && content[cursor + 2] === "'") {
        cursor += 2;
      }
      continue;
    }
    if ((char === '/' && content[cursor + 1] === '/') || char === '#') {
      if (isCommentBoundary(content[cursor - 1])) {
        pastCommentStart = true;
      }
    }
    if (char === '"' || char === "'" || char === '`') {
      quote = char;
    }
  }
  return { quote, pastCommentStart };
}

function isInsideQuotedRegion(content, index, file, initialQuote) {
  return quoteStateAt(content, index, file, initialQuote).quote !== undefined;
}

// The quote state to carry into the line following `content`: undefined if
// a genuine line comment was seen anywhere on this line (any quote left
// open past that point is comment prose, not an unterminated string), or
// the quote otherwise still open at end of line.
function carryQuoteState(content, file, initialQuote) {
  const { quote, pastCommentStart } = quoteStateAt(content, content.length, file, initialQuote);
  return pastCommentStart ? undefined : quote;
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

function fingerprintFor(file, content, occurrence = 1) {
  const occurrenceSuffix = occurrence > 1 ? `\noccurrence:${occurrence}` : '';
  return crypto.createHash('sha256').update(`${file}\n${content}${occurrenceSuffix}`).digest('hex');
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

function commentPrefixesFor(file) {
  if (typeof file !== 'string') {
    return ALL_COMMENT_PREFIXES;
  }
  const ext = fileExtension(file);
  if (SLASH_STYLE_EXTENSIONS.has(ext)) {
    return ['//', '/*'];
  }
  if (HASH_ONLY_EXTENSIONS.has(ext)) {
    return ['#'];
  }
  return ALL_COMMENT_PREFIXES;
}

function sourceURLFor({ serverURL, owner, repo, headSHA, file, line }) {
  const encodedFile = file.split('/').map(encodeURIComponent).join('/');
  return `${serverURL || 'https://github.com'}/${owner}/${repo}/blob/${headSHA}/${encodedFile}#L${line}`;
}

function escapeFence(value) {
  return value.replaceAll('```', '``\u200b`');
}

async function fetchFullFileContent({ github, context, file, ref }) {
  const { data } = await github.rest.repos.getContent({
    owner: context.repo.owner,
    repo: context.repo.repo,
    path: file,
    ref,
  });
  if (Array.isArray(data) || data.type !== 'file') {
    throw new TypeError(`Unable to fetch full content for ${file} to scan for inline suppressions; refusing to publish tracking mutations.`);
  }
  // For files over the Contents API's 1 MiB inline limit, GitHub returns an
  // empty `content` with `encoding: "none"` rather than an error; accepting
  // that as an empty file would silently drop both the occurrence count and
  // the seeded quote state, desyncing from the trusted count derived from
  // the full checkout. Fall back to the Git Blobs API (no such inline-size
  // limit) in that case.
  let base64Content = data.encoding === 'base64' && typeof data.content === 'string' ? data.content : undefined;
  if (base64Content === undefined) {
    if (typeof data.sha !== 'string') {
      throw new TypeError(`Unable to fetch complete content for ${file} to scan for inline suppressions; refusing to publish tracking mutations.`);
    }
    const blob = await github.rest.git.getBlob({
      owner: context.repo.owner,
      repo: context.repo.repo,
      file_sha: data.sha,
    });
    if (blob.data.encoding !== 'base64' || typeof blob.data.content !== 'string') {
      throw new TypeError(`Unable to fetch complete content for ${file} (blob encoding ${blob.data.encoding}) to scan for inline suppressions; refusing to publish tracking mutations.`);
    }
    base64Content = blob.data.content;
  }
  return Buffer.from(base64Content, 'base64').toString('utf8');
}

function occurrenceInLines(lines, targetLine, targetContent) {
  let count = 0;
  for (let index = 0; index < targetLine && index < lines.length; index += 1) {
    if (lines[index] === targetContent) {
      count += 1;
    }
  }
  return count;
}

// The quote state to seed a hunk beginning at 1-indexed `hunkStartLine`:
// derived from the complete head file, not just the diff's own context
// window. A multi-line construct (e.g. an open template literal) can begin
// further above a hunk than GitHub's default 3-line context reaches;
// resetting to "no open quote" at every hunk boundary would make such a
// closing delimiter later in the hunk look like a new opener, potentially
// masking a real suppression comment or misreading ordinary code as one.
function quoteStateSeedFromLines(lines, hunkStartLine, file) {
  let quoteState;
  const priorLineCount = Math.min(hunkStartLine - 1, lines.length);
  for (let index = 0; index < priorLineCount; index += 1) {
    quoteState = carryQuoteState(lines[index], file, quoteState);
  }
  return quoteState;
}

function addSuppression(records, { file, line, content, context, headSHA, occurrence, initialQuote }) {
  validateFile(file);
  if (!Number.isInteger(line) || line < 1 || line > 1000000) {
    throw new RangeError('Invalid inline suppression line.');
  }
  validateString(content, 'content', 4096);

  const markerIndex = commentPrefixIndexForMarker(content, file, initialQuote);
  const metadataScope = markerIndex === -1 ? content : content.slice(markerIndex);
  const rationale = validateString(metadataValue(metadataScope, ['rationale', 'reason']), 'rationale', 1024);
  const owner = validateString(metadataValue(metadataScope, ['owner']), 'owner', 256);
  const removeWhen = validateString(
    metadataValue(metadataScope, ['remove-when', 'removal-condition', 'removal']),
    'remove_when',
    1024,
  );
  const fingerprint = fingerprintFor(file, content, occurrence);
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

function commentPrefixIndexForMarker(content, file, initialQuote) {
  for (let index = 0; index < content.length; index += 1) {
    if (!isCommentBoundary(content[index - 1])) {
      continue;
    }
    if (isInsideQuotedRegion(content, index, file, initialQuote)) {
      continue;
    }
    const prefix = commentPrefixesFor(file).find((candidate) => content.startsWith(candidate, index));
    if (prefix && hasMarkerAfterCommentPrefix(content, markerStartAfterPrefix(content, index, prefix))) {
      return index;
    }
  }
  return -1;
}

function hasInlineSuppressionMarker(content, file, initialQuote) {
  return commentPrefixIndexForMarker(content, file, initialQuote) !== -1;
}

function parseHunkHeader(rawLine, file) {
  const match = /^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@/.exec(rawLine);
  if (!match) {
    throw new SyntaxError(`Unable to parse inline suppression diff hunk for ${file}.`);
  }
  return {
    oldLines: match[2] === undefined ? 1 : Number.parseInt(match[2], 10),
    newStart: Number.parseInt(match[3], 10),
    newLines: match[4] === undefined ? 1 : Number.parseInt(match[4], 10),
  };
}

function parseHunkStart(rawLine, file) {
  return parseHunkHeader(rawLine, file).newStart;
}

function patchLines(patch) {
  return (patch.endsWith('\n') ? patch.slice(0, -1) : patch).split('\n');
}

function truncatedPatchError(file) {
  return new RangeError(`Inline suppression diff patch for ${file} is incomplete or truncated; refusing to publish tracking mutations.`);
}

function assertCompleteHunk(file, hunk) {
  if (!hunk) {
    return;
  }
  if (hunk.oldSeen !== hunk.oldLines || hunk.newSeen !== hunk.newLines) {
    throw truncatedPatchError(file);
  }
}

function patchLineStats(patch, file) {
  const stats = { additions: 0, deletions: 0 };
  let hunk;
  for (const rawLine of patchLines(patch)) {
    if (rawLine.startsWith('@@ ')) {
      assertCompleteHunk(file, hunk);
      const header = parseHunkHeader(rawLine, file);
      hunk = { oldLines: header.oldLines, newLines: header.newLines, oldSeen: 0, newSeen: 0 };
      continue;
    }
    if (!hunk) {
      continue;
    }
    if (rawLine.startsWith('+')) {
      stats.additions += 1;
      hunk.newSeen += 1;
      continue;
    }
    if (rawLine.startsWith('-')) {
      stats.deletions += 1;
      hunk.oldSeen += 1;
      continue;
    }
    if (rawLine.startsWith('\\')) {
      continue;
    }
    hunk.oldSeen += 1;
    hunk.newSeen += 1;
  }
  assertCompleteHunk(file, hunk);
  return stats;
}

function assertCompletePatch(file) {
  if (typeof file.patch !== 'string') {
    return;
  }
  if (!Number.isInteger(file.additions) || !Number.isInteger(file.deletions)) {
    throw truncatedPatchError(file.filename);
  }
  const stats = patchLineStats(file.patch, file.filename);
  if (stats.additions !== file.additions || stats.deletions !== file.deletions) {
    throw truncatedPatchError(file.filename);
  }
}

async function scanPatch(records, { github, file, patch, context, headSHA }) {
  validateFile(file);
  if (typeof patch !== 'string') {
    throw new TypeError(`Inline suppression diff patch is unavailable for ${file}; refusing to publish tracking mutations.`);
  }

  // The diff's own context window (GitHub's default 3 lines) cannot reveal
  // a multi-line construct that opens further above a hunk than that
  // window reaches; only the complete head file can. Fetch it once per
  // file and reuse it both to seed each hunk's starting quote state and to
  // derive each marker's true occurrence ordinal -- the self-hosted shell
  // gate reads the same complete file and must land on the same answers
  // for both.
  const headContent = await fetchFullFileContent({ github, context, file, ref: headSHA });
  const headLines = headContent.split('\n');

  let line = 0;
  // Quote state carries across lines within a hunk: a multi-line string
  // (e.g. a JavaScript template literal) that closes partway through a
  // later line must not make that line's scan think the closing delimiter
  // opens a new quoted region, which would mask a real suppression comment
  // following it on the same line.
  let quoteState;
  for (const rawLine of patchLines(patch)) {
    if (rawLine.startsWith('@@ ')) {
      line = parseHunkStart(rawLine, file);
      quoteState = quoteStateSeedFromLines(headLines, line, file);
      continue;
    }
    if (rawLine.startsWith('+')) {
      const content = rawLine.slice(1);
      if (hasInlineSuppressionMarker(content, file, quoteState)) {
        if (records.size >= MAX_RECORDS) {
          throw new RangeError(`Inline suppression records exceed the ${MAX_RECORDS}-record publication limit.`);
        }
        const occurrence = occurrenceInLines(headLines, line, content);
        addSuppression(records, { file, line, content, context, headSHA, occurrence, initialQuote: quoteState });
      }
      quoteState = carryQuoteState(content, file, quoteState);
      line += 1;
      continue;
    }
    if (rawLine.startsWith('-')) {
      continue;
    }
    if (!rawLine.startsWith('\\')) {
      // Context line: part of the resulting file, so its quote-affecting
      // characters must still be tracked even though it isn't scanned for
      // suppression markers (it isn't newly added by this pull request).
      const content = rawLine.slice(1);
      quoteState = carryQuoteState(content, file, quoteState);
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

function assertCurrentPullMatchesEvent({ eventPull, currentPull }) {
  if (!currentPull?.head?.sha) {
    throw new TypeError('Current pull request head SHA is unavailable; refusing to publish tracking mutations.');
  }
  if (currentPull.head.sha !== eventPull.head.sha) {
    throw new RangeError(
      `Pull request #${eventPull.number} head changed from event SHA ${eventPull.head.sha} to ${currentPull.head.sha}; refusing to use stale inline suppression diff records.`,
    );
  }
  if (!currentPull?.base?.sha) {
    throw new TypeError('Current pull request base SHA is unavailable; refusing to publish tracking mutations.');
  }
  if (currentPull.base.sha !== eventPull.base.sha) {
    throw new RangeError(
      `Pull request #${eventPull.number} base changed from event SHA ${eventPull.base.sha} to ${currentPull.base.sha}; refusing to use a diff computed against a different base than the event.`,
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

async function collectSuppressionRecords({ github, files, context, pull }) {
  const records = new Map();
  for (const file of files) {
    if (!TRACKED_FILE_STATUSES.has(file.status) || !isSourceFile(file.filename)) {
      continue;
    }
    if (
      file.status === 'renamed' &&
      (file.patch === undefined || file.patch === null) &&
      file.additions === 0 &&
      file.deletions === 0
    ) {
      continue;
    }
    assertCompletePatch(file);
    await scanPatch(records, {
      github,
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
  assertCurrentPullMatchesEvent({ eventPull, currentPull: pull });
  const count = await changedFileCount({ github, context, pull });
  assertTrustedFileCount(count);
  const files = await listChangedFiles({ github, context, pull, expectedCount: count });
  const refreshedPull = await fetchCurrentPull({ github, context, pull: eventPull });
  assertMutablePull(refreshedPull);
  assertCurrentPullMatchesEvent({ eventPull, currentPull: refreshedPull });
  if (refreshedPull.changed_files !== count) {
    throw new RangeError('Pull request changed file count drifted while recomputing inline suppression records; refusing to publish tracking mutations.');
  }
  const records = await collectSuppressionRecords({ github, files, context, pull });
  return { records, pull };
}

function pullMarkerFor(pullNumber) {
  return `lopper-inline-suppression-pr:${pullNumber}`;
}

function trackingBody(record, pullNumber) {
  return [
    `<!-- lopper-inline-suppression:${record.fingerprint} -->`,
    `<!-- ${pullMarkerFor(pullNumber)} -->`,
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

function fingerprintFromIssueBody(body) {
  if (typeof body !== 'string') {
    return undefined;
  }
  return body.match(/lopper-inline-suppression:([0-9a-f]{64})/)?.[1];
}

function issueBodyIncludesMarker(issue, marker) {
  if (typeof issue.body !== 'string') {
    return false;
  }
  // trackingBody() always writes the fingerprint marker on the issue
  // body's first line and the pull marker on its second; requiring an
  // exact match there (rather than a substring search anywhere in the
  // body) keeps a suppression's own attacker-controlled source-line
  // content -- also echoed into this same body, further down -- from
  // being able to spoof a canonical marker for a fingerprint or pull
  // number it doesn't actually own.
  const headerLines = issue.body.split('\n', 2);
  return headerLines.includes(`<!-- ${marker} -->`);
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

async function upsertTrackingIssue({ github, context, record, pullNumber }) {
  const marker = `lopper-inline-suppression:${record.fingerprint}`;
  const pullMarker = pullMarkerFor(pullNumber);
  const title = `ci: track inline suppression in ${record.file}:${record.line}`;
  const body = trackingBody(record, pullNumber);
  // Scope reuse to (fingerprint, pull) so two different pull requests that
  // happen to add an identical suppression never share one tracking issue:
  // closing one PR's suppression would otherwise close the issue for the
  // other PR's still-present exception.
  // Constrain the search itself to the trusted author: without this, an
  // untrusted issue matching the marker text could occupy the capped
  // result set ahead of the real trusted issue and make this incorrectly
  // conclude none exists, creating an accumulating duplicate each run.
  const searchTrackingIssue = async () => github.rest.search.issuesAndPullRequests({
    q: `repo:${context.repo.owner}/${context.repo.repo} is:issue is:open author:github-actions[bot] ${marker} ${pullMarker}`,
    per_page: 2,
  });
  const findTrustedExisting = async () => {
    const results = await searchTrackingIssue();
    return results.data.items.find(
      (item) => isTrustedTrackingIssue(item, marker) && issueBodyIncludesMarker(item, pullMarker),
    );
  };
  const existing = await findTrustedExisting();
  if (existing) {
    await github.rest.issues.update({
      owner: context.repo.owner,
      repo: context.repo.repo,
      issue_number: existing.number,
      title,
      body,
    });
    return { action: 'updated', number: existing.number };
  }
  let created;
  try {
    created = await github.rest.issues.create({
      owner: context.repo.owner,
      repo: context.repo.repo,
      title,
      body,
    });
  } catch (error) {
    const concurrentExisting = await findTrustedExisting();
    if (concurrentExisting) {
      await github.rest.issues.update({
        owner: context.repo.owner,
        repo: context.repo.repo,
        issue_number: concurrentExisting.number,
        title,
        body,
      });
      return { action: 'updated', number: concurrentExisting.number };
    }
    throw error;
  }
  return { action: 'opened', number: created.data.number };
}

async function findTrustedTrackingIssuesForPull({ github, context, pullNumber }) {
  const marker = pullMarkerFor(pullNumber);
  const results = await github.rest.search.issuesAndPullRequests({
    q: `repo:${context.repo.owner}/${context.repo.repo} is:issue is:open author:github-actions[bot] ${marker}`,
    per_page: 100,
  });
  return results.data.items.filter((item) => isTrustedTrackingIssue(item, marker));
}

async function reconcileDisappearedSuppressions({ github, context, pull, records, core }) {
  const tracked = await findTrustedTrackingIssuesForPull({ github, context, pullNumber: pull.number });
  for (const issue of tracked) {
    const fingerprint = fingerprintFromIssueBody(issue.body);
    if (!fingerprint || records.has(fingerprint)) {
      continue;
    }
    await github.rest.issues.update({
      owner: context.repo.owner,
      repo: context.repo.repo,
      issue_number: issue.number,
      state: 'closed',
      state_reason: 'not_planned',
    });
    core.info(`Closed inline suppression tracking issue #${issue.number}; the suppression no longer appears in pull request #${pull.number}.`);
  }
}

async function cleanupClosedPullTrackingIssues({ github, context, core }) {
  const pull = context.payload.pull_request;
  if (!pull?.number) {
    throw new TypeError('Pull request number is required to clean up inline suppression tracking issues.');
  }
  const tracked = await findTrustedTrackingIssuesForPull({ github, context, pullNumber: pull.number });
  for (const issue of tracked) {
    await github.rest.issues.update({
      owner: context.repo.owner,
      repo: context.repo.repo,
      issue_number: issue.number,
      state: 'closed',
      state_reason: 'not_planned',
    });
    core.info(`Closed inline suppression tracking issue #${issue.number}; pull request #${pull.number} closed without merging.`);
  }
}

async function trackInlineSuppressions({ github, context, core }) {
  if (context.payload.action === 'closed') {
    // Every other trigger (opened/edited/synchronize/reopened/ready_for_review)
    // only ever runs while the pull request is open, so reconciliation for a
    // suppression that disappears mid-review is already handled by
    // reconcileDisappearedSuppressions on the next such event. A pull request
    // that closes without merging gets no further event, so its bot-created
    // issues would otherwise stay open indefinitely for suppressions that
    // never entered the repository. A merged pull request's suppressions did
    // land, so its tracking issues correctly remain open; nothing to do.
    const pull = context.payload.pull_request;
    if (pull?.merged) {
      core.info(`Pull request #${pull.number} merged; its inline suppression tracking issues remain open.`);
      return;
    }
    await cleanupClosedPullTrackingIssues({ github, context, core });
    return;
  }

  const { records, pull } = await recomputeSuppressionRecords({ github, context });
  await reconcileDisappearedSuppressions({ github, context, pull, records, core });
  if (records.size === 0) {
    core.info('No inline suppression records were produced.');
    return;
  }

  for (const record of records.values()) {
    const result = await upsertTrackingIssue({ github, context, record, pullNumber: pull.number });
    const verb = result.action === 'updated' ? 'Updated' : 'Opened';
    core.info(`${verb} inline suppression tracking issue #${result.number} for ${record.file}:${record.line}.`);
  }
}

module.exports = trackInlineSuppressions;
module.exports.testables = {
  MAX_CHANGED_FILES,
  MAX_RECORDS,
  addSuppression,
  commentPrefixesFor,
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
