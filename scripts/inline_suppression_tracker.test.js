'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const trackInlineSuppressions = require('./inline_suppression_tracker.js');
const { testables } = trackInlineSuppressions;

function trackedLine(marker = 'nolint:staticcheck') {
  return `\t_ = 1 //${marker} // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard`;
}

function patchFor(line) {
  return `@@ -0,0 +1,5 @@\n+package main\n+\n+func main() {\n+${line}\n+}\n`;
}

function reconstructFileFromPatch(patch) {
  if (typeof patch !== 'string') {
    return '';
  }
  const lines = [];
  for (const rawLine of (patch.endsWith('\n') ? patch.slice(0, -1) : patch).split('\n')) {
    if (rawLine.startsWith('@@ ') || rawLine.startsWith('\\')) {
      continue;
    }
    if (rawLine.startsWith('+') || rawLine.startsWith(' ')) {
      lines.push(rawLine.slice(1));
    }
  }
  return lines.join('\n');
}

function patchStats(patch) {
  const stats = { additions: 0, deletions: 0 };
  for (const rawLine of patch.split('\n')) {
    if (rawLine.startsWith('+++') || rawLine.startsWith('---')) {
      continue;
    }
    if (rawLine.startsWith('+')) {
      stats.additions += 1;
    } else if (rawLine.startsWith('-')) {
      stats.deletions += 1;
    }
  }
  return stats;
}

function withPatchStats(file) {
  if (typeof file.patch !== 'string') {
    return file;
  }
  const stats = patchStats(file.patch);
  return {
    additions: stats.additions,
    deletions: stats.deletions,
    ...file,
  };
}

function makeHarness(options = {}) {
  const pull = {
    number: 42,
    state: 'open',
    changed_files: options.changedFiles,
    base: { sha: 'base-sha', repo: { full_name: 'octo/lopper' } },
    head: {
      sha: 'head-sha',
      repo: {
        fork: true,
        full_name: 'fork/lopper',
      },
    },
    ...options.pull,
  };
  const files = (options.files || [
    {
      filename: 'main.go',
      status: 'added',
      patch: patchFor(trackedLine()),
    },
  ]).map(withPatchStats);
  if (!Number.isInteger(pull.changed_files)) {
    pull.changed_files = files.length;
  }
  const currentPull = {
    ...pull,
    changed_files: options.fetchedChangedFiles ?? pull.changed_files,
    head: {
      ...pull.head,
      sha: options.currentHeadSHA ?? pull.head.sha,
    },
    base: {
      ...pull.base,
      sha: options.currentBaseSHA ?? pull.base.sha,
    },
  };

  const calls = {
    created: [],
    gets: [],
    infos: [],
    paginated: 0,
    searches: [],
    updated: [],
  };
  const trustedIssue = (number, marker) => ({
    number,
    body: `<!-- ${marker} -->\n<!-- lopper-inline-suppression-pr:${pull.number} -->\n\n## Inline analysis suppression tracking`,
    user: { login: 'github-actions[bot]', type: 'Bot' },
  });
  const github = {
    rest: {
      issues: {
        create: async (input) => {
          calls.created.push(input);
          if (options.createError) {
            throw options.createError;
          }
          return { data: { number: 101 } };
        },
        update: async (input) => {
          calls.updated.push(input);
        },
      },
      pulls: {
        get: async (input) => {
          calls.gets.push(input);
          return { data: currentPull };
        },
        listFiles: async () => {},
      },
      repos: {
        getContent: async ({ path: filePath }) => {
          calls.contentFetches = (calls.contentFetches || 0) + 1;
          const file = files.find((candidate) => candidate.filename === filePath);
          // A real head file can contain content the diff's own 3-line
          // context window never reveals (e.g. a multi-line construct
          // opening many lines above a hunk); options.fullFileContents lets
          // a test simulate that gap instead of the content always being
          // exactly reconstructable from the patch alone.
          const content = options.fullFileContents?.[filePath] ?? reconstructFileFromPatch(file?.patch);
          if (options.oversizedContentFiles?.includes(filePath)) {
            return { data: { type: 'file', content: '', encoding: 'none', sha: `blob-sha:${filePath}` } };
          }
          return {
            data: { type: 'file', content: Buffer.from(content, 'utf8').toString('base64'), encoding: 'base64', sha: `blob-sha:${filePath}` },
          };
        },
      },
      git: {
        getBlob: async ({ file_sha: fileSha }) => {
          calls.blobFetches = (calls.blobFetches || 0) + 1;
          const filePath = fileSha.replace(/^blob-sha:/, '');
          const file = files.find((candidate) => candidate.filename === filePath);
          const content = options.fullFileContents?.[filePath] ?? reconstructFileFromPatch(file?.patch);
          return { data: { encoding: 'base64', content: Buffer.from(content, 'utf8').toString('base64') } };
        },
      },
      search: {
        issuesAndPullRequests: async (input) => {
          calls.searches.push(input);
          const marker = input.q.match(/lopper-inline-suppression:[0-9a-f]+/)?.[0];
          if (typeof options.searchItems === 'function') {
            return { data: { items: options.searchItems({ input, marker, calls }) } };
          }
          return {
            data: {
              items: options.existingIssue ? [trustedIssue(options.existingIssue, marker)] : [],
            },
          };
        },
      },
    },
    paginate: async (method) => {
      assert.equal(method, github.rest.pulls.listFiles);
      calls.paginated += 1;
      return files;
    },
  };
  return {
    args: {
      github,
      context: {
        repo: { owner: 'octo', repo: 'lopper' },
        payload: { pull_request: pull, action: options.action },
      },
      core: {
        info: (message) => calls.infos.push(message),
      },
    },
    calls,
  };
}

test('tracks fork pull request suppressions from trusted diff recomputation', async () => {
  const harness = makeHarness();

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.updated.length, 0);
  const created = harness.calls.created[0];
  assert.equal(created.title, 'ci: track inline suppression in main.go:4');
  assert.match(created.body, /Location: `main\.go:4`/);
  assert.match(created.body, /Source: https:\/\/github\.com\/octo\/lopper\/blob\/head-sha\/main\.go#L4/);
  assert.match(created.body, /Rationale: temporary scanner false positive/);
  assert.match(harness.calls.infos.join('\n'), /Opened inline suppression tracking issue #101/);
});

test('detects an added suppression line that itself begins with ++', async () => {
  const line = '++counter; ' + trackedLine();
  const patch = patchFor(line);
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'added',
        patch,
        additions: 5,
        deletions: 0,
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, /Location: `main\.go:4`/);
});

test('percent-encodes URL-significant characters in source links', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'odd name#1.go',
        status: 'added',
        patch: patchFor(trackedLine()),
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.match(
    harness.calls.created[0].body,
    /Source: https:\/\/github\.com\/octo\/lopper\/blob\/head-sha\/odd%20name%231\.go#L4/,
  );
});

test('falls back to the Git Blobs API when the Contents API omits inline content', async () => {
  // Files over the Contents API's 1 MiB limit come back with an empty
  // `content` and `encoding: "none"`; treating that as zero lines would
  // assign every occurrence 0 and desync from the trusted full-checkout
  // count. This must fetch the blob directly instead.
  const harness = makeHarness({
    files: [
      {
        filename: 'huge.go',
        status: 'added',
        patch: patchFor(trackedLine()),
      },
    ],
    oversizedContentFiles: ['huge.go'],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.blobFetches, 1);
  assert.match(harness.calls.created[0].body, /Location: `huge\.go:4`/);
});

test('ignores code-side assignments before the suppression marker when extracting metadata', async () => {
  const line = 'owner := service ' + trackedLine();
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'added',
        patch: patchFor(line),
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  const { body } = harness.calls.created[0];
  assert.match(body, /Owner: @security/);
  assert.doesNotMatch(body, /Owner: = service/);
  assert.doesNotMatch(body, /Owner: service/);
});

test('recognizes supported inline suppression marker forms without matching quoted text', () => {
  const matchingLines = [
    '\t_ = 1 /' + '/no' + 'sec G404',
    '\t_ = 1 /' + '* NO' + 'SONAR */',
    '\t_ = 1 /' + '** NO' + 'SONAR */',
    '\t_ = 1 ' + '# no' + 'qa',
    '\t_ = 1 /' + '/ @ts-' + 'ignore',
    '\t_ = 1 /' + '/ @ts-' + 'expect-error',
    '\t_ = 1 /' + '/ eslint-' + 'disable-next-line no-console',
    '\t_ = 1 /' + '/ pragma: ' + 'no cover',
    '\t_ = 1 /' + '/ coverage: ' + 'ignore',
    // A comment delimiter needs no preceding whitespace: it can follow
    // code directly.
    'call();/' + '/no' + 'lint rationale=x; owner=y; remove-when=z',
    'value=1' + '# no' + 'qa rationale=x; owner=y; remove-when=z',
    // A comment delimiter immediately after a *closed* string literal is
    // real code, not text inside the string; only an *unterminated* quoted
    // region should suppress the match.
    '\t_ = "done" /' + '/no' + 'sec G404',
  ];
  for (const line of matchingLines) {
    assert.equal(testables.hasInlineSuppressionMarker(line), true, line);
  }

  const ignoredLines = [
    'const marker = "/' + '/no' + 'sec";',
    'const marker = "/' + '* NO' + 'SONAR */";',
    '\t_ = 1 /' + '/ no' + 'linter',
    '\t_ = 1 /' + '/ coverage: ' + 'ignored',
    '\t_ = 1 /' + '/ pragma: ' + 'no coverage',
    // A URL scheme must not be mistaken for a comment delimiter.
    'const url = "http:/' + '/no' + 'sec.example.com";',
    // The marker-shaped text is preceded by ordinary characters, not a
    // quote, but is still inside an unterminated string literal; checking
    // only the single preceding character misses this.
    'const help = "Use /' + '/no' + 'lint to suppress";',
  ];
  for (const line of ignoredLines) {
    assert.equal(testables.hasInlineSuppressionMarker(line), false, line);
  }
});

test('recognizes a marker following a Rust lifetime without treating it as an open string', () => {
  // Rust has no multi-character single-quoted strings, so a leading "'" is
  // either a self-contained char literal or a lifetime that never closes.
  // Treating every "'" as a generic string delimiter (as other languages
  // require) would make the unterminated lifetime swallow the rest of the
  // line, hiding the real suppression comment that follows it.
  const line = '\tlet value: &' + "'static str = \"x\"; //" + 'coverage: ignore';
  assert.equal(testables.hasInlineSuppressionMarker(line, 'src/lib.rs'), true, line);
  assert.equal(testables.hasInlineSuppressionMarker(line), false, line);

  // A genuine Rust char literal still masks its content as a quoted region.
  const charLiteralLine = "\tlet marker = '/'" + "; //" + 'coverage: ignore';
  assert.equal(testables.hasInlineSuppressionMarker(charLiteralLine, 'src/lib.rs'), true, charLiteralLine);

  // Non-Rust files keep the original behavior: a single quote still opens a
  // real multi-character string.
  const pythonLine = 'help = \'Use /' + '/no' + "lint to suppress'";
  assert.equal(testables.hasInlineSuppressionMarker(pythonLine, 'tool.py'), false, pythonLine);
});

test('recognizes a marker following a C++ digit separator without treating it as an open string', () => {
  // C++14 digit separators group the digits of a large numeric literal with
  // apostrophes (1'000'000) that never close the way a real string does;
  // treating every apostrophe as a generic string delimiter would make the
  // unterminated separator swallow the rest of the line, hiding the real
  // suppression comment that follows it.
  const line = '\tauto n = 1' + "'000; //" + 'NOLINT rationale=x; owner=y; remove-when=z';
  assert.equal(testables.hasInlineSuppressionMarker(line, 'src/main.cpp'), true, line);
  assert.equal(testables.hasInlineSuppressionMarker(line), false, line);

  // A genuine C/C++ char literal on the same line still masks correctly.
  const charLiteralLine = "\tchar c = '0'" + '; //' + 'NOLINT rationale=x; owner=y; remove-when=z';
  assert.equal(testables.hasInlineSuppressionMarker(charLiteralLine, 'src/main.c'), true, charLiteralLine);

  // Every C/C++ header/source extension is covered, not just .cpp.
  for (const file of ['main.c', 'main.cc', 'main.cxx', 'main.h', 'main.hh', 'main.hpp']) {
    assert.equal(testables.hasInlineSuppressionMarker(line, file), true, file);
  }
});

test('does not treat Python floor division as a comment prefix', () => {
  // Python only has "#" comments, so "//" here is floor division, not the
  // start of a comment. Recognizing "//" universally would make this line
  // look like it carries a suppression marker it was never meant to carry.
  const line = '\tvalue = numerator ' + '/' + '/ noqa denominator';
  assert.equal(testables.hasInlineSuppressionMarker(line, 'calc.py'), false, line);

  // A genuine hash-comment marker on the same kind of file is still found.
  const hashLine = '\tvalue = numerator ' + '/' + '/ denominator  # noqa';
  assert.equal(testables.hasInlineSuppressionMarker(hashLine, 'calc.py'), true, hashLine);

  // Slash-style languages keep recognizing "//" comments as before.
  const jsLine = '\tconst value = numerator ' + '/' + '/ denominator; //' + 'noqa';
  assert.equal(testables.hasInlineSuppressionMarker(jsLine, 'calc.js'), true, jsLine);
});

test('requires a free-standing "#" to start a comment in a hash-only language', () => {
  // YAML requires "#" to be separated from the preceding scalar by
  // whitespace (or start the line); a "#" embedded in a URL's fragment
  // identifier is not a comment delimiter.
  const yamlLine = 'url: https://example.test/#noqa';
  assert.equal(testables.hasInlineSuppressionMarker(yamlLine, 'deploy.yaml'), false, yamlLine);

  // Shell only treats "#" as a comment when it begins a word; "#" glued
  // directly onto the preceding token is a literal character.
  const shellLine = 'echo foo#nolint';
  assert.equal(testables.hasInlineSuppressionMarker(shellLine, 'build.sh'), false, shellLine);

  // A free-standing "#" -- preceded by whitespace -- is still recognized.
  const genuineLine = 'echo foo #nolint rationale=x; owner=y; remove-when=z';
  assert.equal(testables.hasInlineSuppressionMarker(genuineLine, 'build.sh'), true, genuineLine);

  // Slash-style languages are unaffected: "//" needs no equivalent
  // free-standing rule.
  const jsLine = 'call();//' + 'nolint rationale=x; owner=y; remove-when=z';
  assert.equal(testables.hasInlineSuppressionMarker(jsLine, 'main.js'), true, jsLine);
});

test('selects comment prefixes per language extension', () => {
  assert.deepEqual(testables.commentPrefixesFor('calc.py'), ['#']);
  assert.deepEqual(testables.commentPrefixesFor('script.sh'), ['#']);
  assert.deepEqual(testables.commentPrefixesFor('deploy.yaml'), ['#']);
  assert.deepEqual(testables.commentPrefixesFor('main.go'), ['//', '/*']);
  assert.deepEqual(testables.commentPrefixesFor('app.ts'), ['//', '/*']);
  assert.deepEqual(testables.commentPrefixesFor('index.php'), ['//', '/*', '#']);
});

test('classifies source files without path validation side effects', () => {
  assert.equal(testables.isSourceFile('internal/main.go'), true);
  assert.equal(testables.isSourceFile('web/component.TSX'), true);
  assert.equal(testables.isSourceFile('.githooks/pre-commit'), true);
  assert.equal(testables.isSourceFile('docs/policy.md'), false);
});

test('parses metadata aliases without dynamic regular expressions', () => {
  const content = '/' + '/nolint //' + ' reason: false positive; owner = @security; removal-condition= analyzer fix';

  assert.equal(testables.metadataValue(content, ['rationale', 'reason']), 'false positive');
  assert.equal(testables.metadataValue(content, ['owner']), '@security');
  assert.equal(testables.metadataValue(content, ['remove-when', 'removal-condition']), 'analyzer fix');
});

test('updates an existing tracking issue by fingerprint', async () => {
  const harness = makeHarness({ existingIssue: 77 });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.equal(harness.calls.updated.length, 1);
  assert.equal(harness.calls.updated[0].issue_number, 77);
  assert.match(harness.calls.infos.join('\n'), /Updated inline suppression tracking issue #77/);
});

test('does not reuse a public fingerprint marker without trusted tracker ownership', async () => {
  const harness = makeHarness({
    searchItems: ({ marker }) => [
      {
        number: 77,
        body: `<!-- ${marker} -->`,
        user: { login: 'random-user', type: 'User' },
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.updated.length, 0);
  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, /Inline analysis suppression tracking/);
});

test('constrains tracking issue searches to the trusted author', async () => {
  // A capped result set (per_page: 2) ranked by GitHub's own relevance
  // scoring could otherwise put two untrusted marker-matching issues ahead
  // of the real trusted one, making this incorrectly conclude none exists
  // and create an accumulating duplicate every run. Restricting the search
  // query itself to the trusted author closes that gap server-side.
  const harness = makeHarness();

  await trackInlineSuppressions(harness.args);

  assert.ok(harness.calls.searches.length > 0);
  for (const search of harness.calls.searches) {
    assert.match(search.q, /author:github-actions\[bot\]/);
  }
});

test('recovers when a concurrent tracker creates the trusted issue first', async () => {
  const fingerprintAttempts = new Map();
  const harness = makeHarness({
    createError: new Error('already exists'),
    searchItems: ({ marker }) => {
      // Reconciliation issues its own search (for the PR-scoped marker) before
      // any per-fingerprint lookup; ignore it so call-count bookkeeping below
      // only tracks the fingerprint-marker search this test exercises.
      if (!marker || !marker.startsWith('lopper-inline-suppression:')) {
        return [];
      }
      const attempts = (fingerprintAttempts.get(marker) || 0) + 1;
      fingerprintAttempts.set(marker, attempts);
      if (attempts === 1) {
        return [];
      }
      return [
        {
          number: 88,
          body: `<!-- ${marker} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
      ];
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.updated.length, 1);
  assert.equal(harness.calls.updated[0].issue_number, 88);
  assert.match(harness.calls.infos.join('\n'), /Updated inline suppression tracking issue #88/);
});

test('ignores a marker spoofed outside the canonical header position', async () => {
  // trackingBody() echoes the suppression's own (attacker-controlled)
  // source line into the same issue body, further down, inside a fenced
  // code block. A crafted suppression whose content contains literal
  // marker-comment text for a fingerprint it doesn't actually own must
  // not let a substring search treat that issue as trusted for it.
  const harness = makeHarness({
    searchItems: ({ marker }) => [
      {
        number: 77,
        body: [
          '<!-- lopper-inline-suppression:0000000000000000000000000000000000000000000000000000000000000000 -->',
          '<!-- lopper-inline-suppression-pr:42 -->',
          '',
          '## Inline analysis suppression tracking',
          '',
          'Source line:',
          '',
          '```text',
          `spoofed content containing <!-- ${marker} --> to look canonical`,
          '```',
        ].join('\n'),
        user: { login: 'github-actions[bot]', type: 'Bot' },
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  // Reconciliation legitimately closes issue #77: its own canonical
  // fingerprint (a fake one) doesn't match any currently-produced record,
  // so it's stale by the real rule (fingerprint no longer in the diff).
  // That's expected and unrelated to spoofing. What must NOT happen is
  // the *real* suppression's own upsert treating #77 as already tracking
  // it because of the spoofed text further down in the body -- that
  // would show up as a second update (a body/title rewrite) rather than
  // a fresh issue being created.
  assert.equal(harness.calls.updated.length, 1);
  assert.equal(harness.calls.updated[0].issue_number, 77);
  assert.equal(harness.calls.updated[0].state, 'closed');
  assert.equal(harness.calls.created.length, 1);
});

test('opens a separate tracking issue when another pull request already owns the identical fingerprint', async () => {
  const harness = makeHarness({
    pull: { number: 99 },
    searchItems: ({ marker }) => {
      if (!marker || !marker.startsWith('lopper-inline-suppression:')) {
        return [];
      }
      // A different pull request (#42) already has a trusted, open issue for
      // this exact fingerprint; it must not be reused/overwritten for #99.
      return [
        {
          number: 77,
          body: `<!-- ${marker} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
      ];
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.updated.length, 0);
  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, /lopper-inline-suppression-pr:99/);
});

test('recognizes a marker after a multi-line template literal closes on a later line', async () => {
  // Quote state resets per line by default; a template literal opened on
  // one added line and closed on a later added line must carry that state
  // across, or the closing backtick looks like it opens a fresh quoted
  // region and masks the real suppression comment that follows it.
  const patch = [
    '@@ -0,0 +1,3 @@',
    '+const s = `line one',
    '+line two',
    '+tail`; //' + 'eslint-disable-line rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard',
    '',
  ].join('\n');
  const harness = makeHarness({
    files: [
      {
        filename: 'main.js',
        status: 'added',
        patch,
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, /Location: `main\.js:3`/);
});

test('seeds quote state from the complete head file across a diff context gap', async () => {
  // GitHub's default 3-line diff context cannot reveal a multi-line
  // construct that opens further above a hunk than that window reaches.
  // Resetting to "no open quote" at the hunk boundary -- instead of
  // deriving it from the complete head file -- would make the closing
  // backtick below look like a fresh opener and mask the real suppression
  // comment that follows it.
  const fullFile = [
    'package main',
    '',
    'var tpl = `line1',
    'line2',
    'line3',
    'line4',
    'tail`; //' + 'eslint-disable-line rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard',
    '',
  ].join('\n');
  const patch = [
    '@@ -5,3 +5,3 @@',
    ' line3',
    ' line4',
    '-tail`;',
    '+tail`; //' + 'eslint-disable-line rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard',
    '',
  ].join('\n');
  const harness = makeHarness({
    files: [
      {
        filename: 'main.js',
        status: 'modified',
        patch,
      },
    ],
    fullFileContents: { 'main.js': fullFile },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, /Location: `main\.js:7`/);
});

test('recognizes a marker on the line after an ordinary comment containing an apostrophe', async () => {
  // An apostrophe inside a genuine line comment (e.g. "don't") is comment
  // prose, not code; it must not be treated as opening a string that
  // leaks into the next added line and masks a real suppression comment
  // there, the same way a real unterminated string legitimately would.
  const patch = [
    '@@ -0,0 +1,2 @@',
    "+\t// don't use this path",
    '+\t_ = 1 //' + 'nolint:staticcheck // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard',
    '',
  ].join('\n');
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'added',
        patch,
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, /Location: `main\.go:2`/);
});

test('ignores marker-shaped example text quoted inside an ordinary comment', async () => {
  // Stopping quote tracking at a genuine comment delimiter must not stop
  // *masking* for the rest of that same line: a well-formed quoted span
  // later in the very same comment (e.g. documentation quoting an example
  // marker in backticks) still needs its interior masked, or the example
  // text is indistinguishable from a real suppression. Only the *carry
  // into the next line* should discard a comment's dangling quote state,
  // not the comment's own remaining content.
  const line = '\t// e.g. `"Use ' + '//nolint' + ' to suppress"`. Blanking out the region.';
  const patch = ['@@ -0,0 +1,1 @@', '+' + line, ''].join('\n');
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'added',
        patch,
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
});

test('closes tracking issues for suppressions that disappeared from the pull diff', async () => {
  const staleFingerprint = testables.fingerprintFor('main.go', trackedLine('nolint:staticcheck'), 1);
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'modified',
        patch: [
          '@@ -1,5 +1,4 @@',
          ' package main',
          ' ',
          ' func main() {',
          `-${trackedLine('nolint:staticcheck')}`,
          ' }',
          '',
        ].join('\n'),
      },
    ],
    searchItems: ({ input }) => {
      if (!input.q.includes('lopper-inline-suppression-pr:')) {
        return [];
      }
      return [
        {
          number: 99,
          body: `<!-- lopper-inline-suppression:${staleFingerprint} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
      ];
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.equal(harness.calls.updated.length, 1);
  assert.equal(harness.calls.updated[0].issue_number, 99);
  assert.equal(harness.calls.updated[0].state, 'closed');
  assert.match(harness.calls.infos.join('\n'), /Closed inline suppression tracking issue #99; the suppression no longer appears in pull request #42/);
});

test('does not close a tracking issue whose suppression is still present in the pull diff', async () => {
  const currentFingerprint = testables.fingerprintFor('main.go', trackedLine('nolint:staticcheck'), 1);
  const harness = makeHarness({
    searchItems: ({ input }) => {
      if (!input.q.includes('lopper-inline-suppression-pr:')) {
        return [];
      }
      return [
        {
          number: 55,
          body: `<!-- lopper-inline-suppression:${currentFingerprint} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
      ];
    },
  });

  await trackInlineSuppressions(harness.args);

  const closeCalls = harness.calls.updated.filter((call) => call.state === 'closed');
  assert.equal(closeCalls.length, 0);
});

test('closes all tracking issues when a pull request closes without merging', async () => {
  const fingerprintA = testables.fingerprintFor('main.go', trackedLine('nolint:staticcheck'), 1);
  const fingerprintB = testables.fingerprintFor('other.go', trackedLine('nosec G404'), 1);
  const harness = makeHarness({
    action: 'closed',
    pull: { merged: false },
    searchItems: ({ input }) => {
      if (!input.q.includes('lopper-inline-suppression-pr:')) {
        return [];
      }
      return [
        {
          number: 61,
          body: `<!-- lopper-inline-suppression:${fingerprintA} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
        {
          number: 62,
          body: `<!-- lopper-inline-suppression:${fingerprintB} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
      ];
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  // A closed-without-merging pull request never had its diff recomputed --
  // recomputeSuppressionRecords would reject a non-open pull outright, and
  // cleanup does not need it.
  assert.equal(harness.calls.gets.length, 0);
  assert.equal(harness.calls.updated.length, 2);
  const closedNumbers = harness.calls.updated.map((call) => call.issue_number).sort();
  assert.deepEqual(closedNumbers, [61, 62]);
  for (const call of harness.calls.updated) {
    assert.equal(call.state, 'closed');
    assert.equal(call.state_reason, 'not_planned');
  }
  assert.match(harness.calls.infos.join('\n'), /Closed inline suppression tracking issue #61; pull request #42 closed without merging\./);
  assert.match(harness.calls.infos.join('\n'), /Closed inline suppression tracking issue #62; pull request #42 closed without merging\./);
});

test('leaves tracking issues open when a pull request closes because it merged', async () => {
  const fingerprint = testables.fingerprintFor('main.go', trackedLine('nolint:staticcheck'), 1);
  const harness = makeHarness({
    action: 'closed',
    pull: { merged: true },
    searchItems: ({ input }) => {
      if (!input.q.includes('lopper-inline-suppression-pr:')) {
        return [];
      }
      return [
        {
          number: 71,
          body: `<!-- lopper-inline-suppression:${fingerprint} -->\n<!-- lopper-inline-suppression-pr:42 -->`,
          user: { login: 'github-actions[bot]', type: 'Bot' },
        },
      ];
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.equal(harness.calls.updated.length, 0);
  assert.equal(harness.calls.gets.length, 0);
  assert.equal(harness.calls.searches.length, 0);
  assert.match(harness.calls.infos.join('\n'), /Pull request #42 merged; its inline suppression tracking issues remain open\./);
});

test('rejects stale event payloads before reading pull file diffs', async () => {
  const harness = makeHarness({ currentHeadSHA: 'new-head-sha' });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /head changed from event SHA head-sha to new-head-sha.*refusing to use stale inline suppression diff records/,
    },
  );
  assert.equal(harness.calls.paginated, 0);
  assert.equal(harness.calls.created.length, 0);
});

test('rejects a base branch that advanced past the event before reading pull file diffs', async () => {
  const harness = makeHarness({ currentBaseSHA: 'new-base-sha' });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /base changed from event SHA base-sha to new-base-sha.*refusing to use a diff computed against a different base/,
    },
  );
  assert.equal(harness.calls.paginated, 0);
  assert.equal(harness.calls.created.length, 0);
});

function makeRenamedOrCopiedHarness(status, filename, extra = {}) {
  return makeHarness({
    files: [
      {
        filename,
        previous_filename: 'main.go',
        status,
        ...extra,
      },
    ],
  });
}

test('scans renamed source files for tracked suppressions', async () => {
  const harness = makeRenamedOrCopiedHarness('renamed', 'renamed.go', { patch: patchFor(trackedLine('nosec G404')) });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.created[0].title, 'ci: track inline suppression in renamed.go:4');
});

test('skips patchless pure source renames', async () => {
  const harness = makeRenamedOrCopiedHarness('renamed', 'renamed.go', { additions: 0, deletions: 0 });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.deepEqual(harness.calls.infos, ['No inline suppression records were produced.']);
});

test('scans copied source files for tracked suppressions', async () => {
  // GitHub's own copy detection reports status "copied" for a file it
  // recognizes as a copy of another; that new path introduces another
  // live occurrence of any suppression in the copied content and must be
  // tracked under its own fingerprint, not silently skipped.
  const harness = makeRenamedOrCopiedHarness('copied', 'copy.go', { patch: patchFor(trackedLine('nosec G404')) });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.created[0].title, 'ci: track inline suppression in copy.go:4');
});

test('fails closed on a patchless copy instead of silently skipping it', async () => {
  // Unlike a pure rename (same occurrence, just moved -- safe to skip
  // when patchless), a copy with byte-identical content still introduces
  // a genuinely new occurrence at a new path; if GitHub omits the patch
  // (e.g. because the content already exists elsewhere), there is no
  // record-scoped way to safely skip it, so this must fail closed.
  const harness = makeRenamedOrCopiedHarness('copied', 'copy.go', { additions: 0, deletions: 0 });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'TypeError',
      message: /Inline suppression diff patch is unavailable for copy\.go/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('tracks repeated identical suppressions with distinct fingerprints', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'added',
        patch: [
          '@@ -0,0 +1,6 @@',
          '+package main',
          '+',
          '+func main() {',
          `+${trackedLine('nolint:staticcheck')}`,
          `+${trackedLine('nolint:staticcheck')}`,
          '+}',
          '',
        ].join('\n'),
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 2);
  const markers = harness.calls.created.map((created) => created.body.match(/lopper-inline-suppression:([0-9a-f]+)/)?.[1]);
  assert.equal(new Set(markers).size, 2);
  assert.match(harness.calls.created[0].body, /Location: `main\.go:4`/);
  assert.match(harness.calls.created[1].body, /Location: `main\.go:5`/);
});

test('counts pre-existing identical suppressions before assigning new fingerprints', async () => {
  const existingFingerprint = testables.fingerprintFor('main.go', trackedLine('nolint:staticcheck'), 1);
  const newFingerprint = testables.fingerprintFor('main.go', trackedLine('nolint:staticcheck'), 2);
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'modified',
        patch: [
          '@@ -1,5 +1,6 @@',
          ' package main',
          ' ',
          ' func main() {',
          ` ${trackedLine('nolint:staticcheck')}`,
          `+${trackedLine('nolint:staticcheck')}`,
          ' }',
          '',
        ].join('\n'),
      },
    ],
    searchItems: ({ marker }) => {
      if (marker === `lopper-inline-suppression:${existingFingerprint}`) {
        return [
          {
            number: 77,
            body: `<!-- ${marker} -->\n\n## Inline analysis suppression tracking`,
            user: { login: 'github-actions[bot]', type: 'Bot' },
          },
        ];
      }
      return [];
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.updated.length, 0);
  assert.equal(harness.calls.created.length, 1);
  assert.match(harness.calls.created[0].body, new RegExp(`lopper-inline-suppression:${newFingerprint}`));
  assert.match(harness.calls.created[0].body, /Location: `main\.go:5`/);
});

test('ignores forged artifact-shaped data because only pull files are read', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'README.md',
        status: 'modified',
        patch: '@@ -1 +1 @@\n-doc\n+doc\n',
      },
    ],
    pull: {
      artifact: {
        suppressions: [
          {
            file: 'evil.go',
            content: trackedLine(),
          },
        ],
      },
    },
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.deepEqual(harness.calls.infos, ['No inline suppression records were produced.']);
});

test('fails closed when GitHub omits a changed file patch', async () => {
  const harness = makeHarness({
    files: [{ filename: 'main.go', status: 'modified' }],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'TypeError',
      message: /diff patch is unavailable.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed when a trusted diff hunk is malformed', async () => {
  const harness = makeHarness({
    files: [{ filename: 'main.go', status: 'modified', patch: '@@ malformed @@\n+' + trackedLine() }],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'SyntaxError',
      message: /Unable to parse inline suppression diff hunk for main\.go/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed when a pull file patch is truncated', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'modified',
        additions: 5,
        deletions: 0,
        patch: '@@ -0,0 +1,1 @@\n+package main\n',
      },
    ],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /diff patch for main\.go is incomplete or truncated.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed when a pull file patch is present but additions/deletions are missing', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'modified',
        // additions/deletions explicitly unset: without a trustworthy
        // line-count baseline from GitHub, a truncated patch cannot be
        // distinguished from a complete one, so this must fail closed
        // rather than silently skip the completeness check.
        additions: undefined,
        deletions: undefined,
        patch: '@@ -0,0 +1,1 @@\n+package main\n',
      },
    ],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /diff patch for main\.go is incomplete or truncated.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed when pagination does not match the authoritative file count', async () => {
  const harness = makeHarness({ changedFiles: 2 });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /saw 1 changed files but GitHub reports 2.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('fails closed above the GitHub 3000-file pull diff boundary', async () => {
  const harness = makeHarness({ changedFiles: testables.MAX_CHANGED_FILES + 1, files: [] });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: /exceeds the 3000-file trusted diff limit.*refusing to publish tracking mutations/,
    },
  );
  assert.equal(harness.calls.created.length, 0);
});

test('stops fetching occurrences once the record limit is reached', async () => {
  // An untrusted PR that adds far more than MAX_RECORDS properly annotated
  // marker lines must not force one Contents/Blob API call per marker
  // before the limit is enforced -- that could exhaust the write-token
  // workflow's API quota or its job timeout instead of failing promptly.
  const lineCount = testables.MAX_RECORDS + 50;
  const lines = [];
  for (let index = 0; index < lineCount; index += 1) {
    lines.push(`\t_ = ${index} //nolint:staticcheck // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard`);
  }
  const patch = `@@ -0,0 +1,${lineCount + 4} @@\n+package main\n+\n+func main() {\n${lines.map((line) => `+${line}`).join('\n')}\n+}\n`;
  const harness = makeHarness({
    files: [
      {
        filename: 'main.go',
        status: 'added',
        patch,
      },
    ],
  });

  await assert.rejects(
    () => trackInlineSuppressions(harness.args),
    {
      name: 'RangeError',
      message: new RegExp(`exceed the ${testables.MAX_RECORDS}-record publication limit`),
    },
  );
  assert.equal(harness.calls.created.length, 0);
  assert.ok(
    harness.calls.contentFetches <= testables.MAX_RECORDS,
    `expected at most ${testables.MAX_RECORDS} content fetches, got ${harness.calls.contentFetches}`,
  );
});

test('skips the head-content fetch entirely for files with no possible marker', async () => {
  // A PR with many changed source files, none of which contain any
  // marker-shaped text, must not pay for a Contents/Blob API fetch on
  // every one of them regardless -- up to the 3000-file trusted diff
  // limit, that could exhaust the workflow's API quota or job timeout
  // before publishing anything.
  const files = [];
  for (let index = 0; index < 20; index += 1) {
    files.push({
      filename: `pkg/file${index}.go`,
      status: 'added',
      patch: `@@ -0,0 +1,3 @@\n+package main\n+\n+func f${index}() { _ = ${index} }\n`,
    });
  }
  const harness = makeHarness({ files });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.equal(harness.calls.contentFetches ?? 0, 0);
});

test('rejects path traversal before issue mutation', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: '../main.go',
        status: 'added',
        patch: patchFor(trackedLine()),
      },
    ],
  });

  await assert.rejects(() => trackInlineSuppressions(harness.args), {
    name: 'TypeError',
    message: /Invalid inline suppression file path/,
  });
  assert.equal(harness.calls.created.length, 0);
});
