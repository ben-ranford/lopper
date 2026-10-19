'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const cp = require('node:child_process');
const { gitBlobIdentity } = require('./queue_me_git_object');
const ci = require('./queue_me_ci');
const { harness } = require('./testdata/queue_waiting/ci_fixture.cjs');
const nativeSupported = ['linux', 'darwin'].includes(process.platform);
const emptyOID = 'e69de29bb2d1d6434b8b29ae775ad8c2e48c5391';
const normal = stdout => ({ status: 0, signal: null, stdout: Buffer.from(stdout), stderr: Buffer.alloc(0) });

function noEffects(t) {
  const temporary = t.mock.method(fs, 'mkdtempSync', () => assert.fail('unexpected temporary directory'));
  const process = t.mock.method(cp, 'spawnSync', () => assert.fail('unexpected backend process'));
  return () => {
    assert.equal(temporary.mock.callCount(), 0);
    assert.equal(process.mock.callCount(), 0);
  };
}

test('imports and ordinary API/readiness have zero backend effects on unsupported hosts', async t => {
  t.mock.method(os, 'platform', () => 'win32');
  const assertNone = noEffects(t);
  const filename = require.resolve('./queue_me_git_object');
  const prior = require.cache[filename];
  try {
    delete require.cache[filename];
    assert.equal(typeof require(filename).gitBlobIdentity, 'function');
    assert.deepEqual(Object.keys(ci).sort(), ['assertUnchangedCI', 'checkReadiness', 'isWaiting', 'testables', 'verifyCI']);
    await ci.verifyCI(harness().input);
    assert.equal((await ci.checkReadiness(harness().input)).state, 'READY');
    assert.equal(typeof require('./suppression_provenance'), 'function');
    assert.equal(typeof require('./suppression_provenance').isDeferred, 'function');
    assertNone();
  } finally { require.cache[filename] = prior; }
});

test('actual unsupported invocation and invalid inputs have zero effects', t => {
  const assertNone = noEffects(t);
  for (const input of [undefined, '', new Uint8Array(1), Buffer.alloc(2097153)]) {
    assert.throws(() => gitBlobIdentity(input), /bounded Buffer/);
  }
  if (nativeSupported) t.mock.method(os, 'platform', () => 'win32');
  assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/);
  assertNone();
});

test('trusted Git preserves independent literal and maximum-size object identities', () => {
  if (!nativeSupported) {
    assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/);
    return;
  }
  for (const [bytes, oid] of [[Buffer.alloc(0), emptyOID],
    [Buffer.from('hello\n'), 'ce013625030ba8dba906f756967f9e9ca394464a'],
    [Buffer.from('hello\r\n'), 'ef0493b275aa2080237f676d2ef6559246f56636'],
    [Buffer.from('00ff800d0a', 'hex'), 'f21a905eaf587067e88aadfe3cda06c916e532a6'],
    [Buffer.alloc(2097152, 0x78), 'c53659eed4add508990a03abebdebad2574b32e5']]) {
    assert.equal(gitBlobIdentity(bytes), oid);
  }
});

function rejectedProcess(t, metadata, object, expected) {
  t.mock.method(os, 'platform', () => 'linux');
  const directories = [];
  const spawn = t.mock.method(cp, 'spawnSync', (_executable, argv, options) => {
    directories.push(options.cwd);
    return argv[0] === 'version' ? metadata : object;
  });
  assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), expected);
  for (const directory of directories) assert.equal(fs.existsSync(directory), false, 'joined failure directory leaked');
  return spawn.mock.callCount();
}

test('backend admission rejects absent duplicate unsafe and malformed declarations before object hashing', async t => {
  for (const text of ['', 'SHA-1: openssl\n', 'SHA-1: SHA1_DC\nSHA-1: openssl\n',
    'SHA-1: SHA1_DC\nSHA-1: SHA1_DC\n', 'SHA-1: SHA1_DC\r\n']) {
    await t.test(JSON.stringify(text), child => {
      assert.equal(rejectedProcess(child, normal(text), normal(`${emptyOID}\n`), /requires SHA1_DC/), 1);
    });
  }
});

test('valid-looking identities cannot override process failures or output bounds', async t => {
  const failures = [ ['nonzero', { status: 1 }], ['null status', { status: null }],
    ['signal', { signal: 'SIGKILL' }], ['timeout', { error: Object.assign(new Error('timeout'), { code: 'ETIMEDOUT' }) }],
    ['stderr', { stderr: Buffer.from('private diagnostic') }], ['overflow', { stdout: Buffer.alloc(4097) }],
    ['nonbuffer', { stdout: emptyOID }] ];
  for (const [name, change] of failures) {
    await t.test(name, child => {
      assert.equal(rejectedProcess(child, normal('SHA-1: SHA1_DC\n'), { ...normal(`${emptyOID}\n`), ...change }, /verification/), 2);
    });
  }
  await t.test('metadata overflow', child => {
    assert.equal(rejectedProcess(child, normal(' '.repeat(65537)), normal(`${emptyOID}\n`), /invalid output/), 1);
  });
});

test('identity output requires exact lowercase ASCII and one LF', async t => {
  for (const output of [emptyOID, `${emptyOID}\r\n`, `${emptyOID}\n\n`, `${emptyOID.toUpperCase()}\n`,
    `${'a'.repeat(64)}\n`, `prefix${emptyOID}\n`, Buffer.from([...Buffer.from(`${emptyOID}\n`)].map(x => x | 0x80))]) {
    await t.test(Buffer.from(output).toString('hex'), child => {
      rejectedProcess(child, normal('SHA-1: SHA1_DC\n'), normal(output), /malformed identity/);
    });
  }
});

test('cleanup failure is fatal and original verification failure is preserved', async t => {
  for (const fails of [false, true]) {
    await t.test(fails ? 'primary preserved' : 'success cannot hide cleanup failure', child => {
      child.mock.method(os, 'platform', () => 'linux');
      let owned;
      child.mock.method(cp, 'spawnSync', (_exe, argv, options) => {
        owned = options.cwd;
        if (fails) return { ...normal('SHA-1: SHA1_DC\n'), status: 1 };
        return normal(argv[0] === 'version' ? 'SHA-1: SHA1_DC\n' : `${emptyOID}\n`);
      });
      const remove = fs.rmSync;
      child.mock.method(fs, 'rmSync', () => { throw new Error('injected cleanup refusal'); });
      try {
        assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), fails ? /process failed/ : /cleanup refusal/);
      } finally { if (owned) remove(owned, { recursive: true }); }
    });
  }
});

test('backend directory writes are rejected and removed after the joined call', t => {
  t.mock.method(os, 'platform', () => 'linux');
  let owned;
  t.mock.method(cp, 'spawnSync', (_exe, _argv, options) => {
    owned = options.cwd;
    fs.writeFileSync(path.join(owned, 'unexpected'), 'owned');
    return normal('SHA-1: SHA1_DC\n');
  });
  assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /changed its private directory/);
  assert.equal(fs.existsSync(owned), false);
});

function restoreEnvironment(saved) {
  for (const [key, value] of saved) {
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
}

test('native Git ignores hostile inherited configuration loader and enclosing repository filters', () => {
  if (!nativeSupported) { assert.throws(() => gitBlobIdentity(Buffer.alloc(0)), /unsupported/); return; }
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'lopper-git-context-'));
  const saved = ['PATH', 'HOME', 'GIT_DIR', 'GIT_CONFIG_COUNT', 'GIT_CONFIG_PARAMETERS',
    'GIT_DEFAULT_HASH', 'LD_PRELOAD', 'DYLD_INSERT_LIBRARIES', 'TMPDIR'].map(key => [key, process.env[key]]);
  try {
    const initialized = cp.spawnSync('/usr/bin/git', ['init', '--object-format=sha256', directory], {
      env: { PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' },
      timeout: 10000, killSignal: 'SIGKILL', maxBuffer: 65536 });
    assert.ifError(initialized.error);
    assert.equal(initialized.status, 0, initialized.stderr?.toString());
    const marker = path.join(directory, 'filter-executed');
    fs.writeFileSync(path.join(directory, '.gitattributes'), '* filter=hostile\n');
    fs.appendFileSync(path.join(directory, '.git/config'), `\n[filter "hostile"]\n\tclean = touch '${marker}'; cat\n`);
    for (const [key] of saved) process.env[key] = '/invalid/hostile';
    process.env.GIT_DIR = path.join(directory, '.git');
    process.env.GIT_DEFAULT_HASH = 'sha256';
    process.env.GIT_CONFIG_COUNT = '999';
    process.env.TMPDIR = directory;
    assert.equal(gitBlobIdentity(Buffer.from('hello\n')), 'ce013625030ba8dba906f756967f9e9ca394464a');
    assert.equal(fs.existsSync(marker), false);
    assert.deepEqual(fs.readdirSync(directory).sort(), ['.git', '.gitattributes']);
  } finally {
    restoreEnvironment(saved);
    fs.rmSync(directory, { recursive: true });
  }
});
