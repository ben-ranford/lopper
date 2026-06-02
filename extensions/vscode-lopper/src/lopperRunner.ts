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
import {
  lopperScopeModeValues,
  type LopperCodemodReport,
  type LopperDependencyReport,
  type LopperReport,
  type LopperScopeMode,
} from "./types";

export type LopperOutputFormat = "json" | "csv" | "sarif" | "pr-comment";

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
  exportWorkspace(folder: vscode.WorkspaceFolder, format: LopperOutputFormat, options?: WorkspaceExportRequest): Promise<string>;
}

export interface WorkspaceAnalysisRequest {
  document?: vscode.TextDocument;
  scopeMode?: LopperScopeMode;
  dependencyName?: string;
  suggestOnly?: boolean;
  runtimeTracePath?: string;
  runtimeTestCommand?: string;
  baselinePath?: string;
  baselineStorePath?: string;
  baselineKey?: string;
  baselineLabel?: string;
  saveBaseline?: boolean;
}

export interface WorkspaceExportRequest {
  document?: vscode.TextDocument;
  scopeMode?: LopperScopeMode;
  runtimeTracePath?: string;
  runtimeTestCommand?: string;
  baselinePath?: string;
  baselineStorePath?: string;
  baselineKey?: string;
  baselineLabel?: string;
  saveBaseline?: boolean;
}

export interface ReportCommandExecutor {
  runCommand(binaryPath: string, args: string[], cwd: string): Promise<string>;
  runReport(binaryPath: string, args: string[], cwd: string): Promise<LopperReport>;
}

export interface LopperRunnerDeps {
  binaryLifecycle?: BinaryLifecycleManager;
  reportExecutor?: ReportCommandExecutor;
}

class LopperCliReportExecutor implements ReportCommandExecutor {
  constructor(private readonly output: Pick<vscode.OutputChannel, "appendLine">) {}

  async runCommand(binaryPath: string, args: string[], cwd: string): Promise<string> {
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
      return stdout;
    } catch (error) {
      const execError = error as NodeJS.ErrnoException & { stdout?: string; stderr?: string };
      if (execError.code === "ENOENT") {
        throw new BinaryResolutionError(
          "Lopper binary not found. Set lopper.binaryPath or LOPPER_BINARY_PATH before running the extension.",
        );
      }

      const stdout = execError.stdout?.trim() ?? "";
      if (stdout.length > 0) {
        return stdout;
      }

      const stderr = execError.stderr?.trim();
      throw new Error(stderr && stderr.length > 0 ? stderr : `lopper command failed for ${binaryPath}`);
    }
  }

  async runReport(binaryPath: string, args: string[], cwd: string): Promise<LopperReport> {
    const stdout = await this.runCommand(binaryPath, args, cwd);
    return this.parseReport(stdout, binaryPath);
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
    const { document, scopeMode: scopeModeOption, dependencyName } = options;
    const binaryPath = await this.resolveBinaryPath(folder);
    const binarySignature = await this.binarySignature(binaryPath);
    const requestedLanguage = resolveLopperLanguage(configuredLopperLanguage(folder), document, folder.uri.fsPath);
    const scopeMode = normalizeScopeMode(scopeModeOption);
    const report = await this.executeReport(binaryPath, folder, {
      format: "json",
      requestedLanguage,
      scopeMode,
      document,
      dependencyName,
      suggestOnly: options.suggestOnly,
      runtimeTracePath: options.runtimeTracePath,
      runtimeTestCommand: options.runtimeTestCommand,
      baselinePath: options.baselinePath,
      baselineStorePath: options.baselineStorePath,
      baselineKey: options.baselineKey,
      baselineLabel: options.baselineLabel,
      saveBaseline: options.saveBaseline,
    });

    const codemodsByDependency = new Map<string, LopperCodemodReport>();
    for (const dependency of report.dependencies) {
      if (!shouldFetchCodemod(dependency, requestedLanguage)) {
        continue;
      }
      const codemod = await this.fetchCodemod(binaryPath, folder, dependency, scopeMode, options);
      if (codemod) {
        codemodsByDependency.set(dependency.name, codemod);
      }
    }

    return { folder, binaryPath, binarySignature, requestedLanguage, scopeMode, report, codemodsByDependency };
  }

  async exportWorkspace(
    folder: vscode.WorkspaceFolder,
    format: LopperOutputFormat,
    options: WorkspaceExportRequest = {},
  ): Promise<string> {
    const { document, scopeMode: scopeModeOption } = options;
    const binaryPath = await this.resolveBinaryPath(folder);
    const requestedLanguage = resolveLopperLanguage(configuredLopperLanguage(folder), document, folder.uri.fsPath);
    const scopeMode = normalizeScopeMode(scopeModeOption);
    return this.executeText(binaryPath, folder, {
      format,
      requestedLanguage,
      scopeMode,
      document,
      runtimeTracePath: options.runtimeTracePath,
      runtimeTestCommand: options.runtimeTestCommand,
      baselinePath: options.baselinePath,
      baselineStorePath: options.baselineStorePath,
      baselineKey: options.baselineKey,
      baselineLabel: options.baselineLabel,
      saveBaseline: options.saveBaseline,
    });
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

  private async executeReport(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    options: {
      format: LopperOutputFormat;
      requestedLanguage: string;
      scopeMode: LopperScopeMode;
      document?: vscode.TextDocument;
      dependencyName?: string;
      suggestOnly?: boolean;
      runtimeTracePath?: string;
      runtimeTestCommand?: string;
      baselinePath?: string;
      baselineStorePath?: string;
      baselineKey?: string;
      baselineLabel?: string;
      saveBaseline?: boolean;
    },
  ): Promise<LopperReport> {
    const args = this.buildAnalysisArgs(folder, options);
    return this.reportExecutor.runReport(binaryPath, args, folder.uri.fsPath);
  }

  private async executeText(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    options: {
      format: LopperOutputFormat;
      requestedLanguage: string;
      scopeMode: LopperScopeMode;
      document?: vscode.TextDocument;
      runtimeTracePath?: string;
      runtimeTestCommand?: string;
      baselinePath?: string;
      baselineStorePath?: string;
      baselineKey?: string;
      baselineLabel?: string;
      saveBaseline?: boolean;
    },
  ): Promise<string> {
    const args = this.buildAnalysisArgs(folder, options);
    return this.reportExecutor.runCommand(binaryPath, args, folder.uri.fsPath);
  }

  private buildAnalysisArgs(
    folder: vscode.WorkspaceFolder,
    options: {
      format: LopperOutputFormat;
      requestedLanguage: string;
      scopeMode: LopperScopeMode;
      document?: vscode.TextDocument;
      dependencyName?: string;
      suggestOnly?: boolean;
      runtimeTracePath?: string;
      runtimeTestCommand?: string;
      baselinePath?: string;
      baselineStorePath?: string;
      baselineKey?: string;
      baselineLabel?: string;
      saveBaseline?: boolean;
    },
  ): string[] {
    const args = ["analyse"];
    if (options.dependencyName) {
      args.push(options.dependencyName);
    } else {
      args.push("--top", String(this.topN(folder)));
    }
    args.push(
      "--repo",
      folder.uri.fsPath,
      "--language",
      options.requestedLanguage,
      "--scope-mode",
      options.scopeMode,
      "--format",
      options.format,
    );

    this.appendThresholdArgs(folder, args);
    this.appendRuntimeArgs(args, options.runtimeTracePath, options.runtimeTestCommand);
    this.appendBaselineArgs(args, options.baselinePath, options.baselineStorePath, options.baselineKey, options.baselineLabel, options.saveBaseline);
    if (options.suggestOnly) {
      args.push("--suggest-only");
    }
    return args;
  }

  private appendThresholdArgs(folder: vscode.WorkspaceFolder, args: string[]): void {
    const configuration = vscode.workspace.getConfiguration("lopper", folder.uri);
    const thresholdFailOnIncrease = configuration.get<number>("thresholdFailOnIncreasePercent", -1);
    const lowConfidenceWarning = configuration.get<number>("thresholdLowConfidenceWarningPercent", 40);
    const minUsagePercent = configuration.get<number>("thresholdMinUsagePercentForRecommendations", 40);
    const maxUncertainImports = configuration.get<number>("thresholdMaxUncertainImportCount", -1);
    const licenseDeny = configuration.get<string[]>("licenseDeny", []);
    const licenseFailOnDeny = configuration.get<boolean>("licenseFailOnDeny", false);
    const licenseIncludeRegistryProvenance = configuration.get<boolean>("licenseProvenanceRegistry", false);

    args.push("--threshold-fail-on-increase", String(thresholdFailOnIncrease));
    args.push("--threshold-low-confidence-warning", String(lowConfidenceWarning));
    args.push("--threshold-min-usage-percent", String(minUsagePercent));
    args.push("--threshold-max-uncertain-imports", String(maxUncertainImports));
    if (licenseDeny.length > 0) {
      args.push("--license-deny", licenseDeny.join(","));
    }
    if (licenseFailOnDeny) {
      args.push("--license-fail-on-deny");
    }
    if (licenseIncludeRegistryProvenance) {
      args.push("--license-provenance-registry");
    }
  }

  private appendRuntimeArgs(args: string[], runtimeTracePath?: string, runtimeTestCommand?: string): void {
    if (runtimeTracePath && runtimeTracePath.trim().length > 0) {
      args.push("--runtime-trace", runtimeTracePath.trim());
    }
    if (runtimeTestCommand && runtimeTestCommand.trim().length > 0) {
      args.push("--runtime-test-command", runtimeTestCommand.trim());
    }
  }

  private appendBaselineArgs(
    args: string[],
    baselinePath?: string,
    baselineStorePath?: string,
    baselineKey?: string,
    baselineLabel?: string,
    saveBaseline?: boolean,
  ): void {
    if (baselinePath && baselinePath.trim().length > 0) {
      args.push("--baseline", baselinePath.trim());
    }
    if (baselineStorePath && baselineStorePath.trim().length > 0) {
      args.push("--baseline-store", baselineStorePath.trim());
    }
    if (baselineKey && baselineKey.trim().length > 0) {
      args.push("--baseline-key", baselineKey.trim());
    }
    if (baselineLabel && baselineLabel.trim().length > 0) {
      args.push("--baseline-label", baselineLabel.trim());
    }
    if (saveBaseline) {
      args.push("--save-baseline");
    }
  }

  private async fetchCodemod(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    dependency: LopperDependencyReport,
    scopeMode: LopperScopeMode,
    options: WorkspaceAnalysisRequest,
  ): Promise<LopperCodemodReport | undefined> {
    if (!dependency.name) {
      return undefined;
    }
    try {
      const report = await this.reportExecutor.runReport(
        binaryPath,
        this.buildAnalysisArgs(folder, {
          format: "json",
          requestedLanguage: "js-ts",
          scopeMode,
          dependencyName: dependency.name,
          runtimeTracePath: options.runtimeTracePath,
          runtimeTestCommand: options.runtimeTestCommand,
          baselinePath: options.baselinePath,
          baselineStorePath: options.baselineStorePath,
          baselineKey: options.baselineKey,
          baselineLabel: options.baselineLabel,
          saveBaseline: options.saveBaseline,
          suggestOnly: true,
        }),
        folder.uri.fsPath,
      );
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
