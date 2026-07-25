import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const hookDir = path.dirname(fileURLToPath(import.meta.url));

test("CommonJS hook persists only sanitized artifact values", (t) => {
  const fixture = createNodeFixture(t, "cjs");
  const hookPath = path.join(hookDir, "require-hook.cjs");

  execFileSync(process.execPath, [`--require=${hookPath}`, fixture.entrypoint], {
    cwd: fixture.repoRoot,
    env: runtimeEnv(fixture),
    stdio: "pipe",
  });

  const events = readEvents(fixture.tracePath);
  assertArtifactPrivacy(events, fixture);
  assert.deepEqual(
    events.find((event) => event.module === "fixture-dep"),
    {
      kind: "require",
      module: "fixture-dep",
      resolved: "node_modules/fixture-dep/index.cjs",
      parent: "main.cjs",
      entrypoint: "",
      isMain: false,
    },
  );
  assert.ok(
    events.some((event) => event.module === "" && event.resolved === ""),
    "outside-root require must be recorded without its path",
  );
  assert.deepEqual(
    findResolvedEvents(events, [
      "node_modules/fixture-dep/C:/Users/alice/private.cjs",
      "node_modules/fixture-dep/https:/secret.cjs",
      "node_modules/fixture-dep/.env/private.cjs",
      "node_modules/fixture-dep/~/.ssh/id_rsa.cjs",
    ]),
    [],
  );
  assert.equal(
    events.filter((event) => event.parent === "main.cjs" && event.module === "" && event.resolved === "").length,
    5,
  );
});

test("ESM loader persists only sanitized artifact values", (t) => {
  const fixture = createNodeFixture(t, "esm");
  const hookPath = path.join(hookDir, "loader.mjs");

  execFileSync(process.execPath, [`--loader=${hookPath}`, fixture.entrypoint], {
    cwd: fixture.repoRoot,
    env: runtimeEnv(fixture),
    stdio: "pipe",
  });

  const events = readEvents(fixture.tracePath);
  assertArtifactPrivacy(events, fixture);
  assert.deepEqual(
    events.find((event) => event.module === "fixture-dep"),
    {
      kind: "resolve",
      module: "fixture-dep",
      resolved: "node_modules/fixture-dep/index.mjs",
      parent: "main.mjs",
      entrypoint: "",
    },
  );
  assert.ok(
    events.some((event) => event.module === "" && event.resolved === ""),
    "outside-root import must be recorded without its path",
  );
  assert.deepEqual(
    findResolvedEvents(events, [
      "node_modules/fixture-dep/C:/Users/alice/private.mjs",
      "node_modules/fixture-dep/https:/secret.mjs",
      "node_modules/fixture-dep/.env/private.mjs",
      "node_modules/fixture-dep/~/.ssh/id_rsa.mjs",
    ]),
    [],
  );
  assert.equal(
    events.filter((event) => event.parent === "main.mjs" && event.module === "" && event.resolved === "").length,
    5,
  );
});

function createNodeFixture(t, format) {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), `lopper-runtime-${format}-`));
  t.after(() => fs.rmSync(fixtureRoot, { recursive: true, force: true }));

  const repoRoot = path.join(fixtureRoot, "repo");
  const packageRoot = path.join(repoRoot, "node_modules", "fixture-dep");
  const extension = format === "esm" ? "mjs" : "cjs";
  const entrypoint = path.join(repoRoot, `main.${extension}`);
  const outsidePath = path.join(fixtureRoot, `outside.${extension}`);
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
  fs.mkdirSync(path.join(packageRoot, "https:"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, ".env"), { recursive: true });
  fs.mkdirSync(path.join(packageRoot, "~", ".ssh"), { recursive: true });
  fs.writeFileSync(
    path.join(packageRoot, "C:", "Users", "alice", `private.${extension}`),
    format === "esm" ? "export default 2;\n" : "module.exports = 2;\n",
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
    path.join(packageRoot, "~", ".ssh", `id_rsa.${extension}`),
    format === "esm" ? "export default 5;\n" : "module.exports = 5;\n",
  );
  fs.writeFileSync(outsidePath, format === "esm" ? "export default 1;\n" : "module.exports = 1;\n");
  fs.writeFileSync(
    entrypoint,
    format === "esm"
      ? [
          'import "fixture-dep";',
          'import "fixture-dep/C:/Users/alice/private.mjs";',
          'import "fixture-dep/https:/secret.mjs";',
          'import "fixture-dep/.env/private.mjs";',
          'import "fixture-dep/~/.ssh/id_rsa.mjs";',
          'import "../outside.mjs";',
          "",
        ].join("\n")
      : [
          'require("fixture-dep");',
          'require("fixture-dep/C:/Users/alice/private.cjs");',
          'require("fixture-dep/https:/secret.cjs");',
          'require("fixture-dep/.env/private.cjs");',
          'require("fixture-dep/~/.ssh/id_rsa.cjs");',
          'require("../outside.cjs");',
          "",
        ].join("\n"),
  );

  return { entrypoint, fixtureRoot, repoRoot, tracePath };
}

function runtimeEnv(fixture) {
  const env = { ...process.env };
  delete env.NODE_OPTIONS;
  env.LOPPER_RUNTIME_REPO_ROOT = fixture.repoRoot;
  env.LOPPER_RUNTIME_TRACE = fixture.tracePath;
  return env;
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
