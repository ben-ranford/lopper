const fs = require("node:fs");
const path = require("node:path");
const { fileURLToPath } = require("node:url");

const windowsDrivePathPattern = /^[A-Za-z]:[\\/]/;
const uncPathPattern = /^(?:[\\/]{2})[^\\/]+[\\/][^\\/]+/;
const schemePattern = /^[A-Za-z][A-Za-z\d+.-]*:/;

function createRuntimeTraceHelpers(env = process.env, options = {}) {
  const outPath = env.LOPPER_RUNTIME_TRACE;
  const realpath = typeof options.realpath === "function" ? options.realpath : fs.realpathSync.native;
  const repoRootValue =
    typeof options.repoRoot === "string" && options.repoRoot.trim()
      ? options.repoRoot
      : env.LOPPER_RUNTIME_REPO_ROOT || "";
  const repoRoot = normalizeRoot(repoRootValue, realpath);

  return {
    append(event) {
      if (!outPath) return;
      fs.mkdirSync(path.dirname(outPath), { recursive: true });
      fs.appendFileSync(outPath, `${JSON.stringify(event)}\n`, "utf8");
    },
    normalizeContext(value) {
      return normalizeContextValue(value, repoRoot, realpath);
    },
    normalizeModule(value, resolved) {
      return normalizeRuntimeModuleValue(value, resolved, repoRoot, realpath);
    },
    normalizeResolved(value) {
      return normalizeRuntimeResolvedValue(value, repoRoot, realpath);
    },
  };
}

function normalizeRoot(value, realpath = fs.realpathSync.native) {
  if (!value) return { original: "", resolved: "", trusted: [] };
  const original = normalizeLexicalAbsolutePath(value);
  if (!original) {
    return { original: "", resolved: "", trusted: [] };
  }
  const resolved = normalizeAbsolutePath(original, realpath) || original;
  const trusted = dedupeTrustedRoots([original, resolved]);
  return { original, resolved, trusted };
}

function dedupeTrustedRoots(roots) {
  const seen = new Set();
  const trusted = [];
  for (const value of roots) {
    const normalized = normalizeLexicalAbsolutePath(value);
    if (!normalized || seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    trusted.push(normalized);
  }
  return trusted;
}

function trustedRepoRoots(repoRoot) {
  if (!repoRoot) return [];
  if (Array.isArray(repoRoot.trusted)) {
    return repoRoot.trusted;
  }
  if (typeof repoRoot === "string") {
    return dedupeTrustedRoots([repoRoot]);
  }
  return [];
}

function normalizedRepoRoot(repoRoot, realpath = fs.realpathSync.native) {
  if (repoRoot && typeof repoRoot === "object" && Array.isArray(repoRoot.trusted)) {
    return repoRoot;
  }
  return normalizeRoot(repoRoot, realpath);
}

function repoRelativeUnderTrustedRoots(candidatePath, repoRoot, realpath = fs.realpathSync.native) {
  for (const trustedRoot of trustedRepoRoots(normalizedRepoRoot(repoRoot, realpath))) {
    const rel = path.relative(trustedRoot, candidatePath);
    if (isSafeRepoRelativePath(rel)) {
      return rel;
    }
  }
  return "";
}

function normalizeRepoRelativePath(value) {
  try {
    return value.split(path.sep).join("/");
  } catch {
    return "";
  }
}

function normalizeContextValue(value, repoRoot, realpath = fs.realpathSync.native) {
  repoRoot = normalizedRepoRoot(repoRoot, realpath);
  const lexicalPath = resolveContextPath(value);
  if (!lexicalPath || trustedRepoRoots(repoRoot).length === 0 || !isLexicallyConfinedToRepo(lexicalPath, repoRoot)) {
    return "";
  }
  const resolvedPath = normalizeAbsolutePath(lexicalPath, realpath);
  if (!resolvedPath) {
    return "";
  }
  const rel = repoRelativeUnderTrustedRoots(resolvedPath, repoRoot);
  if (!rel) {
    return "";
  }
  const normalized = normalizeRepoRelativePath(rel);
  return hasControlWhitespace(normalized) ? "" : normalized;
}

function normalizeRuntimeModuleValue(value, resolved, repoRoot, realpath = fs.realpathSync.native) {
  const normalizedResolvedPath = normalizeContextValue(resolved, repoRoot, realpath);
  const externalNodeModulesResolved = looksLikeNodeModulesResolvedContextValue(resolved, realpath);
  if (normalizedResolvedPath) {
    if (!looksLikeNodeModulesResolvedPath(normalizedResolvedPath)) {
      return "";
    }
  } else if (looksLikeFilesystemContextValue(resolved) && !externalNodeModulesResolved) {
    return "";
  } else if (resolved && looksLikeFilesystemContextValue(value) && !externalNodeModulesResolved) {
    return "";
  }
  const identifier = normalizeModuleIdentifier(value);
  if (identifier) {
    return identifier;
  }
  return "";
}

function normalizeRuntimeResolvedValue(value, repoRoot, realpath = fs.realpathSync.native) {
  const normalizedPath = normalizeContextValue(value, repoRoot, realpath);
  if (normalizedPath) {
    return sanitizeNodeModulesResolvedPath(normalizedPath);
  }
  return normalizeModuleIdentifier(value);
}

function normalizeModuleIdentifier(value) {
  if (!value || typeof value !== "string") return "";
  value = value.trim();
  if (!value || path.isAbsolute(value) || looksLikeWindowsAbsolutePath(value)) {
    return "";
  }
  if (value.startsWith(".") || value.includes("\\") || value.includes("://")) {
    return "";
  }
  if (hasNonFileScheme(value) && !value.startsWith("node:")) {
    return "";
  }

  const label = value.startsWith("node:") ? value.slice("node:".length) : value;
  const parts = label.split("/");
  if (!label || parts.some(isUnsafeModuleSegment)) {
    return "";
  }
  if (parts[0].startsWith("@") && parts.length < 2) {
    return "";
  }
  return value;
}

function isUnsafeModuleSegment(part) {
  if (!part || part === "." || part === ".." || /[\0-\x20\x7f%]/.test(part)) {
    return true;
  }
  return part.startsWith(".") || part.startsWith("~") || part.includes(":");
}

function sanitizeNodeModulesResolvedPath(value) {
  const parts = value.split("/");
  const firstNodeModulesIndex = parts.indexOf("node_modules");
  if (firstNodeModulesIndex === -1) {
    return value;
  }
  const suffixParts = parts.slice(firstNodeModulesIndex + 1);
  if (suffixParts.length === 0) {
    return "";
  }
  if (suffixParts.some(isUnsafeModuleSegment)) {
    return "";
  }
  if (suffixParts[0].startsWith("@") && suffixParts.length < 2) {
    return "";
  }
  return value;
}

function resolveContextPath(value) {
  if (!value || typeof value !== "string") return "";
  value = value.trim();
  if (!value) return "";
  if (looksLikeWindowsAbsolutePath(value)) {
    return path.sep === "\\" ? normalizeLexicalAbsolutePath(value) : "";
  }
  if (value.startsWith("file://")) {
    value = fileURLPath(value);
    if (!value) return "";
  } else if (hasNonFileScheme(value)) {
    return "";
  }
  if (!path.isAbsolute(value)) {
    return "";
  }
  return normalizeLexicalAbsolutePath(value);
}

function looksLikeContextReference(value) {
  if (!value || typeof value !== "string") return false;
  value = value.trim();
  if (!value) return false;
  if (value.startsWith("file://")) return true;
  if (path.isAbsolute(value) || looksLikeWindowsAbsolutePath(value)) return true;
  return value.startsWith(".") || hasNonFileScheme(value);
}

function looksLikeFilesystemContextValue(value) {
  if (looksLikeContextReference(value)) {
    return true;
  }
  if (!value || typeof value !== "string") return false;
  value = value.trim();
  if (!value?.includes("/")) {
    return false;
  }
  const parts = value.split("/");
  if (parts.some((part) => part === "." || part === "..")) {
    return true;
  }
  return path.posix.extname(parts[parts.length - 1]) !== "";
}

function looksLikeNodeModulesResolvedContextValue(value, realpath = fs.realpathSync.native) {
  const resolvedPath = resolveContextPath(value);
  if (!resolvedPath) {
    return false;
  }
  const absolutePath = normalizeAbsolutePath(resolvedPath, realpath);
  if (!absolutePath) {
    return false;
  }
  return sanitizeNodeModulesResolvedPath(nodeModulesSuffix(absolutePath)) !== "";
}

function normalizeLexicalAbsolutePath(value) {
  try {
    return path.resolve(value);
  } catch {
    return "";
  }
}

function normalizeAbsolutePath(value, realpath = fs.realpathSync.native) {
  try {
    const resolved = realpath(value);
    return typeof resolved === "string" ? normalizeLexicalAbsolutePath(resolved) : "";
  } catch {
    return "";
  }
}

function nodeModulesSuffix(value) {
  if (!value || typeof value !== "string") {
    return "";
  }
  const parts = value.split(path.sep);
  const firstNodeModulesIndex = parts.indexOf("node_modules");
  if (firstNodeModulesIndex === -1) {
    return "";
  }
  return parts.slice(firstNodeModulesIndex).join("/");
}

function fileURLPath(value) {
  try {
    return fileURLToPath(value);
  } catch {
    return "";
  }
}

function hasNonFileScheme(value) {
  return schemePattern.test(value);
}

function looksLikeWindowsAbsolutePath(value) {
  return windowsDrivePathPattern.test(value) || uncPathPattern.test(value);
}

function isSafeRepoRelativePath(value) {
  if (!value || relStartsOutsideRepo(value)) {
    return false;
  }
  return !path.isAbsolute(value) && !looksLikeWindowsAbsolutePath(value);
}

function looksLikeNodeModulesResolvedPath(value) {
  return value.split("/").includes("node_modules");
}

function isLexicallyConfinedToRepo(candidatePath, repoRoot) {
  return repoRelativeUnderTrustedRoots(candidatePath, repoRoot) !== "";
}

function relStartsOutsideRepo(value) {
  return value === ".." || value.startsWith(`..${path.sep}`);
}

function hasControlWhitespace(value) {
  return /[\n\r\t]/.test(value);
}

module.exports = {
  createRuntimeTraceHelpers,
  isSafeRepoRelativePath,
  normalizeContextValue,
  normalizeRuntimeModuleValue,
  normalizeRuntimeResolvedValue,
  resolveContextPath,
  sanitizeNodeModulesResolvedPath,
};
