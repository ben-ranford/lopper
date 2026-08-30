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
          const file = files.find((candidate) => candidate.filename === filePath);
          const content = reconstructFileFromPatch(file?.patch);
          return { data: { type: 'file', content: Buffer.from(content, 'utf8').toString('base64') } };
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
        payload: { pull_request: pull },
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
  ];
  for (const line of ignoredLines) {
    assert.equal(testables.hasInlineSuppressionMarker(line), false, line);
  }
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

test('scans renamed source files for tracked suppressions', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'renamed.go',
        previous_filename: 'main.go',
        status: 'renamed',
        patch: patchFor(trackedLine('nosec G404')),
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 1);
  assert.equal(harness.calls.created[0].title, 'ci: track inline suppression in renamed.go:4');
});

test('skips patchless pure source renames', async () => {
  const harness = makeHarness({
    files: [
      {
        filename: 'renamed.go',
        previous_filename: 'main.go',
        status: 'renamed',
        additions: 0,
        deletions: 0,
      },
    ],
  });

  await trackInlineSuppressions(harness.args);

  assert.equal(harness.calls.created.length, 0);
  assert.deepEqual(harness.calls.infos, ['No inline suppression records were produced.']);
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
