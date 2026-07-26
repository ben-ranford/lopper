import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import helpers from "./context-helper.cjs";
import { createDirectoryLinkSync, createFileLinkSync, removeDirectoryLinkSync, skipIfLinkUnsupported } from "./test-link-helpers.mjs";

const {
  createRuntimeTraceHelpers,
  findValidatedLinkedPackageRoot,
  isSafeRepoRelativePath,
  normalizeContextValue,
  normalizeRuntimeModuleValue,
  normalizeRuntimeResolvedValue,
  preservesValidatedLinkedPackageSubpath,
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

test("redacts control whitespace from repo context helpers and runtime hook events", (t) => {
  if (process.platform === "win32") {
    t.skip("Windows does not support control whitespace in filenames");
    return;
  }

  const testRoot = fs.mkdtempSync(path.join(os.tmpdir(), "lopper-runtime-controls-"));
  t.after(() => fs.rmSync(testRoot, { recursive: true, force: true }));
  const repoRoot = path.join(testRoot, "repo");
  const packageRoot = path.join(repoRoot, "node_modules", "fixture-dep");
  const dependencyPath = path.join(packageRoot, "index.cjs");
  const hookPath = fileURLToPath(new URL("./require-hook.cjs", import.meta.url));
  fs.mkdirSync(packageRoot, { recursive: true });
  fs.writeFileSync(
    path.join(packageRoot, "package.json"),
    JSON.stringify({ name: "fixture-dep", version: "1.0.0", main: "index.cjs" }),
    "utf8",
  );
  fs.writeFileSync(dependencyPath, "module.exports = 1;\n", "utf8");

  const safePath = path.join(repoRoot, "hello world-\u4e16\u754c.cjs");
  fs.writeFileSync(safePath, "module.exports = 1;\n", "utf8");
  assert.equal(normalizeContextValue(safePath, repoRoot), "hello world-\u4e16\u754c.cjs");
  assert.equal(normalizeRuntimeModuleValue("fixture-dep", dependencyPath, repoRoot), "fixture-dep");

  const cases = [
    { name: "newline", character: "\n" },
    { name: "carriage return", character: "\r" },
    { name: "tab", character: "\t" },
  ];
  for (const { name, character } of cases) {
    const filename = `main${character}context.cjs`;
    const entrypoint = path.join(repoRoot, filename);
    const tracePath = path.join(testRoot, `${name.replaceAll(" ", "-")}.ndjson`);
    fs.writeFileSync(entrypoint, 'require("fixture-dep");\n', "utf8");

    assert.equal(normalizeContextValue(entrypoint, repoRoot), "", `${name} helper result`);
    execFileSync(process.execPath, [`--require=${hookPath}`, entrypoint], {
      cwd: repoRoot,
      env: {
        ...process.env,
        LOPPER_RUNTIME_REPO_ROOT: repoRoot,
        LOPPER_RUNTIME_TRACE: tracePath,
      },
      stdio: "pipe",
    });

    const artifact = fs.readFileSync(tracePath, "utf8");
    const escapedFilename = JSON.stringify(filename).slice(1, -1);
    assert.equal(artifact.includes(escapedFilename), false, `${name} filename entered runtime NDJSON`);
    const events = artifact
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
    assert.ok(
      events.some((event) => event.module === "fixture-dep" && event.parent === ""),
      `${name} dependency event must redact its parent`,
    );
    assert.ok(
      events.some((event) => event.isMain && event.entrypoint === ""),
      `${name} main event must redact its entrypoint`,
    );
    for (const event of events) {
      for (const value of Object.values(event)) {
        if (typeof value !== "string") continue;
        assert.equal(/[\n\r\t]/.test(value), false, `${name} event field contains control whitespace`);
      }
    }
  }
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

test("preserves validated package specifiers when hoisted node_modules resolutions are redacted", () => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const hoistedRoot = path.join(testRoot, "hoisted", "node_modules");
  const hoistedDepPath = path.join(hoistedRoot, "fixture-dep", "index.mjs");
  const hoistedDepSubpath = path.join(hoistedRoot, "@scope", "pkg", "index.js");
  const localEscapePath = path.join(testRoot, "private.mjs");
  const hiddenDepPath = path.join(hoistedRoot, "fixture-dep", ".env", "private.mjs");
  fs.mkdirSync(path.dirname(hoistedDepPath), { recursive: true });
  fs.mkdirSync(path.dirname(hoistedDepSubpath), { recursive: true });
  fs.mkdirSync(path.dirname(hiddenDepPath), { recursive: true });
  fs.writeFileSync(hoistedDepPath, "export default 1;\n", "utf8");
  fs.writeFileSync(hoistedDepSubpath, "export default 2;\n", "utf8");
  fs.writeFileSync(localEscapePath, "export default 3;\n", "utf8");
  fs.writeFileSync(hiddenDepPath, "export default 4;\n", "utf8");

  const { realpath } = trackRealpathCalls();
  assert.equal(normalizeRuntimeModuleValue("fixture-dep", hoistedDepPath, repoRoot, realpath), "fixture-dep");
  assert.equal(
    normalizeRuntimeModuleValue("@scope/pkg/index.js", hoistedDepSubpath, repoRoot, realpath),
    "@scope/pkg/index.js",
  );
  assert.equal(normalizeRuntimeModuleValue("src/escaped.mjs", localEscapePath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeModuleValue("fixture-dep/.env/private.mjs", hiddenDepPath, repoRoot, realpath), "");

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("preserves bare, scoped, and validated linked package subpath requests while redacting external realpaths", (t) => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const linkedRoot = path.join(testRoot, "linked-packages");
  const barePackagePath = path.join(linkedRoot, "fixture-dep", "index.js");
  const barePackageSubpath = path.join(linkedRoot, "fixture-dep", "lib", "index.js");
  const scopedPackagePath = path.join(linkedRoot, "@scope", "pkg", "index.js");
  const scopedPackageSubpath = path.join(linkedRoot, "@scope", "pkg", "client", "index.js");
  fs.mkdirSync(path.dirname(barePackagePath), { recursive: true });
  fs.mkdirSync(path.dirname(barePackageSubpath), { recursive: true });
  fs.mkdirSync(path.dirname(scopedPackagePath), { recursive: true });
  fs.mkdirSync(path.dirname(scopedPackageSubpath), { recursive: true });
  fs.writeFileSync(path.join(linkedRoot, "fixture-dep", "package.json"), JSON.stringify({ name: "fixture-dep" }), "utf8");
  fs.writeFileSync(path.join(linkedRoot, "@scope", "pkg", "package.json"), JSON.stringify({ name: "@scope/pkg" }), "utf8");
  fs.writeFileSync(barePackagePath, "module.exports = 1;\n", "utf8");
  fs.writeFileSync(barePackageSubpath, "module.exports = 2;\n", "utf8");
  fs.writeFileSync(scopedPackagePath, "module.exports = 3;\n", "utf8");
  fs.writeFileSync(scopedPackageSubpath, "module.exports = 4;\n", "utf8");

  const { realpath } = trackRealpathCalls();
  assert.equal(normalizeRuntimeModuleValue("fixture-dep", barePackagePath, repoRoot, realpath), "fixture-dep");
  assert.equal(normalizeRuntimeModuleValue("fixture-dep/lib", barePackageSubpath, repoRoot, realpath), "fixture-dep/lib");
  assert.equal(normalizeRuntimeModuleValue("@scope/pkg", scopedPackagePath, repoRoot, realpath), "@scope/pkg");
  assert.equal(
    normalizeRuntimeModuleValue("@scope/pkg/client", scopedPackageSubpath, repoRoot, realpath),
    "@scope/pkg/client",
  );
  assert.equal(normalizeRuntimeResolvedValue(barePackagePath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeResolvedValue(scopedPackagePath, repoRoot, realpath), "");

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("rejects unsafe or phantom linked package requests when the realpath is redacted", () => {
  const testRoot = fs.mkdtempSync(path.join(process.cwd(), ".runtime-context-"));
  const repoRoot = path.join(testRoot, "repo");
  const linkedRoot = path.join(testRoot, "linked-packages");
  const barePackagePath = path.join(linkedRoot, "fixture-dep", "index.js");
  const barePackageSubpath = path.join(linkedRoot, "fixture-dep", "lib", "index.js");
  const scopedPackagePath = path.join(linkedRoot, "@scope", "pkg", "index.js");
  const phantomPackagePath = path.join(linkedRoot, "src", "client", "index.js");
  fs.mkdirSync(path.dirname(barePackagePath), { recursive: true });
  fs.mkdirSync(path.dirname(barePackageSubpath), { recursive: true });
  fs.mkdirSync(path.dirname(scopedPackagePath), { recursive: true });
  fs.mkdirSync(path.dirname(phantomPackagePath), { recursive: true });
  fs.writeFileSync(path.join(linkedRoot, "fixture-dep", "package.json"), JSON.stringify({ name: "fixture-dep" }), "utf8");
  fs.writeFileSync(path.join(linkedRoot, "@scope", "pkg", "package.json"), JSON.stringify({ name: "@scope/pkg" }), "utf8");
  fs.writeFileSync(barePackagePath, "module.exports = 1;\n", "utf8");
  fs.writeFileSync(barePackageSubpath, "module.exports = 2;\n", "utf8");
  fs.writeFileSync(scopedPackagePath, "module.exports = 3;\n", "utf8");
  fs.writeFileSync(phantomPackagePath, "module.exports = 4;\n", "utf8");

  const { realpath } = trackRealpathCalls();
  assert.equal(normalizeRuntimeModuleValue("fixture-dep/lib/index.js", barePackagePath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeModuleValue("@scope/pkg/client", scopedPackagePath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeModuleValue("fixture-dep/\nlib", barePackageSubpath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeModuleValue("fixture-dep/.env", barePackageSubpath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeModuleValue("@scope/pkg/../client", scopedPackagePath, repoRoot, realpath), "");
  assert.equal(normalizeRuntimeModuleValue("src/client", phantomPackagePath, repoRoot, realpath), "");

  fs.rmSync(testRoot, { recursive: true, force: true });
});

test("preserves linked package subpaths for drive-letter realpaths without duplicating the Windows root", () => {
  const manifests = new Map([
    [String.raw`C:\linked\fixture-dep\package.json`, JSON.stringify({ name: "fixture-dep" })],
    [String.raw`C:\linked\@scope\pkg\package.json`, JSON.stringify({ name: "@scope/pkg" })],
  ]);
  const windowsFs = {
    existsSync(value) {
      return manifests.has(value);
    },
    readFileSync(value) {
      const manifest = manifests.get(value);
      if (manifest === undefined) {
        throw new Error(`missing manifest: ${value}`);
      }
      return manifest;
    },
  };
  const identityRealpath = (value) => value;

  assert.equal(
    findValidatedLinkedPackageRoot(
      String.raw`C:\linked\fixture-dep\lib\index.js`,
      { packageName: "fixture-dep", packageRootParts: ["fixture-dep"], subpathParts: ["lib"] },
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ),
    String.raw`C:\linked\fixture-dep`,
  );
  assert.equal(
    preservesValidatedLinkedPackageSubpath(
      "fixture-dep/lib",
      "fixture-dep/lib",
      String.raw`C:\linked\fixture-dep\lib\index.js`,
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ),
    true,
  );
  assert.equal(
    preservesValidatedLinkedPackageSubpath(
      "@scope/pkg/client",
      "@scope/pkg/client",
      String.raw`C:\linked\@scope\pkg\client\index.js`,
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ),
    true,
  );
});

test("redacts malformed or escaping drive-letter linked package subpaths", () => {
  const manifests = new Map([
    [String.raw`C:\linked\fixture-dep\package.json`, JSON.stringify({ name: "fixture-dep" })],
    [String.raw`C:\linked\@scope\pkg\package.json`, JSON.stringify({ name: "@scope/pkg" })],
  ]);
  const windowsFs = {
    existsSync(value) {
      return manifests.has(value);
    },
    readFileSync(value) {
      const manifest = manifests.get(value);
      if (manifest === undefined) {
        throw new Error(`missing manifest: ${value}`);
      }
      return manifest;
    },
  };
  const identityRealpath = (value) => value;

  assert.equal(
    findValidatedLinkedPackageRoot(
      String.raw`C:\linked\fixture-dep\lib\index.js`,
      { packageName: "fixture-dep", packageRootParts: ["fixture-dep"], subpathParts: ["lib"] },
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ).includes("C:\\C:\\"),
    false,
  );
  assert.equal(
    preservesValidatedLinkedPackageSubpath(
      "fixture-dep/lib",
      "fixture-dep/lib",
      String.raw`C:\linked\fixture-dep\..\private\lib\index.js`,
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ),
    false,
  );
  assert.equal(
    preservesValidatedLinkedPackageSubpath(
      "@scope/pkg/client",
      "@scope/pkg/client",
      String.raw`C:\linked\@scope\pkg\..\private\client\index.js`,
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ),
    false,
  );
  assert.equal(
    preservesValidatedLinkedPackageSubpath(
      "fixture-dep/lib",
      "fixture-dep/lib",
      String.raw`C:\linked\fixture-dep\C:\private\index.js`,
      identityRealpath,
      { fs: windowsFs, pathImpl: path.win32 },
    ),
    false,
  );
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
