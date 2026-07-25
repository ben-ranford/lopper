import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { createRuntimeTraceHelpers } = require("./context-helper.cjs");
const { append, normalizeContext, normalizeModule, normalizeResolved } =
  createRuntimeTraceHelpers(process.env);

export async function resolve(specifier, context, nextResolve) {
  const resolved = await nextResolve(specifier, context);
  const rawResolved = resolved.url || "";
  append({
    kind: "resolve",
    module: normalizeModule(specifier, rawResolved),
    resolved: normalizeResolved(rawResolved),
    parent: normalizeContext(context.parentURL || ""),
    entrypoint: context.parentURL ? "" : normalizeContext(rawResolved),
  });
  return resolved;
}
