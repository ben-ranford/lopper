const fs = require("node:fs");
const path = require("node:path");
const { fileURLToPath } = require("node:url");

const windowsDrivePathPattern = /^[A-Za-z]:[\\/]/;
const uncPathPattern = /^(?:\\\\|\/\/)[^\\/]+[\\/][^\\/]+/;
const schemePattern = /^[A-Za-z][A-Za-z\d+.-]*:/;

function createRuntimeTraceHelpers(env = process.env) {
  const outPath = env.LOPPER_RUNTIME_TRACE;
  const repoRoot = normalizeRoot(env.LOPPER_RUNTIME_REPO_ROOT || process.cwd());

  return {
    append(event) {
      if (!outPath) return;
      fs.mkdirSync(path.dirname(outPath), { recursive: true });
      fs.appendFileSync(outPath, `${JSON.stringify(event)}\n`, "utf8");
    },
    normalizeContext(value) {
      return normalizeContextValue(value, repoRoot);
    },
  };
}

function normalizeRoot(value) {
  if (!value) return "";
  try {
    return fs.realpathSync.native(value);
  } catch {
    return path.resolve(value);
  }
}

function normalizeContextValue(value, repoRoot) {
  const resolvedPath = resolveContextPath(value);
  if (!resolvedPath || !repoRoot) {
    return "";
  }
  const rel = path.relative(repoRoot, resolvedPath);
  if (!rel || rel === ".." || rel.startsWith(`..${path.sep}`)) {
    return "";
  }
  return rel.split(path.sep).join("/");
}

function resolveContextPath(value) {
  if (!value || typeof value !== "string") return "";
  value = value.trim();
  if (!value) return "";
  if (looksLikeWindowsAbsolutePath(value)) {
    return path.sep === "\\" ? normalizeAbsolutePath(value) : "";
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
  return normalizeAbsolutePath(value);
}

function normalizeAbsolutePath(value) {
  try {
    return fs.realpathSync.native(value);
  } catch {
    try {
      return path.resolve(value);
    } catch {
      return "";
    }
  }
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

module.exports = {
  createRuntimeTraceHelpers,
  normalizeContextValue,
  resolveContextPath,
};
