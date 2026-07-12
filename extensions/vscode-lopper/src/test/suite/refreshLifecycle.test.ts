import * as assert from "node:assert/strict";
import { chmod, mkdtemp, rm, utimes, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { setup, suite, test } from "mocha";
import * as vscode from "vscode";

import { binaryFileSignature } from "../../binaryIdentity";
import { __testing, deactivate } from "../../extension";
import { reachabilityVulnerabilityFeature } from "../../featureCapabilities";
import type { RefreshWorkspaceOptions } from "../../extension";
import type { WorkspaceAnalysis, WorkspaceAnalysisRunner, WorkspaceCodemodApplyResult } from "../../lopperRunner";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: Error) => void;
}

interface LifecycleHarness {
  folder: vscode.WorkspaceFolder;
  document: vscode.TextDocument;
  binaryPath: string;
  signature: string;
}

type TestController = ReturnType<typeof __testing.createController>;

suite("refresh lifecycle", () => {
  setup(() => {
    deactivate();
  });

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
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          const firstRefresh = refresh(controller, harness);
          const secondRefresh = refresh(controller, harness);

          await waitForAssertion(() => assert.equal(analyseCalls, 1));

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
          exportWorkspace: async (): Promise<string> => "",
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

  test("invalidates cached analysis after same-path binary replacement with restored mtime", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const fixedTime = new Date(1_700_000_000_000);
      await utimes(harness.binaryPath, fixedTime, fixedTime);
      let analyseCalls = 0;

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            analyseCalls += 1;
            return makeWorkspaceAnalysis({
              folder: harness.folder,
              binaryPath: harness.binaryPath,
              binarySignature: await binaryFileSignature(harness.binaryPath),
              dependencyCount: analyseCalls,
              usedPercent: 50,
            });
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          await refresh(controller, harness);
          await refresh(controller, harness);
          assert.equal(analyseCalls, 1, "unchanged binary should retain the analysis cache");

          await writeFile(harness.binaryPath, "change", "utf8");
          await utimes(harness.binaryPath, fixedTime, fixedTime);
          await refresh(controller, harness);

          assert.equal(analyseCalls, 2, "binary replacement must invalidate cached analysis");
          assert.doesNotMatch(controller.getLatestSummary(), /cached/);
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
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          const firstRefresh = refresh(controller, harness, { forceFresh: true });
          await waitForAssertion(() => assert.equal(deferredAnalyses.length, 1));
          const secondRefresh = refresh(controller, harness, { forceFresh: true });

          await waitForAssertion(() => {
            assert.equal(deferredAnalyses.length, 2, "expected two independent fresh runs");
          });

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

  test("aborts superseded fresh refreshes", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const deferredAnalyses: Array<Deferred<WorkspaceAnalysis>> = [];
      const signals: AbortSignal[] = [];

      await withController(
        {
          analyseWorkspace: async (_folder, options): Promise<WorkspaceAnalysis> => {
            const next = deferred<WorkspaceAnalysis>();
            deferredAnalyses.push(next);
            assert.ok(options?.signal, "expected refresh signal to be forwarded");
            signals.push(options.signal);
            options.signal.addEventListener("abort", () => next.reject(new Error("aborted")), { once: true });
            return next.promise;
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          const firstRefresh = refresh(controller, harness, { forceFresh: true });
          await waitForAssertion(() => assert.equal(deferredAnalyses.length, 1));
          const secondRefresh = refresh(controller, harness, { forceFresh: true });

          await waitForAssertion(() => {
            assert.equal(deferredAnalyses.length, 2, "expected two independent fresh runs");
          });
          assert.equal(signals[0].aborted, true, "superseded run should be aborted");
          assert.equal(signals[1].aborted, false, "latest run should remain active");

          deferredAnalyses[1].resolve(
            makeAnalysis(harness, {
              dependencyCount: 1,
              usedPercent: 80,
            }),
          );
          await Promise.all([firstRefresh, secondRefresh]);
        },
      );
    });
  });

  test("does not let a superseded cancellation overwrite a newer running status", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const deferredAnalyses: Array<Deferred<WorkspaceAnalysis>> = [];
      const signals: AbortSignal[] = [];

      await withController(
        {
          analyseWorkspace: async (_folder, options): Promise<WorkspaceAnalysis> => {
            const next = deferred<WorkspaceAnalysis>();
            deferredAnalyses.push(next);
            assert.ok(options?.signal, "expected refresh signal to be forwarded");
            signals.push(options.signal);
            return next.promise;
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          const firstRefresh = refresh(controller, harness, { forceFresh: true });
          await waitForAssertion(() => assert.equal(deferredAnalyses.length, 1));
          const secondRefresh = refresh(controller, harness, { forceFresh: true });
          await waitForAssertion(() => assert.equal(deferredAnalyses.length, 2));

          deferredAnalyses[0].resolve(makeAnalysis(harness, { dependencyCount: 3, usedPercent: 25 }));
          await firstRefresh;
          const statusWhileLatestRunIsPending = controller.getLatestSummary();

          deferredAnalyses[1].resolve(makeAnalysis(harness, { dependencyCount: 0, usedPercent: 0 }));
          await secondRefresh;

          assert.equal(signals[0].aborted, true, "superseded run should be aborted");
          assert.match(statusWhileLatestRunIsPending, /analysing/);
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
          exportWorkspace: async (): Promise<string> => "",
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

  test("older cache validation cannot supersede a newer force-fresh runtime request", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const cacheValidationStarted = deferred<void>();
      const cacheValidationResult = deferred<string | undefined>();
      const runtimeAnalysis = deferred<WorkspaceAnalysis>();
      let analyseCalls = 0;
      let runtimeSignal: AbortSignal | undefined;
      let runtimeTracePath: string | undefined;
      const runner: WorkspaceAnalysisRunner = {
        analyseWorkspace: async (_folder, options): Promise<WorkspaceAnalysis> => {
          analyseCalls += 1;
          if (analyseCalls === 1) {
            return makeAnalysis(harness, { dependencyCount: 2, usedPercent: 50 });
          }
          runtimeSignal = options?.signal;
          runtimeTracePath = options?.runtimeTracePath;
          return runtimeAnalysis.promise;
        },
        exportWorkspace: async (): Promise<string> => "",
        applyCodemod: async (): Promise<WorkspaceCodemodApplyResult> => {
          throw new Error("unexpected codemod apply");
        },
      };
      const controller = __testing.createController(runner, {
        binarySignature: async () => {
          cacheValidationStarted.resolve(undefined);
          return cacheValidationResult.promise;
        },
      });

      try {
        await refresh(controller, harness);
        const ordinaryRefresh = refresh(controller, harness);
        await cacheValidationStarted.promise;

        const runtimeRefresh = refresh(controller, harness, {
          forceFresh: true,
          runtimeTracePath: "runtime.ndjson",
        });
        await waitForAssertion(() => assert.equal(analyseCalls, 2));
        assert.equal(runtimeTracePath, "runtime.ndjson");

        cacheValidationResult.resolve(harness.signature);
        await ordinaryRefresh;
        assert.equal(runtimeSignal?.aborted, false, "older cache validation must not cancel the runtime request");

        runtimeAnalysis.resolve(makeAnalysis(harness, {
          dependencyCount: 0,
          usedPercent: 0,
          withUnusedImport: false,
        }));
        await runtimeRefresh;

        assert.equal(analyseCalls, 2);
        assert.match(controller.getLatestSummary(), /0 deps/);
        assert.doesNotMatch(controller.getLatestSummary(), /cached/);
      } finally {
        controller.dispose();
      }
    });
  });

  test("folder configuration invalidation starts fresh work without disturbing siblings", async function () {
    this.timeout(30_000);

    const folders = vscode.workspace.workspaceFolders ?? [];
    assert.ok(folders.length >= 2, "expected a multi-root workspace");
    const [changedFolder, siblingFolder] = folders;
    assert.ok(changedFolder);
    assert.ok(siblingFolder);
    const changedDocument = await workspaceDocument(changedFolder);
    const siblingDocument = await workspaceDocument(siblingFolder);
    const changedConfiguration = vscode.workspace.getConfiguration("lopper", changedFolder.uri);
    const siblingConfiguration = vscode.workspace.getConfiguration("lopper", siblingFolder.uri);
    await changedConfiguration.update("enableFeatures", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
    await siblingConfiguration.update("enableFeatures", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
    await changedConfiguration.update("language", "js-ts", vscode.ConfigurationTarget.WorkspaceFolder);
    await siblingConfiguration.update("language", "js-ts", vscode.ConfigurationTarget.WorkspaceFolder);
    const { binaryPath, signature, cleanup } = await createBinaryFixture();
    const calls: Array<{
      folder: vscode.WorkspaceFolder;
      enabledFeatures: readonly string[];
      pending: Deferred<WorkspaceAnalysis>;
      signal: AbortSignal | undefined;
    }> = [];
    const runner: WorkspaceAnalysisRunner = {
      analyseWorkspace: async (folder, options): Promise<WorkspaceAnalysis> => {
        const pending = deferred<WorkspaceAnalysis>();
        calls.push({
          folder,
          enabledFeatures: vscode.workspace.getConfiguration("lopper", folder.uri).get<string[]>("enableFeatures", []),
          pending,
          signal: options?.signal,
        });
        return pending.promise;
      },
      exportWorkspace: async (): Promise<string> => "",
      applyCodemod: async (): Promise<WorkspaceCodemodApplyResult> => {
        throw new Error("unexpected codemod apply");
      },
    };
    const controller = __testing.createController(runner);

    try {
      const changedRefresh = controller.refreshWorkspace({
        folder: changedFolder,
        document: changedDocument,
        revealErrors: false,
        trigger: "command",
      });
      const siblingRefresh = controller.refreshWorkspace({
        folder: siblingFolder,
        document: siblingDocument,
        revealErrors: false,
        trigger: "command",
      });
      await waitForAssertion(() => assert.equal(calls.length, 2));

      const siblingCall = calls.find((call) => call.folder.uri.toString() === siblingFolder.uri.toString());
      assert.ok(siblingCall);
      siblingCall.pending.resolve(makeWorkspaceAnalysis({
        folder: siblingFolder,
        binaryPath,
        binarySignature: signature,
        dependencyCount: 1,
        usedPercent: 75,
      }));
      await waitForPromise(siblingRefresh, "sibling refresh");

      await waitForPromise(changedConfiguration.update(
        "enableFeatures",
        [reachabilityVulnerabilityFeature],
        vscode.ConfigurationTarget.WorkspaceFolder,
      ), "folder configuration update");
      const configurationRefresh = controller.handleConfigurationChange({
        affectsConfiguration: (_section, resource) => resource?.toString() === changedFolder.uri.toString(),
      });
      await waitForAssertion(() => {
        const changedCalls = calls.filter((call) => call.folder.uri.toString() === changedFolder.uri.toString());
        assert.equal(changedCalls.length, 2, "changed folder must start a post-invalidation run");
      });

      const changedCalls = calls.filter((call) => call.folder.uri.toString() === changedFolder.uri.toString());
      assert.deepEqual(changedCalls.map((call) => call.enabledFeatures), [[], [reachabilityVulnerabilityFeature]]);
      assert.equal(changedCalls[0]?.signal?.aborted, true, "pre-change work should be cancelled");
      assert.equal(siblingCall.signal?.aborted, false, "sibling work must not be cancelled");
      assert.equal(
        calls.filter((call) => call.folder.uri.toString() === siblingFolder.uri.toString()).length,
        1,
        "sibling configuration is unchanged",
      );

      changedCalls[1]?.pending.resolve(makeWorkspaceAnalysis({
        folder: changedFolder,
        binaryPath,
        binarySignature: signature,
        dependencyCount: 0,
        usedPercent: 0,
        withUnusedImport: false,
      }));
      await waitForPromise(configurationRefresh, "post-change configuration refresh");
      await waitForAssertion(() => assert.match(controller.getLatestSummary(), /0 deps/));

      changedCalls[0]?.pending.resolve(makeWorkspaceAnalysis({
        folder: changedFolder,
        binaryPath,
        binarySignature: signature,
        dependencyCount: 3,
        usedPercent: 10,
        withUnusedImport: true,
      }));
      await waitForPromise(changedRefresh, "pre-change refresh completion");
      await waitForPromise(controller.refreshWorkspace({
        folder: changedFolder,
        document: changedDocument,
        revealErrors: false,
        trigger: "command",
      }), "post-change cache refresh");

      assert.equal(
        calls.filter((call) => call.folder.uri.toString() === changedFolder.uri.toString()).length,
        2,
        "post-change refresh should reuse only the post-change cache",
      );
      assert.match(controller.getLatestSummary(), /0 deps.*cached/);
      assert.equal(
        vscode.languages.getDiagnostics(changedDocument.uri).filter((item) => item.source === "lopper").length,
        0,
        "stale pre-change analysis must not render",
      );
    } finally {
      controller.dispose();
      await changedConfiguration.update("enableFeatures", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await siblingConfiguration.update("enableFeatures", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await changedConfiguration.update("language", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await siblingConfiguration.update("language", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await cleanup();
    }
  });

  test("configuration invalidation supersedes refreshes still resolving their language", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      const configuration = vscode.workspace.getConfiguration("lopper", harness.folder.uri);
      await configuration.update("autoRefresh", false, vscode.ConfigurationTarget.Workspace);
      await configuration.update("scopeMode", "package", vscode.ConfigurationTarget.Workspace);

      const pendingLanguage = deferred<"js-ts">();
      let languageResolutionCalls = 0;
      let analyseCalls = 0;
      const runner: WorkspaceAnalysisRunner = {
        analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
          analyseCalls += 1;
          return makeAnalysis(harness, { dependencyCount: 1, usedPercent: 50 });
        },
        exportWorkspace: async (): Promise<string> => "",
        applyCodemod: async (): Promise<WorkspaceCodemodApplyResult> => {
          throw new Error("unexpected codemod apply");
        },
      };
      const controller = __testing.createController(runner, {
        resolveLanguage: async () => {
          languageResolutionCalls += 1;
          return pendingLanguage.promise;
        },
      });

      try {
        const staleRefresh = refresh(controller, harness);
        await waitForAssertion(() => assert.equal(languageResolutionCalls, 1));

        await configuration.update("scopeMode", "repo", vscode.ConfigurationTarget.Workspace);
        await controller.handleConfigurationChange({
          affectsConfiguration: (_section, resource) => resource?.toString() === harness.folder.uri.toString(),
        });

        pendingLanguage.resolve("js-ts");
        await waitForPromise(staleRefresh, "invalidated language resolution");

        assert.equal(analyseCalls, 0, "configuration changes must cancel pre-invalidation refresh requests");
      } finally {
        controller.dispose();
        await configuration.update("autoRefresh", undefined, vscode.ConfigurationTarget.Workspace);
        await configuration.update("scopeMode", undefined, vscode.ConfigurationTarget.Workspace);
      }
    });
  });

  test("preserves the last successful analysis when a fresh refresh is cancelled", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      let analyseCalls = 0;
      let activeSignal: AbortSignal | undefined;

      await withController(
        {
          analyseWorkspace: async (_folder, options): Promise<WorkspaceAnalysis> => {
            analyseCalls += 1;
            if (analyseCalls === 1) {
              return makeAnalysis(harness, { dependencyCount: 1, usedPercent: 50 });
            }

            assert.ok(options?.signal, "expected fresh refresh signal");
            activeSignal = options.signal;
            return new Promise<WorkspaceAnalysis>((_resolve, reject) => {
              options.signal?.addEventListener(
                "abort",
                () => reject(new Error("lopper command was cancelled")),
                { once: true },
              );
            });
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          await refresh(controller, harness);
          const previousSummary = controller.getLatestSummary();
          const previousDiagnostics = vscode.languages
            .getDiagnostics(harness.document.uri)
            .filter((item) => item.source === "lopper");
          assert.equal(previousDiagnostics.length, 1);

          const pendingRefresh = refresh(controller, harness, { forceFresh: true });
          await waitForAssertion(() => assert.ok(activeSignal, "expected fresh refresh to start"));
          controller.cancelRefresh();
          await pendingRefresh;

          const currentDiagnostics = vscode.languages
            .getDiagnostics(harness.document.uri)
            .filter((item) => item.source === "lopper");
          assert.equal(activeSignal?.aborted, true);
          assert.equal(controller.getLatestSummary(), previousSummary);
          assert.equal(currentDiagnostics.length, previousDiagnostics.length);
          assert.deepEqual(
            currentDiagnostics.map((item) => item.message),
            previousDiagnostics.map((item) => item.message),
          );
        },
      );
    });
  });

  test("returns to idle when the first refresh is cancelled", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      let activeSignal: AbortSignal | undefined;

      await withController(
        {
          analyseWorkspace: async (_folder, options): Promise<WorkspaceAnalysis> => {
            assert.ok(options?.signal, "expected refresh signal");
            activeSignal = options.signal;
            return new Promise<WorkspaceAnalysis>((_resolve, reject) => {
              options.signal?.addEventListener(
                "abort",
                () => reject(new Error("lopper command was cancelled")),
                { once: true },
              );
            });
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          const pendingRefresh = refresh(controller, harness, { forceFresh: true });
          await waitForAssertion(() => assert.ok(activeSignal, "expected refresh to start"));
          controller.cancelRefresh();
          await pendingRefresh;

          assert.equal(activeSignal?.aborted, true);
          assert.equal(controller.getLatestSummary(), "Lopper: idle");
          assert.equal(
            vscode.languages.getDiagnostics(harness.document.uri).filter((item) => item.source === "lopper").length,
            0,
          );
        },
      );
    });
  });

  test("still clears stale state for genuine refresh failures", async function () {
    this.timeout(30_000);

    await withHarness(async (harness) => {
      let analyseCalls = 0;

      await withController(
        {
          analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
            analyseCalls += 1;
            if (analyseCalls === 1) {
              return makeAnalysis(harness, { dependencyCount: 1, usedPercent: 50 });
            }
            throw new Error("analysis exploded");
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          await refresh(controller, harness);
          assert.equal(
            vscode.languages.getDiagnostics(harness.document.uri).filter((item) => item.source === "lopper").length,
            1,
          );

          await refresh(controller, harness, { forceFresh: true });

          assert.match(controller.getLatestSummary(), /unavailable/);
          assert.equal(
            vscode.languages.getDiagnostics(harness.document.uri).filter((item) => item.source === "lopper").length,
            0,
          );
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
          exportWorkspace: async (): Promise<string> => "",
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
                analysis.report.dependencies[0].name,
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
                      patch: "--- a/../outside.ts\n+++ b/../outside.ts\n@@ -1 +1 @@\n-import scope-lib from \"scope-lib\";\n+import scope-lib from \"scope-lib\";",
                    },
                    {
                      file: validSuggestionPath,
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
              ],
            ]);
            return analysis;
          },
          exportWorkspace: async (): Promise<string> => "",
        },
        async (controller) => {
          await refresh(controller, harness);

          const diagnostics = vscode.languages.getDiagnostics(harness.document.uri).filter((item) => item.source === "lopper");
          const codemodDiagnostics = diagnostics.filter((item) => item.message.includes("safe import remediation"));
          assert.equal(codemodDiagnostics.length, 1);
          assert.equal(diagnostics.length, 2);
        },
      );
	    });
	  });

	  test("refreshes diagnostics after mixed codemod apply mutates files", async function () {
	    this.timeout(30_000);

	    await withHarness(async (harness) => {
	      let analyseCalls = 0;
	      let applyCalls = 0;
	      let firstAnalysis: WorkspaceAnalysis | undefined;
	      const restoreNotifications = stubCodemodApplyNotifications();
	      try {

	        await withController(
	          {
	            analyseWorkspace: async (): Promise<WorkspaceAnalysis> => {
	              analyseCalls += 1;
	              const analysis = makeAnalysis(harness, {
	                dependencyCount: 1,
	                usedPercent: analyseCalls === 1 ? 50 : 100,
	                withUnusedImport: analyseCalls === 1,
	              });
	              attachScopeLibCodemod(analysis);
	              firstAnalysis ??= analysis;
	              return analysis;
	            },
	            applyCodemod: async (folder, dependencyName): Promise<WorkspaceCodemodApplyResult> => {
	              applyCalls += 1;
	              assert.equal(folder.uri.fsPath, harness.folder.uri.fsPath);
	              assert.equal(dependencyName, "scope-lib");
	              return {
	                folder,
	                binaryPath: harness.binaryPath,
	                requestedLanguage: "js-ts",
	                scopeMode: "package",
	                dependencyName,
	                report: firstAnalysis?.report ?? makeAnalysis(harness, { dependencyCount: 1, usedPercent: 50 }).report,
	                apply: {
	                  appliedFiles: 1,
	                  appliedPatches: 1,
	                  skippedFiles: 0,
	                  skippedPatches: 0,
	                  failedFiles: 1,
	                  failedPatches: 1,
	                  backupPath: ".artifacts/lopper-codemod-backups/scope-lib.json",
	                  results: [
	                    { file: "src/index.ts", status: "applied", patchCount: 1 },
	                    { file: "src/readonly.ts", status: "failed", patchCount: 0, message: "permission denied" },
	                  ],
	                },
	              };
	            },
	            exportWorkspace: async (): Promise<string> => "",
	          },
	          async (controller) => {
	            await refresh(controller, harness);
	            await controller.applyCodemod("scope-lib", harness.folder.uri.fsPath, { skipConfirmation: true });

	            assert.equal(applyCalls, 1);
	            assert.equal(analyseCalls, 2, "mixed apply with file mutations should force a diagnostics refresh");
	          },
	        );
	      } finally {
	        restoreNotifications();
	      }
	    });
	  });

	  test("formats codemod apply summaries and dirty-worktree guard errors", () => {
	    const apply = {
      appliedFiles: 1,
      appliedPatches: 2,
      skippedFiles: 3,
      skippedPatches: 4,
      failedFiles: 0,
      failedPatches: 0,
      backupPath: ".artifacts/lopper-codemod-backups/scope-lib.json",
      results: [{ file: "src/index.ts", status: "applied", patchCount: 2 }],
    };

    const summary = __testing.formatCodemodApplySummary("scope-lib", apply);
    assert.match(summary, /scope-lib/);
    assert.match(summary, /applied 1 files\/2 patches/);
    assert.match(summary, /skipped 3 files\/4 patches/);
    assert.match(summary, /failed 0 files\/0 patches/);
    assert.match(summary, /rollback \.artifacts\/lopper-codemod-backups\/scope-lib\.json/);

    const notification = __testing.formatCodemodApplyNotification("scope-lib", apply, "applied");
    assert.match(notification, /Rollback: \.artifacts\/lopper-codemod-backups\/scope-lib\.json/);
    assert.equal(
      __testing.isDirtyWorktreeApplyError(
        "codemod apply requires a clean git worktree: detected uncommitted changes in README.md; pass --allow-dirty",
      ),
      true,
    );
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
  runner: Omit<WorkspaceAnalysisRunner, "applyCodemod"> & Partial<Pick<WorkspaceAnalysisRunner, "applyCodemod">>,
  run: (controller: TestController) => Promise<void>,
): Promise<void> {
  const completeRunner: WorkspaceAnalysisRunner = {
    applyCodemod: async (): Promise<WorkspaceCodemodApplyResult> => {
      throw new Error("unexpected codemod apply in refresh lifecycle test");
    },
    ...runner,
  };
  const controller = __testing.createController(completeRunner);
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
  const signature = await binaryFileSignature(binaryPath);
  return {
    binaryPath,
    signature,
    cleanup: async () => rm(tempRoot, { recursive: true, force: true }),
  };
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((resolver, rejecter) => {
    resolve = resolver;
    reject = rejecter;
  });
  return { promise, resolve, reject };
}

async function waitForAssertion(assertion: () => void, timeoutMs = 1_000): Promise<void> {
  const started = Date.now();
  let lastError: unknown;
  while (Date.now() - started < timeoutMs) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
  if (lastError) {
    throw lastError;
  }
  assertion();
	}

async function waitForPromise<T>(promise: PromiseLike<T>, label: string, timeoutMs = 2_000): Promise<T> {
  let timeout: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error(`timed out waiting for ${label}`)), timeoutMs);
      }),
    ]);
  } finally {
    if (timeout) {
      clearTimeout(timeout);
    }
  }
}

function attachScopeLibCodemod(analysis: WorkspaceAnalysis): void {
	analysis.codemodsByDependency = new Map([
		[
			"scope-lib",
			{
				mode: "replace",
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
		],
	]);
}

function stubCodemodApplyNotifications(): () => void {
	const originalShowErrorMessage = vscode.window.showErrorMessage;
	const originalShowInformationMessage = vscode.window.showInformationMessage;
	(vscode.window as typeof vscode.window & { showErrorMessage: typeof vscode.window.showErrorMessage }).showErrorMessage =
		(async () => undefined) as typeof vscode.window.showErrorMessage;
	(vscode.window as typeof vscode.window & { showInformationMessage: typeof vscode.window.showInformationMessage }).showInformationMessage =
		(async () => undefined) as typeof vscode.window.showInformationMessage;
	return () => {
		(vscode.window as typeof vscode.window & { showErrorMessage: typeof vscode.window.showErrorMessage }).showErrorMessage = originalShowErrorMessage;
		(vscode.window as typeof vscode.window & { showInformationMessage: typeof vscode.window.showInformationMessage }).showInformationMessage = originalShowInformationMessage;
	};
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
