import { access, readFile } from "node:fs/promises";
import * as path from "node:path";
import * as vscode from "vscode";

export const lopperLanguageValues = [
  "auto",
  "all",
  "cpp",
  "dart",
  "dotnet",
  "elixir",
  "go",
  "js-ts",
  "jvm",
  "kotlin-android",
  "php",
  "powershell",
  "python",
  "ruby",
  "rust",
  "swift",
] as const;

export type LopperLanguage = typeof lopperLanguageValues[number];
export type ConcreteLopperLanguage = Exclude<LopperLanguage, "auto" | "all">;

interface DocumentLike {
  fileName: string;
  isUntitled?: boolean;
  languageId: string;
}

interface AndroidModuleSignalProvider {
  hasAndroidModuleSignals(fileName: string, workspaceFolderPath?: string): Promise<boolean>;
}

const knownLanguages = new Set<LopperLanguage>(lopperLanguageValues);
const jvmLikeLanguageIds = new Set(["java", "kotlin"]);
const jvmLikeExtensions = new Set([".java", ".kt", ".kts"]);
const androidManifestRelativePath = path.join("src", "main", "AndroidManifest.xml");
const androidBuildPluginMarkers = [
  "com.android.application",
  "com.android.dynamic-feature",
  "com.android.library",
  "com.android.test",
  "org.jetbrains.kotlin.android",
];

const adapterByLanguageId = new Map<string, ConcreteLopperLanguage>([
  ["c", "cpp"],
  ["cpp", "cpp"],
  ["cuda-cpp", "cpp"],
  ["csharp", "dotnet"],
  ["dart", "dart"],
  ["elixir", "elixir"],
  ["fsharp", "dotnet"],
  ["go", "go"],
  ["java", "jvm"],
  ["javascript", "js-ts"],
  ["javascriptreact", "js-ts"],
  ["kotlin", "jvm"],
  ["php", "php"],
  ["powershell", "powershell"],
  ["python", "python"],
  ["ruby", "ruby"],
  ["rust", "rust"],
  ["swift", "swift"],
  ["typescript", "js-ts"],
  ["typescriptreact", "js-ts"],
  ["vb", "dotnet"],
]);

const adapterByExtension = new Map<string, ConcreteLopperLanguage>([
  [".c", "cpp"],
  [".cc", "cpp"],
  [".cpp", "cpp"],
  [".cs", "dotnet"],
  [".csx", "dotnet"],
  [".cxx", "cpp"],
  [".dart", "dart"],
  [".ex", "elixir"],
  [".exs", "elixir"],
  [".fs", "dotnet"],
  [".fsi", "dotnet"],
  [".fsx", "dotnet"],
  [".go", "go"],
  [".h", "cpp"],
  [".hpp", "cpp"],
  [".java", "jvm"],
  [".js", "js-ts"],
  [".jsx", "js-ts"],
  [".kt", "jvm"],
  [".kts", "jvm"],
  [".mjs", "js-ts"],
  [".php", "php"],
  [".ps1", "powershell"],
  [".psd1", "powershell"],
  [".psm1", "powershell"],
  [".py", "python"],
  [".rb", "ruby"],
  [".rs", "rust"],
  [".swift", "swift"],
  [".ts", "js-ts"],
  [".tsx", "js-ts"],
  [".vb", "dotnet"],
]);

const alphabeticalOrder = new Intl.Collator("en").compare;
const languageIds = Array.from(new Set(adapterByLanguageId.keys())).sort((left, right) => alphabeticalOrder(left, right));
const extensionPatterns = Array.from(new Set(adapterByExtension.keys()))
  .map((extension) => `**/*${extension}`)
  .sort((left, right) => alphabeticalOrder(left, right));

export const supportedDocumentSelectors: vscode.DocumentFilter[] = [
  ...languageIds.map((language) => ({
    scheme: "file",
    language,
  })),
  ...extensionPatterns.map((pattern) => ({
    scheme: "file",
    pattern,
  })),
];

export function configuredLopperLanguage(folder?: vscode.WorkspaceFolder): LopperLanguage {
  const configured = vscode.workspace.getConfiguration("lopper", folder?.uri).get<string>("language", "auto");
  return normalizeLopperLanguage(configured);
}

export class AndroidModuleSignalCache implements AndroidModuleSignalProvider {
  private readonly moduleSignalsByRoot = new Map<string, Promise<boolean>>();

  async hasAndroidModuleSignals(fileName: string, workspaceFolderPath?: string): Promise<boolean> {
    if (!workspaceFolderPath) {
      return false;
    }

    const workspaceRoot = path.resolve(workspaceFolderPath);
    const resolvedFile = path.resolve(fileName);
    const relativeFile = path.relative(workspaceRoot, resolvedFile);
    if (relativeFile.startsWith("..") || path.isAbsolute(relativeFile)) {
      return false;
    }

    let currentDir = path.dirname(resolvedFile);
    while (currentDir.startsWith(workspaceRoot)) {
      if (await this.moduleSignalsAndroid(currentDir)) {
        return true;
      }
      if (currentDir === workspaceRoot) {
        break;
      }
      const parentDir = path.dirname(currentDir);
      if (parentDir === currentDir) {
        break;
      }
      currentDir = parentDir;
    }

    return false;
  }

  invalidateForPath(filePath: string, workspaceFolderPath?: string): void {
    const moduleRoot = androidSignalModuleRoot(filePath, workspaceFolderPath);
    if (moduleRoot) {
      this.moduleSignalsByRoot.delete(moduleRoot);
    }
  }

  clear(): void {
    this.moduleSignalsByRoot.clear();
  }

  private moduleSignalsAndroid(moduleRoot: string): Promise<boolean> {
    const cacheKey = path.resolve(moduleRoot);
    const cached = this.moduleSignalsByRoot.get(cacheKey);
    if (cached) {
      return cached;
    }

    const result = readAndroidModuleSignals(cacheKey);
    this.moduleSignalsByRoot.set(cacheKey, result);
    return result;
  }
}

const defaultAndroidModuleSignalCache = new AndroidModuleSignalCache();

export async function inferLopperLanguageForDocument(
  document?: DocumentLike,
  workspaceFolderPath?: string,
  androidSignals: AndroidModuleSignalProvider = defaultAndroidModuleSignalCache,
): Promise<ConcreteLopperLanguage | undefined> {
  if (!document || document.isUntitled) {
    return undefined;
  }

  const languageId = document.languageId.trim().toLowerCase();
  if (jvmLikeLanguageIds.has(languageId)) {
    return inferJvmFamilyAdapter(document.fileName, workspaceFolderPath, androidSignals);
  }
  if (adapterByLanguageId.has(languageId)) {
    return adapterByLanguageId.get(languageId);
  }

  const extension = path.extname(document.fileName).toLowerCase();
  if (jvmLikeExtensions.has(extension)) {
    return inferJvmFamilyAdapter(document.fileName, workspaceFolderPath, androidSignals);
  }
  if (adapterByExtension.has(extension)) {
    return adapterByExtension.get(extension);
  }

  return undefined;
}

export async function resolveLopperLanguage(
  configuredLanguage: LopperLanguage,
  document?: DocumentLike,
  workspaceFolderPath?: string,
  androidSignals: AndroidModuleSignalProvider = defaultAndroidModuleSignalCache,
): Promise<LopperLanguage> {
  if (configuredLanguage !== "auto") {
    return configuredLanguage;
  }

  return await inferLopperLanguageForDocument(document, workspaceFolderPath, androidSignals) ?? "auto";
}

export async function shouldAutoRefreshForDocument(
  configuredLanguage: LopperLanguage,
  document: DocumentLike,
  workspaceFolderPath?: string,
  androidSignals: AndroidModuleSignalProvider = defaultAndroidModuleSignalCache,
): Promise<boolean> {
  const inferred = await inferLopperLanguageForDocument(document, workspaceFolderPath, androidSignals);
  if (!inferred) {
    return false;
  }

  if (configuredLanguage === "auto" || configuredLanguage === "all") {
    return true;
  }

  return configuredLanguage === inferred;
}

function normalizeLopperLanguage(value: string | undefined): LopperLanguage {
  const normalized = value?.trim().toLowerCase() as LopperLanguage | undefined;
  if (!normalized || !knownLanguages.has(normalized)) {
    return "auto";
  }
  return normalized;
}

export function invalidateAndroidModuleSignalCacheForPath(filePath: string, workspaceFolderPath?: string): void {
  defaultAndroidModuleSignalCache.invalidateForPath(filePath, workspaceFolderPath);
}

export function clearAndroidModuleSignalCache(): void {
  defaultAndroidModuleSignalCache.clear();
}

async function inferJvmFamilyAdapter(
  fileName: string,
  workspaceFolderPath: string | undefined,
  androidSignals: AndroidModuleSignalProvider,
): Promise<ConcreteLopperLanguage> {
  return await androidSignals.hasAndroidModuleSignals(fileName, workspaceFolderPath) ? "kotlin-android" : "jvm";
}

async function readAndroidModuleSignals(moduleRoot: string): Promise<boolean> {
  if (await pathExists(path.join(moduleRoot, androidManifestRelativePath))) {
    return true;
  }

  for (const buildFileName of ["build.gradle", "build.gradle.kts"]) {
    const buildFilePath = path.join(moduleRoot, buildFileName);
    const buildFile = await readTextFile(buildFilePath);
    if (buildFile && androidBuildPluginMarkers.some((marker) => buildFile.toLowerCase().includes(marker))) {
      return true;
    }
  }

  return false;
}

async function pathExists(filePath: string): Promise<boolean> {
  try {
    await access(filePath);
    return true;
  } catch {
    return false;
  }
}

async function readTextFile(filePath: string): Promise<string | undefined> {
  try {
    return await readFile(filePath, "utf8");
  } catch {
    return undefined;
  }
}

function androidSignalModuleRoot(filePath: string, workspaceFolderPath?: string): string | undefined {
  const resolvedFile = path.resolve(filePath);
  const baseName = path.basename(resolvedFile);
  const moduleRoot = moduleRootForAndroidSignal(resolvedFile, baseName);
  if (!moduleRoot) {
    return undefined;
  }
  if (!workspaceFolderPath) {
    return moduleRoot;
  }

  const workspaceRoot = path.resolve(workspaceFolderPath);
  const relativeModuleRoot = path.relative(workspaceRoot, moduleRoot);
  if (relativeModuleRoot.startsWith("..") || path.isAbsolute(relativeModuleRoot)) {
    return undefined;
  }
  return moduleRoot;
}

function moduleRootForAndroidSignal(resolvedFile: string, baseName: string): string | undefined {
  if (baseName === "build.gradle" || baseName === "build.gradle.kts") {
    return path.dirname(resolvedFile);
  }

  if (baseName !== "AndroidManifest.xml") {
    return undefined;
  }

  const relativeManifestPath = path.join(path.basename(path.dirname(path.dirname(resolvedFile))), path.basename(path.dirname(resolvedFile)), baseName);
  if (relativeManifestPath !== androidManifestRelativePath) {
    return undefined;
  }
  return path.dirname(path.dirname(path.dirname(resolvedFile)));
}
