'use strict';

const assert = require('node:assert/strict');
const { test, mock } = require('node:test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const childProcess = require('node:child_process');
const probe = require('./probe.cjs');

function successful(stdout) {
  return { status: 0, signal: null, stdout: Buffer.from(stdout), stderr: Buffer.alloc(0) };
}

function unsupported() {
  const temp = mock.method(fs, 'mkdtempSync', () => assert.fail('unsupported platform allocated a directory'));
  const spawn = mock.method(childProcess, 'spawnSync', () => assert.fail('unsupported platform started a process'));
  mock.method(os, 'platform', () => 'win32');
  try {
    assert.throws(() => probe.runProbe(), /Unsupported native Git backend platform/);
    assert.equal(temp.mock.callCount(), 0);
    assert.equal(spawn.mock.callCount(), 0);
  } finally { mock.restoreAll(); }
}

if (process.argv.includes('--native')) {
  if (['linux', 'darwin'].includes(process.platform)) {
    try { console.log(JSON.stringify(probe.runProbe())); } catch (error) {
      console.error(JSON.stringify({ error: String(error), receipt: error.receipt }));
      process.exitCode = 1;
    }
  } else {
    unsupported();
    console.log(JSON.stringify({ admission: 'UNSUPPORTED', platform: process.platform, noSideEffects: true }));
  }
} else {
  test('unsupported invocation rejects before allocation or process', unsupported);

  test('backend requires exactly one supported declaration', () => {
    probe.checkBackend(Buffer.from('git version test\nSHA-1: SHA1_DC\n'));
    for (const text of ['', 'SHA-1: openssl\n', 'SHA-1: SHA1_DC\nSHA-1: openssl\n',
      'SHA-1: SHA1_DC\nSHA-1: SHA1_DC\n', 'SHA-1: SHA1_DC\r\n']) {
      assert.throws(() => probe.checkBackend(Buffer.from(text)), /exactly one SHA1_DC/);
    }
  });

  test('objects preserve exact bytes, one lowercase object ID and LF', () => {
    const expected = probe.goldens[0].oid;
    probe.checkObject(Buffer.from(`${expected}\n`), expected);
    for (const text of [expected, `${expected}\r\n`, `${expected}\n\n`, `${expected.toUpperCase()}\n`,
      `${'a'.repeat(64)}\n`, `${'a'.repeat(40)}\n`]) {
      assert.throws(() => probe.checkObject(Buffer.from(text), expected), /golden mismatch/);
    }
  });

  test('valid-looking stdout never overrides process failure or bounds', () => {
    const output = `${probe.goldens[0].oid}\n`;
    for (const changes of [{ status: 1 }, { status: null }, { signal: 'SIGKILL' },
      { error: Object.assign(new Error('timeout'), { code: 'ETIMEDOUT' }) },
      { stderr: Buffer.from('failure') }, { stdout: Buffer.alloc(4097) }, { stdout: output }]) {
      assert.throws(() => probe.checkResult({ ...successful(output), ...changes }, 4096));
    }
    probe.checkResult(successful(output), 4096);
  });

  test('modelled success preserves literal stdin, isolated process settings and cleanup', () => {
    modelFixedGit();
    let calls = 0;
    mock.method(childProcess, 'spawnSync', (executable, argv, options) => {
      assert.equal(executable, '/usr/bin/git');
      assert.equal(options.timeout, 10000);
      assert.equal(options.killSignal, 'SIGKILL');
      assert.deepEqual(options.env, { PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1',
        GIT_CONFIG_SYSTEM: '/dev/null', GIT_CONFIG_GLOBAL: '/dev/null', GIT_NO_REPLACE_OBJECTS: '1',
        GIT_CEILING_DIRECTORIES: path.dirname(options.cwd) });
      assert.deepEqual(fs.readdirSync(options.cwd), []);
      if (calls++ === 0) {
        assert.deepEqual(argv, ['version', '--build-options']);
        assert.equal(options.maxBuffer, 65536);
        return successful('SHA-1: SHA1_DC\n');
      }
      assert.deepEqual(argv, ['-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
        'hash-object', '-t', 'blob', '--stdin', '--no-filters']);
      assert.equal(options.maxBuffer, 4096);
      const golden = probe.goldens[calls - 2];
      const bytes = golden.hex === undefined ? Buffer.alloc(golden.count, golden.byte) : Buffer.from(golden.hex, 'hex');
      assert.deepEqual(options.input, bytes);
      return successful(`${golden.oid}\n`);
    });
    try {
      const receipt = probe.runProbe();
      assert.equal(calls, 6);
      assert.equal(receipt.admission, 'PASS');
      assert.equal(receipt.cleaned, true);
      assert.equal(fs.existsSync(receipt.cwd), false);
    } finally { mock.restoreAll(); }
  });

  test('unexpected directory writes fail and are cleaned after process completion', () => {
    modelFixedGit();
    mock.method(childProcess, 'spawnSync', (_executable, _argv, options) => {
      fs.writeFileSync(path.join(options.cwd, 'unexpected'), 'owned fixture');
      return successful('SHA-1: SHA1_DC\n');
    });
    try {
      assert.throws(() => probe.runProbe(), error => {
        assert.match(error.message, /wrote into its owned cwd/);
        assert.deepEqual(error.receipt.afterProcess, [['unexpected']]);
        assert.deepEqual(error.receipt.after, ['unexpected']);
        assert.equal(error.receipt.cleaned, true);
        assert.match(error.receipt.cleanupError, /cleanup contract failed/);
        return true;
      });
    } finally { mock.restoreAll(); }
  });

  test('cleanup failure does not replace the original process error', () => {
    modelFixedGit();
    let owned;
    const remove = fs.rmSync;
    mock.method(childProcess, 'spawnSync', (_executable, _argv, options) => {
      owned = options.cwd;
      return { ...successful('SHA-1: SHA1_DC\n'), status: 1 };
    });
    mock.method(fs, 'rmSync', () => { throw new Error('injected cleanup failure'); });
    try {
      assert.throws(() => probe.runProbe(), error => {
        assert.match(error.message, /process did not complete/);
        assert.equal(error.receipt.cleaned, false);
        assert.match(error.receipt.cleanupError, /injected cleanup failure/);
        return true;
      });
    } finally {
      mock.restoreAll();
      if (owned) remove(owned, { recursive: true });
    }
  });

  test('failed process retains its error and joined private-directory cleanup receipt', () => {
    modelFixedGit();
    mock.method(childProcess, 'spawnSync', () => ({ ...successful('SHA-1: SHA1_DC\n'), status: 1 }));
    try {
      assert.throws(() => probe.runProbe(), error => {
        assert.match(error.message, /process did not complete/);
        assert.equal(error.receipt.processes.length, 1);
        assert.deepEqual(error.receipt.afterProcess, [[]]);
        assert.equal(error.receipt.cleaned, true);
        assert.equal(fs.existsSync(error.receipt.cwd), false);
        return true;
      });
    } finally { mock.restoreAll(); }
  });
}

function modelFixedGit() {
  mock.method(os, 'platform', () => 'darwin');
  const stat = { uid: 0, mode: 0o100755, isFile: () => true, isSymbolicLink: () => false };
  for (const name of ['lstatSync', 'statSync', 'realpathSync', 'readFileSync']) {
    const original = fs[name];
    mock.method(fs, name, (file, ...args) => {
      if (file !== '/usr/bin/git') return original(file, ...args);
      if (name === 'realpathSync') return '/usr/bin/git';
      if (name === 'readFileSync') return Buffer.from('modelled binary, not native evidence');
      return stat;
    });
  }
  // Windows does not implement Unix permission bits; this is modelled lifecycle evidence only.
  const original = fs.statSync;
  mock.method(fs, 'statSync', (file, ...args) => {
    const result = original(file, ...args);
    if (file !== '/usr/bin/git') result.mode = (result.mode & ~0o777) | 0o700;
    return result;
  });
}
