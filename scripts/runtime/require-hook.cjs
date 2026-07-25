const Module = require("node:module");
const { createRuntimeTraceHelpers } = require("./context-helper.cjs");

const { append, normalizeContext } = createRuntimeTraceHelpers(process.env);

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
