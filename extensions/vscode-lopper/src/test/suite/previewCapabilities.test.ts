import * as assert from "node:assert/strict";
import { realpath } from "node:fs/promises";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as vscode from "vscode";

import { binaryFileSignature } from "../../binaryIdentity";
import { __testing } from "../../extension";
import type {
  WorkspaceAnalysis,
  WorkspaceAnalysisRequest,
  WorkspaceAnalysisRunner,
  WorkspaceCodemodApplyResult,
} from "../../lopperRunner";

suite("VS Code preview capabilities", () => {
  test("uses a CycloneDX-specific default target", () => {
    const descriptor = __testing.exportTargetDescriptor("cyclonedx-json");
    assert.equal(descriptor.defaultFileName, "lopper-analysis.cdx.json");
    assert.equal(descriptor.filterLabel, "CycloneDX JSON");
    assert.deepEqual(descriptor.extensions, ["cdx.json", "json"]);
  });

  test("rejects stale CycloneDX capability before opening the save dialog", async () => {
    const folder = primaryWorkspaceFolder();
    let saveDialogCalls = 0;
    let exportCalls = 0;
    let errorMessage = "";
    const runner: WorkspaceAnalysisRunner = {
      preflightOperation: async () => {
        throw new Error("The selected lopper binary does not report feature vscode-preview-capability-parity.");
      },
      analyseWorkspace: async () => emptyAnalysis(folder, "js-ts"),
      exportWorkspace: async () => {
        exportCalls += 1;
        return "{}";
      },
      applyCodemod: async (): Promise<WorkspaceCodemodApplyResult> => {
        throw new Error("unexpected codemod apply");
      },
    };
    const controller = __testing.createController(runner);
    const originalSaveDialog = vscode.window.showSaveDialog;
    const originalShowError = vscode.window.showErrorMessage;
    (vscode.window as typeof vscode.window & { showSaveDialog: typeof vscode.window.showSaveDialog }).showSaveDialog =
      (async () => {
        saveDialogCalls += 1;
        return undefined;
      }) as typeof vscode.window.showSaveDialog;
    (vscode.window as typeof vscode.window & { showErrorMessage: typeof vscode.window.showErrorMessage }).showErrorMessage =
      (async (message: string) => {
        errorMessage = message;
        return undefined;
      }) as typeof vscode.window.showErrorMessage;
    try {
      await controller.exportAnalysis("cyclonedx-json", folder.uri.fsPath);
    } finally {
      (vscode.window as typeof vscode.window & { showSaveDialog: typeof vscode.window.showSaveDialog }).showSaveDialog = originalSaveDialog;
      (vscode.window as typeof vscode.window & { showErrorMessage: typeof vscode.window.showErrorMessage }).showErrorMessage = originalShowError;
      controller.dispose();
    }

    assert.equal(saveDialogCalls, 0);
    assert.equal(exportCalls, 0);
    assert.match(errorMessage, new RegExp(folder.name));
    assert.match(errorMessage, /selected lopper binary does not report feature/);
  });

  test("accepts JS/TS and Python runtime languages only", () => {
    assert.equal(__testing.isRuntimeLanguageSupported("js-ts"), true);
    assert.equal(__testing.isRuntimeLanguageSupported("python"), true);
    assert.equal(__testing.isRuntimeLanguageSupported("ruby"), false);
    assert.equal(__testing.isRuntimeLanguageSupported("auto"), false);
  });

  test("blocks runtime test-command prompting in an untrusted workspace", async () => {
    const folder = primaryWorkspaceFolder();
    const document = await vscode.workspace.openTextDocument(vscode.Uri.file(path.join(folder.uri.fsPath, "src", "index.ts")));
    await vscode.window.showTextDocument(document);
    let analyseCalls = 0;
    let warning = "";
    const runner = testRunner(async () => {
      analyseCalls += 1;
      return emptyAnalysis(folder, "js-ts");
    });
    const controller = __testing.createController(runner, { isWorkspaceTrusted: () => false });
    const restore = stubRuntimeDialogs({
      showOpenDialog: async () => undefined,
      showWarningMessage: async (message) => {
        warning = message;
        return undefined;
      },
    });
    try {
      await controller.refreshRuntimeWorkspace(folder.uri.fsPath);
    } finally {
      restore();
      controller.dispose();
    }

    assert.equal(analyseCalls, 0);
    assert.match(warning, /Trust this workspace/);
  });

  test("allows an existing Python trace without workspace test execution", async () => {
    const folder = secondaryWorkspaceFolder();
    const pythonUri = vscode.Uri.file(path.join(folder.uri.fsPath, "src", "runtime.py"));
    const pythonTracePath = path.join(folder.uri.fsPath, ".artifacts", "python-runtime.ndjson");
    const configuration = vscode.workspace.getConfiguration("lopper", folder.uri);
    await configuration.update("language", "python", vscode.ConfigurationTarget.WorkspaceFolder);
    await configuration.update("runtimeTracePath", pythonTracePath, vscode.ConfigurationTarget.WorkspaceFolder);
    const document = await vscode.workspace.openTextDocument(pythonUri);
    await vscode.window.showTextDocument(document);
    let request: WorkspaceAnalysisRequest | undefined;
    const runner = testRunner(async (_folder, options) => {
      request = options;
      return emptyAnalysis(folder, "python");
    });
    const controller = __testing.createController(runner, { isWorkspaceTrusted: () => false });
    try {
      await controller.refreshRuntimeWorkspace(folder.uri.fsPath);
    } finally {
      controller.dispose();
      await configuration.update("language", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await configuration.update("runtimeTracePath", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
    }

    assert.ok(request, "expected the Python runtime refresh to reach the runner");
    assert.match(request?.runtimeTracePath ?? "", /python-runtime\.ndjson$/);
    assert.equal(request?.runtimeTestCommand, undefined);
  });

  test("forces runtime refresh past a matching static-analysis cache entry", async () => {
    const folder = secondaryWorkspaceFolder();
    const binaryPath = process.env.LOPPER_BINARY_PATH;
    assert.ok(binaryPath, "expected the smoke binary path");
    const resolvedBinaryPath = await realpath(binaryPath);
    const binarySignature = await binaryFileSignature(resolvedBinaryPath);
    const pythonTracePath = path.join(folder.uri.fsPath, ".artifacts", "python-runtime.ndjson");
    const configuration = vscode.workspace.getConfiguration("lopper", folder.uri);
    await configuration.update("language", "python", vscode.ConfigurationTarget.WorkspaceFolder);
    await configuration.update("runtimeTracePath", pythonTracePath, vscode.ConfigurationTarget.WorkspaceFolder);
    const requests: WorkspaceAnalysisRequest[] = [];
    const runner = testRunner(async (_folder, options = {}) => {
      requests.push(options);
      return {
        ...emptyAnalysis(folder, "python"),
        binaryPath: resolvedBinaryPath,
        binarySignature,
      };
    });
    const controller = __testing.createController(runner, { isWorkspaceTrusted: () => false });
    try {
      await controller.refreshWorkspace({ folder, revealErrors: false, trigger: "command" });
      await controller.refreshRuntimeWorkspace(folder.uri.fsPath);
    } finally {
      controller.dispose();
      await configuration.update("language", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await configuration.update("runtimeTracePath", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
    }

    assert.equal(requests.length, 2, "explicit runtime refresh must not reuse static cached analysis");
    assert.equal(requests[0]?.runtimeTracePath, undefined);
    assert.equal(requests[1]?.runtimeTracePath, pythonTracePath);
  });

  test("does not reuse runtime analysis as an ordinary static refresh", async () => {
    const folder = secondaryWorkspaceFolder();
    const binaryPath = process.env.LOPPER_BINARY_PATH;
    assert.ok(binaryPath, "expected the smoke binary path");
    const resolvedBinaryPath = await realpath(binaryPath);
    const binarySignature = await binaryFileSignature(resolvedBinaryPath);
    const pythonTracePath = path.join(folder.uri.fsPath, ".artifacts", "python-runtime.ndjson");
    const configuration = vscode.workspace.getConfiguration("lopper", folder.uri);
    await configuration.update("language", "python", vscode.ConfigurationTarget.WorkspaceFolder);
    await configuration.update("runtimeTracePath", pythonTracePath, vscode.ConfigurationTarget.WorkspaceFolder);
    const requests: WorkspaceAnalysisRequest[] = [];
    const runner = testRunner(async (_folder, options = {}) => {
      requests.push(options);
      return {
        ...emptyAnalysis(folder, "python"),
        binaryPath: resolvedBinaryPath,
        binarySignature,
      };
    });
    const controller = __testing.createController(runner, { isWorkspaceTrusted: () => false });
    try {
      await controller.refreshRuntimeWorkspace(folder.uri.fsPath);
      await controller.refreshWorkspace({ folder, revealErrors: false, trigger: "command" });
    } finally {
      controller.dispose();
      await configuration.update("language", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await configuration.update("runtimeTracePath", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
    }

    assert.equal(requests.length, 2, "ordinary refresh must not reuse a transient runtime result");
    assert.equal(requests[0]?.runtimeTracePath, pythonTracePath);
    assert.equal(requests[1]?.runtimeTracePath, undefined);
  });

  test("does not deduplicate an ordinary refresh against in-flight runtime analysis", async () => {
    const folder = secondaryWorkspaceFolder();
    const binaryPath = process.env.LOPPER_BINARY_PATH;
    assert.ok(binaryPath, "expected the smoke binary path");
    const resolvedBinaryPath = await realpath(binaryPath);
    const binarySignature = await binaryFileSignature(resolvedBinaryPath);
    const pythonTracePath = path.join(folder.uri.fsPath, ".artifacts", "python-runtime.ndjson");
    const configuration = vscode.workspace.getConfiguration("lopper", folder.uri);
    await configuration.update("language", "python", vscode.ConfigurationTarget.WorkspaceFolder);
    await configuration.update("runtimeTracePath", pythonTracePath, vscode.ConfigurationTarget.WorkspaceFolder);

    const requests: WorkspaceAnalysisRequest[] = [];
    let resolveRuntime!: (analysis: WorkspaceAnalysis) => void;
    let markRuntimeStarted!: () => void;
    const runtimeStarted = new Promise<void>((resolve) => {
      markRuntimeStarted = resolve;
    });
    const pendingRuntime = new Promise<WorkspaceAnalysis>((resolve) => {
      resolveRuntime = resolve;
    });
    const runner = testRunner(async (_folder, options = {}) => {
      requests.push(options);
      if (options.runtimeTracePath) {
        markRuntimeStarted();
        return pendingRuntime;
      }
      return {
        ...emptyAnalysis(folder, "python"),
        binaryPath: resolvedBinaryPath,
        binarySignature,
      };
    });
    const controller = __testing.createController(runner, { isWorkspaceTrusted: () => false });
    const runtimeRefresh = controller.refreshRuntimeWorkspace(folder.uri.fsPath);
    await runtimeStarted;
    const ordinaryRefresh = controller.refreshWorkspace({ folder, revealErrors: false, trigger: "command" });
    try {
      await waitForPromise(ordinaryRefresh, "ordinary static refresh");
      assert.equal(requests.length, 2, "ordinary refresh must start distinct work while runtime analysis is pending");
      assert.equal(requests[1]?.runtimeTracePath, undefined);
    } finally {
      resolveRuntime({
        ...emptyAnalysis(folder, "python"),
        binaryPath: resolvedBinaryPath,
        binarySignature,
      });
      await Promise.all([runtimeRefresh, ordinaryRefresh]);
      controller.dispose();
      await configuration.update("language", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
      await configuration.update("runtimeTracePath", undefined, vscode.ConfigurationTarget.WorkspaceFolder);
    }
  });

  test("does not reuse a baseline save result as an ordinary static refresh", async () => {
    const folder = primaryWorkspaceFolder();
    const binaryPath = process.env.LOPPER_BINARY_PATH;
    assert.ok(binaryPath, "expected the smoke binary path");
    const resolvedBinaryPath = await realpath(binaryPath);
    const binarySignature = await binaryFileSignature(resolvedBinaryPath);
    const requests: WorkspaceAnalysisRequest[] = [];
    const runner = testRunner(async (_folder, options = {}) => {
      requests.push(options);
      return {
        ...emptyAnalysis(folder, "js-ts"),
        binaryPath: resolvedBinaryPath,
        binarySignature,
      };
    });
    const controller = __testing.createController(runner);
    const originalInputBox = vscode.window.showInputBox;
    (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox =
      (async () => "release-candidate") as typeof vscode.window.showInputBox;
    try {
      await controller.saveBaselineSnapshot(folder.uri.fsPath);
      await controller.refreshWorkspace({ folder, revealErrors: false, trigger: "command" });
    } finally {
      (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox = originalInputBox;
      controller.dispose();
    }

    assert.equal(requests.length, 2, "ordinary refresh must not reuse a baseline save result");
    assert.equal(requests[0]?.saveBaseline, true);
    assert.equal(requests[1]?.saveBaseline, undefined);
    assert.equal(requests[1]?.baselineStorePath, undefined);
  });

  test("does not reuse baseline comparison results as ordinary static refreshes", async () => {
    const folder = primaryWorkspaceFolder();
    const binaryPath = process.env.LOPPER_BINARY_PATH;
    assert.ok(binaryPath, "expected the smoke binary path");
    const resolvedBinaryPath = await realpath(binaryPath);
    const binarySignature = await binaryFileSignature(resolvedBinaryPath);

    for (const mode of ["Stored baseline key", "Baseline file"] as const) {
      const requests: WorkspaceAnalysisRequest[] = [];
      const runner = testRunner(async (_folder, options = {}) => {
        requests.push(options);
        return {
          ...emptyAnalysis(folder, "js-ts"),
          binaryPath: resolvedBinaryPath,
          binarySignature,
        };
      });
      const controller = __testing.createController(runner);
      const originalInputBox = vscode.window.showInputBox;
      const originalQuickPick = vscode.window.showQuickPick;
      const originalOpenDialog = vscode.window.showOpenDialog;
      (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox =
        (async () => "commit:abc123") as typeof vscode.window.showInputBox;
      (vscode.window as typeof vscode.window & { showQuickPick: typeof vscode.window.showQuickPick }).showQuickPick =
        (async () => ({ label: mode })) as unknown as typeof vscode.window.showQuickPick;
      (vscode.window as typeof vscode.window & { showOpenDialog: typeof vscode.window.showOpenDialog }).showOpenDialog =
        (async () => [vscode.Uri.file(path.join(folder.uri.fsPath, "baseline.json"))]) as typeof vscode.window.showOpenDialog;
      try {
        await controller.compareBaselineSnapshot(folder.uri.fsPath);
        await controller.refreshWorkspace({ folder, revealErrors: false, trigger: "command" });
      } finally {
        (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox = originalInputBox;
        (vscode.window as typeof vscode.window & { showQuickPick: typeof vscode.window.showQuickPick }).showQuickPick = originalQuickPick;
        (vscode.window as typeof vscode.window & { showOpenDialog: typeof vscode.window.showOpenDialog }).showOpenDialog = originalOpenDialog;
        controller.dispose();
      }

      assert.equal(requests.length, 2, `${mode} result must not satisfy an ordinary refresh`);
      assert.ok(requests[0]?.baselineKey || requests[0]?.baselinePath);
      assert.equal(requests[1]?.baselineKey, undefined);
      assert.equal(requests[1]?.baselinePath, undefined);
      assert.equal(requests[1]?.baselineStorePath, undefined);
    }
  });

  test("forces Save and both Compare baseline actions past a matching static cache", async () => {
    const folder = primaryWorkspaceFolder();
    const binaryPath = process.env.LOPPER_BINARY_PATH;
    assert.ok(binaryPath, "expected the smoke binary path");
    const resolvedBinaryPath = await realpath(binaryPath);
    const binarySignature = await binaryFileSignature(resolvedBinaryPath);
    const requests: WorkspaceAnalysisRequest[] = [];
    const runner = testRunner(async (_folder, options = {}) => {
      requests.push(options);
      return {
        ...emptyAnalysis(folder, "js-ts"),
        binaryPath: resolvedBinaryPath,
        binarySignature,
      };
    });
    const controller = __testing.createController(runner);
    const originalInputBox = vscode.window.showInputBox;
    const originalQuickPick = vscode.window.showQuickPick;
    const originalOpenDialog = vscode.window.showOpenDialog;
    const originalInformation = vscode.window.showInformationMessage;
    const compareModes = ["Stored baseline key", "Baseline file"];
    (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox =
      ((async (options?: vscode.InputBoxOptions) => options?.title === "Save Lopper baseline"
        ? "release-candidate"
        : "commit:abc123") as typeof vscode.window.showInputBox);
    (vscode.window as typeof vscode.window & { showQuickPick: typeof vscode.window.showQuickPick }).showQuickPick =
      (async () => ({ label: compareModes.shift() ?? "" })) as unknown as typeof vscode.window.showQuickPick;
    (vscode.window as typeof vscode.window & { showOpenDialog: typeof vscode.window.showOpenDialog }).showOpenDialog =
      (async () => [vscode.Uri.file(path.join(folder.uri.fsPath, "baseline.json"))]) as typeof vscode.window.showOpenDialog;
    (vscode.window as typeof vscode.window & { showInformationMessage: typeof vscode.window.showInformationMessage }).showInformationMessage =
      (async () => undefined) as typeof vscode.window.showInformationMessage;
    try {
      await controller.refreshWorkspace({ folder, revealErrors: false, trigger: "command" });
      await controller.saveBaselineSnapshot(folder.uri.fsPath);
      await controller.compareBaselineSnapshot(folder.uri.fsPath);
      await controller.compareBaselineSnapshot(folder.uri.fsPath);
    } finally {
      (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox = originalInputBox;
      (vscode.window as typeof vscode.window & { showQuickPick: typeof vscode.window.showQuickPick }).showQuickPick = originalQuickPick;
      (vscode.window as typeof vscode.window & { showOpenDialog: typeof vscode.window.showOpenDialog }).showOpenDialog = originalOpenDialog;
      (vscode.window as typeof vscode.window & { showInformationMessage: typeof vscode.window.showInformationMessage }).showInformationMessage = originalInformation;
      controller.dispose();
    }

    assert.equal(requests.length, 4, "each explicit baseline action must execute after the static prime");
    assert.equal(requests[1]?.saveBaseline, true);
    assert.match(requests[1]?.baselineStorePath ?? "", /lopper-baselines$/);
    assert.equal(requests[2]?.baselineKey, "commit:abc123");
    assert.match(requests[2]?.baselineStorePath ?? "", /lopper-baselines$/);
    assert.match(requests[3]?.baselinePath ?? "", /baseline\.json$/);
  });

  test("cancels Save Baseline when the label prompt is dismissed", async () => {
    const folder = primaryWorkspaceFolder();
    let analyseCalls = 0;
    const controller = __testing.createController(testRunner(async () => {
      analyseCalls += 1;
      return emptyAnalysis(folder, "js-ts");
    }));
    const originalInputBox = vscode.window.showInputBox;
    (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox =
      (async () => undefined) as typeof vscode.window.showInputBox;
    try {
      await controller.saveBaselineSnapshot(folder.uri.fsPath);
    } finally {
      (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox = originalInputBox;
      controller.dispose();
    }

    assert.equal(analyseCalls, 0, "dismissing the label prompt must not save a baseline");
  });

  test("saves an unlabelled baseline when an empty label is submitted", async () => {
    const folder = primaryWorkspaceFolder();
    let request: WorkspaceAnalysisRequest | undefined;
    const controller = __testing.createController(testRunner(async (_folder, options) => {
      request = options;
      return emptyAnalysis(folder, "js-ts");
    }));
    const originalInputBox = vscode.window.showInputBox;
    (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox =
      (async () => "") as typeof vscode.window.showInputBox;
    try {
      await controller.saveBaselineSnapshot(folder.uri.fsPath);
    } finally {
      (vscode.window as typeof vscode.window & { showInputBox: typeof vscode.window.showInputBox }).showInputBox = originalInputBox;
      controller.dispose();
    }

    assert.equal(request?.saveBaseline, true);
    assert.equal(request?.baselineLabel, undefined);
  });
});

function testRunner(
  analyseWorkspace: WorkspaceAnalysisRunner["analyseWorkspace"],
): WorkspaceAnalysisRunner {
  return {
    analyseWorkspace,
    exportWorkspace: async (): Promise<string> => "",
    applyCodemod: async (): Promise<WorkspaceCodemodApplyResult> => {
      throw new Error("unexpected codemod apply");
    },
  };
}

function emptyAnalysis(folder: vscode.WorkspaceFolder, language: "js-ts" | "python"): WorkspaceAnalysis {
  return {
    folder,
    binaryPath: "/managed/lopper",
    binarySignature: "/managed/lopper:1",
    requestedLanguage: language,
    scopeMode: "package",
    report: { dependencies: [], summary: { dependencyCount: 0, usedPercent: 0 } },
    codemodsByDependency: new Map(),
  };
}

function primaryWorkspaceFolder(): vscode.WorkspaceFolder {
  const folder = vscode.workspace.workspaceFolders?.[0];
  assert.ok(folder);
  return folder;
}

function secondaryWorkspaceFolder(): vscode.WorkspaceFolder {
  const folder = vscode.workspace.workspaceFolders?.[1];
  assert.ok(folder);
  return folder;
}

async function waitForPromise<T>(promise: Promise<T>, label: string, timeoutMs = 2_000): Promise<T> {
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

function stubRuntimeDialogs(options: {
  showOpenDialog: () => Promise<readonly vscode.Uri[] | undefined>;
  showWarningMessage: (message: string) => Promise<string | undefined>;
}): () => void {
  const originalOpenDialog = vscode.window.showOpenDialog;
  const originalWarning = vscode.window.showWarningMessage;
  const originalInformation = vscode.window.showInformationMessage;
  (vscode.window as typeof vscode.window & { showOpenDialog: typeof vscode.window.showOpenDialog }).showOpenDialog =
    options.showOpenDialog as typeof vscode.window.showOpenDialog;
  (vscode.window as typeof vscode.window & { showWarningMessage: typeof vscode.window.showWarningMessage }).showWarningMessage =
    options.showWarningMessage as typeof vscode.window.showWarningMessage;
  (vscode.window as typeof vscode.window & { showInformationMessage: typeof vscode.window.showInformationMessage }).showInformationMessage =
    (async () => undefined) as typeof vscode.window.showInformationMessage;
  return () => {
    (vscode.window as typeof vscode.window & { showOpenDialog: typeof vscode.window.showOpenDialog }).showOpenDialog = originalOpenDialog;
    (vscode.window as typeof vscode.window & { showWarningMessage: typeof vscode.window.showWarningMessage }).showWarningMessage = originalWarning;
    (vscode.window as typeof vscode.window & { showInformationMessage: typeof vscode.window.showInformationMessage }).showInformationMessage = originalInformation;
  };
}
