import * as path from "node:path";
import * as vscode from "vscode";

import { BinaryResolutionError, LopperRunner, type WorkspaceAnalysis } from "./lopperRunner";
import type {
  LopperCodemodSuggestion,
  LopperDependencyReport,
  LopperImportUse,
  LopperLocation,
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

class LopperController implements vscode.Disposable, vscode.HoverProvider, vscode.CodeActionProvider {
  private readonly diagnostics = vscode.languages.createDiagnosticCollection("lopper");
  private readonly statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  private readonly output = vscode.window.createOutputChannel("Lopper");
  private readonly runner = new LopperRunner(this.output);
  private readonly metadataByDocument = new Map<string, Map<string, DiagnosticMetadata>>();
  private readonly documentUrisByWorkspace = new Map<string, Set<string>>();
  private readonly refreshTimers = new Map<string, NodeJS.Timeout>();
  private latestSummary = "Lopper: idle";
  private missingBinaryWarningShown = false;
  private readonly disposable: vscode.Disposable;

  constructor() {
    this.statusBar.command = "lopper.refreshWorkspace";
    this.statusBar.text = this.latestSummary;
    this.statusBar.tooltip = "Refresh Lopper diagnostics";
    this.statusBar.show();

    this.disposable = vscode.Disposable.from(
      this.diagnostics,
      this.statusBar,
      this.output,
      vscode.commands.registerCommand("lopper.refreshWorkspace", async () => {
        await this.refreshWorkspace();
      }),
      vscode.languages.registerHoverProvider(
        [{ scheme: "file", language: "javascript" }, { scheme: "file", language: "typescript" }],
        this,
      ),
      vscode.languages.registerCodeActionsProvider(
        [{ scheme: "file", language: "javascript" }, { scheme: "file", language: "typescript" }],
        this,
        { providedCodeActionKinds: [vscode.CodeActionKind.QuickFix] },
      ),
      vscode.workspace.onDidSaveTextDocument((document) => {
        if (!this.shouldAutoRefresh(document)) {
          return;
        }
        const folder = vscode.workspace.getWorkspaceFolder(document.uri);
        if (!folder) {
          return;
        }
        const timerKey = folder.uri.toString();
        const existingTimer = this.refreshTimers.get(timerKey);
        if (existingTimer) {
          clearTimeout(existingTimer);
        }
        this.refreshTimers.set(
          timerKey,
          setTimeout(() => {
            this.refreshTimers.delete(timerKey);
            void this.refreshWorkspace(folder, false);
          }, 400),
        );
      }),
    );
  }

  async initialize(): Promise<void> {
    const folder = this.primaryWorkspaceFolder();
    if (!folder) {
      this.updateStatus("Lopper: no workspace", "Open a folder to analyse with Lopper.");
      return;
    }
    if (vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true)) {
      await this.refreshWorkspace(folder, false);
    }
  }

  async refreshWorkspace(
    folder = this.primaryWorkspaceFolder(),
    revealErrors = true,
  ): Promise<void> {
    if (!folder) {
      this.updateStatus("Lopper: no workspace", "Open a folder to analyse with Lopper.");
      if (revealErrors) {
        void vscode.window.showInformationMessage("Open a folder before running Lopper diagnostics.");
      }
      return;
    }

    this.updateStatus("Lopper: analysing...", `Scanning ${folder.name} with the local lopper CLI.`);

    try {
      const analysis = await this.runner.analyseWorkspace(folder);
      this.renderAnalysis(analysis);
      this.missingBinaryWarningShown = false;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.clearWorkspaceDiagnostics(folder);
      this.updateStatus("Lopper: unavailable", message);
      this.output.appendLine(message);
      if (error instanceof BinaryResolutionError) {
        if (revealErrors && !this.missingBinaryWarningShown) {
          this.missingBinaryWarningShown = true;
          void vscode.window.showWarningMessage(message);
        }
        return;
      }
      if (revealErrors) {
        void vscode.window.showErrorMessage(`Lopper refresh failed: ${message}`);
      }
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
      markdown.appendMarkdown(`**${item.dependency.name}**\n\n`);
      markdown.appendMarkdown(
        `Used exports: ${item.dependency.usedExportsCount}/${item.dependency.totalExportsCount} (${item.dependency.usedPercent.toFixed(1)}%)`,
      );
      if (item.kind === "unused-import") {
        markdown.appendMarkdown(`\n\nUnused import on this line.`);
      }
      if (item.kind === "codemod" && item.suggestion) {
        markdown.appendMarkdown(
          `\n\nSafe quick fix available: \`${item.suggestion.fromModule}\` -> \`${item.suggestion.toModule}\`.`,
        );
      }
      const topRiskCue = item.dependency.riskCues?.[0];
      if (topRiskCue) {
        markdown.appendMarkdown(`\n\nRisk cue: ${topRiskCue.message}`);
      }
      const recommendation = item.dependency.recommendations?.[0];
      if (recommendation) {
        markdown.appendMarkdown(`\n\nRecommendation: ${recommendation.message}`);
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
      if (!metadata || metadata.kind !== "codemod" || !metadata.suggestion) {
        continue;
      }

      const lineIndex = metadata.suggestion.line - 1;
      if (lineIndex < 0 || lineIndex >= document.lineCount) {
        continue;
      }
      const currentLine = document.lineAt(lineIndex).text;
      if (currentLine !== metadata.suggestion.original) {
        continue;
      }

      const action = new vscode.CodeAction(
        `Use ${metadata.suggestion.toModule} subpath import`,
        vscode.CodeActionKind.QuickFix,
      );
      action.isPreferred = true;
      action.diagnostics = [diagnostic];
      const edit = new vscode.WorkspaceEdit();
      edit.replace(document.uri, document.lineAt(lineIndex).range, metadata.suggestion.replacement);
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

  private shouldAutoRefresh(document: vscode.TextDocument): boolean {
    if (document.isUntitled || (document.languageId !== "javascript" && document.languageId !== "typescript")) {
      return false;
    }
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    if (!folder) {
      return false;
    }
    return vscode.workspace.getConfiguration("lopper", folder.uri).get<boolean>("autoRefresh", true);
  }

  private renderAnalysis(analysis: WorkspaceAnalysis): void {
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
    this.updateStatus(
      `Lopper: ${dependencyCount} deps | ${usedPercent.toFixed(1)}% used`,
      `Diagnostics sourced from ${path.basename(analysis.binaryPath)}`,
    );
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

  private updateStatus(text: string, tooltip: string): void {
    this.latestSummary = text;
    this.statusBar.text = text;
    this.statusBar.tooltip = tooltip;
  }
}

let controller: LopperController | undefined;

export async function activate(): Promise<ExtensionApi> {
  controller = new LopperController();
  await controller.initialize();
  return {
    refreshWorkspace: async () => {
      await controller?.refreshWorkspace();
    },
    getLatestSummary: () => controller?.getLatestSummary() ?? "Lopper: idle",
  };
}

export function deactivate(): void {
  controller?.dispose();
  controller = undefined;
}
