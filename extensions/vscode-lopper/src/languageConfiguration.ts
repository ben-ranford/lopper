import { constants, type Stats } from "node:fs";
import { lstat, open, realpath, type FileHandle } from "node:fs/promises";
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
// At most 64 Gradle files (16 MiB plus one-byte overflow probes) per inference.
const maxAndroidAncestors = 32;
const maxGradleBytes = 256 * 1024;
const maxCachedAndroidModules = 256;
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
  private generation = 0;

  async hasAndroidModuleSignals(fileName: string, workspaceFolderPath?: string): Promise<boolean> {
    if (!workspaceFolderPath) {
      return false;
    }

    const workspaceRoot = await realpath(workspaceFolderPath).catch(() => undefined);
    if (!workspaceRoot) {
      return false;
    }
    const resolvedFile = path.resolve(fileName);
    const relativeFile = path.relative(path.resolve(workspaceFolderPath), resolvedFile);
    if (relativeFile === ".." || relativeFile.startsWith(`..${path.sep}`) || path.isAbsolute(relativeFile)) {
      return false;
    }

    let currentDir = path.dirname(path.join(workspaceRoot, relativeFile));
    const generation = this.generation;
    for (let depth = 0; depth < maxAndroidAncestors && generation === this.generation; depth++) {
      if (await this.moduleSignalsAndroid(currentDir, workspaceRoot) && generation === this.generation) {
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
      this.clear();
    }
  }

  clear(): void {
    this.moduleSignalsByRoot.clear();
    this.generation++;
  }

  private moduleSignalsAndroid(moduleRoot: string, workspaceRoot: string): Promise<boolean> {
    const cacheKey = `${workspaceRoot}\0${path.resolve(moduleRoot)}`;
    const cached = this.moduleSignalsByRoot.get(cacheKey);
    if (cached) {
      return cached;
    }

    if (this.moduleSignalsByRoot.size >= maxCachedAndroidModules) {
      this.moduleSignalsByRoot.clear();
    }
    const generation = this.generation;
    const result = readAndroidModuleSignals(moduleRoot, workspaceRoot, () => generation === this.generation);
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

async function readAndroidModuleSignals(moduleRoot: string, workspaceRoot: string, active: () => boolean): Promise<boolean> {
  const directories = await checkedDirectories(moduleRoot, workspaceRoot);
  if (!directories || !active()) {
    return false;
  }
  if (await regularManifest(moduleRoot, workspaceRoot)) {
    return directoriesUnchanged(directories);
  }

  for (const buildFileName of ["build.gradle", "build.gradle.kts"]) {
    const buildFile = (await readTextFile(path.join(moduleRoot, buildFileName), active))?.toLowerCase();
    if (buildFile && androidBuildPluginMarkers.some((marker) => buildFile.includes(marker))) {
      return directoriesUnchanged(directories);
    }
  }
  return false;
}

async function checkedDirectories(directory: string, workspaceRoot: string): Promise<Map<string, Stats> | undefined> {
  const relative = path.relative(workspaceRoot, directory);
  const parts = relative ? relative.split(path.sep) : [];
  if (parts.length >= maxAndroidAncestors || parts.includes("..") || path.isAbsolute(relative)) {
    return undefined;
  }
  const snapshots = new Map<string, Stats>();
  let current = workspaceRoot;
  try {
    for (const part of ["", ...parts]) {
      current = path.join(current, part);
      const metadata = await lstat(current);
      if (!metadata.isDirectory() || metadata.isSymbolicLink()) {
        return undefined;
      }
      snapshots.set(current, metadata);
    }
    return snapshots;
  } catch {
    return undefined;
  }
}

function sameFile(left: Stats, right: Stats): boolean {
  return left.dev === right.dev && left.ino === right.ino && left.mode === right.mode;
}

async function directoriesUnchanged(snapshots: Map<string, Stats>): Promise<boolean> {
  try {
    for (const [directory, metadata] of snapshots) {
      if (!sameFile(metadata, await lstat(directory))) {
        return false;
      }
    }
    return true;
  } catch {
    return false;
  }
}

async function regularManifest(moduleRoot: string, workspaceRoot: string): Promise<boolean> {
  const manifest = path.join(moduleRoot, androidManifestRelativePath);
  const directories = await checkedDirectories(path.dirname(manifest), workspaceRoot);
  if (!directories) {
    return false;
  }
  try {
    return (await lstat(manifest)).isFile() && await directoriesUnchanged(directories);
  } catch {
    return false;
  }
}

async function readTextFile(filePath: string, active: () => boolean): Promise<string | undefined> {
  try {
    const before = await lstat(filePath);
    if (!active() || !before.isFile() || before.size > maxGradleBytes) {
      return undefined;
    }
    // NOFOLLOW rejects replacement symlinks; NONBLOCK prevents replacement FIFOs
    // from waiting for a writer. Both are supported on our POSIX hosts.
    const file = await open(filePath, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) | (constants.O_NONBLOCK ?? 0));
    try {
      const opened = await file.stat();
      if (!sameFile(before, opened) || !opened.isFile() || opened.size > maxGradleBytes) {
        return undefined;
      }
      return await readStableText(file, opened, filePath, active);
    } finally {
      await file.close();
    }
  } catch {
    return undefined;
  }
}

async function readStableText(file: FileHandle, opened: Stats, filePath: string, active: () => boolean): Promise<string | undefined> {
  const buffer = Buffer.alloc(opened.size + 1);
  let offset = 0;
  for (let reads = 0; reads < 32 && offset < buffer.length && active(); reads++) {
    const { bytesRead } = await file.read(buffer, offset, buffer.length - offset, offset);
    if (bytesRead === 0) {
      break;
    }
    offset += bytesRead;
  }
  const after = await file.stat();
  const named = await lstat(filePath);
  if (!active() || offset !== opened.size || !sameFile(opened, named) || !sameContentMetadata(opened, after)) {
    return undefined;
  }
  return buffer.subarray(0, offset).toString("utf8");
}

function sameContentMetadata(left: Stats, right: Stats): boolean {
  return left.size === right.size && left.mtimeMs === right.mtimeMs && left.ctimeMs === right.ctimeMs;
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
  if (relativeModuleRoot === ".." || relativeModuleRoot.startsWith(`..${path.sep}`) || path.isAbsolute(relativeModuleRoot)) {
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
