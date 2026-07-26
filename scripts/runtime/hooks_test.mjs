import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { createDirectoryLinkSync, createFileLinkSync, skipIfLinkUnsupported } from "./test-link-helpers.mjs";

const hookDir = path.dirname(fileURLToPath(import.meta.url));

test("CommonJS hook persists only sanitized artifact values", (t) => {
  const fixture = createNodeFixture(t, "cjs");
  const events = captureNodeHookEvents(fixture, "--require", "require-hook.cjs");
  assertSanitizedRuntimeArtifact(events, fixture, {
    expectedDependencyEvent: {
      kind: "require",
      module: "fixture-dep",
      resolved: "node_modules/fixture-dep/index.cjs",
      parent: "main.cjs",
      entrypoint: "",
      isMain: false,
    },
    expectedNestedDependencyEvent: {
      kind: "require",
      module: "fixture-dep/safe/node_modules/child/index.cjs",
      resolved: "node_modules/fixture-dep/safe/node_modules/child/index.cjs",
      parent: "main.cjs",
      entrypoint: "",
      isMain: false,
    },
    expectedMainParent: "main.cjs",
    disallowedResolvedValues: [
      "node_modules/fixture-dep/C:/Users/alice/private.cjs",
      "node_modules/fixture-dep/https:/secret.cjs",
      "node_modules/fixture-dep/.env/private.cjs",
      "node_modules/fixture-dep/~/.ssh/id_rsa.cjs",
      "node_modules/fixture-dep/C:/Users/alice/node_modules/child/index.cjs",
      "node_modules/fixture-dep/.env/node_modules/child/index.cjs",
      "node_modules/fixture-dep/~/.ssh/node_modules/child/index.cjs",
    ],
    disallowedModuleValues: [
      "main.cjs",
      "local.cjs",
      "fixture-dep/C:/Users/alice/node_modules/child/index.cjs",
      "fixture-dep/.env/node_modules/child/index.cjs",
      "fixture-dep/~/.ssh/node_modules/child/index.cjs",
    ],
    expectedRedactedMainParentCount: 8,
    redactedMessage: "outside-root require must be recorded without its path",
  });
});

test("ESM loader persists only sanitized artifact values", (t) => {
  const fixture = createNodeFixture(t, "esm");
  const events = captureNodeHookEvents(fixture, "--loader", "loader.mjs");
  assertSanitizedRuntimeArtifact(events, fixture, {
    expectedDependencyEvent: {
      kind: "resolve",
      module: "fixture-dep",
      resolved: "node_modules/fixture-dep/index.mjs",
      parent: "main.mjs",
      entrypoint: "",
    },
    expectedNestedDependencyEvent: {
      kind: "resolve",
      module: "fixture-dep/safe/node_modules/child/index.mjs",
      resolved: "node_modules/fixture-dep/safe/node_modules/child/index.mjs",
      parent: "main.mjs",
      entrypoint: "",
    },
    expectedMainParent: "main.mjs",
    disallowedResolvedValues: [
      "node_modules/fixture-dep/C:/Users/alice/private.mjs",
      "node_modules/fixture-dep/https:/secret.mjs",
      "node_modules/fixture-dep/.env/private.mjs",
      "node_modules/fixture-dep/~/.ssh/id_rsa.mjs",
      "node_modules/fixture-dep/C:/Users/alice/node_modules/child/index.mjs",
      "node_modules/fixture-dep/.env/node_modules/child/index.mjs",
      "node_modules/fixture-dep/~/.ssh/node_modules/child/index.mjs",
    ],
    disallowedModuleValues: [
      "main.mjs",
      "local.mjs",
      "fixture-dep/C:/Users/alice/node_modules/child/index.mjs",
      "fixture-dep/.env/node_modules/child/index.mjs",
      "fixture-dep/~/.ssh/node_modules/child/index.mjs",
    ],
    expectedRedactedMainParentCount: 8,
    redactedMessage: "outside-root import must be recorded without its path",
  });
});

test("runtime hooks redact symlink-escaped parent and entrypoint contexts", (t) => {
  for (const format of ["cjs", "esm"]) {
    const fixture = createNodeSymlinkEscapeFixture(t, format);
    if (!fixture) {
      return;
    }
    const events = captureNodeHookEvents(
      fixture,
      format === "cjs" ? "--require" : "--loader",
      format === "cjs" ? "require-hook.cjs" : "loader.mjs",
    );

    assert.ok(
      events.some((event) => event.parent === "" && event.module === "fixture-dep"),
      `${format} dependency event should redact escaped parent context`,
    );
    assert.ok(
      events.some((event) => event.entrypoint === "" && event.parent === "" && event.module === "" && event.resolved === ""),
      `${format} main event should redact escaped entrypoint context`,
    );
  }
});

test("runtime hooks preserve hoisted external node_modules dependencies while redacting resolved paths", (t) => {
  for (const format of ["cjs", "esm"]) {
    const fixture = createNodeHoistedDependencyFixture(t, format);
    const events = captureNodeHookEvents(
      fixture,
      format === "cjs" ? "--require" : "--loader",
      format === "cjs" ? "require-hook.cjs" : "loader.mjs",
    );

    assertArtifactPrivacy(events, fixture);
    assert.ok(
      events.some(
        (event) =>
          event.module === "fixture-dep" &&
          event.resolved === "" &&
          event.parent === `main.${format === "cjs" ? "cjs" : "mjs"}` &&
          event.entrypoint === "",
      ),
      `${format} hook should keep the hoisted dependency specifier even when resolved is redacted`,
    );
    assert.equal(
      JSON.stringify(events).includes(path.join(fixture.fixtureRoot, "external-store")),
      false,
      `${format} hook should not leak the hoisted node_modules path`,
    );
  }
});

function createNodeFixture(t, format) {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), `lopper-runtime-${format}-`));
  t.after(() => fs.rmSync(fixtureRoot, { recursive: true, force: true }));

  const repoRoot = path.join(fixtureRoot, "repo");
  const packageRoot = path.join(repoRoot, "node_modules", "fixture-dep");
  const extension = format === "esm" ? "mjs" : "cjs";
  const entrypoint = path.join(repoRoot, `main.${extension}`);
  const outsidePath = path.join(fixtureRoot, `outside.${extension}`);
  const localPath = path.join(repoRoot, `local.${extension}`);
  const tracePath = path.join(fixtureRoot, `${format}.ndjson`);

  fs.mkdirSync(packageRoot, { recursive: true });
  fs.writeFileSync(
    path.join(packageRoot, "package.json"),
    JSON.stringify({
      name: "fixture-dep",
      version: "1.0.0",
      ...(format === "esm" ? { type: "module", main: "index.mjs" } : { main: "index.cjs" }),
    }),
  );
  fs.writeFileSync(
    path.join(packageRoot, `index.${extension}`),
    format === "esm" ? "export default 1;\n" : "module.exports = 1;\n",
  );
  fs.mkdirSync(path.join(packageRoot, "C:", "Users", "alice"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, "C:", "Users", "alice", "node_modules", "child"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, "https:"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, ".env"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, ".env", "node_modules", "child"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, "~", ".ssh"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, "~", ".ssh", "node_modules", "child"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, "safe", "node_modules", "child"), { recursive: true });
  fs.writeFileSync(
    path.join(packageRoot, "C:", "Users", "alice", `private.${extension}`),
    format === "esm" ? "export default 2;\n" : "module.exports = 2;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, "C:", "Users", "alice", "node_modules", "child", `index.${extension}`),
    format === "esm" ? "export default 7;\n" : "module.exports = 7;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, "https:", `secret.${extension}`),
    format === "esm" ? "export default 3;\n" : "module.exports = 3;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, ".env", `private.${extension}`),
    format === "esm" ? "export default 4;\n" : "module.exports = 4;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, ".env", "node_modules", "child", `index.${extension}`),
    format === "esm" ? "export default 8;\n" : "module.exports = 8;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, "~", ".ssh", `id_rsa.${extension}`),
    format === "esm" ? "export default 5;\n" : "module.exports = 5;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, "~", ".ssh", "node_modules", "child", `index.${extension}`),
    format === "esm" ? "export default 9;\n" : "module.exports = 9;\n",
  );
  fs.writeFileSync(
    path.join(packageRoot, "safe", "node_modules", "child", `index.${extension}`),
    format === "esm" ? "export default 10;\n" : "module.exports = 10;\n",
  );
  fs.writeFileSync(outsidePath, format === "esm" ? "export default 1;\n" : "module.exports = 1;\n");
  fs.writeFileSync(localPath, format === "esm" ? "export default 6;\n" : "module.exports = 6;\n");
  fs.writeFileSync(
    entrypoint,
    format === "esm"
      ? [
          'import "./local.mjs";',
          'import "fixture-dep";',
          'import "fixture-dep/C:/Users/alice/private.mjs";',
          'import "fixture-dep/https:/secret.mjs";',
          'import "fixture-dep/.env/private.mjs";',
          'import "fixture-dep/~/.ssh/id_rsa.mjs";',
          'import "fixture-dep/safe/node_modules/child/index.mjs";',
          'import "fixture-dep/C:/Users/alice/node_modules/child/index.mjs";',
          'import "fixture-dep/.env/node_modules/child/index.mjs";',
          'import "fixture-dep/~/.ssh/node_modules/child/index.mjs";',
          'import "../outside.mjs";',
          "",
        ].join("\n")
      : [
          'require("./local.cjs");',
          'require("fixture-dep");',
          'require("fixture-dep/C:/Users/alice/private.cjs");',
          'require("fixture-dep/https:/secret.cjs");',
          'require("fixture-dep/.env/private.cjs");',
          'require("fixture-dep/~/.ssh/id_rsa.cjs");',
          'require("fixture-dep/safe/node_modules/child/index.cjs");',
          'require("fixture-dep/C:/Users/alice/node_modules/child/index.cjs");',
          'require("fixture-dep/.env/node_modules/child/index.cjs");',
          'require("fixture-dep/~/.ssh/node_modules/child/index.cjs");',
          'require("../outside.cjs");',
          "",
        ].join("\n"),
  );

  return { entrypoint, fixtureRoot, repoRoot, tracePath };
}

function createNodeHoistedDependencyFixture(t, format) {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), `lopper-runtime-hoisted-${format}-`));
  t.after(() => fs.rmSync(fixtureRoot, { recursive: true, force: true }));

  const repoRoot = path.join(fixtureRoot, "repo");
  const externalStoreRoot = path.join(fixtureRoot, "external-store");
  const outsideNodeModules = path.join(externalStoreRoot, "node_modules");
  const packageRoot = path.join(outsideNodeModules, "fixture-dep");
  const extension = format === "esm" ? "mjs" : "cjs";
  const entrypoint = path.join(repoRoot, `main.${extension}`);
  const tracePath = path.join(fixtureRoot, `${format}.ndjson`);

  fs.mkdirSync(packageRoot, { recursive: true });
  fs.writeFileSync(
    path.join(packageRoot, "package.json"),
    JSON.stringify({
      name: "fixture-dep",
      version: "1.0.0",
      ...(format === "esm" ? { type: "module", main: "index.mjs" } : { main: "index.cjs" }),
    }),
  );
  fs.writeFileSync(
    path.join(packageRoot, `index.${extension}`),
    format === "esm" ? "export default 1;\n" : "module.exports = 1;\n",
  );
  fs.mkdirSync(repoRoot, { recursive: true });
  createDirectoryLinkSync(outsideNodeModules, path.join(repoRoot, "node_modules"));
  fs.writeFileSync(
    entrypoint,
    format === "esm" ? 'import "fixture-dep";\n' : 'require("fixture-dep");\n',
  );

  return { entrypoint, fixtureRoot, repoRoot, tracePath };
}

function createNodeSymlinkEscapeFixture(t, format) {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), `lopper-runtime-symlink-${format}-`));
  t.after(() => fs.rmSync(fixtureRoot, { recursive: true, force: true }));

  const repoRoot = path.join(fixtureRoot, "repo");
  const packageRoot = path.join(repoRoot, "node_modules", "fixture-dep");
  const extension = format === "esm" ? "mjs" : "cjs";
  const tracePath = path.join(fixtureRoot, `${format}.ndjson`);
  const outsideNodeModules = path.join(fixtureRoot, "node_modules");
  const parentTarget = path.join(fixtureRoot, `outside-parent.${extension}`);
  const parentLink = path.join(repoRoot, `link-parent.${extension}`);
  const entrypointTarget = path.join(fixtureRoot, `outside-entry.${extension}`);
  const entrypoint = path.join(repoRoot, `run.${extension}`);

  fs.mkdirSync(packageRoot, { recursive: true });
  fs.writeFileSync(
    path.join(packageRoot, "package.json"),
    JSON.stringify({
      name: "fixture-dep",
      version: "1.0.0",
      ...(format === "esm" ? { type: "module", main: "index.mjs" } : { main: "index.cjs" }),
    }),
  );
  fs.writeFileSync(
    path.join(packageRoot, `index.${extension}`),
    format === "esm" ? "export default 1;\n" : "module.exports = 1;\n",
  );
  try {
    createDirectoryLinkSync(path.join(repoRoot, "node_modules"), outsideNodeModules);
  } catch (error) {
    if (skipIfLinkUnsupported(t, error)) {
      return null;
    }
    throw error;
  }
  fs.writeFileSync(
    parentTarget,
    format === "esm" ? 'import "fixture-dep";\n' : 'require("fixture-dep");\n',
  );
  const parentSpecifier =
    format === "esm" ? pathToFileURL(parentLink).href : JSON.stringify(parentLink);
  fs.writeFileSync(
    entrypointTarget,
    format === "esm" ? `import ${JSON.stringify(parentSpecifier)};\n` : `require(${parentSpecifier});\n`,
  );
  try {
    createFileLinkSync(parentTarget, parentLink);
    createFileLinkSync(entrypointTarget, entrypoint);
  } catch (error) {
    if (skipIfLinkUnsupported(t, error)) {
      return null;
    }
    throw error;
  }

  return { entrypoint, fixtureRoot, repoRoot, tracePath };
}

function runtimeEnv(fixture) {
  const env = { ...process.env };
  delete env.NODE_OPTIONS;
  env.LOPPER_RUNTIME_REPO_ROOT = fixture.repoRoot;
  env.LOPPER_RUNTIME_TRACE = fixture.tracePath;
  return env;
}

function captureNodeHookEvents(fixture, flag, hookFile) {
  const hookPath = path.join(hookDir, hookFile);
  execFileSync(process.execPath, [`${flag}=${hookPath}`, fixture.entrypoint], {
    cwd: fixture.repoRoot,
    env: runtimeEnv(fixture),
    stdio: "pipe",
  });
  return readEvents(fixture.tracePath);
}

function readEvents(tracePath) {
  return fs
    .readFileSync(tracePath, "utf8")
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

function findResolvedEvents(events, resolvedValues) {
  return events.filter((event) => resolvedValues.includes(event.resolved));
}

function findModuleEvents(events, moduleValues) {
  return events.filter((event) => moduleValues.includes(event.module));
}

function assertArtifactPrivacy(events, fixture) {
  assert.ok(events.length > 0, "hook must emit at least one event");
  const artifact = JSON.stringify(events);
  for (const forbidden of [fixture.fixtureRoot, fixture.repoRoot, hookDir, "file://"]) {
    assert.equal(artifact.includes(forbidden), false, `artifact leaked ${forbidden}`);
  }
  for (const event of events) {
    for (const value of Object.values(event)) {
      if (typeof value !== "string") continue;
      assert.equal(path.isAbsolute(value), false, `artifact persisted absolute path ${value}`);
      assert.equal(value === ".." || value.startsWith("../"), false, `artifact persisted traversal ${value}`);
    }
  }
}

function assertSanitizedRuntimeArtifact(events, fixture, expectations) {
  assertArtifactPrivacy(events, fixture);
  assert.deepEqual(events.find((event) => event.module === "fixture-dep"), expectations.expectedDependencyEvent);
  assert.deepEqual(
    events.find((event) => event.module === expectations.expectedNestedDependencyEvent.module),
    expectations.expectedNestedDependencyEvent,
  );
  assert.ok(
    events.some((event) => event.module === "" && event.resolved === ""),
    expectations.redactedMessage,
  );
  assert.deepEqual(findResolvedEvents(events, expectations.disallowedResolvedValues), []);
  assert.equal(
    events.filter((event) => event.parent === expectations.expectedMainParent && event.module === "" && event.resolved === "")
      .length,
    expectations.expectedRedactedMainParentCount,
  );
  assert.deepEqual(findModuleEvents(events, expectations.disallowedModuleValues), []);
}
