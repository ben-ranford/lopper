import { realpath, stat } from "node:fs/promises";
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
  type WorkspaceAnalysis,
  type WorkspaceAnalysisRequest,
  type WorkspaceAnalysisRunner,
} from "./lopperRunner";
import { RefreshSessionStore } from "./refreshSession";
import { lopperScopeModeValues } from "./types";
import type {
  LopperDependencyLicense,
  LopperDependencyProvenance,
  LopperCodemodSuggestion,
  LopperDependencyReport,
  LopperImportUse,
  LopperLocation,
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

class LopperController implements LopperControllerContract, vscode.HoverProvider, vscode.CodeActionProvider {
  private readonly diagnostics = vscode.languages.createDiagnosticCollection("lopper");
  private readonly statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  private readonly output = vscode.window.createOutputChannel("Lopper");
  private readonly runner: WorkspaceAnalysisRunner;
  private readonly metadataByDocument = new Map<string, Map<string, DiagnosticMetadata>>();
  private readonly documentUrisByWorkspace = new Map<string, Set<string>>();
  private readonly refreshTimers = new Map<string, NodeJS.Timeout>();
  private readonly refreshSessions = new RefreshSessionStore<WorkspaceAnalysis>();
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
    this.statusBar.command = "lopper.refreshWorkspace";
    this.statusBar.text = this.latestSummary;
    this.statusBar.tooltip = "Refresh Lopper diagnostics";
    this.statusBar.show();

    const disposables: vscode.Disposable[] = [this.diagnostics, this.statusBar, this.output];
    if (registerWithVSCode) {
      disposables.push(
        vscode.commands.registerCommand("lopper.refreshWorkspace", async () => {
          await this.refreshWorkspace({ trigger: "command" });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.force", async () => {
          await this.refreshWorkspace({ forceFresh: true, trigger: "command" });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.package", async () => {
          await this.refreshWorkspace({ scopeModeOverride: "package", trigger: "command" });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.repo", async () => {
          await this.refreshWorkspace({ scopeModeOverride: "repo", trigger: "command" });
        }),
        vscode.commands.registerCommand("lopper.refreshWorkspace.changedPackages", async () => {
          await this.refreshWorkspace({ scopeModeOverride: "changed-packages", trigger: "command" });
        }),
        vscode.languages.registerHoverProvider(supportedDocumentSelectors, this),
        vscode.languages.registerCodeActionsProvider(supportedDocumentSelectors, this, {
          providedCodeActionKinds: [vscode.CodeActionKind.QuickFix],
        }),
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
          }
        }),
        vscode.workspace.onDidGrantWorkspaceTrust(async () => {
          const folder = this.primaryWorkspaceFolder();
          if (!folder) {
            return;
          }
          if (!vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
            return;
          }
          await this.refreshWorkspace({
            folder,
            revealErrors: false,
            document: this.activeDocumentForFolder(folder),
            trigger: "workspace-trust",
          });
        }),
      );
    }
    this.disposable = vscode.Disposable.from(...disposables);
  }

  async initialize(): Promise<void> {
    const folder = this.primaryWorkspaceFolder();
    if (!folder) {
      this.updateStatus("Lopper: no workspace", "Open a folder to analyse with Lopper.");
      return;
    }
    if (vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
      await this.refreshWorkspace({
        folder,
        revealErrors: false,
        document: this.activeDocumentForFolder(folder),
        trigger: "initial",
      });
    }
  }

  async refreshWorkspace(options: RefreshWorkspaceOptions = {}): Promise<void> {
    const folder = options.folder ?? this.commandWorkspaceFolder();
    const revealErrors = options.revealErrors ?? true;
    const document = options.document ?? this.activeDocumentForFolder(folder);
    const forceFresh = options.forceFresh ?? false;
    const trigger = options.trigger ?? "command";
    if (!folder) {
      this.updateStatus("Lopper: no workspace", "Open a folder to analyse with Lopper.");
      if (revealErrors) {
        await vscode.window.showInformationMessage("Open a folder before running Lopper diagnostics.");
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

    if (!forceFresh) {
      const inFlight = this.refreshSessions.inFlight(workspaceKey, sessionKey);
      if (inFlight) {
        this.output.appendLine(
          `[refresh:reused-running] ${folder.name} (${requestedLanguage}, ${scopeMode}) trigger=${trigger}`,
        );
        this.updateStatus(
          `Lopper: analysing (${scopeMode})`,
          `Reusing in-flight analysis for ${folder.name} (${requestedLanguage}, scope ${scopeMode}).`,
        );
        await inFlight.promise;
        return;
      }

      const cached = this.refreshSessions.getCache(workspaceKey, sessionKey);
      if (cached && await this.canReuseCachedAnalysis(cached.value)) {
        const runId = this.refreshSessions.reserveRun(workspaceKey);
        this.output.appendLine(
          `[refresh:reused-cache] ${folder.name} (${requestedLanguage}, ${scopeMode}) trigger=${trigger}`,
        );
        this.applyAnalysisIfCurrent(workspaceKey, runId, cached.value, "cache");
        return;
      }
      if (cached) {
        this.output.appendLine(
          `[refresh:cache-invalidated] ${folder.name} (${requestedLanguage}, ${scopeMode}) binary changed`,
        );
      }
    }

    const runId = this.refreshSessions.reserveRun(workspaceKey);
    this.updateStatus(
      `Lopper: analysing (${scopeMode})`,
      `Running lopper for ${folder.name} (${requestedLanguage}, scope ${scopeMode}).`,
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
    });
    this.refreshSessions.setInFlight(workspaceKey, sessionKey, runId, refreshPromise);
    await refreshPromise;
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
  }): Promise<void> {
    const { folder, workspaceKey, sessionKey, runId, revealErrors, scopeMode, document, requestedLanguage } = options;
    try {
      const request: WorkspaceAnalysisRequest = { document, scopeMode };
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
    for (const timer of this.refreshTimers.values()) {
      clearTimeout(timer);
    }
    this.refreshTimers.clear();
    this.disposable.dispose();
  }

  private primaryWorkspaceFolder(): vscode.WorkspaceFolder | undefined {
    return vscode.workspace.workspaceFolders?.[0];
  }

  private commandWorkspaceFolder(): vscode.WorkspaceFolder | undefined {
    const activeDocument = vscode.window.activeTextEditor?.document;
    if (activeDocument) {
      const activeFolder = vscode.workspace.getWorkspaceFolder(activeDocument.uri);
      if (activeFolder) {
        return activeFolder;
      }
    }
    return this.primaryWorkspaceFolder();
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
      `Scope: ${analysis.scopeMode} | Adapter: ${analysis.requestedLanguage} | Source: ${sourceSummary} | Binary: ${path.basename(analysis.binaryPath)}${warningSummary}`,
    );
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
        const { uri, range } = this.resolveLocation(analysis.folder, location, importUse);
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
      const uri = vscode.Uri.file(path.join(analysis.folder.uri.fsPath, suggestion.file));
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
  ): { uri: vscode.Uri; range: vscode.Range } {
    const uri = vscode.Uri.file(path.join(folder.uri.fsPath, location.file));
    const line = Math.max(0, location.line - 1);
    const startColumn = Math.max(0, location.column - 1);
    const endColumn = startColumn + Math.max(importUse.name.length, 1);
    return {
      uri,
      range: new vscode.Range(line, startColumn, line, endColumn),
    };
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

  private updateStatus(text: string, tooltip: string): void {
    this.latestSummary = text;
    this.statusBar.text = text;
    this.statusBar.tooltip = tooltip;
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
