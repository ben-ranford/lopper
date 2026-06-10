import { mkdir, realpath, stat, writeFile } from "node:fs/promises";
import * as path from "node:path";
import * as vscode from "vscode";

import {
  configuredLopperLanguage,
  resolveLopperLanguage,
  shouldAutoRefreshForDocument,
  supportedDocumentSelectors,
} from "./languageConfiguration";
import {
  BinaryResolutionError,
  LopperRunner,
  type LopperOutputFormat,
  type WorkspaceAnalysis,
  type WorkspaceAnalysisRequest,
  type WorkspaceAnalysisRunner,
} from "./lopperRunner";
import { RefreshSessionStore } from "./refreshSession";
import { lopperScopeModeValues } from "./types";
import type {
  LopperBaselineComparison,
  LopperDependencyLicense,
  LopperDependencyDelta,
  LopperEffectivePolicy,
  LopperEffectiveThresholds,
  LopperDependencyProvenance,
  LopperCodemodSuggestion,
  LopperDependencyReport,
  LopperImportUse,
  LopperLocation,
  LopperReachabilityConfidence,
  LopperRuntimeUsage,
  LopperScopeMode,
} from "./types";

type DiagnosticKind = "unused-import" | "codemod";

interface DiagnosticMetadata {
  key: string;
  kind: DiagnosticKind;
  dependency: LopperDependencyReport;
  range: vscode.Range;
  suggestion?: LopperCodemodSuggestion;
}

interface ExtensionApi {
  refreshWorkspace(): Promise<void>;
  getLatestSummary(): string;
}

export type RefreshTrigger = "command" | "auto-save" | "workspace-trust" | "initial" | "config-change" | "api";
type AnalysisSource = "fresh" | "cache";

export interface RefreshWorkspaceOptions {
  folder?: vscode.WorkspaceFolder;
  revealErrors?: boolean;
  document?: vscode.TextDocument;
  forceFresh?: boolean;
  scopeModeOverride?: LopperScopeMode;
  trigger?: RefreshTrigger;
  runtimeTracePath?: string;
  runtimeTestCommand?: string;
  baselinePath?: string;
  baselineStorePath?: string;
  baselineKey?: string;
  baselineLabel?: string;
  saveBaseline?: boolean;
}

interface LopperControllerContract extends vscode.Disposable {
  initialize(): Promise<void>;
  refreshWorkspace(options?: RefreshWorkspaceOptions): Promise<void>;
  getLatestSummary(): string;
}

interface LopperControllerFactory {
  create(context: vscode.ExtensionContext): LopperControllerContract;
}

class DefaultLopperControllerFactory implements LopperControllerFactory {
  create(context: vscode.ExtensionContext): LopperControllerContract {
    return new LopperController(context);
  }
}

interface LopperControllerOptions {
  registerWithVSCode?: boolean;
}

interface RefreshAbortHandle {
  workspaceKey: string;
  folderName: string;
  runId: number;
  controller: AbortController;
}

class LopperController implements LopperControllerContract, vscode.HoverProvider, vscode.CodeActionProvider {
  private readonly diagnostics = vscode.languages.createDiagnosticCollection("lopper");
  private readonly statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  private readonly output = vscode.window.createOutputChannel("Lopper");
  private readonly runner: WorkspaceAnalysisRunner;
  private readonly metadataByDocument = new Map<string, Map<string, DiagnosticMetadata>>();
  private readonly documentUrisByWorkspace = new Map<string, Set<string>>();
  private readonly refreshTimers = new Map<string, NodeJS.Timeout>();
  private readonly refreshSessions = new RefreshSessionStore<WorkspaceAnalysis>();
  private readonly refreshAbortHandles = new Map<string, RefreshAbortHandle>();
  private readonly analysisByWorkspace = new Map<string, WorkspaceAnalysis>();
  private readonly detailPanels = new Map<string, vscode.WebviewPanel>();
  private readonly explorer: LopperExplorerTreeDataProvider;
  private latestSummary = "Lopper: idle";
  private missingBinaryWarningShown = false;
  private readonly disposable: vscode.Disposable;

  constructor(
    context: vscode.ExtensionContext,
    runner?: WorkspaceAnalysisRunner,
    options: LopperControllerOptions = {},
  ) {
    const registerWithVSCode = options.registerWithVSCode ?? true;
    this.runner = runner ?? new LopperRunner(this.output, context);
    this.explorer = new LopperExplorerTreeDataProvider(
      () => vscode.workspace.workspaceFolders ?? [],
      (folder) => this.analysisByWorkspace.get(folder.uri.toString()),
    );
    this.statusBar.command = "lopper.refreshWorkspace";
    this.statusBar.text = this.latestSummary;
    this.statusBar.tooltip = "Refresh Lopper diagnostics";
    this.statusBar.show();

    const disposables: vscode.Disposable[] = [this.diagnostics, this.statusBar, this.output];
    if (registerWithVSCode) {
      disposables.push(
        vscode.commands.registerCommand("lopper.refreshWorkspace", async (folderPath?: string) => {
          await this.refreshWorkspace({
            folder: await this.resolveDependencyWorkspaceFolder(folderPath),
            trigger: "command",
          });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.force", async (folderPath?: string) => {
          await this.refreshWorkspace({
            folder: await this.resolveDependencyWorkspaceFolder(folderPath),
            forceFresh: true,
            trigger: "command",
          });
        }),
        vscode.commands.registerCommand("lopper.cancelRefresh", () => {
          this.cancelActiveRefreshes("user requested cancellation");
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.runtime", async (folderPath?: string) => {
          await this.refreshRuntimeWorkspace(folderPath);
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.package", async (folderPath?: string) => {
          await this.refreshWorkspace({
            folder: await this.resolveDependencyWorkspaceFolder(folderPath),
            scopeModeOverride: "package",
            trigger: "command",
          });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.repo", async (folderPath?: string) => {
          await this.refreshWorkspace({
            folder: await this.resolveDependencyWorkspaceFolder(folderPath),
            scopeModeOverride: "repo",
            trigger: "command",
          });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.changedPackages", async (folderPath?: string) => {
          await this.refreshWorkspace({
            folder: await this.resolveDependencyWorkspaceFolder(folderPath),
            scopeModeOverride: "changed-packages",
            trigger: "command",
          });
        }),
        vscode.commands.registerCommand("lopper.saveBaseline", async (folderPath?: string) => {
          await this.saveBaselineSnapshot(folderPath);
        }),
        vscode.commands.registerCommand("lopper.compareBaseline", async (folderPath?: string) => {
          await this.compareBaselineSnapshot(folderPath);
        }),
        vscode.commands.registerCommand("lopper.analyseDependency", async (dependencyName?: string, folderPath?: string) => {
          await this.analyseDependency(dependencyName, folderPath);
        }),
        vscode.commands.registerCommand("lopper.exportAnalysis.json", async (folderPath?: string) => {
          await this.exportAnalysis("json", folderPath);
        }),
        vscode.commands.registerCommand("lopper.exportAnalysis.csv", async (folderPath?: string) => {
          await this.exportAnalysis("csv", folderPath);
        }),
        vscode.commands.registerCommand("lopper.exportAnalysis.sarif", async (folderPath?: string) => {
          await this.exportAnalysis("sarif", folderPath);
        }),
        vscode.commands.registerCommand("lopper.exportAnalysis.prComment", async (folderPath?: string) => {
          await this.exportAnalysis("pr-comment", folderPath);
        }),
        vscode.commands.registerCommand("lopper.openLocation", async (filePath: string, line = 1, column = 1) => {
          await this.openLocation(filePath, line, column);
        }),
        vscode.languages.registerHoverProvider(supportedDocumentSelectors, this),
        vscode.languages.registerCodeActionsProvider(supportedDocumentSelectors, this, {
          providedCodeActionKinds: [vscode.CodeActionKind.QuickFix],
        }),
        vscode.window.registerTreeDataProvider("lopperExplorer", this.explorer),
        vscode.workspace.onDidSaveTextDocument((document) => {
          const folder = vscode.workspace.getWorkspaceFolder(document.uri);
          if (!folder) {
            return;
          }
          this.invalidateWorkspaceSession(folder, `document saved: ${path.basename(document.fileName)}`);
          if (!this.shouldAutoRefresh(document)) {
            return;
          }
          const timerKey = folder.uri.toString();
          this.clearRefreshTimer(timerKey);
          this.refreshTimers.set(
            timerKey,
            setTimeout(() => {
              this.refreshTimers.delete(timerKey);
              void this.refreshWorkspace({ folder, revealErrors: false, document, trigger: "auto-save" });
            }, 400),
          );
        }),
        vscode.workspace.onDidChangeConfiguration((event) => {
          for (const folder of vscode.workspace.workspaceFolders ?? []) {
            if (!event.affectsConfiguration("lopper", folder.uri)) {
              continue;
            }
            this.invalidateWorkspaceSession(folder, "lopper settings changed");
            if (!vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
              continue;
            }
            void this.refreshWorkspace({
              folder,
              revealErrors: false,
              document: this.activeDocumentForFolder(folder),
              trigger: "config-change",
            });
          }
        }),
        vscode.workspace.onDidChangeWorkspaceFolders((event) => {
          for (const removedFolder of event.removed) {
            const folderKey = removedFolder.uri.toString();
            this.clearRefreshTimer(folderKey);
            this.clearWorkspaceDiagnostics(removedFolder);
            this.refreshSessions.clearFolder(folderKey);
            this.analysisByWorkspace.delete(folderKey);
          }
          for (const addedFolder of event.added) {
            if (!vscode.workspace.getConfiguration("lopper", addedFolder.uri).get<boolean>("autoRefresh", true)) {
              continue;
            }
            void this.refreshWorkspace({
              folder: addedFolder,
              revealErrors: false,
              document: this.activeDocumentForFolder(addedFolder),
              trigger: "initial",
            });
          }
          this.explorer.refresh();
        }),
        vscode.workspace.onDidGrantWorkspaceTrust(async () => {
          for (const folder of vscode.workspace.workspaceFolders ?? []) {
            if (!vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
              continue;
            }
            await this.refreshWorkspace({
              folder,
              revealErrors: false,
              document: this.activeDocumentForFolder(folder),
              trigger: "workspace-trust",
            });
          }
        }),
      );
    }
    this.disposable = vscode.Disposable.from(...disposables);
  }

  async initialize(): Promise<void> {
    const folders = vscode.workspace.workspaceFolders ?? [];
    if (folders.length === 0) {
      this.updateStatus("Lopper: no workspace", "Open a folder to analyse with Lopper.");
      return;
    }
    for (const folder of folders) {
      if (!vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
        continue;
      }
      await this.refreshWorkspace({
        folder,
        revealErrors: false,
        document: this.activeDocumentForFolder(folder),
        trigger: "initial",
      });
    }
  }

  async refreshWorkspace(options: RefreshWorkspaceOptions = {}): Promise<void> {
    const folder = options.folder ?? await this.resolveWorkspaceFolder();
    const revealErrors = options.revealErrors ?? true;
    const document = options.document ?? this.activeDocumentForFolder(folder);
    const forceFresh = options.forceFresh ?? false;
    const trigger = options.trigger ?? "command";
    if (!folder) {
      if ((vscode.workspace.workspaceFolders ?? []).length === 0) {
        this.updateStatus("Lopper: no workspace", "Open a folder to analyse with Lopper.");
        if (revealErrors) {
          await vscode.window.showInformationMessage("Open a folder before running Lopper diagnostics.");
        }
      }
      return;
    }
    if (!this.isWorkspaceFolderPresent(folder)) {
      this.output.appendLine(`[refresh:skipped] ${folder.name} no longer exists in the workspace`);
      return;
    }

    const scopeMode = this.requestedScopeMode(folder, options.scopeModeOverride);
    const requestedLanguage = resolveLopperLanguage(configuredLopperLanguage(folder), document, folder.uri.fsPath);
    const workspaceKey = folder.uri.toString();
    const sessionKey = this.sessionKey(workspaceKey, requestedLanguage, scopeMode);

    const reuse = this.tryReuseAnalysis({
      folder,
      workspaceKey,
      sessionKey,
      requestedLanguage,
      scopeMode,
      forceFresh,
      trigger,
    });
    if (reuse === true || (reuse instanceof Promise && await reuse)) {
      return;
    }

    const runId = this.refreshSessions.reserveRun(workspaceKey);
    this.abortSupersededRefreshes(workspaceKey, runId);
    const abortController = new AbortController();
    const abortKey = this.refreshAbortKey(workspaceKey, runId);
    this.refreshAbortHandles.set(abortKey, {
      workspaceKey,
      folderName: folder.name,
      runId,
      controller: abortController,
    });
    this.updateStatus(
      `Lopper: analysing (${scopeMode})`,
      `Running lopper for ${folder.name} (${requestedLanguage}, scope ${scopeMode}).`,
      "lopper.cancelRefresh",
    );
    this.output.appendLine(
      `[refresh:running] ${folder.name} (${requestedLanguage}, ${scopeMode}) trigger=${trigger}${forceFresh ? " force-fresh" : ""}`,
    );

    const refreshPromise = this.executeFreshRefresh({
      folder,
      workspaceKey,
      sessionKey,
      runId,
      revealErrors,
      scopeMode,
      document,
      requestedLanguage,
      runtimeTracePath: options.runtimeTracePath,
      runtimeTestCommand: options.runtimeTestCommand,
      baselinePath: options.baselinePath,
      baselineStorePath: options.baselineStorePath,
      baselineKey: options.baselineKey,
      baselineLabel: options.baselineLabel,
      saveBaseline: options.saveBaseline,
      signal: abortController.signal,
      abortKey,
    });
    this.refreshSessions.setInFlight(workspaceKey, sessionKey, runId, refreshPromise);
    await refreshPromise;
  }

  private tryReuseAnalysis(options: {
    folder: vscode.WorkspaceFolder;
    workspaceKey: string;
    sessionKey: string;
    requestedLanguage: string;
    scopeMode: LopperScopeMode;
    forceFresh: boolean;
    trigger: RefreshTrigger;
  }): boolean | Promise<boolean> {
    if (options.forceFresh) {
      return false;
    }

    const inFlight = this.refreshSessions.inFlight(options.workspaceKey, options.sessionKey);
    if (inFlight) {
      this.output.appendLine(
        `[refresh:reused-running] ${options.folder.name} (${options.requestedLanguage}, ${options.scopeMode}) trigger=${options.trigger}`,
      );
      this.updateStatus(
        `Lopper: analysing (${options.scopeMode})`,
        `Reusing in-flight analysis for ${options.folder.name} (${options.requestedLanguage}, scope ${options.scopeMode}).`,
        "lopper.cancelRefresh",
      );
      return inFlight.promise.then(() => true);
    }

    const cached = this.refreshSessions.getCache(options.workspaceKey, options.sessionKey);
    if (cached) {
      return this.canReuseCachedAnalysis(cached.value).then((canReuse) => {
        if (canReuse) {
          const runId = this.refreshSessions.reserveRun(options.workspaceKey);
          this.abortSupersededRefreshes(options.workspaceKey, runId);
          this.output.appendLine(
            `[refresh:reused-cache] ${options.folder.name} (${options.requestedLanguage}, ${options.scopeMode}) trigger=${options.trigger}`,
          );
          this.applyAnalysisIfCurrent(options.workspaceKey, runId, cached.value, "cache");
          return true;
        }

        this.output.appendLine(
          `[refresh:cache-invalidated] ${options.folder.name} (${options.requestedLanguage}, ${options.scopeMode}) binary changed`,
        );
        return false;
      });
    }

    return false;
  }

  private async executeFreshRefresh(options: {
    folder: vscode.WorkspaceFolder;
    workspaceKey: string;
    sessionKey: string;
    runId: number;
    revealErrors: boolean;
    scopeMode: LopperScopeMode;
    document?: vscode.TextDocument;
    requestedLanguage: string;
    runtimeTracePath?: string;
    runtimeTestCommand?: string;
    baselinePath?: string;
    baselineStorePath?: string;
    baselineKey?: string;
    baselineLabel?: string;
    saveBaseline?: boolean;
    signal?: AbortSignal;
    abortKey?: string;
  }): Promise<void> {
    const {
      folder,
      workspaceKey,
      sessionKey,
      runId,
      revealErrors,
      scopeMode,
      document,
      requestedLanguage,
      runtimeTracePath,
      runtimeTestCommand,
      baselinePath,
      baselineStorePath,
      baselineKey,
      baselineLabel,
      saveBaseline,
      signal,
      abortKey,
    } = options;
    try {
      const request: WorkspaceAnalysisRequest = {
        document,
        scopeMode,
        runtimeTracePath,
        runtimeTestCommand,
        baselinePath,
        baselineStorePath,
        baselineKey,
        baselineLabel,
        saveBaseline,
        signal,
      };
      const analysis = await this.runner.analyseWorkspace(folder, request);
      if (!this.refreshSessions.isLatestRun(workspaceKey, runId)) {
        this.output.appendLine(
          `[refresh:stale] ${folder.name} (${requestedLanguage}, ${scopeMode}) run=${runId} ignored in favor of run=${this.refreshSessions.latestRunId(workspaceKey)}`,
        );
        this.output.appendLine(`[refresh:cancelled] stale run ${runId} did not update diagnostics`);
        return;
      }
      this.refreshSessions.setCache(workspaceKey, sessionKey, analysis);
      this.renderAnalysis(analysis, "fresh");
      this.missingBinaryWarningShown = false;
    } catch (error) {
      if (!this.refreshSessions.isLatestRun(workspaceKey, runId)) {
        this.output.appendLine(
          `[refresh:stale] ${folder.name} (${requestedLanguage}, ${scopeMode}) run=${runId} failed after supersession`,
        );
        this.output.appendLine(`[refresh:cancelled] stale run ${runId} failure ignored`);
        return;
      }

      const message = error instanceof Error ? error.message : String(error);
      this.clearWorkspaceDiagnostics(folder);
      this.analysisByWorkspace.delete(workspaceKey);
      this.explorer.refresh();
      this.updateStatus(`Lopper: unavailable (${scopeMode})`, message);
      this.output.appendLine(`[refresh:error] ${message}`);
      if (error instanceof BinaryResolutionError) {
        if (revealErrors && !this.missingBinaryWarningShown) {
          this.missingBinaryWarningShown = true;
          await vscode.window.showWarningMessage(message);
        }
        return;
      }
      if (revealErrors) {
        await vscode.window.showErrorMessage(`Lopper refresh failed: ${message}`);
      }
    } finally {
      this.refreshSessions.clearInFlight(workspaceKey, sessionKey, runId);
      if (abortKey) {
        this.refreshAbortHandles.delete(abortKey);
      }
    }
  }

  private applyAnalysisIfCurrent(
    workspaceKey: string,
    runId: number,
    analysis: WorkspaceAnalysis,
    source: AnalysisSource,
  ): void {
    if (!this.refreshSessions.isLatestRun(workspaceKey, runId)) {
      this.output.appendLine(
        `[refresh:stale] ${analysis.folder.name} (${analysis.requestedLanguage}, ${analysis.scopeMode}) run=${runId} ignored before render`,
      );
      this.output.appendLine(`[refresh:cancelled] stale cached analysis did not update diagnostics`);
      return;
    }
    this.renderAnalysis(analysis, source);
  }

  private requestedScopeMode(folder: vscode.WorkspaceFolder, override?: LopperScopeMode): LopperScopeMode {
    if (override) {
      return override;
    }
    const configured = vscode.workspace.getConfiguration("lopper", folder.uri).get<string>("scopeMode", "package");
    return normalizeScopeMode(configured);
  }

  private sessionKey(workspaceKey: string, requestedLanguage: string, scopeMode: LopperScopeMode): string {
    return [workspaceKey, requestedLanguage, scopeMode].join("|");
  }

  private invalidateWorkspaceSession(folder: vscode.WorkspaceFolder, reason: string): void {
    this.refreshSessions.bumpInputVersion(folder.uri.toString());
    this.output.appendLine(`[refresh:invalidate] ${folder.name}: ${reason}`);
  }

  private async canReuseCachedAnalysis(analysis: WorkspaceAnalysis): Promise<boolean> {
    const currentSignature = await this.binarySignature(analysis.binaryPath);
    return currentSignature !== undefined && currentSignature === analysis.binarySignature;
  }

  private async binarySignature(binaryPath: string): Promise<string | undefined> {
    try {
      const resolvedPath = await realpath(binaryPath);
      const details = await stat(resolvedPath);
      return `${resolvedPath}:${Math.floor(details.mtimeMs)}`;
    } catch {
      return undefined;
    }
  }

  getLatestSummary(): string {
    return this.latestSummary;
  }

  provideHover(document: vscode.TextDocument, position: vscode.Position): vscode.Hover | undefined {
    const metadata = this.metadataFor(document.uri, position);
    if (metadata.length === 0) {
      return undefined;
    }

    const blocks = metadata.map((item) => {
      const markdown = new vscode.MarkdownString();
      markdown.appendMarkdown("**");
      markdown.appendText(item.dependency.name);
      markdown.appendMarkdown("**\n\n");
      markdown.appendText(
        `Used exports: ${item.dependency.usedExportsCount}/${item.dependency.totalExportsCount} (${item.dependency.usedPercent.toFixed(1)}%)`,
      );
      if (item.kind === "unused-import") {
        markdown.appendMarkdown(`\n\nUnused import on this line.`);
      }
      if (item.kind === "codemod" && item.suggestion) {
        markdown.appendMarkdown(`\n\nSafe quick fix available: `);
        markdown.appendMarkdown("`");
        markdown.appendText(item.suggestion.fromModule);
        markdown.appendMarkdown("` -> `");
        markdown.appendText(item.suggestion.toModule);
        markdown.appendMarkdown("`.");
      }
      const topRiskCue = item.dependency.riskCues?.[0];
      if (topRiskCue) {
        markdown.appendMarkdown(`\n\nRisk cue: `);
        markdown.appendText(topRiskCue.message);
      }
      const recommendation = item.dependency.recommendations?.[0];
      if (recommendation) {
        markdown.appendMarkdown(`\n\nRecommendation: `);
        markdown.appendText(recommendation.message);
      }
      const licenseSummary = formatDependencyLicense(item.dependency.license);
      if (licenseSummary) {
        markdown.appendMarkdown(`\n\nLicense: `);
        markdown.appendText(licenseSummary);
      }
      const provenanceSummary = formatDependencyProvenance(item.dependency.provenance);
      if (provenanceSummary) {
        markdown.appendMarkdown(`\n\nProvenance: `);
        markdown.appendText(provenanceSummary);
      }
      return markdown;
    });

    return new vscode.Hover(blocks, metadata[0].range);
  }

  provideCodeActions(
    document: vscode.TextDocument,
    _range: vscode.Range | vscode.Selection,
    context: vscode.CodeActionContext,
  ): vscode.CodeAction[] {
    const documentMetadata = this.metadataByDocument.get(document.uri.toString());
    if (!documentMetadata) {
      return [];
    }

    const actions: vscode.CodeAction[] = [];
    for (const diagnostic of context.diagnostics) {
      const code = typeof diagnostic.code === "string" ? diagnostic.code : undefined;
      if (!code) {
        continue;
      }
      const metadata = documentMetadata.get(code);
      const suggestion = metadata?.suggestion;
      if (metadata?.kind !== "codemod" || !suggestion) {
        continue;
      }

      const lineIndex = suggestion.line - 1;
      if (lineIndex < 0 || lineIndex >= document.lineCount) {
        continue;
      }
      const currentLine = document.lineAt(lineIndex).text;
      if (currentLine !== suggestion.original) {
        continue;
      }

      const action = new vscode.CodeAction(
        `Use ${suggestion.toModule} subpath import`,
        vscode.CodeActionKind.QuickFix,
      );
      action.isPreferred = true;
      action.diagnostics = [diagnostic];
      const edit = new vscode.WorkspaceEdit();
      edit.replace(document.uri, document.lineAt(lineIndex).range, suggestion.replacement);
      action.edit = edit;
      actions.push(action);
    }

    return actions;
  }

  dispose(): void {
    this.cancelActiveRefreshes("extension disposed");
    this.refreshAbortHandles.clear();
    for (const timer of this.refreshTimers.values()) {
      clearTimeout(timer);
    }
    this.refreshTimers.clear();
    this.disposable.dispose();
  }

  private async resolveWorkspaceFolder(folder?: vscode.WorkspaceFolder): Promise<vscode.WorkspaceFolder | undefined> {
    if (folder) {
      return folder;
    }

    const activeDocument = vscode.window.activeTextEditor?.document;
    if (activeDocument) {
      const activeFolder = vscode.workspace.getWorkspaceFolder(activeDocument.uri);
      if (activeFolder) {
        return activeFolder;
      }
    }

    const folders = vscode.workspace.workspaceFolders ?? [];
    if (folders.length === 0) {
      return undefined;
    }
    if (folders.length === 1) {
      return folders[0];
    }

    const picked = await vscode.window.showWorkspaceFolderPick({
      placeHolder: "Choose a workspace folder for the Lopper analysis",
    });
    return picked ?? undefined;
  }

  private primaryWorkspaceFolder(): vscode.WorkspaceFolder | undefined {
    return vscode.workspace.workspaceFolders?.[0];
  }

  private activeDocumentForFolder(folder?: vscode.WorkspaceFolder): vscode.TextDocument | undefined {
    const document = vscode.window.activeTextEditor?.document;
    if (!document) {
      return undefined;
    }
    if (!folder) {
      return document;
    }
    const activeFolder = vscode.workspace.getWorkspaceFolder(document.uri);
    return activeFolder?.uri.toString() === folder.uri.toString() ? document : undefined;
  }

  private shouldAutoRefresh(document: vscode.TextDocument): boolean {
    if (document.isUntitled) {
      return false;
    }
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    if (!folder) {
      return false;
    }
    if (!vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
      return false;
    }
    return shouldAutoRefreshForDocument(configuredLopperLanguage(folder), document, folder.uri.fsPath);
  }

  private async refreshRuntimeWorkspace(folderPath?: string): Promise<void> {
    const folder = await this.resolveDependencyWorkspaceFolder(folderPath);
    if (!folder) {
      await vscode.window.showInformationMessage("Open a folder before running Lopper runtime analysis.");
      return;
    }

    const requestedLanguage = resolveLopperLanguage(
      configuredLopperLanguage(folder),
      this.activeDocumentForFolder(folder),
      folder.uri.fsPath,
    );
    if (requestedLanguage !== "js-ts") {
      await vscode.window.showInformationMessage("Runtime-aware analysis is currently intended for JS/TS workspaces.");
      return;
    }

    const runtimeTracePath = await this.resolveRuntimeTracePath(folder);
    const runtimeTestCommand = runtimeTracePath ? undefined : await this.resolveRuntimeTestCommand(folder);
    if (!runtimeTracePath && !runtimeTestCommand) {
      await vscode.window.showInformationMessage("Provide a runtime trace file or test command to run runtime-aware analysis.");
      return;
    }

    await this.refreshWorkspace({
      folder,
      document: this.activeDocumentForFolder(folder),
      revealErrors: true,
      trigger: "command",
      runtimeTracePath,
      runtimeTestCommand,
    });
  }

  private async saveBaselineSnapshot(folderPath?: string): Promise<void> {
    const folder = await this.resolveDependencyWorkspaceFolder(folderPath);
    if (!folder) {
      await vscode.window.showInformationMessage("Open a folder before saving a baseline snapshot.");
      return;
    }

    const baselineStorePath = this.defaultBaselineStorePath(folder);
    const baselineLabel = await vscode.window.showInputBox({
      title: "Save Lopper baseline",
      prompt: "Optional label for the saved baseline snapshot.",
      placeHolder: "release-candidate",
    });

    await this.refreshWorkspace({
      folder,
      document: this.activeDocumentForFolder(folder),
      revealErrors: true,
      trigger: "command",
      baselineStorePath,
      baselineLabel: baselineLabel?.trim().length ? baselineLabel.trim() : undefined,
      saveBaseline: true,
    });
  }

  private async compareBaselineSnapshot(folderPath?: string): Promise<void> {
    const folder = await this.resolveDependencyWorkspaceFolder(folderPath);
    if (!folder) {
      await vscode.window.showInformationMessage("Open a folder before comparing against a baseline.");
      return;
    }

    const mode = await vscode.window.showQuickPick(
      [
        { label: "Stored baseline key", description: "Compare against a saved baseline snapshot key." },
        { label: "Baseline file", description: "Compare against a baseline JSON file." },
      ],
      {
        title: "Compare Lopper baseline",
        placeHolder: "Choose a baseline source",
      },
    );
    if (!mode) {
      return;
    }

    if (mode.label === "Baseline file") {
      const fileUris = await vscode.window.showOpenDialog({
        canSelectFiles: true,
        canSelectFolders: false,
        canSelectMany: false,
        title: "Select a baseline file",
        defaultUri: vscode.Uri.file(folder.uri.fsPath),
        filters: {
          JSON: ["json"],
        },
      });
      const baselinePath = fileUris?.[0]?.fsPath;
      if (!baselinePath) {
        return;
      }
      await this.refreshWorkspace({
        folder,
        document: this.activeDocumentForFolder(folder),
        revealErrors: true,
        trigger: "command",
        baselinePath,
      });
      return;
    }

    const baselineKey = await vscode.window.showInputBox({
      title: "Compare Lopper baseline",
      prompt: "Baseline key from the snapshot store.",
      placeHolder: "commit:abc123",
    });
    if (!baselineKey || baselineKey.trim().length === 0) {
      return;
    }

    await this.refreshWorkspace({
      folder,
      document: this.activeDocumentForFolder(folder),
      revealErrors: true,
      trigger: "command",
      baselineStorePath: this.defaultBaselineStorePath(folder),
      baselineKey: baselineKey.trim(),
    });
  }

  private async analyseDependency(dependencyName?: string, folderPath?: string): Promise<void> {
    const folder = await this.resolveDependencyWorkspaceFolder(folderPath);
    if (!folder) {
      await vscode.window.showInformationMessage("Open a folder before analysing a dependency.");
      return;
    }

    const targetDependency = await this.resolveDependencyName(dependencyName);
    if (!targetDependency) {
      return;
    }

    const analysis = this.analysisByWorkspace.get(folder.uri.toString());
    const dependency = analysis?.report.dependencies.find((item) => item.name === targetDependency);
    if (dependency && analysis) {
      this.showDependencyDetailPanel(folder, analysis, dependency);
      return;
    }

    const focused = await this.runner.analyseWorkspace(folder, {
      document: this.activeDocumentForFolder(folder),
      scopeMode: this.requestedScopeMode(folder),
      dependencyName: targetDependency,
    });
    const focusedDependency = focused.report.dependencies[0];
    if (!focusedDependency) {
      await vscode.window.showInformationMessage(`No dependency details were returned for ${targetDependency}.`);
      return;
    }
    this.showDependencyDetailPanel(folder, focused, focusedDependency, targetDependency);
  }

  private async exportAnalysis(format: LopperOutputFormat, folderPath?: string): Promise<void> {
    const folder = await this.resolveDependencyWorkspaceFolder(folderPath);
    if (!folder) {
      await vscode.window.showInformationMessage("Open a folder before exporting analysis results.");
      return;
    }

    const exportPath = await this.pickExportTarget(folder, format);
    if (!exportPath) {
      return;
    }

    const content = await this.runner.exportWorkspace(folder, format, {
      document: this.activeDocumentForFolder(folder),
      scopeMode: this.requestedScopeMode(folder),
    });
    await this.writeExportFile(exportPath, content);
    const document = await vscode.workspace.openTextDocument(vscode.Uri.file(exportPath));
    await vscode.window.showTextDocument(document, { preview: true });
    await vscode.window.showInformationMessage(`Lopper exported ${format} output to ${exportPath}`);
  }

  private async openLocation(filePath: string, line = 1, column = 1): Promise<void> {
    const uri = vscode.Uri.file(filePath);
    const workspaceFolder = vscode.workspace.getWorkspaceFolder(uri);
    if (!workspaceFolder || !isPathInsideWorkspace(uri.fsPath, workspaceFolder.uri.fsPath)) {
      this.output.appendLine(`[open-location:skipped] refusing to open outside workspace: ${filePath}`);
      return;
    }
    const document = await vscode.workspace.openTextDocument(uri);
    const editor = await vscode.window.showTextDocument(document, { preview: true });
    const targetLine = Math.max(0, line - 1);
    const targetColumn = Math.max(0, column - 1);
    const lineText = document.lineAt(Math.min(targetLine, document.lineCount - 1));
    const range = new vscode.Range(
      Math.min(targetLine, document.lineCount - 1),
      targetColumn,
      Math.min(targetLine, document.lineCount - 1),
      Math.min(lineText.text.length, Math.max(targetColumn + 1, targetColumn)),
    );
    editor.revealRange(range, vscode.TextEditorRevealType.InCenter);
    editor.selection = new vscode.Selection(range.start, range.end);
  }

  private async resolveRuntimeTracePath(folder: vscode.WorkspaceFolder): Promise<string | undefined> {
    const configured = vscode.workspace.getConfiguration("lopper", folder.uri).get<string>("runtimeTracePath", "");
    if (configured.trim().length > 0) {
      return configured.trim();
    }

    const selected = await vscode.window.showOpenDialog({
      title: "Select a runtime trace file",
      canSelectFiles: true,
      canSelectFolders: false,
      canSelectMany: false,
      defaultUri: vscode.Uri.file(folder.uri.fsPath),
      filters: {
        "Runtime traces": ["ndjson", "json", "log"],
      },
    });
    return selected?.[0]?.fsPath;
  }

  private async resolveRuntimeTestCommand(folder: vscode.WorkspaceFolder): Promise<string | undefined> {
    const configured = vscode.workspace.getConfiguration("lopper", folder.uri).get<string>("runtimeTestCommand", "");
    if (configured.trim().length > 0) {
      return configured.trim();
    }

    const command = await vscode.window.showInputBox({
      title: "Runtime test command",
      prompt: "Command to run while capturing the runtime trace.",
      placeHolder: "npm test",
    });
    return command?.trim().length ? command.trim() : undefined;
  }

  private defaultBaselineStorePath(folder: vscode.WorkspaceFolder): string {
    return path.join(folder.uri.fsPath, ".artifacts", "lopper-baselines");
  }

  private async pickExportTarget(folder: vscode.WorkspaceFolder, format: LopperOutputFormat): Promise<string | undefined> {
    const defaultExtension = format === "pr-comment" ? "md" : format;
    const defaultFileName = `lopper-analysis.${defaultExtension}`;
    const target = await vscode.window.showSaveDialog({
      title: `Export Lopper ${format.toUpperCase()} output`,
      defaultUri: vscode.Uri.file(path.join(folder.uri.fsPath, defaultFileName)),
      filters: {
        [format.toUpperCase()]: [defaultExtension],
      },
    });
    return target?.fsPath;
  }

  private async writeExportFile(filePath: string, content: string): Promise<void> {
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, content, "utf8");
  }

  private async resolveDependencyWorkspaceFolder(folderPath?: string): Promise<vscode.WorkspaceFolder | undefined> {
    if (folderPath) {
      const resolved = path.resolve(folderPath);
      const folder = (vscode.workspace.workspaceFolders ?? []).find(
        (candidate) => path.resolve(candidate.uri.fsPath) === resolved,
      );
      if (folder) {
        return folder;
      }
    }
    return this.resolveWorkspaceFolder();
  }

  private async resolveDependencyName(dependencyName?: string): Promise<string | undefined> {
    const trimmed = dependencyName?.trim();
    if (trimmed) {
      return trimmed;
    }
    const value = await vscode.window.showInputBox({
      title: "Analyse dependency",
      prompt: "Enter a dependency name to analyse.",
      placeHolder: "scope-lib",
    });
    return value?.trim().length ? value.trim() : undefined;
  }

  private showDependencyDetailPanel(
    folder: vscode.WorkspaceFolder,
    analysis: WorkspaceAnalysis,
    dependency: LopperDependencyReport,
    requestedDependencyName?: string,
  ): void {
    const dependencyName = requestedDependencyName ?? dependency.name;
    const panelKey = `${folder.uri.toString()}|${dependencyName}`;
    const title = `Lopper: ${dependencyName}`;
    const existingPanel = this.detailPanels.get(panelKey);
    if (existingPanel) {
      existingPanel.title = title;
      existingPanel.webview.html = renderDependencyDetailHtml(folder, analysis, dependency, dependencyName);
      existingPanel.reveal(vscode.ViewColumn.Beside);
      return;
    }

    const panel = vscode.window.createWebviewPanel(
      "lopperDependencyDetail",
      title,
      vscode.ViewColumn.Beside,
      {
        enableScripts: true,
        enableCommandUris: true,
        retainContextWhenHidden: true,
      },
    );
    this.detailPanels.set(panelKey, panel);
    panel.onDidDispose(() => {
      this.detailPanels.delete(panelKey);
    });
    panel.webview.html = renderDependencyDetailHtml(folder, analysis, dependency, dependencyName);
  }

  private renderAnalysis(analysis: WorkspaceAnalysis, source: AnalysisSource): void {
    this.clearWorkspaceDiagnostics(analysis.folder);

    const diagnosticsByUri = new Map<string, vscode.Diagnostic[]>();
    const metadataByUri = new Map<string, Map<string, DiagnosticMetadata>>();

    for (const dependency of analysis.report.dependencies) {
      this.addUnusedImportDiagnostics(analysis, dependency, diagnosticsByUri, metadataByUri);
      this.addCodemodDiagnostics(analysis, dependency, diagnosticsByUri, metadataByUri);
    }

    const trackedUris = new Set<string>();
    for (const [uriString, diagnostics] of diagnosticsByUri) {
      const uri = vscode.Uri.parse(uriString);
      this.diagnostics.set(uri, diagnostics);
      const metadata = metadataByUri.get(uriString);
      if (metadata) {
        this.metadataByDocument.set(uriString, metadata);
      }
      trackedUris.add(uriString);
    }
    this.documentUrisByWorkspace.set(analysis.folder.uri.toString(), trackedUris);

    const dependencyCount = analysis.report.summary?.dependencyCount ?? analysis.report.dependencies.length;
    const usedPercent = analysis.report.summary?.usedPercent ?? 0;
    const warningCount = analysis.report.warnings?.length ?? 0;
    const sourceSummary = source === "cache" ? "cached" : "fresh";
    const warningSummary = warningCount > 0 ? ` | Warnings: ${warningCount}` : "";
    this.updateStatus(
      `Lopper: ${dependencyCount} deps | ${usedPercent.toFixed(1)}% used | ${analysis.scopeMode}${source === "cache" ? " | cached" : ""}`,
      `Folder: ${analysis.folder.name} | Scope: ${analysis.scopeMode} | Adapter: ${analysis.requestedLanguage} | Source: ${sourceSummary} | Binary: ${path.basename(analysis.binaryPath)}${warningSummary}`,
    );
    this.analysisByWorkspace.set(analysis.folder.uri.toString(), analysis);
    this.explorer.refresh();
    for (const warning of analysis.report.warnings ?? []) {
      this.output.appendLine(`[refresh:warning] ${analysis.folder.name} (${analysis.scopeMode}): ${warning}`);
    }
  }

  private addUnusedImportDiagnostics(
    analysis: WorkspaceAnalysis,
    dependency: LopperDependencyReport,
    diagnosticsByUri: Map<string, vscode.Diagnostic[]>,
    metadataByUri: Map<string, Map<string, DiagnosticMetadata>>,
  ): void {
    for (const importUse of dependency.unusedImports ?? []) {
      for (const location of importUse.locations ?? []) {
        const resolvedLocation = this.resolveLocation(analysis.folder, location, importUse);
        if (!resolvedLocation) {
          this.output.appendLine(
            `[refresh:skipped] ignoring out-of-workspace unused-import location for ${dependency.name}: ${location.file}`,
          );
          continue;
        }
        const { uri, range } = resolvedLocation;
        const key = this.metadataKey(dependency.name, "unused-import", location.file, location.line, importUse.name);
        const diagnostic = new vscode.Diagnostic(
          range,
          `${dependency.name} import "${importUse.name}" is unused. Workspace usage is ${dependency.usedPercent.toFixed(1)}%.`,
          vscode.DiagnosticSeverity.Warning,
        );
        diagnostic.source = "lopper";
        diagnostic.code = key;
        this.pushDiagnostic(diagnosticsByUri, metadataByUri, uri, diagnostic, {
          key,
          kind: "unused-import",
          dependency,
          range,
        });
      }
    }
  }

  private addCodemodDiagnostics(
    analysis: WorkspaceAnalysis,
    dependency: LopperDependencyReport,
    diagnosticsByUri: Map<string, vscode.Diagnostic[]>,
    metadataByUri: Map<string, Map<string, DiagnosticMetadata>>,
  ): void {
    const codemod = analysis.codemodsByDependency.get(dependency.name);
    for (const suggestion of codemod?.suggestions ?? []) {
      const resolvedFilePath = resolveWorkspaceFilePath(analysis.folder.uri.fsPath, suggestion.file);
      if (!resolvedFilePath) {
        this.output.appendLine(
          `[refresh:skipped] ignoring out-of-workspace codemod suggestion for ${dependency.name}: ${suggestion.file}`,
        );
        continue;
      }
      const uri = vscode.Uri.file(resolvedFilePath);
      const lineIndex = Math.max(0, suggestion.line - 1);
      const range = new vscode.Range(lineIndex, 0, lineIndex, suggestion.original.length);
      const key = this.metadataKey(dependency.name, "codemod", suggestion.file, suggestion.line, suggestion.importName);
      const diagnostic = new vscode.Diagnostic(
        range,
        `Use subpath import ${suggestion.toModule} for ${suggestion.importName} to keep imports narrow and deterministic.`,
        vscode.DiagnosticSeverity.Information,
      );
      diagnostic.source = "lopper";
      diagnostic.code = key;
      this.pushDiagnostic(diagnosticsByUri, metadataByUri, uri, diagnostic, {
        key,
        kind: "codemod",
        dependency,
        range,
        suggestion,
      });
    }
  }

  private resolveLocation(
    folder: vscode.WorkspaceFolder,
    location: LopperLocation,
    importUse: LopperImportUse,
  ): { uri: vscode.Uri; range: vscode.Range } | undefined {
    const resolvedFilePath = resolveWorkspaceFilePath(folder.uri.fsPath, location.file);
    if (!resolvedFilePath) {
      return undefined;
    }
    const uri = vscode.Uri.file(resolvedFilePath);
    const line = Math.max(0, location.line - 1);
    const startColumn = Math.max(0, location.column - 1);
    const endColumn = startColumn + Math.max(importUse.name.length, 1);
    return {
      uri,
      range: new vscode.Range(line, startColumn, line, endColumn),
    };
  }

  private findWorkspaceFolder(folderPath: string): vscode.WorkspaceFolder | undefined {
    const resolved = path.resolve(folderPath);
    return (vscode.workspace.workspaceFolders ?? []).find((folder) => path.resolve(folder.uri.fsPath) === resolved);
  }

  private pushDiagnostic(
    diagnosticsByUri: Map<string, vscode.Diagnostic[]>,
    metadataByUri: Map<string, Map<string, DiagnosticMetadata>>,
    uri: vscode.Uri,
    diagnostic: vscode.Diagnostic,
    metadata: DiagnosticMetadata,
  ): void {
    const uriKey = uri.toString();
    const diagnostics = diagnosticsByUri.get(uriKey) ?? [];
    diagnostics.push(diagnostic);
    diagnosticsByUri.set(uriKey, diagnostics);

    const metadataMap = metadataByUri.get(uriKey) ?? new Map<string, DiagnosticMetadata>();
    metadataMap.set(metadata.key, metadata);
    metadataByUri.set(uriKey, metadataMap);
  }

  private metadataFor(uri: vscode.Uri, position: vscode.Position): DiagnosticMetadata[] {
    const documentMetadata = this.metadataByDocument.get(uri.toString());
    if (!documentMetadata) {
      return [];
    }
    return Array.from(documentMetadata.values()).filter((item) => item.range.contains(position));
  }

  private metadataKey(
    dependencyName: string,
    kind: DiagnosticKind,
    file: string,
    line: number,
    importName: string,
  ): string {
    return [dependencyName, kind, file, String(line), importName].join(":");
  }

  private clearWorkspaceDiagnostics(folder: vscode.WorkspaceFolder): void {
    const workspaceKey = folder.uri.toString();
    const documentUris = this.documentUrisByWorkspace.get(workspaceKey);
    if (!documentUris) {
      return;
    }
    for (const uriString of documentUris) {
      const uri = vscode.Uri.parse(uriString);
      this.diagnostics.delete(uri);
      this.metadataByDocument.delete(uriString);
    }
    this.documentUrisByWorkspace.delete(workspaceKey);
  }

  private clearRefreshTimer(workspaceKey: string): void {
    const timer = this.refreshTimers.get(workspaceKey);
    if (!timer) {
      return;
    }
    clearTimeout(timer);
    this.refreshTimers.delete(workspaceKey);
  }

  private isWorkspaceFolderPresent(folder: vscode.WorkspaceFolder): boolean {
    return (vscode.workspace.workspaceFolders ?? []).some(
      (workspaceFolder) => workspaceFolder.uri.toString() === folder.uri.toString(),
    );
  }

  private abortSupersededRefreshes(workspaceKey: string, latestRunId: number): void {
    for (const handle of this.refreshAbortHandles.values()) {
      if (handle.workspaceKey !== workspaceKey || handle.runId === latestRunId || handle.controller.signal.aborted) {
        continue;
      }
      this.output.appendLine(
        `[refresh:abort] ${handle.folderName} run=${handle.runId} superseded by run=${latestRunId}`,
      );
      handle.controller.abort();
    }
  }

  private cancelActiveRefreshes(reason: string): void {
    let cancelled = 0;
    for (const handle of this.refreshAbortHandles.values()) {
      if (handle.controller.signal.aborted) {
        continue;
      }
      cancelled += 1;
      this.output.appendLine(`[refresh:abort] ${handle.folderName} run=${handle.runId}: ${reason}`);
      handle.controller.abort();
    }
    if (cancelled > 0) {
      this.updateStatus("Lopper: cancelling", "Cancelling active Lopper analysis.");
    }
  }

  private refreshAbortKey(workspaceKey: string, runId: number): string {
    return `${workspaceKey}|${runId}`;
  }

  private updateStatus(text: string, tooltip: string, command = "lopper.refreshWorkspace"): void {
    this.latestSummary = text;
    this.statusBar.text = text;
    this.statusBar.tooltip = tooltip;
    this.statusBar.command = command;
  }
}

function formatDependencyLicense(license?: LopperDependencyLicense): string | undefined {
  if (!license) {
    return undefined;
  }

  const segments: string[] = [];
  if (license.denied) {
    segments.push("DENIED");
  } else if (license.unknown) {
    segments.push("Unknown");
  }

  if (license.spdx) {
    segments.push(license.spdx);
  } else if (license.raw) {
    segments.push(license.raw);
  } else if (license.unknown || license.denied) {
    segments.push("unresolved");
  } else {
    segments.push("missing");
  }

  if (license.source) {
    segments.push(`source: ${license.source}`);
  }
  if (license.confidence) {
    segments.push(`confidence: ${license.confidence}`);
  }
  const evidence = license.evidence ?? [];
  if (evidence.length > 0) {
    segments.push(`evidence: ${evidence.join(", ")}`);
  }

  return segments.join(" • ");
}

function formatDependencyProvenance(provenance?: LopperDependencyProvenance): string | undefined {
  if (!provenance) {
    return undefined;
  }

  const segments: string[] = [];
  if (provenance.source) {
    segments.push(`source: ${provenance.source}`);
  }
  if (provenance.confidence) {
    segments.push(`confidence: ${provenance.confidence}`);
  }
  const signals = provenance.signals ?? [];
  if (signals.length > 0) {
    segments.push(`signals: ${signals.join(", ")}`);
  }
  if (segments.length === 0) {
    return "unresolved";
  }
  return segments.join(" • ");
}

class LopperExtensionBootstrap implements vscode.Disposable {
  private controller: LopperControllerContract | undefined;

  constructor(private readonly factory: LopperControllerFactory = new DefaultLopperControllerFactory()) {}

  async activate(context: vscode.ExtensionContext): Promise<ExtensionApi> {
    this.controller?.dispose();
    this.controller = undefined;

    const controller = this.factory.create(context);
    try {
      await controller.initialize();
      this.controller = controller;
      return this.extensionApi();
    } catch (error) {
      controller.dispose();
      throw error;
    }
  }

  dispose(): void {
    this.controller?.dispose();
    this.controller = undefined;
  }

  private extensionApi(): ExtensionApi {
    return {
      refreshWorkspace: async () => {
        await this.controller?.refreshWorkspace({ trigger: "api" });
      },
      getLatestSummary: () => this.controller?.getLatestSummary() ?? "Lopper: idle",
    };
  }
}

export const __testing = {
  createController: (runner: WorkspaceAnalysisRunner): LopperControllerContract =>
    new LopperController({} as vscode.ExtensionContext, runner, { registerWithVSCode: false }),
};

const bootstrap = new LopperExtensionBootstrap();

export async function activate(context: vscode.ExtensionContext): Promise<ExtensionApi> {
  return bootstrap.activate(context);
}

export function deactivate(): void {
  bootstrap.dispose();
}

function normalizeScopeMode(value: string | undefined): LopperScopeMode {
  const normalized = value?.trim().toLowerCase() as LopperScopeMode | undefined;
  if (normalized && (lopperScopeModeValues as readonly string[]).includes(normalized)) {
    return normalized;
  }
  return "package";
}

type LopperExplorerNodeKind = "folder" | "summary" | "dependency" | "group" | "import" | "metric";

interface LopperExplorerNodeData {
  kind: LopperExplorerNodeKind;
  folderPath: string;
  dependencyName?: string;
  groupKind?: "usedImports" | "unusedImports";
  filePath?: string;
  line?: number;
  column?: number;
}

class LopperExplorerTreeItem extends vscode.TreeItem {
  constructor(
    label: string,
    public readonly data: LopperExplorerNodeData,
    collapsibleState: vscode.TreeItemCollapsibleState,
  ) {
    super(label, collapsibleState);
  }
}

class LopperExplorerTreeDataProvider implements vscode.TreeDataProvider<LopperExplorerTreeItem> {
  private readonly changeEmitter = new vscode.EventEmitter<void>();

  readonly onDidChangeTreeData = this.changeEmitter.event;

  constructor(
    private readonly getFolders: () => readonly vscode.WorkspaceFolder[],
    private readonly getAnalysis: (folder: vscode.WorkspaceFolder) => WorkspaceAnalysis | undefined,
  ) {}

  refresh(): void {
    this.changeEmitter.fire();
  }

  getTreeItem(element: LopperExplorerTreeItem): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: LopperExplorerTreeItem): Promise<LopperExplorerTreeItem[]> {
    if (!element) {
      return this.getFolders().map((folder) => this.createFolderItem(folder));
    }

    switch (element.data.kind) {
      case "folder":
        return this.getFolderChildren(element.data.folderPath);
      case "dependency":
        return this.getDependencyChildren(element.data.folderPath, element.data.dependencyName ?? "");
      case "group":
        return this.getImportChildren(
          element.data.folderPath,
          element.data.dependencyName ?? "",
          element.data.groupKind ?? "usedImports",
        );
      default:
        return [];
    }
  }

  private createFolderItem(folder: vscode.WorkspaceFolder): LopperExplorerTreeItem {
    const analysis = this.getAnalysis(folder);
    const folderPath = folder.uri.fsPath;
    const item = new LopperExplorerTreeItem(
      folder.name,
      { kind: "folder", folderPath },
      vscode.TreeItemCollapsibleState.Collapsed,
    );
    item.iconPath = new vscode.ThemeIcon("folder-opened");
    item.command = {
      command: "lopper.refreshWorkspace",
      title: "Refresh Lopper diagnostics",
      arguments: [folderPath],
    };
    item.description = analysis ? `${analysis.report.dependencies.length} deps • ${analysis.report.summary?.usedPercent.toFixed(1) ?? "0.0"}% used` : "No analysis yet";
    item.tooltip = analysis ? buildFolderTooltip(folder, analysis) : `${folder.name}\nClick to run Lopper diagnostics.`;
    return item;
  }

  private async getFolderChildren(folderPath: string): Promise<LopperExplorerTreeItem[]> {
    const folder = findWorkspaceFolder(folderPath);
    if (!folder) {
      return [];
    }
    const analysis = this.getAnalysis(folder);
    if (!analysis) {
      return [
        this.createSummaryItem(folder, undefined),
        this.createActionItem(folder, "Run diagnostics", "lopper.refreshWorkspace"),
      ];
    }

    const dependencies = [...analysis.report.dependencies].sort((left, right) => {
      const leftWaste = 100 - left.usedPercent;
      const rightWaste = 100 - right.usedPercent;
      if (leftWaste !== rightWaste) {
        return rightWaste - leftWaste;
      }
      return left.name.localeCompare(right.name);
    });

    return [
      this.createSummaryItem(folder, analysis),
      ...dependencies.map((dependency) => this.createDependencyItem(folder, analysis, dependency)),
    ];
  }

  private createSummaryItem(folder: vscode.WorkspaceFolder, analysis: WorkspaceAnalysis | undefined): LopperExplorerTreeItem {
    const folderPath = folder.uri.fsPath;
    const item = new LopperExplorerTreeItem(
      "Workspace summary",
      { kind: "summary", folderPath },
      vscode.TreeItemCollapsibleState.None,
    );
    item.iconPath = new vscode.ThemeIcon("dashboard");
    item.command = {
      command: "lopper.refreshWorkspace",
      title: "Refresh Lopper diagnostics",
      arguments: [folderPath],
    };
    if (!analysis) {
      item.description = "Run diagnostics to populate summary data.";
      item.tooltip = "No analysis has been run for this folder yet.";
      return item;
    }

    const summary = analysis.report.summary;
    const warningCount = analysis.report.warnings?.length ?? 0;
    const baselineDelta = analysis.report.wasteIncreasePercent;
    const runtimeStatus = runtimeUsageSummary(analysis.report.dependencies[0]?.runtimeUsage);
    item.description = [
      `${summary?.dependencyCount ?? analysis.report.dependencies.length} deps`,
      `${summary?.usedPercent.toFixed(1) ?? "0.0"}% used`,
      analysis.scopeMode,
      runtimeStatus,
      baselineDelta === undefined ? undefined : `delta ${baselineDelta.toFixed(1)}% waste`,
    ]
      .filter((value): value is string => Boolean(value && value.length > 0))
      .join(" • ");
    item.tooltip = buildFolderTooltip(folder, analysis);
    if (warningCount > 0) {
      item.iconPath = new vscode.ThemeIcon("warning");
    }
    return item;
  }

  private createActionItem(
    folder: vscode.WorkspaceFolder,
    label: string,
    command: string,
  ): LopperExplorerTreeItem {
    const folderPath = folder.uri.fsPath;
    const item = new LopperExplorerTreeItem(
      label,
      { kind: "metric", folderPath },
      vscode.TreeItemCollapsibleState.None,
    );
    item.command = {
      command,
      title: label,
      arguments: [folderPath],
    };
    item.iconPath = new vscode.ThemeIcon("run");
    return item;
  }

  private createDependencyItem(
    folder: vscode.WorkspaceFolder,
    analysis: WorkspaceAnalysis,
    dependency: LopperDependencyReport,
  ): LopperExplorerTreeItem {
    const folderPath = folder.uri.fsPath;
    const item = new LopperExplorerTreeItem(
      dependency.name,
      { kind: "dependency", folderPath, dependencyName: dependency.name },
      vscode.TreeItemCollapsibleState.Collapsed,
    );
    item.iconPath = new vscode.ThemeIcon("package");
    item.command = {
      command: "lopper.analyseDependency",
      title: `Analyse ${dependency.name}`,
      arguments: [dependency.name, folderPath],
    };
    item.description = dependencySummaryText(dependency, analysis);
    item.tooltip = buildDependencyTooltip(folder, analysis, dependency);
    return item;
  }

  private getDependencyChildren(folderPath: string, dependencyName: string): LopperExplorerTreeItem[] {
    const folder = findWorkspaceFolder(folderPath);
    if (!folder) {
      return [];
    }
    const analysis = this.getAnalysis(folder);
    if (!analysis) {
      return [];
    }
    const dependency = analysis.report.dependencies.find((item) => item.name === dependencyName);
    if (!dependency) {
      return [];
    }

    const children: LopperExplorerTreeItem[] = [];

    const runtimeLabel = runtimeUsageSummary(dependency.runtimeUsage);
    if (runtimeLabel) {
      children.push(this.createMetricItem(folder, dependency.name, "Runtime", runtimeLabel));
    }
    const baselineLabel = dependencyBaselineSummary(analysis.report.baselineComparison, dependency.name);
    if (baselineLabel) {
      children.push(this.createMetricItem(folder, dependency.name, "Baseline", baselineLabel));
    }
    const reachabilityLabel = reachabilitySummary(dependency.reachabilityConfidence);
    if (reachabilityLabel) {
      children.push(this.createMetricItem(folder, dependency.name, "Reachability", reachabilityLabel));
    }
    const licenseLabel = formatDependencyLicense(dependency.license);
    if (licenseLabel) {
      children.push(this.createMetricItem(folder, dependency.name, "License", licenseLabel));
    }
    const provenanceLabel = formatDependencyProvenance(dependency.provenance);
    if (provenanceLabel) {
      children.push(this.createMetricItem(folder, dependency.name, "Provenance", provenanceLabel));
    }
    for (const cue of dependency.riskCues ?? []) {
      children.push(this.createMetricItem(folder, dependency.name, "Risk cue", cue.message));
    }
    for (const recommendation of dependency.recommendations ?? []) {
      children.push(this.createMetricItem(folder, dependency.name, "Recommendation", recommendation.message));
    }

    if ((dependency.usedImports?.length ?? 0) > 0) {
      children.push(this.createImportGroupItem(folder, dependency.name, "usedImports", dependency.usedImports?.length ?? 0));
    }
    if ((dependency.unusedImports?.length ?? 0) > 0) {
      children.push(this.createImportGroupItem(folder, dependency.name, "unusedImports", dependency.unusedImports?.length ?? 0));
    }

    return children;
  }

  private createMetricItem(
    folder: vscode.WorkspaceFolder,
    dependencyName: string,
    label: string,
    description: string,
  ): LopperExplorerTreeItem {
    const item = new LopperExplorerTreeItem(
      label,
      { kind: "metric", folderPath: folder.uri.fsPath, dependencyName },
      vscode.TreeItemCollapsibleState.None,
    );
    item.description = description;
    item.iconPath = new vscode.ThemeIcon("info");
    return item;
  }

  private createImportGroupItem(
    folder: vscode.WorkspaceFolder,
    dependencyName: string,
    groupKind: "usedImports" | "unusedImports",
    count: number,
  ): LopperExplorerTreeItem {
    const label = groupKind === "usedImports" ? `Used imports (${count})` : `Unused imports (${count})`;
    const item = new LopperExplorerTreeItem(
      label,
      { kind: "group", folderPath: folder.uri.fsPath, dependencyName, groupKind },
      vscode.TreeItemCollapsibleState.Collapsed,
    );
    item.iconPath = new vscode.ThemeIcon(groupKind === "usedImports" ? "references" : "trash");
    return item;
  }

  private getImportChildren(
    folderPath: string,
    dependencyName: string,
    groupKind: "usedImports" | "unusedImports",
  ): LopperExplorerTreeItem[] {
    const folder = findWorkspaceFolder(folderPath);
    if (!folder) {
      return [];
    }
    const analysis = this.getAnalysis(folder);
    const dependency = analysis?.report.dependencies.find((item) => item.name === dependencyName);
    if (!dependency) {
      return [];
    }
    const imports = groupKind === "usedImports" ? dependency.usedImports ?? [] : dependency.unusedImports ?? [];
    const items: LopperExplorerTreeItem[] = [];
    for (const importUse of imports) {
      const locations = importUse.locations ?? [];
      const firstLocation = locations[0];
      const resolvedFilePath = firstLocation ? resolveWorkspaceFilePath(folder.uri.fsPath, firstLocation.file) : undefined;
      const item = new LopperExplorerTreeItem(
        importUse.name,
        {
          kind: "import",
          folderPath: folder.uri.fsPath,
          dependencyName,
          filePath: resolvedFilePath,
          line: firstLocation?.line,
          column: firstLocation?.column,
        },
        vscode.TreeItemCollapsibleState.None,
      );
      item.description = importUse.module ?? firstLocation?.file ?? "import";
      item.tooltip = importUse.locations
        ? `${importUse.name}\n${importUse.locations.map(formatLocationSummary).join("\n")}`
        : importUse.name;
      if (firstLocation && resolvedFilePath) {
        item.command = {
          command: "lopper.openLocation",
          title: `Open ${importUse.name}`,
          arguments: [resolvedFilePath, firstLocation.line, firstLocation.column],
        };
      }
      item.iconPath = new vscode.ThemeIcon(groupKind === "usedImports" ? "symbol-method" : "trash");
      items.push(item);
    }
    return items;
  }
}

function resolveWorkspaceFilePath(workspaceRoot: string, relativePath: string): string | undefined {
  const resolvedWorkspaceRoot = path.resolve(workspaceRoot);
  const candidatePath = path.resolve(resolvedWorkspaceRoot, relativePath);
  return isPathInsideWorkspace(candidatePath, resolvedWorkspaceRoot) ? candidatePath : undefined;
}

function isPathInsideWorkspace(candidatePath: string, workspaceRoot: string): boolean {
  const relativePath = path.relative(path.resolve(workspaceRoot), path.resolve(candidatePath));
  return relativePath === "" || (!relativePath.startsWith("..") && !path.isAbsolute(relativePath));
}

function formatLocationSummary(location: LopperLocation): string {
  return `${location.file}:${location.line}:${location.column}`;
}

function findWorkspaceFolder(folderPath: string): vscode.WorkspaceFolder | undefined {
  const resolved = path.resolve(folderPath);
  return (vscode.workspace.workspaceFolders ?? []).find((folder) => path.resolve(folder.uri.fsPath) === resolved);
}

function dependencySummaryText(dependency: LopperDependencyReport, analysis: WorkspaceAnalysis): string {
  const parts = [dependency.usedPercent.toFixed(1) + "% used"];
  const runtime = runtimeUsageSummary(dependency.runtimeUsage);
  if (runtime) {
    parts.push(runtime);
  }
  const baseline = dependencyBaselineSummary(analysis.report.baselineComparison, dependency.name);
  if (baseline) {
    parts.push(baseline);
  }
  return parts.join(" • ");
}

function buildFolderTooltip(folder: vscode.WorkspaceFolder, analysis: WorkspaceAnalysis): string {
  const summary = analysis.report.summary;
  const lines = [
    folder.name,
    `Scope: ${analysis.scopeMode} | Adapter: ${analysis.requestedLanguage}`,
    `Dependencies: ${summary?.dependencyCount ?? analysis.report.dependencies.length}`,
    `Used percent: ${summary?.usedPercent.toFixed(1) ?? "0.0"}%`,
  ];
  if (analysis.report.effectiveThresholds) {
    lines.push(`Thresholds: ${effectiveThresholdsText(analysis.report.effectiveThresholds)}`);
  }
  if (analysis.report.effectivePolicy) {
    lines.push(`Policy: ${effectivePolicyText(analysis.report.effectivePolicy)}`);
  }
  if ((analysis.report.warnings?.length ?? 0) > 0) {
    lines.push(`Warnings: ${analysis.report.warnings?.length ?? 0}`);
  }
  return lines.join("\n");
}

function buildDependencyTooltip(folder: vscode.WorkspaceFolder, analysis: WorkspaceAnalysis, dependency: LopperDependencyReport): string {
  const lines = [
    `${dependency.name} (${folder.name})`,
    `Used exports: ${dependency.usedExportsCount}/${dependency.totalExportsCount} (${dependency.usedPercent.toFixed(1)}%)`,
  ];
  if (dependency.estimatedUnusedBytes !== undefined) {
    lines.push(`Estimated unused bytes: ${dependency.estimatedUnusedBytes}`);
  }
  const runtime = runtimeUsageSummary(dependency.runtimeUsage);
  if (runtime) {
    lines.push(`Runtime: ${runtime}`);
  }
  const reachability = reachabilitySummary(dependency.reachabilityConfidence);
  if (reachability) {
    lines.push(`Reachability: ${reachability}`);
  }
  const baseline = dependencyBaselineSummary(analysis.report.baselineComparison, dependency.name);
  if (baseline) {
    lines.push(`Baseline: ${baseline}`);
  }
  const license = formatDependencyLicense(dependency.license);
  if (license) {
    lines.push(`License: ${license}`);
  }
  const provenance = formatDependencyProvenance(dependency.provenance);
  if (provenance) {
    lines.push(`Provenance: ${provenance}`);
  }
  return lines.join("\n");
}

function effectiveThresholdsText(thresholds: LopperEffectiveThresholds): string {
  return [
    `fail ${thresholds.failOnIncreasePercent}`,
    `warn ${thresholds.lowConfidenceWarningPercent}`,
    `recommend ${thresholds.minUsagePercentForRecommendations}`,
    `uncertain ${thresholds.maxUncertainImportCount}`,
  ].join(", ");
}

function effectivePolicyText(policy: LopperEffectivePolicy): string {
  const license = policy.license;
  const sources = policy.sources?.length ? `sources: ${policy.sources.join(", ")}` : "sources: default";
  const deny = license.deny?.length ? `deny: ${license.deny.join(", ")}` : "deny: none";
  return `${sources}; ${deny}; license fail: ${license.failOnDenied ? "yes" : "no"}; registry provenance: ${license.includeRegistryProvenance ? "yes" : "no"}`;
}

function runtimeUsageSummary(runtimeUsage?: LopperRuntimeUsage): string | undefined {
  if (!runtimeUsage) {
    return undefined;
  }
  const parts = [`runtime ${runtimeUsage.loadCount}`];
  if (runtimeUsage.correlation) {
    parts.push(runtimeUsage.correlation);
  }
  if (runtimeUsage.runtimeOnly) {
    parts.push("runtime-only");
  }
  return parts.join(" • ");
}

function reachabilitySummary(reachability?: LopperReachabilityConfidence): string | undefined {
  if (!reachability) {
    return undefined;
  }
  const parts = [`${reachability.score.toFixed(1)}`];
  if (reachability.summary) {
    parts.push(reachability.summary);
  }
  return parts.join(" • ");
}

function dependencyBaselineSummary(
  comparison: LopperBaselineComparison | undefined,
  dependencyName: string,
): string | undefined {
  if (!comparison) {
    return undefined;
  }
  const dependencyDelta = comparison.dependencies?.find((item) => item.name === dependencyName);
  const parts: string[] = [];
  if (comparison.baselineKey) {
    parts.push(comparison.baselineKey);
  }
  if (comparison.summaryDelta.wastePercentDelta !== 0) {
    parts.push(`waste ${comparison.summaryDelta.wastePercentDelta > 0 ? "+" : ""}${comparison.summaryDelta.wastePercentDelta.toFixed(1)}%`);
  }
  if (dependencyDelta) {
    parts.push(`dep ${dependencyDelta.usedPercentDelta > 0 ? "+" : ""}${dependencyDelta.usedPercentDelta.toFixed(1)}%`);
  }
  return parts.length > 0 ? parts.join(" • ") : undefined;
}

function renderDependencyDetailHtml(
  folder: vscode.WorkspaceFolder,
  analysis: WorkspaceAnalysis,
  dependency: LopperDependencyReport,
  dependencyName: string,
): string {
  const report = analysis.report;
  const selectedDependency = dependency.name === dependencyName ? dependency : report.dependencies.find((item) => item.name === dependencyName) ?? dependency;
  const baseline = report.baselineComparison;
  const baselineDependency = baseline?.dependencies?.find((item) => item.name === dependencyName);
  const sections = [
    renderDependencyOverviewSection(selectedDependency),
    renderDependencyActionsSection(folder, dependencyName),
    renderDependencyContextSection(report, baseline, baselineDependency),
    renderDependencyMetadataSection(selectedDependency),
    selectedDependency.runtimeUsage ? renderHtmlSection("Runtime usage", renderRuntimeUsage(selectedDependency.runtimeUsage)) : "",
    (selectedDependency.usedImports?.length ?? 0) > 0 ? renderHtmlSection("Used imports", renderImports(folder, selectedDependency.usedImports ?? [])) : "",
    (selectedDependency.unusedImports?.length ?? 0) > 0 ? renderHtmlSection("Unused imports", renderImports(folder, selectedDependency.unusedImports ?? [])) : "",
    (selectedDependency.recommendations?.length ?? 0) > 0 ? renderDependencyRecommendationsSection(selectedDependency.recommendations ?? []) : "",
    (selectedDependency.riskCues?.length ?? 0) > 0 ? renderDependencyRiskCuesSection(selectedDependency.riskCues ?? []) : "",
  ].filter((section): section is string => section.length > 0);

  return `<!doctype html>
  <html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <style>
      :root {
        color-scheme: light dark;
        --bg: #0b1020;
        --panel: rgba(15, 23, 42, 0.72);
        --border: rgba(148, 163, 184, 0.22);
        --text: #e2e8f0;
        --muted: #94a3b8;
        --accent: #38bdf8;
      }
      body {
        margin: 0;
        padding: 24px;
        background: radial-gradient(circle at top left, rgba(56, 189, 248, 0.2), transparent 28%), linear-gradient(180deg, #020617, #0f172a 54%, #111827);
        color: var(--text);
        font-family: var(--vscode-font-family, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif);
      }
      h1, h2 {
        margin: 0 0 12px 0;
      }
      h1 { font-size: 28px; }
      h2 { font-size: 16px; color: var(--muted); }
      .subtitle { color: var(--muted); margin-bottom: 24px; }
      .panel {
        background: var(--panel);
        border: 1px solid var(--border);
        border-radius: 16px;
        padding: 18px;
        margin-bottom: 18px;
        backdrop-filter: blur(16px);
      }
      .stat-grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
        gap: 12px;
      }
      .stat {
        background: rgba(148, 163, 184, 0.08);
        border: 1px solid rgba(148, 163, 184, 0.14);
        border-radius: 12px;
        padding: 12px;
      }
      .stat .label {
        color: var(--muted);
        font-size: 12px;
        margin-bottom: 6px;
      }
      .stat .value {
        font-size: 16px;
        font-weight: 600;
      }
      a.button {
        display: inline-block;
        margin-right: 10px;
        margin-bottom: 8px;
        padding: 8px 12px;
        border-radius: 999px;
        background: rgba(56, 189, 248, 0.15);
        border: 1px solid rgba(56, 189, 248, 0.35);
        color: var(--text);
        text-decoration: none;
      }
      ul { margin: 0; padding-left: 20px; }
      li { margin-bottom: 6px; }
      .kv { display: grid; grid-template-columns: max-content 1fr; gap: 8px 16px; }
      .kv .key { color: var(--muted); }
      .kv .value { word-break: break-word; }
      code { background: rgba(148, 163, 184, 0.12); padding: 2px 6px; border-radius: 6px; }
    </style>
  </head>
  <body>
    <h1>${escapeHtml(dependencyName)}</h1>
    <div class="subtitle">${escapeHtml(folder.name)} • ${escapeHtml(analysis.scopeMode)} • ${escapeHtml(analysis.requestedLanguage)}</div>
    ${sections.join("")}
  </body>
  </html>`;
}

function renderDependencyOverviewSection(dependency: LopperDependencyReport): string {
  return renderHtmlSection(
    "Overview",
    [
      `<div class="stat-grid">`,
      renderStat("Used exports", `${dependency.usedExportsCount}/${dependency.totalExportsCount}`),
      renderStat("Used percent", `${dependency.usedPercent.toFixed(1)}%`),
      renderStat(
        "Estimated unused bytes",
        typeof dependency.estimatedUnusedBytes === "number" ? String(dependency.estimatedUnusedBytes) : "n/a",
      ),
      renderStat("Risk cues", String(dependency.riskCues?.length ?? 0)),
      renderStat("Recommendations", String(dependency.recommendations?.length ?? 0)),
      `</div>`,
    ].join(""),
  );
}

function renderDependencyActionsSection(folder: vscode.WorkspaceFolder, dependencyName: string): string {
  return renderHtmlSection(
    "Actions",
    [
      `<a class="button" href="${commandUri("lopper.refreshWorkspace", [folder.uri.fsPath])}">Refresh folder</a>`,
      `<a class="button" href="${commandUri("lopper.analyseDependency", [dependencyName, folder.uri.fsPath])}">Re-run detail analysis</a>`,
    ].join(" "),
  );
}

function renderDependencyContextSection(
  report: WorkspaceAnalysis["report"],
  baseline: LopperBaselineComparison | undefined,
  baselineDependency: LopperDependencyDelta | undefined,
): string {
  const lines = [
    report.scope?.mode ? `<p><strong>Scope:</strong> ${escapeHtml(report.scope.mode)}</p>` : "",
    report.effectiveThresholds ? `<p><strong>Thresholds:</strong> ${escapeHtml(effectiveThresholdsText(report.effectiveThresholds))}</p>` : "",
    report.effectivePolicy ? `<p><strong>Policy:</strong> ${escapeHtml(effectivePolicyText(report.effectivePolicy))}</p>` : "",
    report.warnings?.length ? `<p><strong>Warnings:</strong> ${escapeHtml(report.warnings.join(" | "))}</p>` : "",
    typeof report.wasteIncreasePercent === "number"
      ? `<p><strong>Waste increase vs baseline:</strong> ${escapeHtml(report.wasteIncreasePercent.toFixed(1))}%</p>`
      : "",
    baseline ? renderBaselineHtml(baseline, baselineDependency) : "",
  ];
  return renderHtmlSection("Context", lines.join(""));
}

function renderDependencyMetadataSection(dependency: LopperDependencyReport): string {
  const parts = [
    dependency.license
      ? renderKeyValueList([["License", formatDependencyLicense(dependency.license) ?? "n/a"]])
      : "",
    dependency.provenance
      ? renderKeyValueList([["Provenance", formatDependencyProvenance(dependency.provenance) ?? "n/a"]])
      : "",
    dependency.reachabilityConfidence
      ? renderKeyValueList([["Reachability", reachabilitySummary(dependency.reachabilityConfidence) ?? "n/a"]])
      : "",
  ];
  return renderHtmlSection("License and provenance", parts.join(""));
}

function renderDependencyRecommendationsSection(recommendations: NonNullable<LopperDependencyReport["recommendations"]>): string {
  return renderHtmlSection("Recommendations", renderHtmlList(recommendations.map(renderRecommendationItem)));
}

function renderDependencyRiskCuesSection(riskCues: NonNullable<LopperDependencyReport["riskCues"]>): string {
  return renderHtmlSection("Risk cues", renderHtmlList(riskCues.map(renderRiskCueItem)));
}

function renderHtmlSection(title: string, body: string): string {
  return `<section class="panel"><h2>${escapeHtml(title)}</h2>${body}</section>`;
}

function renderHtmlList(items: string[]): string {
  return `<ul>${items.join("")}</ul>`;
}

function renderListItem(content: string): string {
  return `<li>${content}</li>`;
}

function renderRecommendationItem(item: { priority: string; message: string }): string {
  return renderListItem(`<strong>${escapeHtml(item.priority)}</strong> ${escapeHtml(item.message)}`);
}

function renderRiskCueItem(item: { severity: string; message: string }): string {
  return renderListItem(`<strong>${escapeHtml(item.severity)}</strong> ${escapeHtml(item.message)}`);
}

function renderStat(label: string, value: string): string {
  return `<div class="stat"><div class="label">${escapeHtml(label)}</div><div class="value">${escapeHtml(value)}</div></div>`;
}

function renderKeyValueList(entries: Array<[string, string]>): string {
  return `<div class="kv">${entries.map(([key, value]) => renderKeyValueEntry(key, value)).join("")}</div>`;
}

function renderKeyValueEntry(key: string, value: string): string {
  return `<div class="key">${escapeHtml(key)}</div><div class="value">${escapeHtml(value)}</div>`;
}

function renderBaselineHtml(baseline: LopperBaselineComparison, dependencyDelta?: LopperDependencyDelta): string {
  const summary = baseline.summaryDelta;
  const lines = [
    baseline.baselineKey ? `<p><strong>Baseline key:</strong> ${escapeHtml(baseline.baselineKey)}</p>` : "",
    baseline.currentKey ? `<p><strong>Current key:</strong> ${escapeHtml(baseline.currentKey)}</p>` : "",
    renderKeyValueList([
      ["Waste delta", `${summary.wastePercentDelta.toFixed(1)}%`],
      ["Used percent delta", `${summary.usedPercentDelta.toFixed(1)}%`],
      ["Unused bytes delta", String(summary.unusedBytesDelta)],
      ["Denied license delta", String(summary.deniedLicenseCountDelta)],
    ]),
    dependencyDelta ? renderKeyValueList([
      ["Dependency change", dependencyDelta.kind],
      ["Dependency waste delta", `${dependencyDelta.wastePercentDelta.toFixed(1)}%`],
      ["Dependency used delta", `${dependencyDelta.usedPercentDelta.toFixed(1)}%`],
    ]) : "",
    baseline.newDeniedLicenses?.length ? `<p><strong>New denied licenses:</strong> ${escapeHtml(baseline.newDeniedLicenses.map((item) => item.name).join(", "))}</p>` : "",
  ];
  return lines.join("");
}

function renderRuntimeUsage(runtime: LopperRuntimeUsage): string {
  const parts = [
    renderKeyValueList([
      ["Load count", String(runtime.loadCount)],
      ["Correlation", runtime.correlation ?? "n/a"],
      ["Runtime only", runtime.runtimeOnly ? "yes" : "no"],
    ]),
  ];
  if (runtime.modules?.length) {
    parts.push(renderHtmlSection("Modules", renderHtmlList(runtime.modules.map(renderRuntimeModuleItem))));
  }
  if (runtime.topSymbols?.length) {
    parts.push(renderHtmlSection("Top symbols", renderHtmlList(runtime.topSymbols.map(renderRuntimeTopSymbolItem))));
  }
  return parts.join("");
}

function renderRuntimeModuleItem(module: { module: string; count: number }): string {
  return renderListItem(`<code>${escapeHtml(module.module)}</code> x ${module.count}`);
}

function renderRuntimeTopSymbolItem(symbol: { symbol: string; module?: string; count: number }): string {
  const parts = [`<code>${escapeHtml(symbol.symbol)}</code>`];
  if (symbol.module) {
    parts.push(` in <code>${escapeHtml(symbol.module)}</code>`);
  }
  parts.push(` x ${symbol.count}`);
  return renderListItem(parts.join(""));
}

function renderImports(folder: vscode.WorkspaceFolder, imports: LopperImportUse[]): string {
  return renderHtmlList(imports.map((importUse) => {
    const location = importUse.locations?.[0];
    const filePath = location ? resolveWorkspaceFilePath(folder.uri.fsPath, location.file) : undefined;
    const labelParts = [escapeHtml(importUse.name)];
    if (importUse.module) {
      labelParts.push(` <code>${escapeHtml(importUse.module)}</code>`);
    }
    const label = filePath && location
      ? `<a href="${commandUri("lopper.openLocation", [filePath, location.line, location.column])}">${labelParts.join("")}</a>`
      : labelParts.join("");
    const locationText = importUse.locations?.length
      ? importUse.locations.map(formatLocationSummary).join(", ")
      : "n/a";
    return renderListItem(`${label} <span style="color:#94a3b8">${escapeHtml(locationText)}</span>`);
  }));
}

function commandUri(command: string, args: unknown[]): string {
  return `command:${command}?${encodeURIComponent(JSON.stringify(args))}`;
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll("\"", "&quot;")
    .replaceAll("'", "&#39;");
}
