import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import helpers from "./context-helper.cjs";

const { normalizeContextValue } = helpers;

test("rejects windows drive paths before repo-relative joining on any host", () => {
  const repoRoot = path.resolve("/repo");
  assert.equal(normalizeContextValue(String.raw`C:\Users\alice\project\main.js`, repoRoot), "");
});

test("rejects UNC paths before repo-relative joining on any host", () => {
  const repoRoot = path.resolve("/repo");
  assert.equal(normalizeContextValue(String.raw`\\server\share\project\main.js`, repoRoot), "");
});

test("rejects non-file URL schemes before filesystem normalization", () => {
  const repoRoot = path.resolve("/repo");
  assert.equal(normalizeContextValue("https://example.test/main.js", repoRoot), "");
  assert.equal(normalizeContextValue("node:internal/modules/cjs/loader", repoRoot), "");
});

test("decodes percent-encoded file URLs with fileURLToPath", () => {
  const repoRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const srcDir = path.join(repoRoot, "src");
  fs.mkdirSync(srcDir, { recursive: true });
  const modulePath = path.join(srcDir, "hello world.js");
  fs.writeFileSync(modulePath, "export {};\n", "utf8");

  assert.equal(normalizeContextValue(pathToFileURL(modulePath).href, repoRoot), "src/hello world.js");

  fs.rmSync(repoRoot, { recursive: true, force: true });
});
