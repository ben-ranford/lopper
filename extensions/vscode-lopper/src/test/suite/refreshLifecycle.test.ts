import * as assert from "node:assert/strict";
import { chmod, mkdtemp, realpath, rm, stat, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as vscode from "vscode";

import { __testing } from "../../extension";
import type { RefreshWorkspaceOptions } from "../../extension";
import type { WorkspaceAnalysis, WorkspaceAnalysisRunner } from "../../lopperRunner";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

interface LifecycleHarness {
  folder: vscode.WorkspaceFolder;
  document: vscode.TextDocument;
  binaryPath: string;
  signature: string;
}

type TestController = ReturnType<typeof __testing.createController>;

suite("refresh lifecycle", () => {
  test("reuses in-flight refreshes for identical requests", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      let analyseCalls = 0;
      const pendingAnalysis = deferred<WorkspaceAnalysis>();

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            analyseCalls += 1;
            return pendingAnalysis.promise;
          },
        },
        async (controller) => {
          const firstRefresh = refresh(controller, harness);
          const secondRefresh = refresh(controller, harness);

          assert.equal(analyseCalls, 1);

          pendingAnalysis.resolve(makeAnalysis(harness, { dependencyCount: 1, usedPercent: 50 }));
          await Promise.all([firstRefresh, secondRefresh]);
        },
      );
    });
  });

  test("reuses cached analysis when binary signature still matches", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      let analyseCalls = 0;

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            analyseCalls += 1;
            return makeAnalysis(harness, { dependencyCount: 2, usedPercent: 66.6 });
          },
        },
        async (controller) => {
          await refresh(controller, harness);
          await refresh(controller, harness);

          assert.equal(analyseCalls, 1, "second refresh should reuse cached analysis");
          assert.match(controller.getLatestSummary(), /cached/);
        },
      );
    });
  });

  test("suppresses stale runs so older completions cannot overwrite latest state", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const deferredAnalyses: Array<Deferred<WorkspaceAnalysis>> = [];

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            const next = deferred<WorkspaceAnalysis>();
            deferredAnalyses.push(next);
            return next.promise;
          },
        },
        async (controller) => {
          const firstRefresh = refresh(controller, harness, { forceFresh: true });
          const secondRefresh = refresh(controller, harness, { forceFresh: true });

          assert.equal(deferredAnalyses.length, 2, "expected two independent fresh runs");

          deferredAnalyses[1].resolve(
            makeAnalysis(harness, {
              dependencyCount: 0,
              usedPercent: 0,
              withUnusedImport: false,
            }),
          );
          await secondRefresh;

          deferredAnalyses[0].resolve(
            makeAnalysis(harness, {
              dependencyCount: 3,
              usedPercent: 12.5,
              withUnusedImport: true,
            }),
          );
          await firstRefresh;

          assert.match(controller.getLatestSummary(), /0 deps/);
        },
      );
    });
  });

  test("forceFresh bypasses cache and starts a new analysis run", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      let analyseCalls = 0;

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            analyseCalls += 1;
            return makeAnalysis(harness, { dependencyCount: 1, usedPercent: 75 });
          },
        },
        async (controller) => {
          await refresh(controller, harness);
          await refresh(controller, harness, { forceFresh: true });

          assert.equal(analyseCalls, 2, "forceFresh should bypass cache and dedupe");
          assert.doesNotMatch(controller.getLatestSummary(), /cached/);
        },
      );
    });
  });

  test("skips unused import diagnostics for out-of-workspace file paths", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const escapedPath = "../outside.ts";

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            return makeAnalysis(harness, {
              dependencyCount: 1,
              usedPercent: 50,
              unusedImportLocations: [{ file: escapedPath, line: 1, column: 1 }],
            });
          },
        },
        async (controller) => {
          await refresh(controller, harness);

          const diagnostics = vscode.languages.getDiagnostics(harness.document.uri).filter((item) => item.source === "lopper");
          assert.equal(diagnostics.length, 0);
        },
      );
    });
  });

  test("skips escaped codemod suggestions while keeping in-workspace diagnostics", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const validSuggestionPath = "src/index.ts";
      const escapedSuggestionPath = "../outside.ts";

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            const analysis = makeAnalysis(harness, {
              dependencyCount: 1,
              usedPercent: 50,
              withUnusedImport: true,
            });
            analysis.codemodsByDependency = new Map([
              [
                analysis.report.dependencies[0]!.name,
                {
                  mode: "replace",
                  suggestions: [
                    {
                      file: escapedSuggestionPath,
                      line: 1,
                      importName: "scope-lib",
                      fromModule: "scope-lib",
                      toModule: "scope-lib",
                      original: "import scope-lib from \"scope-lib\";",
                      replacement: "import scope-lib from \"scope-lib\";",
                    },
                    {
                      file: validSuggestionPath,
                      line: 1,
                      importName: "chunk",
                      fromModule: "scope-lib",
                      toModule: "scope-lib/chunk",
                      original: "import chunk from \"scope-lib\";",
                      replacement: "import chunk from \"scope-lib/chunk\";",
                    },
                  ],
                },
              ],
            ]);
            return analysis;
          },
        },
        async (controller) => {
          await refresh(controller, harness);

          const diagnostics = vscode.languages.getDiagnostics(harness.document.uri).filter((item) => item.source === "lopper");
          const codemodDiagnostics = diagnostics.filter((item) => item.message.includes("subpath import"));
          assert.equal(codemodDiagnostics.length, 1);
          assert.equal(diagnostics.length, 2);
        },
      );
    });
  });
});

async function withHarness(run: (harness: LifecycleHarness) => Promise<void>): Promise<void> {
  const folder = primaryWorkspaceFolder();
  const document = await workspaceDocument(folder);
  const { binaryPath, signature, cleanup } = await createBinaryFixture();
  try {
    await run({ folder, document, binaryPath, signature });
  } finally {
    await cleanup();
  }
}

async function withController(
  runner: WorkspaceAnalysisRunner,
  run: (controller: TestController) => Promise<void>,
): Promise<void> {
  const controller = __testing.createController(runner);
  try {
    await run(controller);
  } finally {
    controller.dispose();
  }
}

function refresh(
  controller: TestController,
  harness: LifecycleHarness,
  options: Partial<RefreshWorkspaceOptions> = {},
): Promise<void> {
  return controller.refreshWorkspace({
    folder: harness.folder,
    document: harness.document,
    revealErrors: false,
    trigger: "command",
    ...options,
  });
}

function primaryWorkspaceFolder(): vscode.WorkspaceFolder {
  const folder = vscode.workspace.workspaceFolders?.[0];
  assert.ok(folder, "expected workspace folder for lifecycle tests");
  return folder;
}

async function workspaceDocument(folder: vscode.WorkspaceFolder): Promise<vscode.TextDocument> {
  return vscode.workspace.openTextDocument(vscode.Uri.file(path.join(folder.uri.fsPath, "src", "index.ts")));
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

function makeAnalysis(
  harness: LifecycleHarness,
  options: {
    dependencyCount: number;
    usedPercent: number;
    withUnusedImport?: boolean;
    unusedImportLocations?: Array<{ file: string; line: number; column: number }>;
  },
): WorkspaceAnalysis {
  return makeWorkspaceAnalysis({
    folder: harness.folder,
    binaryPath: harness.binaryPath,
    binarySignature: harness.signature,
    ...options,
  });
}

function makeWorkspaceAnalysis(options: {
  folder: vscode.WorkspaceFolder;
  binaryPath: string;
  binarySignature: string;
  dependencyCount: number;
  usedPercent: number;
  withUnusedImport?: boolean;
  unusedImportLocations?: Array<{ file: string; line: number; column: number }>;
}): WorkspaceAnalysis {
  const unusedImportLocations = options.unusedImportLocations ?? [{ file: "src/index.ts", line: 1, column: 1 }];
  const dependency = {
    name: "scope-lib",
    usedExportsCount: 1,
    totalExportsCount: 2,
    usedPercent: options.usedPercent,
    unusedImports:
      options.withUnusedImport === false
        ? []
        : [{ name: "chunk", locations: unusedImportLocations }],
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
