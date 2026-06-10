import * as assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as vscode from "vscode";

import type { BinaryResolutionRequest } from "../../managedBinary";
import { BinaryResolutionError, LopperCliReportExecutor, LopperRunner, type ReportCommandExecutor } from "../../lopperRunner";
import type { LopperReport } from "../../types";

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

  test("rejects configured symlinks that resolve to workspace-local binaries in untrusted workspaces", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const folder = workspaceFolder();
    const workspaceRoot = await mkdtemp(path.join(folder.uri.fsPath, ".lopper-untrusted-symlink-target-"));
    const workspaceBinary = path.join(workspaceRoot, platformBinaryName());
    const outsideRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-untrusted-symlink-source-"));
    const outsideSymlink = path.join(outsideRoot, platformBinaryName());
    const previousPath = process.env.LOPPER_BINARY_PATH;

    try {
      await writeExecutable(workspaceBinary);
      await symlink(workspaceBinary, outsideSymlink);
      process.env.LOPPER_BINARY_PATH = outsideSymlink;

      const runner = createRunner(outsideRoot);
      await assert.rejects(
        runner.resolveBinaryPath(folder, false),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("workspace-local binary in an untrusted workspace"),
      );
    } finally {
      restoreEnv("LOPPER_BINARY_PATH", previousPath);
      await rm(workspaceRoot, { recursive: true, force: true });
      await rm(outsideRoot, { recursive: true, force: true });
    }
  });

  test("skips workspace-local PATH binaries in untrusted workspaces", async () => {
    const folder = workspaceFolder();
    const workspacePathRoot = await mkdtemp(path.join(folder.uri.fsPath, ".lopper-runner-path-workspace-"));
    const workspacePathBinary = path.join(workspacePathRoot, platformBinaryName());
    const outsidePathRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-runner-path-outside-"));
    const outsideBinary = path.join(outsidePathRoot, platformBinaryName());
    const previousBinaryPath = process.env.LOPPER_BINARY_PATH;
    const previousPathEnv = process.env.PATH;

    try {
      delete process.env.LOPPER_BINARY_PATH;
      await writeExecutable(workspacePathBinary);
      await writeExecutable(outsideBinary);
      process.env.PATH = joinPathEntries([workspacePathRoot, outsidePathRoot, previousPathEnv]);

      const runner = createRunner(outsidePathRoot);
      const resolvedPath = await runner.resolveBinaryPath(folder, false);
      assert.equal(resolvedPath, outsideBinary);
    } finally {
      restoreEnv("LOPPER_BINARY_PATH", previousBinaryPath);
      restoreEnv("PATH", previousPathEnv);
      await rm(workspacePathRoot, { recursive: true, force: true });
      await rm(outsidePathRoot, { recursive: true, force: true });
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

  test("orchestrates analysis through binary lifecycle and command executor seams", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const managedBinaryPath = path.join(folder.uri.fsPath, ".lopper-managed", "lopper");
    const calls: Array<{ binaryPath: string; args: string[]; cwd: string }> = [];
    let resolvedRequest: BinaryResolutionRequest | undefined;

    const reportExecutor: ReportCommandExecutor = {
      runCommand: async () => "",
      runReport: async (binaryPath, args, cwd): Promise<LopperReport> => {
        calls.push({ binaryPath, args, cwd });
        if (args.includes("--suggest-only")) {
          return {
            dependencies: [
              {
                name: "scope-lib",
                usedExportsCount: 1,
                totalExportsCount: 2,
                usedPercent: 50,
                codemod: {
                  mode: "suggest-only",
                  suggestions: [
                    {
                      file: "src/index.ts",
                      line: 1,
                      importName: "chunk",
                      fromModule: "scope-lib",
                      toModule: "scope-lib/chunk",
                      original: "import chunk from \"scope-lib\";",
                      replacement: "import chunk from \"scope-lib/chunk\";",
                    },
                  ],
                },
              },
            ],
          };
        }
        return {
          dependencies: [
            {
              name: "scope-lib",
              language: "js-ts",
              usedExportsCount: 1,
              totalExportsCount: 2,
              usedPercent: 50,
            },
          ],
        };
      },
    };

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: {
          resolveBinaryPath: async (request) => {
            resolvedRequest = request;
            return managedBinaryPath;
          },
        },
        reportExecutor,
      },
    );

    const analysis = await runner.analyseWorkspace(folder);
    assert.equal(analysis.binaryPath, managedBinaryPath);
    assert.equal(calls.length, 2);
    const [firstCall, secondCall] = calls;
    assert.ok(firstCall, "expected primary analysis command");
    assert.ok(secondCall, "expected follow-up codemod command");
    assert.equal(firstCall.args[0], "analyse");
    assert.ok(firstCall.args.includes("--scope-mode"), "expected scope mode arg in primary command");
    assert.equal(firstCall.args[firstCall.args.indexOf("--scope-mode") + 1], "package");
    assert.ok(secondCall.args.includes("--scope-mode"), "expected scope mode arg in codemod command");
    assert.equal(secondCall.args[secondCall.args.indexOf("--scope-mode") + 1], "package");
    assert.ok(secondCall.args.includes("--suggest-only"), "expected codemod command to request suggest-only mode");
    assert.deepEqual(secondCall.args.slice(-2), ["--", "scope-lib"]);
    assert.equal(analysis.scopeMode, "package");
    assert.equal(analysis.codemodsByDependency.get("scope-lib")?.suggestions?.[0]?.toModule, "scope-lib/chunk");
    assert.equal(resolvedRequest?.workspaceRoot, folder.uri.fsPath);
  });

  test("skips codemod analysis for flag-shaped dependency names", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const calls: string[][] = [];
    const outputLines: string[] = [];

    const runner = new LopperRunner(
      { appendLine: (value) => outputLines.push(value) },
      context,
      {
        binaryLifecycle: {
          resolveBinaryPath: async () => path.join(folder.uri.fsPath, ".lopper-managed", "lopper"),
        },
        reportExecutor: {
          runCommand: async () => "",
          runReport: async (_binaryPath, args): Promise<LopperReport> => {
            calls.push(args);
            return {
              dependencies: [
                {
                  name: "--cache-path=/tmp/lopper-escape",
                  language: "js-ts",
                  usedExportsCount: 1,
                  totalExportsCount: 2,
                  usedPercent: 50,
                },
              ],
            };
          },
        },
      },
    );

    const analysis = await runner.analyseWorkspace(folder);

    assert.equal(calls.length, 1, "flag-shaped dependency must not be forwarded to a child process");
    assert.equal(analysis.codemodsByDependency.size, 0);
    assert.ok(
      outputLines.some((line) => line.includes("unsafe dependency name rejected")),
      "expected unsafe dependency skip to be logged",
    );
  });

  test("passes explicit scope mode to primary and codemod analysis commands", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const calls: Array<{ args: string[] }> = [];

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: {
          resolveBinaryPath: async () => path.join(folder.uri.fsPath, ".lopper-managed", "lopper"),
        },
        reportExecutor: {
          runCommand: async () => "",
          runReport: async (_binaryPath, args): Promise<LopperReport> => {
            calls.push({ args });
            if (args.includes("--suggest-only")) {
              return {
                dependencies: [
                  {
                    name: "scope-lib",
                    usedExportsCount: 1,
                    totalExportsCount: 2,
                    usedPercent: 50,
                    codemod: { mode: "suggest-only", suggestions: [] },
                  },
                ],
              };
            }
            return {
              dependencies: [
                {
                  name: "scope-lib",
                  language: "js-ts",
                  usedExportsCount: 1,
                  totalExportsCount: 2,
                  usedPercent: 50,
                },
              ],
            };
          },
        },
      },
    );

    const analysis = await runner.analyseWorkspace(folder, { scopeMode: "repo" });
    assert.equal(analysis.scopeMode, "repo");
    assert.equal(calls.length, 2);
    for (const call of calls) {
      assert.ok(call.args.includes("--scope-mode"), "expected --scope-mode for every analysis call");
      assert.equal(call.args[call.args.indexOf("--scope-mode") + 1], "repo");
    }
  });

  test("threads runtime, baseline, and export format flags through the runner", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const analysisCalls: string[][] = [];
    const exportCalls: string[][] = [];

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: {
          resolveBinaryPath: async () => path.join(folder.uri.fsPath, ".lopper-managed", "lopper"),
        },
        reportExecutor: {
          runCommand: async (_binaryPath, args): Promise<string> => {
            exportCalls.push(args);
            return "dependency_name,used_percent\nscope-lib,50\n";
          },
          runReport: async (_binaryPath, args): Promise<LopperReport> => {
            analysisCalls.push(args);
            return {
              dependencies: [
                {
                  name: "scope-lib",
                  language: "js-ts",
                  usedExportsCount: 1,
                  totalExportsCount: 2,
                  usedPercent: 50,
                },
              ],
            };
          },
        },
      },
    );

    await runner.analyseWorkspace(folder, {
      runtimeTracePath: "trace.ndjson",
      runtimeTestCommand: "npm test",
      baselinePath: "baseline.json",
      baselineStorePath: ".artifacts/lopper-baselines",
      baselineKey: "commit:abc123",
      baselineLabel: "release-candidate",
      saveBaseline: true,
    });

    const analysisArgs = analysisCalls[0];
    assert.ok(analysisArgs, "expected analysis command invocation");
    assert.ok(analysisArgs.includes("--runtime-trace"));
    assert.equal(analysisArgs[analysisArgs.indexOf("--runtime-trace") + 1], "trace.ndjson");
    assert.ok(analysisArgs.includes("--runtime-test-command"));
    assert.equal(analysisArgs[analysisArgs.indexOf("--runtime-test-command") + 1], "npm test");
    assert.ok(analysisArgs.includes("--baseline"));
    assert.equal(analysisArgs[analysisArgs.indexOf("--baseline") + 1], "baseline.json");
    assert.ok(analysisArgs.includes("--baseline-store"));
    assert.equal(analysisArgs[analysisArgs.indexOf("--baseline-store") + 1], ".artifacts/lopper-baselines");
    assert.ok(analysisArgs.includes("--baseline-key"));
    assert.equal(analysisArgs[analysisArgs.indexOf("--baseline-key") + 1], "commit:abc123");
    assert.ok(analysisArgs.includes("--baseline-label"));
    assert.equal(analysisArgs[analysisArgs.indexOf("--baseline-label") + 1], "release-candidate");
    assert.ok(analysisArgs.includes("--save-baseline"));

    const exportOutput = await runner.exportWorkspace(folder, "sarif", {
      runtimeTracePath: "trace.ndjson",
      baselineStorePath: ".artifacts/lopper-baselines",
      baselineKey: "commit:abc123",
    });
    assert.match(exportOutput, /dependency_name/);
    const exportArgs = exportCalls[0];
    assert.ok(exportArgs, "expected export command invocation");
    assert.ok(exportArgs.includes("--format"));
    assert.equal(exportArgs[exportArgs.indexOf("--format") + 1], "sarif");
    assert.ok(exportArgs.includes("--runtime-trace"));
    assert.ok(exportArgs.includes("--baseline-key"));
  });

  test("times out hung lopper commands", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-runner-timeout-"));
    const binaryPath = path.join(tempRoot, "lopper");

    try {
      await writeSleepingExecutable(binaryPath);
      const executor = new LopperCliReportExecutor({ appendLine: () => undefined });

      await assert.rejects(
        executor.runCommand(binaryPath, [], tempRoot, { timeoutMs: 50 }),
        /lopper command timed out after 50ms/,
      );
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("aborts lopper commands when the caller cancels", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-runner-abort-"));
    const binaryPath = path.join(tempRoot, "lopper");
    const controller = new AbortController();

    try {
      await writeSleepingExecutable(binaryPath);
      const executor = new LopperCliReportExecutor({ appendLine: () => undefined });
      const abortTimer = setTimeout(() => controller.abort(), 50);

      try {
        await assert.rejects(
          executor.runCommand(binaryPath, [], tempRoot, { signal: controller.signal, timeoutMs: 5_000 }),
          /lopper command was cancelled/,
        );
      } finally {
        clearTimeout(abortTimer);
      }
    } finally {
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

async function writeExecutable(binaryPath: string): Promise<void> {
  await mkdir(path.dirname(binaryPath), { recursive: true });
  await writeFile(binaryPath, "#!/bin/sh\nexit 0\n", "utf8");
  if (process.platform !== "win32") {
    await chmod(binaryPath, 0o755);
  }
}

async function writeSleepingExecutable(binaryPath: string): Promise<void> {
  await mkdir(path.dirname(binaryPath), { recursive: true });
  await writeFile(binaryPath, "#!/bin/sh\nsleep 10\n", "utf8");
  await chmod(binaryPath, 0o755);
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
