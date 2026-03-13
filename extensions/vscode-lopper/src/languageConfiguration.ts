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
  "php",
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

export const supportedDocumentSelectors: vscode.DocumentFilter[] = languageIds.map((language) => ({
  scheme: "file",
  language,
}));

export function configuredLopperLanguage(folder?: vscode.WorkspaceFolder): LopperLanguage {
  const configured = vscode.workspace.getConfiguration("lopper", folder?.uri).get<string>("language", "auto");
  return normalizeLopperLanguage(configured);
}

export function inferLopperLanguageForDocument(document?: DocumentLike): ConcreteLopperLanguage | undefined {
  if (!document || document.isUntitled) {
    return undefined;
  }

  const languageId = document.languageId.trim().toLowerCase();
  if (adapterByLanguageId.has(languageId)) {
    return adapterByLanguageId.get(languageId);
  }

  const extension = path.extname(document.fileName).toLowerCase();
  if (adapterByExtension.has(extension)) {
    return adapterByExtension.get(extension);
  }

  return undefined;
}

export function resolveLopperLanguage(configuredLanguage: LopperLanguage, document?: DocumentLike): LopperLanguage {
  if (configuredLanguage !== "auto") {
    return configuredLanguage;
  }

  return inferLopperLanguageForDocument(document) ?? "auto";
}

export function shouldAutoRefreshForDocument(configuredLanguage: LopperLanguage, document: DocumentLike): boolean {
  const inferred = inferLopperLanguageForDocument(document);
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
