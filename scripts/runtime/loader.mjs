import fs from "node:fs";
import path from "node:path";

const outPath = process.env.LOPPER_RUNTIME_TRACE;

function append(event) {
  if (!outPath) return;
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.appendFileSync(outPath, `${JSON.stringify(event)}\n`, "utf8");
}

export async function resolve(specifier, context, nextResolve) {
  const resolved = await nextResolve(specifier, context);
  append({
    kind: "resolve",
    module: specifier,
    resolved: resolved.url || "",
    parent: context.parentURL || "",
  });
  return resolved;
}
