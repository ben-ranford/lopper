import * as fs from "node:fs";
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

const knownLanguages = new Set<LopperLanguage>(lopperLanguageValues);
const jvmLikeLanguageIds = new Set(["java", "kotlin"]);
const jvmLikeExtensions = new Set([".java", ".kt", ".kts"]);
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

export function inferLopperLanguageForDocument(
  document?: DocumentLike,
  workspaceFolderPath?: string,
): ConcreteLopperLanguage | undefined {
  if (!document || document.isUntitled) {
    return undefined;
  }

  const languageId = document.languageId.trim().toLowerCase();
  if (jvmLikeLanguageIds.has(languageId)) {
    return inferJvmFamilyAdapter(document.fileName, workspaceFolderPath);
  }
  if (adapterByLanguageId.has(languageId)) {
    return adapterByLanguageId.get(languageId);
  }

  const extension = path.extname(document.fileName).toLowerCase();
  if (jvmLikeExtensions.has(extension)) {
    return inferJvmFamilyAdapter(document.fileName, workspaceFolderPath);
  }
  if (adapterByExtension.has(extension)) {
    return adapterByExtension.get(extension);
  }

  return undefined;
}

export function resolveLopperLanguage(
  configuredLanguage: LopperLanguage,
  document?: DocumentLike,
  workspaceFolderPath?: string,
): LopperLanguage {
  if (configuredLanguage !== "auto") {
    return configuredLanguage;
  }

  return inferLopperLanguageForDocument(document, workspaceFolderPath) ?? "auto";
}

export function shouldAutoRefreshForDocument(
  configuredLanguage: LopperLanguage,
  document: DocumentLike,
  workspaceFolderPath?: string,
): boolean {
  const inferred = inferLopperLanguageForDocument(document, workspaceFolderPath);
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

function inferJvmFamilyAdapter(fileName: string, workspaceFolderPath?: string): ConcreteLopperLanguage {
  return hasAndroidModuleSignals(fileName, workspaceFolderPath) ? "kotlin-android" : "jvm";
}

function hasAndroidModuleSignals(fileName: string, workspaceFolderPath?: string): boolean {
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
    if (moduleSignalsAndroid(currentDir)) {
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

function moduleSignalsAndroid(moduleRoot: string): boolean {
  if (fs.existsSync(path.join(moduleRoot, "src", "main", "AndroidManifest.xml"))) {
    return true;
  }

  for (const buildFileName of ["build.gradle", "build.gradle.kts"]) {
    const buildFilePath = path.join(moduleRoot, buildFileName);
    if (!fs.existsSync(buildFilePath)) {
      continue;
    }
    try {
      const buildFile = fs.readFileSync(buildFilePath, "utf8").toLowerCase();
      if (androidBuildPluginMarkers.some((marker) => buildFile.includes(marker))) {
        return true;
      }
    } catch {
      continue;
    }
  }

  return false;
}
