import * as assert from "node:assert/strict";
import { chmod, mkdtemp, realpath, rm, stat, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as vscode from "vscode";

import { __testing } from "../../extension";
import type { WorkspaceAnalysis } from "../../lopperRunner";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

suite("refresh lifecycle", () => {
  test("reuses in-flight refreshes for identical requests", async function () {
    this.timeout(30_000);

    const folder = workspaceFolder();
    const document = await workspaceDocument(folder);
    const { binaryPath, signature, cleanup } = await createBinaryFixture();
    let analyseCalls = 0;
    const pendingAnalysis = deferred<WorkspaceAnalysis>();

    const controller = __testing.createController({
      analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
        analyseCalls += 1;
        return pendingAnalysis.promise;
      },
    });

    try {
      const firstRefresh = controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
      });
      const secondRefresh = controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
      });

      assert.equal(analyseCalls, 1);

      pendingAnalysis.resolve(
        makeAnalysis({
          folder,
          binaryPath,
          binarySignature: signature,
          dependencyCount: 1,
          usedPercent: 50,
        }),
      );

      await Promise.all([firstRefresh, secondRefresh]);
    } finally {
      controller.dispose();
      await cleanup();
    }
  });

  test("reuses cached analysis when binary signature still matches", async function () {
    this.timeout(30_000);

    const folder = workspaceFolder();
    const document = await workspaceDocument(folder);
    const { binaryPath, signature, cleanup } = await createBinaryFixture();
    let analyseCalls = 0;

    const controller = __testing.createController({
      analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
        analyseCalls += 1;
        return makeAnalysis({
          folder,
          binaryPath,
          binarySignature: signature,
          dependencyCount: 2,
          usedPercent: 66.6,
        });
      },
    });

    try {
      await controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
      });
      await controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
      });

      assert.equal(analyseCalls, 1, "second refresh should reuse cached analysis");
      assert.match(controller.getLatestSummary(), /cached/);
    } finally {
      controller.dispose();
      await cleanup();
    }
  });

  test("suppresses stale runs so older completions cannot overwrite latest state", async function () {
    this.timeout(30_000);

    const folder = workspaceFolder();
    const document = await workspaceDocument(folder);
    const { binaryPath, signature, cleanup } = await createBinaryFixture();
    const deferredAnalyses: Array<Deferred<WorkspaceAnalysis>> = [];

    const controller = __testing.createController({
      analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
        const next = deferred<WorkspaceAnalysis>();
        deferredAnalyses.push(next);
        return next.promise;
      },
    });

    try {
      const firstRefresh = controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
        forceFresh: true,
      });
      const secondRefresh = controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
        forceFresh: true,
      });

      assert.equal(deferredAnalyses.length, 2, "expected two independent fresh runs");

      deferredAnalyses[1].resolve(
        makeAnalysis({
          folder,
          binaryPath,
          binarySignature: signature,
          dependencyCount: 0,
          usedPercent: 0,
          withUnusedImport: false,
        }),
      );
      await secondRefresh;

      deferredAnalyses[0].resolve(
        makeAnalysis({
          folder,
          binaryPath,
          binarySignature: signature,
          dependencyCount: 3,
          usedPercent: 12.5,
          withUnusedImport: true,
        }),
      );
      await firstRefresh;

      assert.match(controller.getLatestSummary(), /0 deps/);
    } finally {
      controller.dispose();
      await cleanup();
    }
  });

  test("forceFresh bypasses cache and starts a new analysis run", async function () {
    this.timeout(30_000);

    const folder = workspaceFolder();
    const document = await workspaceDocument(folder);
    const { binaryPath, signature, cleanup } = await createBinaryFixture();
    let analyseCalls = 0;

    const controller = __testing.createController({
      analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
        analyseCalls += 1;
        return makeAnalysis({
          folder,
          binaryPath,
          binarySignature: signature,
          dependencyCount: 1,
          usedPercent: 75,
        });
      },
    });

    try {
      await controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
      });
      await controller.refreshWorkspace({
        folder,
        document,
        revealErrors: false,
        trigger: "command",
        forceFresh: true,
      });

      assert.equal(analyseCalls, 2, "forceFresh should bypass cache and dedupe");
      assert.doesNotMatch(controller.getLatestSummary(), /cached/);
    } finally {
      controller.dispose();
      await cleanup();
    }
  });
});

function workspaceFolder(): vscode.WorkspaceFolder {
  const folder = vscode.workspace.workspaceFolders?.[0];
  assert.ok(folder, "expected workspace folder for lifecycle tests");
  return folder;
}

async function workspaceDocument(folder: vscode.WorkspaceFolder): Promise<vscode.TextDocument> {
  const fixtureUri = vscode.Uri.file(path.join(folder.uri.fsPath, "src", "index.ts"));
  return vscode.workspace.openTextDocument(fixtureUri);
}

async function createBinaryFixture(): Promise<{
  binaryPath: string;
  signature: string;
  cleanup: () => Promise<void>;
}> {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-vscode-refresh-"));
  const binaryPath = path.join(tempRoot, process.platform === "win32" ? "lopper.exe" : "lopper");
  await writeFile(binaryPath, "binary", "utf8");
  if (process.platform !== "win32") {
    await chmod(binaryPath, 0o755);
  }
  const signature = await binarySignature(binaryPath);
  return {
    binaryPath,
    signature,
    cleanup: async () => rm(tempRoot, { recursive: true, force: true }),
  };
}

async function binarySignature(binaryPath: string): Promise<string> {
  const resolvedPath = await realpath(binaryPath);
  const details = await stat(resolvedPath);
  return `${resolvedPath}:${Math.floor(details.mtimeMs)}`;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolver) => {
    resolve = resolver;
  });
  return { promise, resolve };
}

function makeAnalysis(options: {
  folder: vscode.WorkspaceFolder;
  binaryPath: string;
  binarySignature: string;
  dependencyCount: number;
  usedPercent: number;
  withUnusedImport?: boolean;
}): WorkspaceAnalysis {
  const dependency = {
    name: "scope-lib",
    usedExportsCount: 1,
    totalExportsCount: 2,
    usedPercent: options.usedPercent,
    unusedImports: options.withUnusedImport === false ? [] : [
      {
        name: "chunk",
        locations: [{ file: "src/index.ts", line: 1, column: 1 }],
      },
    ],
  };

  return {
    folder: options.folder,
    binaryPath: options.binaryPath,
    binarySignature: options.binarySignature,
    requestedLanguage: "js-ts",
    scopeMode: "package",
    report: {
      summary: {
        dependencyCount: options.dependencyCount,
        usedPercent: options.usedPercent,
      },
      dependencies: options.dependencyCount === 0 ? [] : [dependency],
    },
    codemodsByDependency: new Map(),
  };
}
