import * as assert from "node:assert/strict";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
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

function restoreEnv(name: string, value: string | undefined): void {
  if (value === undefined) {
    delete process.env[name];
    return;
  }
  process.env[name] = value;
}
