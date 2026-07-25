import fs from "node:fs";
import path from "node:path";

const outPath = process.env.LOPPER_RUNTIME_TRACE;
const repoRoot = normalizeRoot(process.env.LOPPER_RUNTIME_REPO_ROOT || process.cwd());

function append(event) {
  if (!outPath) return;
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.appendFileSync(outPath, `${JSON.stringify(event)}\n`, "utf8");
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
  if (!value || typeof value !== "string") return "";
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

export async function resolve(specifier, context, nextResolve) {
  const resolved = await nextResolve(specifier, context);
  append({
    kind: "resolve",
    module: specifier,
    resolved: resolved.url || "",
    parent: normalizeContext(context.parentURL || ""),
    entrypoint: context.parentURL ? "" : normalizeContext(resolved.url || ""),
  });
  return resolved;
}
