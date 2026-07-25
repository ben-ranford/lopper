import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import helpers from "./context-helper.cjs";

const {
  isSafeRepoRelativePath,
  normalizeContextValue,
  normalizeRuntimeModuleValue,
  normalizeRuntimeResolvedValue,
} = helpers;

test("rejects windows drive paths before repo-relative joining on any host", () => {
  const repoRoot = path.resolve("/repo");
  assert.equal(normalizeContextValue(String.raw`C:\Users\alice\project\main.js`, repoRoot), "");
});

test("rejects UNC paths before repo-relative joining on any host", () => {
  const repoRoot = path.resolve("/repo");
  assert.equal(normalizeContextValue(String.raw`\\server\share\project\main.js`, repoRoot), "");
  assert.equal(normalizeContextValue("//server/share/project/main.js", repoRoot), "");
  assert.equal(normalizeContextValue(String.raw`\/server/share/project/main.js`, repoRoot), "");
  assert.equal(normalizeContextValue(String.raw`/\server/share/project/main.js`, repoRoot), "");
});

test("rejects cross-volume absolute results returned from path.relative", () => {
  assert.equal(isSafeRepoRelativePath(String.raw`C:\other\project\main.js`), false);
  assert.equal(isSafeRepoRelativePath(String.raw`\\server\share\project\main.js`), false);
});

test("rejects non-file URL schemes before filesystem normalization", () => {
  const repoRoot = path.resolve("/repo");
  assert.equal(normalizeContextValue("x:private-token", repoRoot), "");
  assert.equal(normalizeContextValue("a:foo/bar.js", repoRoot), "");
  assert.equal(normalizeContextValue("C:Users/alice/private.js", repoRoot), "");
  assert.equal(normalizeContextValue("https://example.test/main.js", repoRoot), "");
  assert.equal(normalizeContextValue("data:text/plain,secret", repoRoot), "");
  assert.equal(normalizeContextValue("mailto:test@example.com", repoRoot), "");
  assert.equal(normalizeContextValue("https:foo", repoRoot), "");
  assert.equal(normalizeContextValue("https:/foo", repoRoot), "");
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

test("sanitizes runtime module and resolved artifact values", () => {
  const repoRoot = path.resolve("/repo");

  assert.equal(
    normalizeRuntimeModuleValue("lodash/map", "/repo/node_modules/lodash/map.js", repoRoot),
    "lodash/map",
  );
  assert.equal(
    normalizeRuntimeModuleValue("file:///repo/src/main.mjs", "file:///repo/src/main.mjs", repoRoot),
    "src/main.mjs",
  );
  assert.equal(
    normalizeRuntimeResolvedValue("file:///repo/node_modules/lodash/map.js", repoRoot),
    "node_modules/lodash/map.js",
  );
  for (const value of [
    "/private/tmp/foreign.js",
    "file:///private/tmp/foreign.js",
    String.raw`C:\Users\alice\private.js`,
    "C:Users/alice/private.js",
    String.raw`\\server\share\private.js`,
    "x:private-token",
    "../private.js",
  ]) {
    assert.equal(normalizeRuntimeModuleValue(value, value, repoRoot), "");
    assert.equal(normalizeRuntimeResolvedValue(value, repoRoot), "");
  }
});
