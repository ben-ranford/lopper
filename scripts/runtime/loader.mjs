import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { createRuntimeTraceHelpers } = require("./context-helper.cjs");
const { append, normalizeContext } = createRuntimeTraceHelpers(process.env);

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
