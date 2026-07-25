const Module = require("node:module");
const { createRuntimeTraceHelpers } = require("./context-helper.cjs");

const { append, normalizeContext, normalizeModule, normalizeResolved } =
  createRuntimeTraceHelpers(process.env);

const originalLoad = Module._load;
Module._load = function patchedLoad(request, parent, isMain) {
  const loaded = originalLoad.apply(this, arguments);
  let rawResolved = "";
  try {
    rawResolved = Module._resolveFilename(request, parent);
  } catch {
    rawResolved = "";
  }
  append({
    kind: "require",
    module: normalizeModule(request, rawResolved),
    resolved: normalizeResolved(rawResolved),
    parent: normalizeContext(parent?.filename ?? ""),
    entrypoint: isMain ? normalizeContext(rawResolved) : "",
    isMain: Boolean(isMain),
  });
  return loaded;
};
