const fs = require("node:fs");
const path = require("node:path");
const Module = require("node:module");

const outPath = process.env.LOPPER_RUNTIME_TRACE;

function append(event) {
  if (!outPath) return;
  const payload = JSON.stringify(event) + "\n";
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.appendFileSync(outPath, payload, "utf8");
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
    parent: parent?.filename ?? "",
    isMain: Boolean(isMain),
  });
  return loaded;
};
