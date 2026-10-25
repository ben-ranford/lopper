import assert from "node:assert/strict";
import * as cp from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { gzipSync } from "node:zlib";
import { PassThrough, Readable } from "node:stream";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

import {
  EXPECTED_TEST_ELECTRON_VERSION,
  KNOWN_BROKEN_DECOMPRESSOR,
  patchTestElectronDecompressor,
  spawnDecompressorChild,
  transformTestElectronSource,
} from "./decompressorLifecycle.mjs";

async function waitForFile(filePath) {
  const until = Date.now() + 5000;
  while (Date.now() < until) {
    try {
      return await readFile(filePath, "utf8");
    } catch {
      await delay(5);
    }
  }
  throw new Error(`Timed out waiting for ${filePath}`);
}

async function waitForPidExit(pid) {
  const until = Date.now() + 5000;
  while (Date.now() < until) {
    try {
      process.kill(pid, 0);
    } catch (error) {
      if (error.code === "ESRCH") return;
      throw error;
    }
    await delay(5);
  }
  throw new Error(`Child process ${pid} was not reaped`);
}

for (const { name, closeInput, expectedError } of [
  {
    name: "input failure waits for a ready TERM-ignoring child to be reaped before retry",
    closeInput: (input) => input.destroy(new Error("simulated truncated archive")),
    expectedError: /simulated truncated archive/,
  },
  {
    name: "premature input close reaps a ready TERM-ignoring child before retry",
    closeInput: (input) => input.destroy(),
    expectedError: /Premature close/,
  },
]) {
  test(name, async () => {
    const tempDir = await mkdtemp(path.join(os.tmpdir(), "lopper-decompressor-child-"));
    const readyPath = path.join(tempDir, "ready");
    const termPath = path.join(tempDir, "term");
    const input = new PassThrough();
    const childCode = [
      "const fs = require('node:fs');",
      `process.on('SIGTERM', () => fs.writeFileSync(${JSON.stringify(termPath)}, 'term'));`,
      `fs.writeFileSync(${JSON.stringify(readyPath)}, String(process.pid));`,
      "setInterval(() => {}, 1000);",
    ].join(" ");
    let childPid;
    let timer;
    const pending = spawnDecompressorChild(process.execPath, ["-e", childCode], input);
    try {
      childPid = Number(await waitForFile(readyPath));
      input.write(Buffer.alloc(4096));
      closeInput(input);
      const deadline = new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error("decompressor did not terminate after input closure")), 5000);
      });
      await assert.rejects(Promise.race([pending, deadline]), expectedError);
      assert.equal(await readFile(termPath, "utf8"), "term", "TERM escalation should be exercised");
      await waitForPidExit(childPid);

      // Model the package retry: prove the first child is already gone before
      // the next decompressor starts.
      await spawnDecompressorChild(process.execPath, ["-e", "process.stdin.resume()"], Readable.from(["retry archive"]));
    } finally {
      clearTimeout(timer);
      if (childPid) {
        try {
          process.kill(childPid, "SIGKILL");
        } catch {}
        await waitForPidExit(childPid).catch(() => {});
      }
      await rm(tempDir, { recursive: true, force: true });
    }
  });
}

test("successful decompression completes; nonzero exit wins over secondary stdin EPIPE", async () => {
  const tempDir = await mkdtemp(path.join(os.tmpdir(), "lopper-decompressor-success-"));
  try {
    const outputPath = path.join(tempDir, "extracted.txt");
    const childCode = `process.stdin.pipe(require('node:zlib').createGunzip()).pipe(require('node:fs').createWriteStream(${JSON.stringify(outputPath)}));`;
    await spawnDecompressorChild(process.execPath, ["-e", childCode], Readable.from([gzipSync("extracted successfully")]));
    assert.equal(await readFile(outputPath, "utf8"), "extracted successfully");
  } finally {
    await rm(tempDir, { recursive: true, force: true });
  }
  const largeInput = Readable.from([Buffer.alloc(4 * 1024 * 1024)]);
  let sawEpipe = false;
  const observeEpipe = (...args) => {
    const child = cp.spawn(...args);
    child.stdin.on("error", (error) => {
      if (error.code === "EPIPE") sawEpipe = true;
    });
    return child;
  };
  await assert.rejects(
    spawnDecompressorChild(process.execPath, ["-e", "process.exit(7)"], largeInput, observeEpipe),
    /exited with 7/,
  );
  assert.equal(sawEpipe, true, "the failure ordering case must actually observe stdin EPIPE");
});

test("spawn failures reject and late input errors during cleanup are observed", async () => {
  await assert.rejects(
    spawnDecompressorChild("lopper-command-that-does-not-exist", [], Readable.from(["archive"])),
    /lopper-command-that-does-not-exist/,
  );

  class DelayedDestroyReadable extends Readable {
    _read() {}
    _destroy(_error, callback) {
      setTimeout(() => this.emit("error", new Error("late stream error")), 10);
      setTimeout(() => callback(null), 50);
    }
  }
  const lateInput = new DelayedDestroyReadable();
  let childPid;
  const captureChild = (...args) => {
    const child = cp.spawn(...args);
    childPid = child.pid;
    return child;
  };
  await assert.rejects(
    spawnDecompressorChild(process.execPath, ["-e", "process.exit(9)"], lateInput, captureChild),
    (error) => {
      assert.equal(error instanceof AggregateError, true);
      assert.equal(error.errors.length, 2);
      assert.match(error.errors.find((item) => /exited with 9/.test(item.message)).message, /exited with 9/);
      assert.match(error.errors.find((item) => /late stream error/.test(item.message)).message, /late stream error/);
      return true;
    },
  );
  assert.equal(lateInput.listenerCount("error"), 0, "cleanup removes the temporary stream listener after close");
  await waitForPidExit(childPid);
  await delay(60);

  class CloseDisabledDelayedFailure extends Readable {
    constructor() {
      super({ emitClose: false });
    }
    _read() {}
    _destroy(_error, callback) {
      setTimeout(() => callback(new Error("late close-disabled input failure")), 20);
    }
  }
  let closeDisabledPid;
  const captureCloseDisabledChild = (...args) => {
    const child = cp.spawn(...args);
    closeDisabledPid = child.pid;
    return child;
  };
  const startedAt = Date.now();
  await assert.rejects(
    spawnDecompressorChild(process.execPath, ["-e", "process.exit(9)"], new CloseDisabledDelayedFailure(), captureCloseDisabledChild),
    (error) => {
      assert.equal(error instanceof AggregateError, true);
      assert.equal(error.errors.length, 2);
      assert.match(error.errors.find((item) => /exited with 9/.test(item.message)).message, /exited with 9/);
      assert.match(error.errors.find((item) => /late close-disabled input failure/.test(item.message)).message, /late close-disabled input failure/);
      return true;
    },
  );
  assert.ok(Date.now() - startedAt < 1000, "a stream with emitClose disabled must not cause an unbounded wait");
  await waitForPidExit(closeDisabledPid);

  class SlowCloseFailure extends Readable {
    constructor() {
      super({ emitClose: false });
    }
    _read() {}
    _destroy(_error, callback) {
      setTimeout(() => callback(new Error("slow close-disabled cleanup error")), 1200);
    }
  }
  let successChildPid;
  const captureSuccessChild = (...args) => {
    const child = cp.spawn(...args);
    successChildPid = child.pid;
    return child;
  };
  let resolveLateWarning;
  const lateWarning = new Promise((resolve) => { resolveLateWarning = resolve; });
  const onWarning = (warning) => {
    if (warning.name === "LateDecompressorInputError") resolveLateWarning(warning);
  };
  process.on("warning", onWarning);
  try {
    await assert.rejects(
      spawnDecompressorChild(process.execPath, ["-e", "process.exit(0)"], new SlowCloseFailure(), captureSuccessChild),
      /Timed out waiting for decompressor input cleanup/,
    );
    await waitForPidExit(successChildPid);
    const warning = await lateWarning;
    assert.match(warning.message, /slow close-disabled cleanup error/);
  } finally {
    process.removeListener("warning", onWarning);
    try {
      process.kill(successChildPid, "SIGKILL");
    } catch {}
    await waitForPidExit(successChildPid).catch(() => {});
  }
});

test("guarded transform patches exactly once and rejects version or source drift", async () => {
  const source = `header\n${KNOWN_BROKEN_DECOMPRESSOR}\nfooter\n`;
  const patched = transformTestElectronSource(source, EXPECTED_TEST_ELECTRON_VERSION);
  assert.notEqual(patched, source);
  assert.equal(transformTestElectronSource(patched, EXPECTED_TEST_ELECTRON_VERSION), patched);
  assert.throws(() => transformTestElectronSource(patched + KNOWN_BROKEN_DECOMPRESSOR, EXPECTED_TEST_ELECTRON_VERSION), /Could not safely patch/);
  assert.throws(() => transformTestElectronSource(patched + patched, EXPECTED_TEST_ELECTRON_VERSION), /Could not safely patch/);
  assert.throws(() => transformTestElectronSource(source + KNOWN_BROKEN_DECOMPRESSOR, EXPECTED_TEST_ELECTRON_VERSION), /Could not safely patch/);
  assert.throws(() => transformTestElectronSource(source, "3.2.0"), /Expected @vscode\/test-electron/);
  assert.throws(() => transformTestElectronSource("unknown source", EXPECTED_TEST_ELECTRON_VERSION), /found 0/);

  const tempDir = await mkdtemp(path.join(os.tmpdir(), "lopper-test-electron-patch-"));
  try {
    const packageJsonPath = path.join(tempDir, "package.json");
    const downloadPath = path.join(tempDir, "out", "download.js");
    await mkdir(path.dirname(downloadPath), { recursive: true });
    await writeFile(packageJsonPath, JSON.stringify({ version: EXPECTED_TEST_ELECTRON_VERSION }));
    await writeFile(downloadPath, source);
    await patchTestElectronDecompressor(packageJsonPath);
    const result = await readFile(downloadPath, "utf8");
    assert.equal(result, patched);
    await patchTestElectronDecompressor(packageJsonPath);
    assert.equal(await readFile(downloadPath, "utf8"), patched);
  } finally {
    await rm(tempDir, { recursive: true, force: true });
  }
});
