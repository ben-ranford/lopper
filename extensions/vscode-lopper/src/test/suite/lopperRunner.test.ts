import * as assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, rm, symlink, utimes, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as vscode from "vscode";

import {
  pythonRunnerProfilesFeature,
  reachabilityVulnerabilityFeature,
  sbomAttestationExportsFeature,
  vscodePreviewCapabilityParityFeature,
} from "../../featureCapabilities";
import type { BinaryResolutionRequest } from "../../managedBinary";
import {
  BinaryResolutionError,
  defaultCodemodAnalysisConcurrency,
  LopperCliReportExecutor,
  LopperRunner,
  type ReportCommandExecutor,
} from "../../lopperRunner";
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
                      patch: "--- a/src/index.ts\n+++ b/src/index.ts\n@@ -1 +1 @@\n-import chunk from \"scope-lib\";\n+import chunk from \"scope-lib/chunk\";",
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

  test("applies codemods with guarded CLI flags and optional dirty override", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const calls: string[][] = [];

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
            calls.push(args);
            return {
              dependencies: [
                {
                  name: "scope-lib",
                  language: "js-ts",
                  usedExportsCount: 1,
                  totalExportsCount: 2,
                  usedPercent: 50,
                  codemod: {
                    mode: "apply",
                    apply: {
                      appliedFiles: 1,
                      appliedPatches: 2,
                      skippedFiles: 0,
                      skippedPatches: 0,
                      failedFiles: 0,
                      failedPatches: 0,
                      backupPath: ".artifacts/lopper-codemod-backups/scope-lib.json",
                      results: [{ file: "src/index.ts", status: "applied", patchCount: 2 }],
                    },
                  },
                },
              ],
            };
          },
        },
      },
    );

    const result = await runner.applyCodemod(folder, "scope-lib", {
      scopeMode: "repo",
      requestedLanguage: "js-ts",
    });
    assert.equal(result.apply?.appliedFiles, 1);
    assert.equal(result.apply?.backupPath, ".artifacts/lopper-codemod-backups/scope-lib.json");

    const applyArgs = calls[0];
    assert.ok(applyArgs, "expected codemod apply invocation");
    assert.deepEqual(applyArgs.slice(0, 1), ["analyse"]);
    assert.ok(applyArgs.includes("--apply-codemod"), "expected guarded apply flag");
    assert.ok(applyArgs.includes("--apply-codemod-confirm"), "expected apply confirmation flag");
    assert.ok(!applyArgs.includes("--suggest-only"), "apply command must not combine suggest-only");
    assert.ok(!applyArgs.includes("--allow-dirty"), "dirty override must be opt-in");
    assert.equal(applyArgs[applyArgs.indexOf("--format") + 1], "json");
    assert.equal(applyArgs[applyArgs.indexOf("--language") + 1], "js-ts");
    assert.equal(applyArgs[applyArgs.indexOf("--scope-mode") + 1], "repo");
    assert.deepEqual(applyArgs.slice(-2), ["--", "scope-lib"]);

    await runner.applyCodemod(folder, "scope-lib", {
      scopeMode: "repo",
      requestedLanguage: "js-ts",
      allowDirty: true,
    });
    const dirtyApplyArgs = calls[1];
    assert.ok(dirtyApplyArgs?.includes("--allow-dirty"), "expected explicit dirty override flag");
  });

  test("fetches codemods with bounded concurrency and reuses duplicate dependencies", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const codemodNames: string[] = [];
    let activeCodemods = 0;
    let maxActiveCodemods = 0;

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
            if (args.includes("--suggest-only")) {
              const dependencyName = args.at(-1);
              assert.ok(dependencyName, "expected dependency name in codemod command");
              codemodNames.push(dependencyName);
              activeCodemods += 1;
              maxActiveCodemods = Math.max(maxActiveCodemods, activeCodemods);
              try {
                await delay(20);
              } finally {
                activeCodemods -= 1;
              }

              return {
                dependencies: [
                  {
                    name: dependencyName,
                    usedExportsCount: 1,
                    totalExportsCount: 2,
                    usedPercent: 50,
                    codemod: { mode: "suggest-only", suggestions: [] },
                  },
                ],
              };
            }

            return {
              dependencies: ["scope-a", "scope-b", "scope-a", "scope-c", "scope-d", "scope-e"].map((name) => ({
                name,
                language: "js-ts",
                usedExportsCount: 1,
                totalExportsCount: 2,
                usedPercent: 50,
              })),
            };
          },
        },
      },
    );

    const analysis = await runner.analyseWorkspace(folder);

    assert.equal(codemodNames.length, 5);
    assert.equal(codemodNames.filter((name) => name === "scope-a").length, 1);
    assert.ok(maxActiveCodemods > 1, "expected codemod analyses to run concurrently");
    assert.ok(
      maxActiveCodemods <= defaultCodemodAnalysisConcurrency,
      "codemod concurrency must stay below the default cap",
    );
    assert.deepEqual([...analysis.codemodsByDependency.keys()], [
      "scope-a",
      "scope-b",
      "scope-c",
      "scope-d",
      "scope-e",
    ]);
  });

  test("runs a runtime test command exactly once per analysis", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    let activeCodemods = 0;
    let maxActiveCodemods = 0;
    let runtimeCommandCalls = 0;

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
            if (args.includes("--runtime-test-command")) {
              runtimeCommandCalls += 1;
            }
            if (args.includes("--suggest-only")) {
              activeCodemods += 1;
              maxActiveCodemods = Math.max(maxActiveCodemods, activeCodemods);
              try {
                await delay(20);
              } finally {
                activeCodemods -= 1;
              }

              return {
                dependencies: [
                  {
                    name: args.at(-1) ?? "unknown",
                    usedExportsCount: 1,
                    totalExportsCount: 2,
                    usedPercent: 50,
                    codemod: { mode: "suggest-only", suggestions: [] },
                  },
                ],
              };
            }

            return {
              dependencies: ["scope-a", "scope-b", "scope-c"].map((name) => ({
                name,
                language: "js-ts",
                usedExportsCount: 1,
                totalExportsCount: 2,
                usedPercent: 50,
              })),
            };
          },
        },
      },
    );

    await runner.analyseWorkspace(folder, { runtimeTestCommand: "npm test" });

    assert.equal(runtimeCommandCalls, 1, "an authorized runtime test command must execute once");
    assert.equal(maxActiveCodemods, 0, "command-backed analysis must not start focused codemod runs");
  });

  test("keeps focused codemod discovery for trace-file-only runtime analysis", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const reportCalls: string[][] = [];

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
            reportCalls.push(args);
            if (args.includes("--suggest-only")) {
              return {
                dependencies: [{
                  name: "scope-lib",
                  usedExportsCount: 1,
                  totalExportsCount: 2,
                  usedPercent: 50,
                  codemod: { mode: "suggest-only", suggestions: [] },
                }],
              };
            }
            return {
              dependencies: [{
                name: "scope-lib",
                language: "js-ts",
                usedExportsCount: 1,
                totalExportsCount: 2,
                usedPercent: 50,
              }],
            };
          },
        },
      },
    );

    const analysis = await runner.analyseWorkspace(folder, { runtimeTracePath: "trace.ndjson" });

    assert.equal(reportCalls.length, 2, "trace-only analysis should run primary and focused codemod reports");
    assert.ok(!reportCalls[0]?.includes("--suggest-only"));
    assert.ok(reportCalls[1]?.includes("--suggest-only"));
    for (const args of reportCalls) {
      assert.ok(args.includes("--runtime-trace"));
      assert.equal(args[args.indexOf("--runtime-trace") + 1], "trace.ndjson");
    }
    assert.ok(analysis.codemodsByDependency.has("scope-lib"));
  });

  test("saves a baseline exactly once per analysis", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    let baselineSaveCalls = 0;

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
            if (args.includes("--save-baseline")) {
              baselineSaveCalls += 1;
            }
            if (args.includes("--suggest-only")) {
              return {
                dependencies: [{
                  name: args.at(-1) ?? "unknown",
                  usedExportsCount: 1,
                  totalExportsCount: 2,
                  usedPercent: 50,
                  codemod: { mode: "suggest-only", suggestions: [] },
                }],
              };
            }
            return {
              dependencies: ["scope-a", "scope-b", "scope-c"].map((name) => ({
                name,
                language: "js-ts",
                usedExportsCount: 1,
                totalExportsCount: 2,
                usedPercent: 50,
              })),
            };
          },
        },
      },
    );

    await runner.analyseWorkspace(folder, {
      baselineStorePath: ".artifacts/lopper-baselines",
      baselineLabel: "release-candidate",
      saveBaseline: true,
    });

    assert.equal(baselineSaveCalls, 1, "an explicit baseline save must execute once");
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

  test("keeps allowlisted feature settings scoped per workspace folder", async () => {
    const folders = vscode.workspace.workspaceFolders ?? [];
    assert.ok(folders.length >= 2, "expected a multi-root workspace");
    const [enabledFolder, disabledFolder] = folders;
    assert.ok(enabledFolder);
    assert.ok(disabledFolder);
    const context = { globalStorageUri: vscode.Uri.file(enabledFolder.uri.fsPath) } as vscode.ExtensionContext;
    const analysisCalls: Array<{ cwd: string; args: string[] }> = [];
    let featureCalls = 0;

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => "/managed/lopper:1",
        featureSettings: (folder) => folder.uri.toString() === enabledFolder.uri.toString()
          ? { enable: [reachabilityVulnerabilityFeature], disable: [] }
          : { enable: [], disable: [reachabilityVulnerabilityFeature] },
        reportExecutor: {
          runCommand: async (_binaryPath, args): Promise<string> => {
            assert.deepEqual(args, ["features", "--format", "json"]);
            featureCalls += 1;
            return featureManifestOutput();
          },
          runReport: async (_binaryPath, args, cwd): Promise<LopperReport> => {
            analysisCalls.push({ cwd, args });
            return { dependencies: [] };
          },
        },
      },
    );

    await runner.analyseWorkspace(enabledFolder);
    await runner.analyseWorkspace(disabledFolder);

    assert.equal(featureCalls, 1, "same binary signature should share one catalog lookup");
    assert.deepEqual(featureFlagValues(analysisCalls[0]?.args ?? [], "--enable-feature"), [
      reachabilityVulnerabilityFeature,
    ]);
    assert.deepEqual(featureFlagValues(analysisCalls[1]?.args ?? [], "--disable-feature"), [
      reachabilityVulnerabilityFeature,
    ]);
    assert.equal(analysisCalls[0]?.cwd, enabledFolder.uri.fsPath);
    assert.equal(analysisCalls[1]?.cwd, disabledFolder.uri.fsPath);
  });

  test("uses stable Python runner profiles without redundant feature flags", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const analysisCalls: string[][] = [];

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => "/managed/lopper:1",
        featureSettings: () => ({ enable: [], disable: [] }),
        reportExecutor: {
          runCommand: async (): Promise<string> => featureManifestOutput(),
          runReport: async (_binaryPath, args): Promise<LopperReport> => {
            analysisCalls.push(args);
            return { dependencies: [] };
          },
        },
      },
    );

    await runner.analyseWorkspace(folder);
    await runner.analyseWorkspace(folder, {
      document: {
        fileName: path.join(folder.uri.fsPath, "src", "runtime.py"),
        isUntitled: false,
        languageId: "python",
      } as vscode.TextDocument,
      runtimeTestCommand: "python -m unittest",
    });

    assert.deepEqual(featureFlagValues(analysisCalls[0] ?? [], "--enable-feature"), []);
    assert.deepEqual(featureFlagValues(analysisCalls[1] ?? [], "--enable-feature"), []);
  });

  test("scopes an explicit Python runner rollback to runtime-test analysis", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const analysisCalls: string[][] = [];

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => "/managed/lopper:1",
        featureSettings: () => ({ enable: [], disable: [pythonRunnerProfilesFeature] }),
        reportExecutor: {
          runCommand: async (): Promise<string> => featureManifestOutput(),
          runReport: async (_binaryPath, args): Promise<LopperReport> => {
            analysisCalls.push(args);
            return { dependencies: [] };
          },
        },
      },
    );

    await runner.analyseWorkspace(folder);
    await runner.analyseWorkspace(folder, {
      document: {
        fileName: path.join(folder.uri.fsPath, "src", "runtime.py"),
        isUntitled: false,
        languageId: "python",
      } as vscode.TextDocument,
      runtimeTestCommand: "python -m unittest",
    });

    assert.deepEqual(featureFlagValues(analysisCalls[0] ?? [], "--disable-feature"), []);
    assert.deepEqual(featureFlagValues(analysisCalls[1] ?? [], "--enable-feature"), []);
    assert.deepEqual(featureFlagValues(analysisCalls[1] ?? [], "--disable-feature"), [
      pythonRunnerProfilesFeature,
    ]);
  });

  test("rejects preview-backed operations when stable VS Code parity is rolled back", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    let exportCalls = 0;
    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => "/managed/lopper:1",
        featureSettings: () => ({ enable: [], disable: [vscodePreviewCapabilityParityFeature] }),
        reportExecutor: {
          runCommand: async (): Promise<string> => featureManifestOutput(),
          runReport: async (): Promise<LopperReport> => {
            exportCalls += 1;
            return { dependencies: [] };
          },
        },
      },
    );

    await assert.rejects(
      runner.preflightOperation(folder, "cyclonedx-export"),
      /vscode-preview-capability-parity is disabled but required/,
    );
    assert.equal(exportCalls, 0);
  });

  test("rejects required stable parity reported disabled by the selected binary", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => "/managed/lopper:1",
        featureSettings: () => ({ enable: [], disable: [] }),
        reportExecutor: {
          runCommand: async (): Promise<string> => featureManifestOutput(false),
          runReport: async (): Promise<LopperReport> => ({ dependencies: [] }),
        },
      },
    );

    await assert.rejects(
      runner.preflightOperation(folder, "python-runtime"),
      /stable feature vscode-preview-capability-parity is disabled by the selected binary/,
    );
  });

  test("refreshes the feature catalog when the selected binary signature changes", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    let signature = "/managed/lopper:1";
    let featureCalls = 0;
    const exportCalls: string[][] = [];

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => signature,
        featureSettings: () => ({ enable: [], disable: [] }),
        reportExecutor: {
          runCommand: async (_binaryPath, args): Promise<string> => {
            if (args[0] === "features") {
              featureCalls += 1;
              return featureCalls === 1
                ? featureManifestOutput()
                : JSON.stringify(JSON.parse(featureManifestOutput()).slice(1));
            }
            exportCalls.push(args);
            return "{}";
          },
          runReport: async (): Promise<LopperReport> => ({ dependencies: [] }),
        },
      },
    );

    await runner.exportWorkspace(folder, "cyclonedx-json");
    await runner.exportWorkspace(folder, "cyclonedx-json");
    assert.equal(featureCalls, 1);
    assert.equal(exportCalls.length, 2);
    for (const args of exportCalls) {
      assert.deepEqual(featureFlagValues(args, "--enable-feature"), [sbomAttestationExportsFeature]);
    }

    signature = "/managed/lopper:2";
    await assert.rejects(
      runner.exportWorkspace(folder, "cyclonedx-json"),
      /selected lopper binary does not report feature/,
    );
    assert.equal(featureCalls, 2);
    assert.equal(exportCalls.length, 2, "stale binary must be rejected before export execution");
  });

  test("refreshes the feature catalog after same-path binary replacement with restored mtime", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-feature-signature-"));
    const binaryPath = path.join(tempRoot, "lopper");
    const fixedTime = new Date(1_700_000_000_000);
    let featureCalls = 0;
    let exportCalls = 0;

    try {
      await writeFile(binaryPath, "first", "utf8");
      await chmod(binaryPath, 0o755);
      await utimes(binaryPath, fixedTime, fixedTime);
      const runner = new LopperRunner(
        { appendLine: () => undefined },
        context,
        {
          binaryLifecycle: { resolveBinaryPath: async () => binaryPath },
          featureSettings: () => ({ enable: [], disable: [] }),
          reportExecutor: {
            runCommand: async (_binaryPath, args): Promise<string> => {
              if (args[0] === "features") {
                featureCalls += 1;
                return featureCalls === 1
                  ? featureManifestOutput()
                  : JSON.stringify(JSON.parse(featureManifestOutput()).slice(1));
              }
              exportCalls += 1;
              return "{}";
            },
            runReport: async (): Promise<LopperReport> => ({ dependencies: [] }),
          },
        },
      );

      await runner.exportWorkspace(folder, "cyclonedx-json");
      assert.equal(featureCalls, 1);
      assert.equal(exportCalls, 1);

      await writeFile(binaryPath, "other", "utf8");
      await utimes(binaryPath, fixedTime, fixedTime);
      await assert.rejects(
        runner.exportWorkspace(folder, "cyclonedx-json"),
        /selected lopper binary does not report feature/,
      );
      assert.equal(featureCalls, 2, "replaced binary must refresh its catalog");
      assert.equal(exportCalls, 1, "stale catalog must fail before export execution");
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("isolates concurrent feature catalog cancellation across workspace folders", async () => {
    const folders = vscode.workspace.workspaceFolders ?? [];
    assert.ok(folders.length >= 2, "expected a multi-root workspace");
    const [cancelledFolder, successfulFolder] = folders;
    assert.ok(cancelledFolder);
    assert.ok(successfulFolder);
    const context = { globalStorageUri: vscode.Uri.file(cancelledFolder.uri.fsPath) } as vscode.ExtensionContext;
    const cancelledController = new AbortController();
    let featureCalls = 0;
    let signatureCalls = 0;
    let resolveFirstCatalogStarted!: () => void;
    let resolveSecondSignatureRequested!: () => void;
    const firstCatalogStarted = new Promise<void>((resolve) => {
      resolveFirstCatalogStarted = resolve;
    });
    const secondSignatureRequested = new Promise<void>((resolve) => {
      resolveSecondSignatureRequested = resolve;
    });

    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => {
          signatureCalls += 1;
          if (signatureCalls === 2) {
            resolveSecondSignatureRequested();
          }
          return "/managed/lopper:1";
        },
        featureSettings: () => ({ enable: [reachabilityVulnerabilityFeature], disable: [] }),
        reportExecutor: {
          runCommand: async (_binaryPath, args, _cwd, options): Promise<string> => {
            assert.deepEqual(args, ["features", "--format", "json"]);
            featureCalls += 1;
            if (featureCalls > 1) {
              return featureManifestOutput();
            }

            resolveFirstCatalogStarted();
            const signal = options?.signal;
            assert.ok(signal, "expected the first catalog lookup to carry its caller's cancellation signal");
            return new Promise<string>((_resolve, reject) => {
              const abort = () => reject(new Error("catalog query aborted"));
              if (signal.aborted) {
                abort();
                return;
              }
              signal.addEventListener("abort", abort, { once: true });
            });
          },
          runReport: async (): Promise<LopperReport> => ({ dependencies: [] }),
        },
      },
    );

    const cancelledAnalysis = runner.analyseWorkspace(cancelledFolder, { signal: cancelledController.signal });
    await firstCatalogStarted;
    const successfulAnalysis = runner.analyseWorkspace(successfulFolder);
    await secondSignatureRequested;
    await delay(0);
    cancelledController.abort();

    await assert.rejects(cancelledAnalysis, /catalog query aborted/);
    const analysis = await successfulAnalysis;
    assert.equal(analysis.folder.uri.toString(), successfulFolder.uri.toString());
    assert.equal(featureCalls, 2, "each in-flight caller must own its cancellable catalog lookup");
    await runner.analyseWorkspace(cancelledFolder);
    assert.equal(featureCalls, 2, "an aborted lookup must not evict a concurrently successful catalog");
  });

  test("rejects arbitrary feature settings before analysis", async () => {
    const folder = workspaceFolder();
    const context = { globalStorageUri: vscode.Uri.file(folder.uri.fsPath) } as vscode.ExtensionContext;
    let analysisCalls = 0;
    const runner = new LopperRunner(
      { appendLine: () => undefined },
      context,
      {
        binaryLifecycle: { resolveBinaryPath: async () => "/managed/lopper" },
        binarySignature: async () => "/managed/lopper:1",
        featureSettings: () => ({ enable: ["mcp-mutation-tools"], disable: [] }),
        reportExecutor: {
          runCommand: async (): Promise<string> => featureManifestOutput(),
          runReport: async (): Promise<LopperReport> => {
            analysisCalls += 1;
            return { dependencies: [] };
          },
        },
      },
    );

    await assert.rejects(runner.analyseWorkspace(folder), /not available to VS Code/);
    assert.equal(analysisCalls, 0);
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

async function delay(ms: number): Promise<void> {
  await new Promise<void>((resolve) => {
    setTimeout(resolve, ms);
  });
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

function featureManifestOutput(vscodeParityEnabledByDefault = true): string {
  return JSON.stringify([
    {
      code: "LOP-FEAT-0013",
      name: sbomAttestationExportsFeature,
      description: "CycloneDX",
      lifecycle: "preview",
      enabledByDefault: false,
    },
    {
      code: "LOP-FEAT-0015",
      name: reachabilityVulnerabilityFeature,
      description: "Vulnerability priority",
      lifecycle: "preview",
      enabledByDefault: false,
    },
    {
      code: "LOP-FEAT-0018",
      name: pythonRunnerProfilesFeature,
      description: "Python runners",
      lifecycle: "stable",
      enabledByDefault: true,
    },
    {
      code: "LOP-FEAT-0020",
      name: vscodePreviewCapabilityParityFeature,
      description: "VS Code parity",
      lifecycle: "stable",
      enabledByDefault: vscodeParityEnabledByDefault,
    },
  ]);
}

function featureFlagValues(args: readonly string[], flag: string): string[] {
  const values: string[] = [];
  for (let index = 0; index < args.length; index += 1) {
    if (args[index] === flag && args[index + 1]) {
      values.push(args[index + 1]);
    }
  }
  return values;
}
