import { readFile, writeFile } from "node:fs/promises";
import * as cp from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(import.meta.url);
export const EXPECTED_TEST_ELECTRON_VERSION = "3.1.0";
export const KNOWN_BROKEN_DECOMPRESSOR = `function spawnDecompressorChild(command, args, input) {
    return new Promise((resolve, reject) => {
        const child = cp.spawn(command, args, { stdio: 'pipe' });
        if (input) {
            input.on('error', reject);
            input.pipe(child.stdin);
        }
        child.stderr.pipe(process.stderr);
        child.stdout.pipe(process.stdout);
        child.on('error', reject);
        child.on('exit', (code) => code === 0 ? resolve() : reject(new Error(\`Failed to unzip archive, exited with \${code}\`)));
    });
}`;

export function spawnDecompressorChild(command, args, input, spawn = cp.spawn) {
  return new Promise((resolve, reject) => {
    const { finished } = require("node:stream");
    const child = spawn(command, args, { stdio: "pipe" });
    let settled = false;
    let failure;
    let failureSource;
    const lateInputErrors = [];
    let childClosed = false;
    let childResult;
    let inputFinished = !input;
    let inputTimedOut = false;
    let killTimer;
    let inputTimer;
    let inputCleanup;

    const finish = (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(inputTimer);
      child.stdin.removeListener("error", onStdinError);
      child.removeListener("error", onChildError);
      child.removeListener("close", onChildClose);
      if (inputFinished) {
        inputCleanup?.();
        if (!inputTimedOut) input?.removeListener("error", onInputError);
      }
      if (error) reject(error);
      else resolve();
    };

    const settleWhenComplete = () => {
      if (!childClosed || !inputFinished) return;
      const errors = [];
      if (failure) errors.push(failure);
      if (childResult && childResult !== failure) errors.push(childResult);
      errors.push(...lateInputErrors);
      if (errors.length > 1) finish(new AggregateError(errors, errors[0].message));
      else if (errors.length === 1) finish(errors[0]);
      else finish();
    };

    const stopInput = () => {
      if (!input) return;
      input.unpipe(child.stdin);
      if (typeof input.pause === "function") input.pause();
      if (typeof input.destroy === "function" && !input.destroyed) input.destroy();
    };

    const fail = (error, source) => {
      if (failure) {
        if (failureSource === "stdin" && failure.code === "EPIPE" && (source === "input" || source === "child") && error.code !== "EPIPE") {
          failure = error;
          failureSource = source;
        }
        return;
      }
      failure = error;
      failureSource = source;
      stopInput();
      child.stdin.destroy();
      if (child.exitCode !== null || child.signalCode !== null) return;
      try {
        child.kill("SIGTERM");
      } catch {}
      killTimer = setTimeout(() => {
        if (child.exitCode === null && child.signalCode === null) {
          try {
            child.kill("SIGKILL");
          } catch {}
        }
      }, 250);
      killTimer.unref?.();
    };

    const onInputError = (error) => {
      if (settled) {
        process.emitWarning(error.message, { type: "LateDecompressorInputError" });
        input.removeListener("error", onInputError);
      } else if (childClosed) {
        lateInputErrors.push(error);
      } else {
        fail(error, "input");
      }
    };
    const onStdinError = (error) => fail(error, "stdin");
    const onChildError = (error) => fail(error, "child");
    const onInputFinished = (error) => {
      inputFinished = true;
      if (error && error !== failure && !lateInputErrors.includes(error)) {
        if (childClosed) lateInputErrors.push(error);
        else if (!failure) {
          fail(error, "input");
        }
      }
      settleWhenComplete();
    };
    const onChildClose = (code, signal) => {
      childClosed = true;
      clearTimeout(killTimer);
      stopInput();
      if (code !== 0 && failureSource === "stdin" && failure?.code === "EPIPE") {
        failure = new Error(`Failed to unzip archive, exited with ${code ?? signal}`);
        failureSource = "child";
      } else if (code !== 0 && (!failure || code !== null)) {
        childResult = new Error(`Failed to unzip archive, exited with ${code ?? signal}`);
      }
      if (!inputFinished) {
        inputTimer = setTimeout(() => {
          inputTimedOut = true;
          inputFinished = true;
          lateInputErrors.push(new Error("Timed out waiting for decompressor input cleanup"));
          inputCleanup?.();
          settleWhenComplete();
        }, 1000);
      }
      settleWhenComplete();
    };
    child.stderr.pipe(process.stderr);
    child.stdout.pipe(process.stdout);
    child.on("error", onChildError);
    child.stdin.on("error", onStdinError);
    child.on("close", onChildClose);
    if (input) {
      input.on("error", onInputError);
      inputCleanup = finished(input, { cleanup: true }, onInputFinished);
      input.pipe(child.stdin);
    }
  });
}

export function transformTestElectronSource(source, version) {
  if (version !== EXPECTED_TEST_ELECTRON_VERSION) {
    throw new Error(`Expected @vscode/test-electron ${EXPECTED_TEST_ELECTRON_VERSION}; found ${version}. Review its decompressor lifecycle before updating this test patch.`);
  }
  const patchedFunction = `function spawnDecompressorChild(command, args, input) {\n    return (${spawnDecompressorChild.toString()})(command, args, input);\n}`;
  const occurrences = source.split(KNOWN_BROKEN_DECOMPRESSOR).length - 1;
  const patchedOccurrences = source.split(patchedFunction).length - 1;
  if (occurrences === 0 && patchedOccurrences === 1) return source;
  if (occurrences !== 1 || patchedOccurrences !== 0) {
    throw new Error(`Could not safely patch @vscode/test-electron ${EXPECTED_TEST_ELECTRON_VERSION}: expected one original or one repaired decompressor function, found ${occurrences} original and ${patchedOccurrences} repaired.`);
  }
  return source.replace(KNOWN_BROKEN_DECOMPRESSOR, patchedFunction);
}

export async function patchTestElectronDecompressor(packageJsonPath = require.resolve("@vscode/test-electron/package.json")) {
  const packageJson = JSON.parse(await readFile(packageJsonPath, "utf8"));
  const sourcePath = path.join(path.dirname(packageJsonPath), "out", "download.js");
  const source = await readFile(sourcePath, "utf8");
  const patchedSource = transformTestElectronSource(source, packageJson.version);
  if (patchedSource !== source) await writeFile(sourcePath, patchedSource, "utf8");
}
