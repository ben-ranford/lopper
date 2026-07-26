import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

function createHelpersImportHref(caseId = "default", baseUrl = import.meta.url) {
  const helperUrl = new URL("./test-link-helpers.mjs", baseUrl);
  helperUrl.searchParams.set("case", caseId);
  return helperUrl.href;
}

async function loadHelpers(caseId = "default", baseUrl = import.meta.url) {
  return import(createHelpersImportHref(caseId, baseUrl));
}

function createPrivateTempDir(prefix) {
  const parentDir = fs.mkdtempSync(path.join(os.tmpdir(), "lopper-runtime-"));
  fs.chmodSync(parentDir, 0o700);
  const tempDir = path.join(parentDir, prefix);
  fs.mkdirSync(tempDir, { mode: 0o700 });
  return { parentDir, tempDir };
}

test("createDirectoryLinkSync surfaces Windows junction privilege failures", async (t) => {
  if (process.platform !== "win32") {
    t.skip("Windows-only junction failure coverage");
  }

  const originalSymlinkSync = fs.symlinkSync;
  const error = Object.assign(new Error("privilege missing"), { code: "EPERM" });
  fs.symlinkSync = () => {
    throw error;
  };
  t.after(() => {
    fs.symlinkSync = originalSymlinkSync;
  });

  const { createDirectoryLinkSync } = await loadHelpers("windows-junction-privilege");
  assert.throws(() => createDirectoryLinkSync("target", "link"), (caught) => caught === error);
});

test("createFileLinkSync preserves skip semantics for file symlink privilege failures", async (t) => {
  const originalSymlinkSync = fs.symlinkSync;
  const originalWriteFileSync = fs.writeFileSync;
  const originalMkdtempSync = fs.mkdtempSync;
  const originalRmSync = fs.rmSync;
  const error = Object.assign(new Error("privilege missing"), { code: "EPERM" });

  fs.mkdtempSync = () => path.join(os.tmpdir(), "lopper-file-symlink-probe");
  fs.writeFileSync = () => {};
  fs.rmSync = () => {};
  fs.symlinkSync = () => {
    throw error;
  };
  t.after(() => {
    fs.symlinkSync = originalSymlinkSync;
    fs.writeFileSync = originalWriteFileSync;
    fs.mkdtempSync = originalMkdtempSync;
    fs.rmSync = originalRmSync;
  });

  const { createFileLinkSync, skipIfLinkUnsupported } = await loadHelpers("file-symlink-privilege");
  let skipped = false;
  let thrown;
  try {
    createFileLinkSync("target", "link");
  } catch (error) {
    thrown = error;
  }
  assert.ok(thrown);
  const didSkip = skipIfLinkUnsupported(
    {
      skip(message) {
        skipped = true;
        assert.match(message, /file links require symlink privileges|unable to create file link/);
      },
    },
    thrown,
  );

  assert.equal(didSkip, true);
  assert.equal(skipped, true);
});

test("loadHelpers preserves spaced and escaped file URL segments", async (t) => {
  const { parentDir, tempDir: tempRoot } = createPrivateTempDir("lopper test-link-helpers %23");
  t.after(() => {
    fs.rmSync(parentDir, { recursive: true, force: true });
  });

  const runtimeDir = path.join(tempRoot, "runtime fixtures");
  fs.mkdirSync(runtimeDir, { recursive: true });

  const helperSourcePath = new URL("./test-link-helpers.mjs", import.meta.url);
  const helperTargetPath = path.join(runtimeDir, "test-link-helpers.mjs");
  fs.copyFileSync(helperSourcePath, helperTargetPath);

  const syntheticTestPath = path.join(runtimeDir, "test-link-helpers_test.mjs");
  const syntheticBaseUrl = pathToFileURL(syntheticTestPath).href;
  const helpers = await loadHelpers("spaced-escaped", syntheticBaseUrl);

  assert.equal(typeof helpers.createDirectoryLinkSync, "function");
  assert.equal(typeof helpers.createFileLinkSync, "function");

  const importedUrl = new URL(createHelpersImportHref("spaced-escaped", syntheticBaseUrl));
  assert.match(importedUrl.href, /runtime%20fixtures/);
  assert.match(importedUrl.href, /%2523/);
});
