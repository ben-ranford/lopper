const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const Module = require('node:module');
const { execFileSync } = require('node:child_process');

function loadProduction() {
  const originalLoad = Module._load;
  Module._load = function (id, ...args) {
    return id === 'vscode' ? {} : originalLoad.call(this, id, ...args);
  };
  try { return require(process.argv[2]); }
  finally { Module._load = originalLoad; }
}

async function checkSizes({ gradle, marker, infer }) {
  await fs.writeFile(gradle, marker);
  assert.equal(await infer(), 'kotlin-android', 'normal Android inference');
  const huge = await fs.open(gradle, 'w');
  try { await huge.write(marker, 8 * 1024 * 1024); }
  finally { await huge.close(); }
  const originalReadFile = fs.readFile;
  let consumed = 0;
  fs.readFile = async (...args) => {
    const result = await originalReadFile(...args);
    if (args[0] === gradle) consumed += Buffer.byteLength(result);
    return result;
  };
  try {
    const oversized = await infer();
    console.log(`oversized Gradle: inferred=${oversized}, readFileBytes=${consumed}`);
    assert.equal(oversized, 'jvm', 'oversized Gradle must not be consumed or recognized');
    assert.equal(consumed, 0, 'oversized Gradle must not use unbounded readFile');
  } finally { fs.readFile = originalReadFile; }
  await fs.writeFile(gradle, ' '.repeat(256 * 1024 - marker.length) + marker);
  assert.equal(await infer(), 'kotlin-android', 'late marker at supported byte limit');
  await fs.appendFile(gradle, ' ');
  assert.equal(await infer(), 'jvm', 'one byte beyond limit');
}

async function checkSpecialFiles({ gradle, external, infer }) {
  await fs.rm(gradle);
  await fs.mkdir(gradle);
  assert.equal(await infer(), 'jvm', 'directory is not a Gradle file');
  await fs.rm(gradle, { recursive: true });
  await fs.symlink(external, gradle);
  assert.equal(await infer(), 'jvm', 'Gradle symlink must not be followed');
  await fs.rm(gradle);
  if (process.platform !== 'win32') {
    execFileSync('mkfifo', [gradle]);
    assert.equal(await infer(), 'jvm', 'FIFO must not block inference');
    await fs.rm(gradle);
  }
}

function injectRead(handle, replacement, gradle) {
  const read = handle.read.bind(handle);
  if (replacement === 'read-error') {
    handle.read = async () => { throw new Error('injected read failure'); };
  } else if (replacement === 'short-read') {
    let calls = 0;
    handle.read = async (buffer, offset, length, position) => {
      calls++;
      assert.ok(calls <= 32, 'short reads must have a bounded operation count');
      return read(buffer, offset, Math.min(length, 1), position);
    };
  } else if (replacement === 'growth') {
    handle.read = async (...readArgs) => {
      await fs.appendFile(gradle, 'x');
      return read(...readArgs);
    };
  }
}

async function replaceGradle({ gradle, marker, external }, replacement) {
  if (replacement === 'error') throw new Error('injected open failure');
  if (replacement === 'regular' || replacement === 'symlink' || replacement === 'fifo') {
    await fs.rename(gradle, gradle + '.old');
    if (replacement === 'regular') await fs.writeFile(gradle, marker);
    else if (replacement === 'symlink') await fs.symlink(external, gradle);
    else execFileSync('mkfifo', [gradle]);
  }
}

async function checkReplacement(fixture, replacement) {
  const { gradle, marker, infer, AndroidModuleSignalCache } = fixture;
  await fs.writeFile(gradle, marker);
  const cache = new AndroidModuleSignalCache();
  const originalOpen = fs.open;
  let injected = false;
  fs.open = async (name, ...args) => {
    if (name !== gradle || injected) return originalOpen(name, ...args);
    injected = true;
    await replaceGradle(fixture, replacement);
    const handle = await originalOpen(name, ...args);
    if (replacement === 'cancel') cache.clear();
    injectRead(handle, replacement, gradle);
    return handle;
  };
  try {
    assert.equal(await infer(cache), 'jvm', `${replacement} during open must discard inference`);
    assert.equal(injected, true, `${replacement} injection reached production open`);
  } finally {
    fs.open = originalOpen;
    await fs.rm(gradle, { force: true });
  }
}

async function checkReplacedAncestor({ root, marker, infer }) {
  const directory = path.join(root, 'replaced');
  await fs.mkdir(directory);
  await fs.writeFile(path.join(directory, 'build.gradle'), marker);
  const originalOpen = fs.open;
  fs.open = async (name, ...args) => {
    if (name !== path.join(directory, 'build.gradle')) return originalOpen(name, ...args);
    await fs.rename(directory, directory + '.old');
    await fs.mkdir(directory);
    const handle = await originalOpen(path.join(directory + '.old', 'build.gradle'), ...args);
    await fs.link(path.join(directory + '.old', 'build.gradle'), name);
    return handle;
  };
  try {
    assert.equal(await infer(undefined, path.join(directory, 'Main.kt')), 'jvm', 'replaced ancestor even with same file identity');
  } finally { fs.open = originalOpen; }
}

async function checkAncestors({ root, gradle, marker, infer }) {
  const linkedDir = path.join(root, 'linked');
  const outside = await fs.mkdtemp(path.join(os.tmpdir(), 'lopper-gradle-outside-'));
  try {
    await fs.writeFile(path.join(outside, 'build.gradle'), marker);
    await fs.symlink(outside, linkedDir, 'dir');
    assert.equal(await infer(undefined, path.join(linkedDir, 'Main.kt')), 'jvm', 'ancestor symlink');
  } finally { await fs.rm(outside, { recursive: true, force: true }); }
  await fs.writeFile(gradle, marker);
  const deep = path.join(root, ...Array(40).fill('nested'));
  await fs.mkdir(deep, { recursive: true });
  assert.equal(await infer(undefined, path.join(deep, 'Main.kt')), 'jvm', 'ancestor work bound');
}

async function main() {
  const { AndroidModuleSignalCache, inferLopperLanguageForDocument } = loadProduction();
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'lopper-gradle-bound-')));
  const marker = 'plugins { id("com.android.application") }';
  const gradle = path.join(root, 'build.gradle');
  const external = path.join(root, 'external.gradle');
  const infer = (cache = new AndroidModuleSignalCache(), file = path.join(root, 'Main.kt')) =>
    inferLopperLanguageForDocument({ fileName: file, languageId: 'kotlin' }, root, cache);
  const fixture = { root, gradle, external, marker, infer, AndroidModuleSignalCache };
  try {
    await fs.writeFile(external, marker);
    await checkSizes(fixture);
    await checkSpecialFiles(fixture);
    const replacements = ['regular', 'symlink', 'cancel', 'error', 'growth', 'short-read', 'read-error'];
    if (process.platform !== 'win32') replacements.push('fifo');
    for (const replacement of replacements) await checkReplacement(fixture, replacement);
    await checkReplacedAncestor(fixture);
    await checkAncestors(fixture);
    console.log('Gradle bounds: normal, exact/+1 size, symlink, special file, replacement, cancellation, errors, growth and ancestors passed');
  } finally { await fs.rm(root, { recursive: true, force: true }); }
}
main().catch((error) => {
  console.error(error);
  if (error.code === 'ERR_ASSERTION') {
    console.error('LOPPER_GRADLE_ASSERTION');
    process.exitCode = 1;
  } else { process.exitCode = 2; }
});
