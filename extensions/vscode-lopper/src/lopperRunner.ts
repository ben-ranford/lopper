import { execFile } from "node:child_process";
import { access } from "node:fs/promises";
import * as path from "node:path";
import { promisify } from "node:util";
import * as vscode from "vscode";

import { configuredLopperLanguage, resolveLopperLanguage, type LopperLanguage } from "./languageConfiguration";
import { findExecutableInPath, ManagedBinaryInstaller } from "./managedBinary";
import type {
  LopperCodemodReport,
  LopperDependencyReport,
  LopperReport,
} from "./types";

const execFileAsync = promisify(execFile);

export interface WorkspaceAnalysis {
  folder: vscode.WorkspaceFolder;
  binaryPath: string;
  requestedLanguage: LopperLanguage;
  report: LopperReport;
  codemodsByDependency: Map<string, LopperCodemodReport>;
}

export class BinaryResolutionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BinaryResolutionError";
  }
}

export class LopperRunner {
  private readonly installer: ManagedBinaryInstaller;

  constructor(
    private readonly output: Pick<vscode.OutputChannel, "appendLine">,
    private readonly context: vscode.ExtensionContext,
  ) {
    this.installer = new ManagedBinaryInstaller(context.globalStorageUri.fsPath, output);
  }

  async analyseWorkspace(
    folder: vscode.WorkspaceFolder,
    document?: vscode.TextDocument,
  ): Promise<WorkspaceAnalysis> {
    const binaryPath = await this.resolveBinaryPath(folder);
    const requestedLanguage = resolveLopperLanguage(configuredLopperLanguage(folder), document);
    const topN = this.topN(folder);
    const report = await this.runReport(binaryPath, [
      "analyse",
      "--top",
      String(topN),
      "--repo",
      folder.uri.fsPath,
      "--language",
      requestedLanguage,
      "--format",
      "json",
    ], folder.uri.fsPath);

    const codemodsByDependency = new Map<string, LopperCodemodReport>();
    for (const dependency of report.dependencies) {
      if (!shouldFetchCodemod(dependency, requestedLanguage)) {
        continue;
      }
      const codemod = await this.fetchCodemod(binaryPath, folder, dependency);
      if (codemod) {
        codemodsByDependency.set(dependency.name, codemod);
      }
    }

    return { folder, binaryPath, requestedLanguage, report, codemodsByDependency };
  }

  private topN(folder: vscode.WorkspaceFolder): number {
    const configured = vscode.workspace.getConfiguration("lopper", folder.uri).get<number>("topN", 20);
    return Number.isFinite(configured) && configured > 0 ? Math.floor(configured) : 20;
  }

  private async fetchCodemod(
    binaryPath: string,
    folder: vscode.WorkspaceFolder,
    dependency: LopperDependencyReport,
  ): Promise<LopperCodemodReport | undefined> {
    if (!dependency.name) {
      return undefined;
    }
    try {
      const report = await this.runReport(binaryPath, [
        "analyse",
        dependency.name,
        "--repo",
        folder.uri.fsPath,
        "--language",
        "js-ts",
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

  async resolveBinaryPath(folder: vscode.WorkspaceFolder): Promise<string> {
    const envBinaryPath = process.env.LOPPER_BINARY_PATH?.trim();
    if (envBinaryPath) {
      return this.ensureConfiguredBinaryExists(envBinaryPath, "LOPPER_BINARY_PATH");
    }

    const configuredBinaryPath = vscode.workspace
      .getConfiguration("lopper", folder.uri)
      .get<string>("binaryPath", "")
      .trim();
    if (configuredBinaryPath) {
      const resolvedPath = path.isAbsolute(configuredBinaryPath)
        ? configuredBinaryPath
        : path.join(folder.uri.fsPath, configuredBinaryPath);
      return this.ensureConfiguredBinaryExists(resolvedPath, "lopper.binaryPath");
    }

    const localBinary = path.join(folder.uri.fsPath, "bin", process.platform === "win32" ? "lopper.exe" : "lopper");
    try {
      await access(localBinary);
      return localBinary;
    } catch {
      const pathBinary = await findExecutableInPath(process.platform === "win32" ? "lopper.exe" : "lopper");
      if (pathBinary) {
        return pathBinary;
      }
    }

    const autoDownloadEnabled = vscode.workspace
      .getConfiguration("lopper", folder.uri)
      .get<boolean>("autoDownloadBinary", true);
    if (!autoDownloadEnabled) {
      throw new BinaryResolutionError(
        "Lopper binary not found. Install it manually, set lopper.binaryPath or LOPPER_BINARY_PATH, or enable lopper.autoDownloadBinary.",
      );
    }

    const releaseTag = vscode.workspace
      .getConfiguration("lopper", folder.uri)
      .get<string>("managedBinaryTag", "")
      .trim();

    const cachedBinary = await this.installer.findInstalledBinary(releaseTag);
    if (cachedBinary) {
      return cachedBinary;
    }

    const installResult = await vscode.window.withProgress(
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
        return this.installer.ensureInstalled(releaseTag);
      },
    );

    if (installResult.downloaded) {
      this.output.appendLine(`managed lopper binary installed: ${installResult.binaryPath} (${installResult.tag})`);
    }
    return installResult.binaryPath;
  }

  private async ensureConfiguredBinaryExists(binaryPath: string, source: string): Promise<string> {
    try {
      await access(binaryPath);
      return binaryPath;
    } catch {
      throw new BinaryResolutionError(`${source} points to a missing binary: ${binaryPath}`);
    }
  }

  private async runReport(binaryPath: string, args: string[], cwd: string): Promise<LopperReport> {
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
          `Lopper binary not found. Set lopper.binaryPath or LOPPER_BINARY_PATH before running the extension.`,
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

function shouldFetchCodemod(dependency: LopperDependencyReport, requestedLanguage: LopperLanguage): boolean {
  const dependencyLanguage = dependency.language?.trim().toLowerCase();
  if (dependencyLanguage) {
    return dependencyLanguage === "js-ts";
  }
  return requestedLanguage === "js-ts";
}
