import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import helpers from "./context-helper.cjs";
import { createDirectoryLinkSync, createFileLinkSync, removeDirectoryLinkSync, skipIfLinkUnsupported } from "./test-link-helpers.mjs";

const {
  createRuntimeTraceHelpers,
  isSafeRepoRelativePath,
  normalizeContextValue,
  normalizeRuntimeModuleValue,
  normalizeRuntimeResolvedValue,
  sanitizeNodeModulesResolvedPath,
} = helpers;

function trackRealpathCalls() {
  const calls = [];
  const realpath = (value) => {
    calls.push(value);
    return fs.realpathSync.native(value);
  };
  return { calls, realpath };
}

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

test("rejects hostile absolute and traversal inputs before realpath", (t) => {
  const repoRoot = { trusted: [path.resolve("/repo")] };
  const { calls, realpath } = trackRealpathCalls();

  for (const value of ["/private.js", "file:///private.js", "/repo/../private.js"]) {
    assert.equal(normalizeContextValue(value, repoRoot, realpath), "");
  }
  assert.deepEqual(calls, []);
});

test("realpaths trusted repo-local absolute and file URL inputs after lexical confinement", (t) => {
  const repoRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const srcDir = path.join(repoRoot, "src");
  const modulePath = path.join(srcDir, "main.js");
  fs.mkdirSync(srcDir, { recursive: true });
  fs.writeFileSync(modulePath, "export {};\n", "utf8");

  const { calls, realpath } = trackRealpathCalls();
  assert.equal(normalizeContextValue(modulePath, repoRoot, realpath), "src/main.js");
  assert.equal(normalizeContextValue(pathToFileURL(modulePath).href, repoRoot, realpath), "src/main.js");
  assert.ok(calls.length >= 2);

  fs.rmSync(repoRoot, { recursive: true, force: true });
});

test("fails closed when a trusted candidate cannot be realpathed", () => {
  const repoRoot = { trusted: [path.resolve("/repo")] };
  const realpath = () => {
    throw new Error("candidate changed before realpath");
  };

  assert.equal(normalizeContextValue(path.resolve("/repo/main.js"), repoRoot, realpath), "");
});

test("createRuntimeTraceHelpers accepts injected realpath without global monkeypatching", () => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const modulePath = path.join(repoRoot, "src", "main.js");
  fs.mkdirSync(path.dirname(modulePath), { recursive: true });
  fs.writeFileSync(modulePath, "export {};\n", "utf8");

  const { calls, realpath } = trackRealpathCalls();
  const runtimeHelpers = createRuntimeTraceHelpers(
    { LOPPER_RUNTIME_REPO_ROOT: repoRoot, LOPPER_RUNTIME_TRACE: "" },
    { realpath },
  );

  assert.equal(runtimeHelpers.normalizeContext(modulePath), "src/main.js");
  assert.equal(runtimeHelpers.normalizeResolved(pathToFileURL(modulePath).href), "src/main.js");
  assert.ok(calls.length >= 2);

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("manual helpers fail closed without an explicit repo root", () => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const nestedRoot = path.join(repoRoot, "packages", "nested");
  const modulePath = path.join(nestedRoot, "src", "main.js");
  fs.mkdirSync(path.dirname(modulePath), { recursive: true });
  fs.writeFileSync(modulePath, "export {};\n", "utf8");

  const previousCwd = process.cwd();
  process.chdir(nestedRoot);
  try {
    const runtimeHelpers = createRuntimeTraceHelpers({ LOPPER_RUNTIME_TRACE: "" });
    assert.equal(runtimeHelpers.normalizeContext(modulePath), "");
    assert.equal(runtimeHelpers.normalizeResolved(pathToFileURL(modulePath).href), "");
  } finally {
    process.chdir(previousCwd);
    fs.rmSync(testRoot, { recursive: true, force: true });
  }
});

test("preserves repo attribution for symlinked repo roots and redacts symlink escapes", (t) => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const realRepoRoot = path.join(testRoot, "repo-real");
  const aliasRepoRoot = path.join(testRoot, "repo-alias");
  const safeModulePath = path.join(realRepoRoot, "src", "main.js");
  const escapedTargetPath = path.join(testRoot, "private.js");
  const escapedLinkPath = path.join(realRepoRoot, "src", "escaped.js");
  const retargetedRepoRoot = path.join(testRoot, "repo-private");
  const retargetedModulePath = path.join(retargetedRepoRoot, "src", "private.js");
  fs.mkdirSync(path.dirname(safeModulePath), { recursive: true });
  fs.mkdirSync(path.dirname(retargetedModulePath), { recursive: true });
  fs.writeFileSync(safeModulePath, "export {};\n", "utf8");
  fs.writeFileSync(escapedTargetPath, "export {};\n", "utf8");
  fs.writeFileSync(retargetedModulePath, "export {};\n", "utf8");
  try {
    createDirectoryLinkSync(realRepoRoot, aliasRepoRoot);
    createFileLinkSync(escapedTargetPath, escapedLinkPath);
  } catch (error) {
    if (skipIfLinkUnsupported(t, error)) {
      fs.rmSync(testRoot, { recursive: true, force: true });
      return;
    }
    throw error;
  }

  const { calls, realpath } = trackRealpathCalls();
  const runtimeHelpers = createRuntimeTraceHelpers(
    { LOPPER_RUNTIME_REPO_ROOT: aliasRepoRoot, LOPPER_RUNTIME_TRACE: "" },
    { realpath },
  );

  assert.equal(runtimeHelpers.normalizeContext(safeModulePath), "src/main.js");
  assert.equal(runtimeHelpers.normalizeContext(path.join(aliasRepoRoot, "src", "main.js")), "src/main.js");
  assert.equal(runtimeHelpers.normalizeResolved(pathToFileURL(safeModulePath).href), "src/main.js");
  assert.equal(runtimeHelpers.normalizeContext(path.join(aliasRepoRoot, "src", "escaped.js")), "");
  assert.equal(runtimeHelpers.normalizeResolved(pathToFileURL(escapedLinkPath).href), "");

  removeDirectoryLinkSync(aliasRepoRoot);
  createDirectoryLinkSync(retargetedRepoRoot, aliasRepoRoot);
  assert.equal(runtimeHelpers.normalizeContext(path.join(aliasRepoRoot, "src", "private.js")), "");
  assert.ok(calls.length >= 5);

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("redacts escaped repo-relative runtime module requests without demoting package subpaths", (t) => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const repoSrcDir = path.join(repoRoot, "src");
  const escapedTargetPath = path.join(testRoot, "private.js");
  const escapedLinkPath = path.join(repoSrcDir, "escaped.js");
  const packageResolvedPath = path.join(repoRoot, "node_modules", "fixture-dep", "lib", "index.js");
  fs.mkdirSync(repoSrcDir, { recursive: true });
  fs.mkdirSync(path.dirname(packageResolvedPath), { recursive: true });
  fs.writeFileSync(escapedTargetPath, "export {};\n", "utf8");
  fs.writeFileSync(packageResolvedPath, "export {};\n", "utf8");
  try {
    createFileLinkSync(escapedTargetPath, escapedLinkPath);
  } catch (error) {
    if (skipIfLinkUnsupported(t, error)) {
      fs.rmSync(testRoot, { recursive: true, force: true });
      return;
    }
    throw error;
  }

  const { realpath } = trackRealpathCalls();
  assert.equal(normalizeRuntimeModuleValue("src/escaped.js", escapedLinkPath, repoRoot, realpath), "");
  assert.equal(
    normalizeRuntimeModuleValue("fixture-dep/lib/index.js", packageResolvedPath, repoRoot, realpath),
    "fixture-dep/lib/index.js",
  );

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("sanitizes runtime module and resolved artifact values", () => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const repoSrcDir = path.join(repoRoot, "src");
  const repoEntryPath = path.join(repoSrcDir, "main.mjs");
  const repoDepPath = path.join(repoRoot, "node_modules", "lodash", "map.js");
  const nestedRepoDepPath = path.join(repoRoot, "packages", "web", "node_modules", "lodash", "map.js");
  const nestedValidDepPath = path.join(
    repoRoot,
    "node_modules",
    "fixture-dep",
    "lib",
    "node_modules",
    "child",
    "index.mjs",
  );
  const nestedHybridDepPath = path.join(
    repoRoot,
    "packages",
    "web",
    "node_modules",
    "fixture-dep",
    "C:",
    "Users",
    "alice",
    "private.mjs",
  );
  const nestedHiddenDepPath = path.join(
    repoRoot,
    "node_modules",
    "fixture-dep",
    ".env",
    "node_modules",
    "child",
    "index.mjs",
  );
  const nestedTildeDepPath = path.join(
    repoRoot,
    "node_modules",
    "fixture-dep",
    "~",
    ".ssh",
    "node_modules",
    "child",
    "index.mjs",
  );
  const nestedDriveLikeDepPath = path.join(
    repoRoot,
    "node_modules",
    "fixture-dep",
    "C:",
    "Users",
    "alice",
    "node_modules",
    "child",
    "index.mjs",
  );
  const hiddenRepoDepPath = path.join(repoRoot, "node_modules", "fixture-dep", ".env", "private.mjs");
  const tildeRepoDepPath = path.join(repoRoot, "node_modules", "fixture-dep", "~", ".ssh", "id_rsa.mjs");
  const foreignPath = path.join(testRoot, "foreign.js");
  fs.mkdirSync(path.dirname(repoDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(nestedRepoDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(nestedValidDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(nestedHybridDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(nestedHiddenDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(nestedTildeDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(nestedDriveLikeDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(hiddenRepoDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(tildeRepoDepPath), { recursive: true });
  fs.mkdirSync(repoSrcDir, { recursive: true });
  fs.writeFileSync(repoEntryPath, "export {};\n", "utf8");
  fs.writeFileSync(repoDepPath, "export default function map() {}\n", "utf8");
  fs.writeFileSync(nestedRepoDepPath, "export default function nestedMap() {}\n", "utf8");
  fs.writeFileSync(nestedValidDepPath, "export default 11;\n", "utf8");
  fs.writeFileSync(nestedHybridDepPath, "export default 7;\n", "utf8");
  fs.writeFileSync(nestedHiddenDepPath, "export default 12;\n", "utf8");
  fs.writeFileSync(nestedTildeDepPath, "export default 13;\n", "utf8");
  fs.writeFileSync(nestedDriveLikeDepPath, "export default 14;\n", "utf8");
  fs.writeFileSync(hiddenRepoDepPath, "export default 2;\n", "utf8");
  fs.writeFileSync(tildeRepoDepPath, "export default 3;\n", "utf8");
  fs.writeFileSync(foreignPath, "export {};\n", "utf8");
  const { realpath } = trackRealpathCalls();

  assert.equal(
    normalizeRuntimeModuleValue("lodash/map", repoDepPath, repoRoot, realpath),
    "lodash/map",
  );
  assert.equal(
    normalizeRuntimeModuleValue(
      pathToFileURL(repoEntryPath).href,
      pathToFileURL(repoEntryPath).href,
      repoRoot,
      realpath,
    ),
    "",
  );
  assert.equal(
    normalizeRuntimeResolvedValue(pathToFileURL(repoDepPath).href, repoRoot, realpath),
    "node_modules/lodash/map.js",
  );
  assert.equal(
    normalizeRuntimeResolvedValue(nestedRepoDepPath, repoRoot, realpath),
    "packages/web/node_modules/lodash/map.js",
  );
  assert.equal(
    normalizeRuntimeResolvedValue(nestedValidDepPath, repoRoot, realpath),
    "node_modules/fixture-dep/lib/node_modules/child/index.mjs",
  );
  assert.equal(normalizeRuntimeResolvedValue(pathToFileURL(hiddenRepoDepPath).href, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeResolvedValue(pathToFileURL(tildeRepoDepPath).href, repoRoot, realpath), "");
  assert.equal(
    normalizeRuntimeResolvedValue(
      nestedHybridDepPath,
      repoRoot,
      realpath,
    ),
    "",
  );
  assert.equal(normalizeRuntimeResolvedValue(nestedHiddenDepPath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeResolvedValue(nestedTildeDepPath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeResolvedValue(nestedDriveLikeDepPath, repoRoot, realpath), "");
  for (const value of ["lodash/fp.js", "@scope/pkg/index.js", "node:fs", "node:fs/promises"]) {
    assert.equal(normalizeRuntimeModuleValue(value, "", repoRoot, realpath), value);
    assert.equal(normalizeRuntimeResolvedValue(value, repoRoot, realpath), value);
  }
  for (const value of [
    pathToFileURL(repoEntryPath).href,
    "./src/main.mjs",
    "../main.mjs",
  ]) {
    assert.equal(normalizeRuntimeModuleValue(value, value, repoRoot, realpath), "");
  }
  for (const value of [
    foreignPath,
    pathToFileURL(foreignPath).href,
    String.raw`C:\Users\alice\private.js`,
    "C:Users/alice/private.js",
    String.raw`\\server\share\private.js`,
    "x:private-token",
    "../private.js",
    "fixture-dep/C:/Users/alice/private.cjs",
    "pkg/https:/secret",
    "pkg/~/.ssh/id_rsa",
    "pkg/./secret",
    "pkg/../secret",
  ]) {
    assert.equal(normalizeRuntimeModuleValue(value, value, repoRoot, realpath), "");
    assert.equal(normalizeRuntimeResolvedValue(value, repoRoot, realpath), "");
  }

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("sanitizes nested node_modules suffixes from the first marker onward", () => {
  const validNestedPath = "node_modules/pkg/lib/node_modules/child/index.js";
  assert.equal(sanitizeNodeModulesResolvedPath(validNestedPath), validNestedPath);

  for (const value of [
    "node_modules/pkg/.env/node_modules/child/index.js",
    "node_modules/pkg/~/.ssh/node_modules/child/index.js",
    "node_modules/pkg/C:/Users/alice/node_modules/child/index.js",
    "node_modules/pkg/../node_modules/child/index.js",
    `node_modules/pkg/\u0001private/node_modules/child/index.js`,
  ]) {
    assert.equal(sanitizeNodeModulesResolvedPath(value), "");
  }
});
