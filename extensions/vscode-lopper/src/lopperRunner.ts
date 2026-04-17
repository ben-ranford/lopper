import { execFile } from "node:child_process";
import { realpath, stat } from "node:fs/promises";
import { promisify } from "node:util";
import * as vscode from "vscode";

import { configuredLopperLanguage, resolveLopperLanguage, type LopperLanguage } from "./languageConfiguration";
import {
  BinaryResolutionError,
  LopperBinaryLifecycleManager,
  ManagedBinaryInstaller,
  type BinaryLifecycleManager,
  type BinaryResolutionRequest,
} from "./managedBinary";
export { BinaryResolutionError } from "./managedBinary";
import { lopperScopeModeValues, type LopperCodemodReport, type LopperDependencyReport, type LopperScopeMode, type LopperReport } from "./types";

const execFileAsync = promisify(execFile);

export interface WorkspaceAnalysis {
  folder: vscode.WorkspaceFolder;
  binaryPath: string;
  binarySignature: string;
  requestedLanguage: LopperLanguage;
  scopeMode: LopperScopeMode;
  report: LopperReport;
  codemodsByDependency: Map<string, LopperCodemodReport>;
}

export interface WorkspaceAnalysisRunner {
  analyseWorkspace(folder: vscode.WorkspaceFolder, options?: WorkspaceAnalysisRequest): Promise<WorkspaceAnalysis>;
}

export interface WorkspaceAnalysisRequest {
  document?: vscode.TextDocument;
  scopeMode?: LopperScopeMode;
}

export interface ReportCommandExecutor {
  runReport(binaryPath: string, args: string[], cwd: string): Promise<LopperReport>;
}

export interface LopperRunnerDeps {
  binaryLifecycle?: BinaryLifecycleManager;
  reportExecutor?: ReportCommandExecutor;
}

class LopperCliReportExecutor implements ReportCommandExecutor {
  constructor(private readonly output: Pick<vscode.OutputChannel, "appendLine">) {}

  async runReport(binaryPath: string, args: string[], cwd: string): Promise<LopperReport> {
    this.output.appendLine(`running: ${binaryPath} ${args.join(" ")}`);
    try {
      const { stdout, stderr } = await execFileAsync(binaryPath, args, {
        cwd,
        env: process.env,
        maxBuffer: 10 * 1024 * 1024,
      });
      if (stderr.trim().length > 0) {
        this.output.appendLine(stderr.trim());
      }
      return this.parseReport(stdout, binaryPath);
    } catch (error) {
      const execError = error as NodeJS.ErrnoException & { stdout?: string; stderr?: string };
      if (execError.code === "ENOENT") {
        throw new BinaryResolutionError(
          "Lopper binary not found. Set lopper.binaryPath or LOPPER_BINARY_PATH before running the extension.",
        );
      }

      const stdout = execError.stdout?.trim() ?? "";
      if (stdout.startsWith("{")) {
        return this.parseReport(stdout, binaryPath);
      }

      const stderr = execError.stderr?.trim();
      throw new Error(stderr && stderr.length > 0 ? stderr : `lopper command failed for ${binaryPath}`);
    }
  }

  private parseReport(stdout: string, binaryPath: string): LopperReport {
    try {
      return JSON.parse(stdout) as LopperReport;
    } catch (error) {
      throw new Error(
        `failed to parse JSON from ${binaryPath}: ${error instanceof Error ? error.message : String(error)}`,
      );
    }
  }
}

export class LopperRunner implements WorkspaceAnalysisRunner {
  private readonly binaryLifecycle: BinaryLifecycleManager;
  private readonly reportExecutor: ReportCommandExecutor;

  constructor(
    private readonly output: Pick<vscode.OutputChannel, "appendLine">,
    context: vscode.ExtensionContext,
    deps: LopperRunnerDeps = {},
  ) {
    this.binaryLifecycle = deps.binaryLifecycle ?? new LopperBinaryLifecycleManager(
      new ManagedBinaryInstaller(context.globalStorageUri.fsPath, output),
      output,
      {
        install: async (releaseTag, install) => {
          return vscode.window.withProgress(
            {
              location: vscode.ProgressLocation.Notification,
              title: "Installing lopper CLI",
            },
            async (progress) => {
              progress.report({
                message: releaseTag
                  ? `Downloading ${releaseTag} for ${process.platform}/${process.arch}`
                  : `Downloading latest release for ${process.platform}/${process.arch}`,
              });
              return install();
            },
          );
        },
      },
    );
    this.reportExecutor = deps.reportExecutor ?? new LopperCliReportExecutor(output);
  }

  async analyseWorkspace(
    folder: vscode.WorkspaceFolder,
    options: WorkspaceAnalysisRequest = {},
  ): Promise<WorkspaceAnalysis> {
    const { document, scopeMode: scopeModeOption } = options;
    const binaryPath = await this.resolveBinaryPath(folder);
    const binarySignature = await this.binarySignature(binaryPath);
    const requestedLanguage = resolveLopperLanguage(configuredLopperLanguage(folder), document, folder.uri.fsPath);
    const scopeMode = normalizeScopeMode(scopeModeOption);
    const topN = this.topN(folder);
    const report = await this.reportExecutor.runReport(binaryPath, [
      "analyse",
      "--top",
      String(topN),
      "--repo",
      folder.uri.fsPath,
      "--language",
      requestedLanguage,
      "--scope-mode",
      scopeMode,
      "--format",
      "json",
    ], folder.uri.fsPath);

    const codemodsByDependency = new Map<string, LopperCodemodReport>();
    for (const dependency of report.dependencies) {
      if (!shouldFetchCodemod(dependency, requestedLanguage)) {
        continue;
      }
      const codemod = await this.fetchCodemod(binaryPath, folder, dependency, scopeMode);
      if (codemod) {
        codemodsByDependency.set(dependency.name, codemod);
      }
    }

    return { folder, binaryPath, binarySignature, requestedLanguage, scopeMode, report, codemodsByDependency };
  }

  async resolveBinaryPath(
    folder: vscode.WorkspaceFolder,
    workspaceTrusted = vscode.workspace.isTrusted,
  ): Promise<string> {
    const configuration = vscode.workspace.getConfiguration("lopper", folder.uri);
    const request: BinaryResolutionRequest = {
      workspaceRoot: folder.uri.fsPath,
      workspaceTrusted,
      autoDownloadBinary: configuration.get<boolean>("autoDownloadBinary", true),
      envBinaryPath: process.env.LOPPER_BINARY_PATH,
      configuredBinaryPath: configuration.get<string>("binaryPath", ""),
      managedBinaryTag: configuration.get<string>("managedBinaryTag", ""),
    };
    return this.binaryLifecycle.resolveBinaryPath(request);
  }

  private topN(folder: vscode.WorkspaceFolder): number {
    const configured = vscode.workspace.getConfiguration("lopper", folder.uri).get<number>("topN", 20);
    return Number.isFinite(configured) && configured > 0 ? Math.floor(configured) : 20;
  }

  private async fetchCodemod(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    dependency: LopperDependencyReport,
    scopeMode: LopperScopeMode,
  ): Promise<LopperCodemodReport | undefined> {
    if (!dependency.name) {
      return undefined;
    }
    try {
      const report = await this.reportExecutor.runReport(binaryPath, [
        "analyse",
        dependency.name,
        "--repo",
        folder.uri.fsPath,
        "--language",
        "js-ts",
        "--scope-mode",
        scopeMode,
        "--format",
        "json",
        "--suggest-only",
      ], folder.uri.fsPath);
      return report.dependencies[0]?.codemod;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.output.appendLine(`codemod analysis skipped for ${dependency.name}: ${message}`);
      return undefined;
    }
  }

  private async binarySignature(binaryPath: string): Promise<string> {
    try {
      const resolvedPath = await realpath(binaryPath);
      const details = await stat(resolvedPath);
      return `${resolvedPath}:${Math.floor(details.mtimeMs)}`;
    } catch {
      return `${binaryPath}:unknown`;
    }
  }
}

function shouldFetchCodemod(dependency: LopperDependencyReport, requestedLanguage: LopperLanguage): boolean {
  const dependencyLanguage = dependency.language?.trim().toLowerCase();
  if (dependencyLanguage) {
    return dependencyLanguage === "js-ts";
  }
  return requestedLanguage === "js-ts";
}

function normalizeScopeMode(scopeMode: LopperScopeMode | undefined): LopperScopeMode {
  if (scopeMode && (lopperScopeModeValues as readonly string[]).includes(scopeMode)) {
    return scopeMode;
  }
  return "package";
}
