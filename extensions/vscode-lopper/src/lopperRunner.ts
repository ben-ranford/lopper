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

export const defaultLopperRunTimeoutMs = 120_000;
export const defaultCodemodAnalysisConcurrency = 4;

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
  signal?: AbortSignal;
  runtimeTracePath?: string;
  runtimeTestCommand?: string;
  baselinePath?: string;
  baselineStorePath?: string;
  baselineKey?: string;
  baselineLabel?: string;
  saveBaseline?: boolean;
}

interface CodemodFetchContext {
  binarySignature: string;
  requestedLanguage: LopperLanguage;
  scopeMode: LopperScopeMode;
  timeoutMs: number | undefined;
}

export interface WorkspaceExportRequest {
  document?: vscode.TextDocument;
  scopeMode?: LopperScopeMode;
  signal?: AbortSignal;
  runtimeTracePath?: string;
  runtimeTestCommand?: string;
  baselinePath?: string;
  baselineStorePath?: string;
  baselineKey?: string;
  baselineLabel?: string;
  saveBaseline?: boolean;
}

export interface ReportCommandExecutor {
  runCommand(binaryPath: string, args: string[], cwd: string, options?: LopperExecutionOptions): Promise<string>;
  runReport(binaryPath: string, args: string[], cwd: string, options?: LopperExecutionOptions): Promise<LopperReport>;
}

export interface LopperRunnerDeps {
  binaryLifecycle?: BinaryLifecycleManager;
  reportExecutor?: ReportCommandExecutor;
}

export interface LopperExecutionOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
}

export class LopperCliReportExecutor implements ReportCommandExecutor {
  constructor(private readonly output: Pick<vscode.OutputChannel, "appendLine">) {}

  async runCommand(
    binaryPath: string,
    args: string[],
    cwd: string,
    options: LopperExecutionOptions = {},
  ): Promise<string> {
    this.output.appendLine(`running: ${binaryPath} ${args.join(" ")}`);
    try {
      const { stdout, stderr } = await execFileAsync(binaryPath, args, {
        cwd,
        env: process.env,
        maxBuffer: 10 * 1024 * 1024,
        signal: options.signal,
        timeout: options.timeoutMs,
      });
      if (stderr.trim().length > 0) {
        this.output.appendLine(stderr.trim());
      }
      return stdout;
    } catch (error) {
      return this.handleRunCommandError(
        error as NodeJS.ErrnoException & { killed?: boolean; signal?: NodeJS.Signals; stdout?: string; stderr?: string },
        args,
        binaryPath,
        options,
      );
    }
  }

  async runReport(
    binaryPath: string,
    args: string[],
    cwd: string,
    options: LopperExecutionOptions = {},
  ): Promise<LopperReport> {
    const stdout = await this.runCommand(binaryPath, args, cwd, options);
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

  private handleRunCommandError(
    execError: NodeJS.ErrnoException & { killed?: boolean; signal?: NodeJS.Signals; stdout?: string; stderr?: string },
    args: string[],
    binaryPath: string,
    options: LopperExecutionOptions,
  ): string {
    if (execError.code === "ENOENT") {
      throw new BinaryResolutionError(
        "Lopper binary not found. Set lopper.binaryPath or LOPPER_BINARY_PATH before running the extension.",
      );
    }

    if (isAbortError(execError) || options.signal?.aborted) {
      throw new Error("lopper command was cancelled");
    }

    if (isExecTimeout(execError, options.timeoutMs)) {
      throw new Error(`lopper command timed out after ${options.timeoutMs}ms`);
    }

    const stdout = execError.stdout ?? "";
    if (this.shouldReturnStdout(stdout, args)) {
      return stdout;
    }

    const stderr = execError.stderr?.trim();
    throw new Error(stderr && stderr.length > 0 ? stderr : `lopper command failed for ${binaryPath}`);
  }

  private shouldReturnStdout(stdout: string, args: string[]): boolean {
    const trimmedStdout = stdout.trim();
    if (trimmedStdout.length === 0) {
      return false;
    }

    const requestedFormat = this.requestedFormat(args);
    if (requestedFormat && requestedFormat !== "json") {
      return true;
    }

    return this.looksLikeJsonPayload(trimmedStdout);
  }

  private requestedFormat(args: string[]): string | undefined {
    const formatIndex = args.indexOf("--format");
    return formatIndex >= 0 ? args[formatIndex + 1] : undefined;
  }

  private looksLikeJsonPayload(stdout: string): boolean {
    return stdout.startsWith("{") || stdout.startsWith("[");
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
              cancellable: true,
            },
            async (progress, token) => {
              progress.report({
                message: releaseTag
                  ? `Downloading ${releaseTag} for ${process.platform}/${process.arch}`
                  : `Downloading latest release for ${process.platform}/${process.arch}`,
              });
              const abortController = new AbortController();
              const cancellation = token.onCancellationRequested(() => abortController.abort());
              try {
                if (token.isCancellationRequested) {
                  abortController.abort();
                }
                return install(abortController.signal);
              } finally {
                cancellation.dispose();
              }
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
    const requestedLanguage = await resolveLopperLanguage(configuredLopperLanguage(folder), document, folder.uri.fsPath);
    const scopeMode = normalizeScopeMode(scopeModeOption);
    const timeoutMs = this.runTimeoutMs(folder);
    const report = await this.executeReport(binaryPath, folder, {
      format: "json",
      requestedLanguage,
      scopeMode,
      document,
      dependencyName,
      suggestOnly: options.suggestOnly,
      signal: options.signal,
      timeoutMs,
      runtimeTracePath: options.runtimeTracePath,
      runtimeTestCommand: options.runtimeTestCommand,
      baselinePath: options.baselinePath,
      baselineStorePath: options.baselineStorePath,
      baselineKey: options.baselineKey,
      baselineLabel: options.baselineLabel,
      saveBaseline: options.saveBaseline,
    });

    const codemodsByDependency = await this.fetchCodemods(binaryPath, folder, report.dependencies, options, {
      binarySignature,
      requestedLanguage,
      scopeMode,
      timeoutMs,
    });

    return { folder, binaryPath, binarySignature, requestedLanguage, scopeMode, report, codemodsByDependency };
  }

  async exportWorkspace(
    folder: vscode.WorkspaceFolder,
    format: LopperOutputFormat,
    options: WorkspaceExportRequest = {},
  ): Promise<string> {
    const { document, scopeMode: scopeModeOption } = options;
    const binaryPath = await this.resolveBinaryPath(folder);
    const requestedLanguage = await resolveLopperLanguage(configuredLopperLanguage(folder), document, folder.uri.fsPath);
    const scopeMode = normalizeScopeMode(scopeModeOption);
    const timeoutMs = this.runTimeoutMs(folder);
    return this.executeText(binaryPath, folder, {
      format,
      requestedLanguage,
      scopeMode,
      document,
      signal: options.signal,
      timeoutMs,
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

  private runTimeoutMs(folder: vscode.WorkspaceFolder): number | undefined {
    const configured = vscode.workspace.getConfiguration("lopper", folder.uri).get<number>(
      "runTimeoutMs",
      defaultLopperRunTimeoutMs,
    );
    if (!Number.isFinite(configured)) {
      return defaultLopperRunTimeoutMs;
    }
    const timeoutMs = Math.floor(configured);
    return timeoutMs > 0 ? timeoutMs : undefined;
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
      signal?: AbortSignal;
      timeoutMs?: number;
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
    return this.reportExecutor.runReport(binaryPath, args, folder.uri.fsPath, {
      signal: options.signal,
      timeoutMs: options.timeoutMs,
    });
  }

  private async executeText(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    options: {
      format: LopperOutputFormat;
      requestedLanguage: string;
      scopeMode: LopperScopeMode;
      document?: vscode.TextDocument;
      signal?: AbortSignal;
      timeoutMs?: number;
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
    return this.reportExecutor.runCommand(binaryPath, args, folder.uri.fsPath, {
      signal: options.signal,
      timeoutMs: options.timeoutMs,
    });
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
      signal?: AbortSignal;
      timeoutMs?: number;
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
    const dependencyName = normalizeDependencyArgument(options.dependencyName);
    if (!dependencyName) {
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
    if (dependencyName) {
      args.push("--", dependencyName);
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

    const thresholdArgs = [
      "--threshold-fail-on-increase",
      String(thresholdFailOnIncrease),
      "--threshold-low-confidence-warning",
      String(lowConfidenceWarning),
      "--threshold-min-usage-percent",
      String(minUsagePercent),
      "--threshold-max-uncertain-imports",
      String(maxUncertainImports),
      ...(licenseDeny.length > 0 ? ["--license-deny", licenseDeny.join(",")] : []),
      ...(licenseFailOnDeny ? ["--license-fail-on-deny"] : []),
      ...(licenseIncludeRegistryProvenance ? ["--license-provenance-registry"] : []),
    ];
    args.push(...thresholdArgs);
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
    timeoutMs: number | undefined,
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
          signal: options.signal,
          timeoutMs,
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
        {
          signal: options.signal,
          timeoutMs,
        },
      );
      return report.dependencies[0]?.codemod;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.output.appendLine(`codemod analysis skipped for ${dependency.name}: ${message}`);
      return undefined;
    }
  }

  private async fetchCodemods(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    dependencies: LopperDependencyReport[],
    options: WorkspaceAnalysisRequest,
    context: CodemodFetchContext,
  ): Promise<Map<string, LopperCodemodReport>> {
    const { binarySignature, requestedLanguage, scopeMode, timeoutMs } = context;
    const dependencyByCacheKey = new Map<string, LopperDependencyReport>();
    const cacheKeys: string[] = [];
    for (const dependency of dependencies) {
      if (!shouldFetchCodemod(dependency, requestedLanguage)) {
        continue;
      }

      const cacheKey = codemodCacheKey(binarySignature, scopeMode, dependency.name);
      if (dependencyByCacheKey.has(cacheKey)) {
        continue;
      }

      dependencyByCacheKey.set(cacheKey, dependency);
      cacheKeys.push(cacheKey);
    }

    const codemodByCacheKey = new Map<string, LopperCodemodReport | undefined>();
    await runWithConcurrency(cacheKeys, codemodAnalysisConcurrency(options), async (cacheKey) => {
      const dependency = dependencyByCacheKey.get(cacheKey);
      if (!dependency) {
        return;
      }

      const codemod = await this.fetchCodemod(binaryPath, folder, dependency, scopeMode, options, timeoutMs);
      codemodByCacheKey.set(cacheKey, codemod);
    });

    const codemodsByDependency = new Map<string, LopperCodemodReport>();
    for (const dependency of dependencies) {
      if (!shouldFetchCodemod(dependency, requestedLanguage)) {
        continue;
      }

      const codemod = codemodByCacheKey.get(codemodCacheKey(binarySignature, scopeMode, dependency.name));
      if (codemod) {
        codemodsByDependency.set(dependency.name, codemod);
      }
    }

    return codemodsByDependency;
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

function codemodCacheKey(binarySignature: string, scopeMode: LopperScopeMode, dependencyName: string): string {
  return [binarySignature, scopeMode, dependencyName].join("\0");
}

function codemodAnalysisConcurrency(options: WorkspaceAnalysisRequest): number {
  if (hasValue(options.runtimeTestCommand) || options.saveBaseline) {
    return 1;
  }
  return defaultCodemodAnalysisConcurrency;
}

function hasValue(value: string | undefined): boolean {
  return value !== undefined && value.trim().length > 0;
}

async function runWithConcurrency<T>(
  items: T[],
  concurrency: number,
  worker: (item: T) => Promise<void>,
): Promise<void> {
  const workerCount = Math.min(items.length, Math.max(1, Math.floor(concurrency)));
  let nextIndex = 0;

  await Promise.all(
    Array.from({ length: workerCount }, async () => {
      while (nextIndex < items.length) {
        const item = items[nextIndex];
        nextIndex += 1;
        await worker(item);
      }
    }),
  );
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

function normalizeDependencyArgument(dependencyName: string | undefined): string | undefined {
  const normalized = dependencyName?.trim();
  if (!normalized) {
    return undefined;
  }
  if (normalized.startsWith("-")) {
    throw new Error(`unsafe dependency name rejected before lopper execution: ${normalized}`);
  }
  return normalized;
}

function isAbortError(error: NodeJS.ErrnoException): boolean {
  return error.name === "AbortError" || error.code === "ABORT_ERR";
}

function isExecTimeout(
  error: NodeJS.ErrnoException & { killed?: boolean; signal?: NodeJS.Signals },
  timeoutMs: number | undefined,
): boolean {
  return timeoutMs !== undefined && error.killed === true && error.signal === "SIGTERM";
}
