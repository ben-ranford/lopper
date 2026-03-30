import * as assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as vscode from "vscode";

import { BinaryResolutionError, LopperRunner } from "../../lopperRunner";

suite("lopper runner", () => {
  test("rejects configured directory paths before execution", async () => {
    const folder = workspaceFolder();
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-runner-dir-"));
    const previousPath = process.env.LOPPER_BINARY_PATH;

    try {
      process.env.LOPPER_BINARY_PATH = tempRoot;
      const runner = createRunner(tempRoot);
      await assert.rejects(
        runner.resolveBinaryPath(folder),
        /LOPPER_BINARY_PATH must point to a file/,
      );
    } finally {
      restoreEnv("LOPPER_BINARY_PATH", previousPath);
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects non-executable configured binaries on unix hosts", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const folder = workspaceFolder();
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-runner-file-"));
    const binaryPath = path.join(tempRoot, "lopper");
    const previousPath = process.env.LOPPER_BINARY_PATH;

    try {
      await writeFile(binaryPath, "#!/bin/sh\nexit 0\n", "utf8");
      await chmod(binaryPath, 0o644);
      process.env.LOPPER_BINARY_PATH = binaryPath;

      const runner = createRunner(tempRoot);
      await assert.rejects(
        runner.resolveBinaryPath(folder),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("LOPPER_BINARY_PATH points to a non-executable file"),
      );
    } finally {
      restoreEnv("LOPPER_BINARY_PATH", previousPath);
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects workspace-local configured binaries in untrusted workspaces", async () => {
    const folder = workspaceFolder();
    const tempRoot = await mkdtemp(path.join(folder.uri.fsPath, ".lopper-untrusted-configured-"));
    const binaryPath = path.join(tempRoot, "lopper");
    const previousPath = process.env.LOPPER_BINARY_PATH;

    try {
      await writeExecutable(binaryPath);
      process.env.LOPPER_BINARY_PATH = binaryPath;

      const runner = createRunner(tempRoot);
      await assert.rejects(
        runner.resolveBinaryPath(folder, false),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("workspace-local binary in an untrusted workspace"),
      );
    } finally {
      restoreEnv("LOPPER_BINARY_PATH", previousPath);
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("skips workspace-local bin/lopper in untrusted workspaces", async function () {
    const folder = workspaceFolder();
    const workspaceBinary = path.join(folder.uri.fsPath, "bin", platformBinaryName());
    const pathRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-runner-path-"));
    const fallbackBinary = path.join(pathRoot, platformBinaryName());
    const previousBinaryPath = process.env.LOPPER_BINARY_PATH;
    const previousPathEnv = process.env.PATH;

    try {
      delete process.env.LOPPER_BINARY_PATH;
      await writeExecutable(workspaceBinary);
      await writeExecutable(fallbackBinary);
      process.env.PATH = joinPathEntries([pathRoot, previousPathEnv]);

      const runner = createRunner(pathRoot);
      const resolvedPath = await runner.resolveBinaryPath(folder, false);
      assert.equal(resolvedPath, fallbackBinary);
    } finally {
      restoreEnv("LOPPER_BINARY_PATH", previousBinaryPath);
      restoreEnv("PATH", previousPathEnv);
      await rm(workspaceBinary, { force: true });
      await rm(pathRoot, { recursive: true, force: true });
    }
  });
});

function workspaceFolder(): vscode.WorkspaceFolder {
  const folder = vscode.workspace.workspaceFolders?.[0];
  assert.ok(folder, "expected a workspace folder for lopper runner tests");
  return folder;
}

function createRunner(storageRoot: string): LopperRunner {
  const context = { globalStorageUri: vscode.Uri.file(storageRoot) } as vscode.ExtensionContext;
  return new LopperRunner({ appendLine: () => undefined }, context);
}

async function writeExecutable(binaryPath: string): Promise<void> {
  await mkdir(path.dirname(binaryPath), { recursive: true });
  await writeFile(binaryPath, "#!/bin/sh\nexit 0\n", "utf8");
  if (process.platform !== "win32") {
    await chmod(binaryPath, 0o755);
  }
}

function joinPathEntries(entries: Array<string | undefined>): string {
  return entries
    .filter((entry): entry is string => entry !== undefined && entry.length > 0)
    .join(path.delimiter);
}

function platformBinaryName(): string {
  return process.platform === "win32" ? "lopper.exe" : "lopper";
}

function restoreEnv(name: string, value: string | undefined): void {
  if (value === undefined) {
    delete process.env[name];
    return;
  }
  process.env[name] = value;
}
