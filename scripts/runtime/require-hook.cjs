const fs = require("node:fs");
const path = require("node:path");
const Module = require("node:module");

const outPath = process.env.LOPPER_RUNTIME_TRACE;
const repoRoot = normalizeRoot(process.env.LOPPER_RUNTIME_REPO_ROOT || process.cwd());

function append(event) {
  if (!outPath) return;
  const payload = JSON.stringify(event) + "\n";
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.appendFileSync(outPath, payload, "utf8");
}

function normalizeRoot(value) {
  if (!value) return "";
  try {
    return fs.realpathSync.native(value);
  } catch {
    return path.resolve(value);
  }
}

function normalizeContext(value) {
  if (!value) return "";
  if (typeof value !== "string") return "";
  if (value.startsWith("file://")) {
    try {
      value = new URL(value);
      value = value.pathname;
    } catch {
      value = value.slice("file://".length);
    }
  }
  if (!path.isAbsolute(value)) {
    return "";
  }
  let resolved;
  try {
    resolved = fs.realpathSync.native(value);
  } catch {
    try {
      resolved = path.resolve(value);
    } catch {
      return "";
    }
  }
  const rel = path.relative(repoRoot, resolved);
  if (!rel || rel === ".." || rel.startsWith(`..${path.sep}`)) {
    return "";
  }
  return rel.split(path.sep).join("/");
}

const originalLoad = Module._load;
Module._load = function patchedLoad(request, parent, isMain) {
  const loaded = originalLoad.apply(this, arguments);
  let resolved = "";
  try {
    resolved = Module._resolveFilename(request, parent);
  } catch {
    resolved = "";
  }
  append({
    kind: "require",
    module: request,
    resolved,
    parent: normalizeContext(parent?.filename ?? ""),
    entrypoint: isMain ? normalizeContext(resolved || "") : "",
    isMain: Boolean(isMain),
  });
  return loaded;
};
