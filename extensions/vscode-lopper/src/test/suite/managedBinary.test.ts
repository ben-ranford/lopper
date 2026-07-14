import AdmZip from "adm-zip";
import * as assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as tar from "tar";

import {
  BinaryResolutionError,
  archiveDestinationPath,
  assetNameForRelease,
  downloadAsset,
  fetchRelease,
  LopperBinaryLifecycleManager,
  ManagedBinaryInstaller,
  type GitHubRelease,
  type GitHubReleaseAsset,
  selectReleaseAsset,
} from "../../managedBinary";

suite("managed binary installer", () => {
  test("builds expected release asset names", () => {
    assert.equal(
      assetNameForRelease("v1.2.3", { platform: "linux", arch: "x64" }),
      "lopper_v1.2.3_linux_amd64.tar.gz",
    );
    assert.equal(
      assetNameForRelease("1.2.3", { platform: "linux", arch: "x64" }),
      "lopper_1.2.3_linux_amd64.tar.gz",
    );
    assert.equal(
      assetNameForRelease("v1.2.3", { platform: "win32", arch: "arm64" }),
      "lopper_v1.2.3_windows_arm64.zip",
    );
    assert.equal(
      assetNameForRelease("1.2.3", { platform: "win32", arch: "arm64" }),
      "lopper_1.2.3_windows_arm64.zip",
    );
  });

  test("selects tagged release asset names produced by release builds", () => {
    const host = { platform: "linux" as const, arch: "x64" };
    const release: GitHubRelease = {
      tag_name: "v9.8.7",
      assets: [
        {
          name: "other.zip",
          browser_download_url: "file://other",
        },
        {
          name: assetNameForRelease("v9.8.7", host),
          browser_download_url: "file://release-asset",
        },
      ],
    };

    const asset = selectReleaseAsset(release, host);
    assert.equal(asset.name, assetNameForRelease("v9.8.7", host));
  });

  test("falls back to legacy non-prefixed assets for compatibility", () => {
    const host = { platform: "linux" as const, arch: "x64" };
    const release: GitHubRelease = {
      tag_name: "v9.8.7",
      assets: [
        {
          name: assetNameForRelease("9.8.7", host),
          browser_download_url: "file://legacy-asset",
        },
      ],
    };

    const asset = selectReleaseAsset(release, host);
    assert.equal(asset.name, assetNameForRelease("9.8.7", host));
  });

  test("fails when release assets do not include expected candidates", () => {
    const host = { platform: "linux" as const, arch: "x64" };
    const release: GitHubRelease = {
      tag_name: "v9.8.7",
      assets: [{ name: "lopper_9.8.7_other_linux_amd64.tar.gz", browser_download_url: "file://not-matching" }],
    };

    assert.throws(
      () => selectReleaseAsset(release, host),
      (error: unknown) =>
        error instanceof Error &&
        error.message.includes("release v9.8.7 does not contain expected asset"),
    );
  });

  test("fails when the release tag is blank", () => {
    const host = { platform: "linux" as const, arch: "x64" };
    const release: GitHubRelease = {
      tag_name: "   ",
      assets: [],
    };

    assert.throws(
      () => selectReleaseAsset(release, host),
      (error: unknown) => error instanceof Error && error.message.includes("release tag is required"),
    );
  });

  test("installs tar.gz managed binaries into cache", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-test-"));
    try {
      const releaseTag = "v9.8.7";
      const host = { platform: "linux" as const, arch: "x64" };
      const archivePath = await createTarballFixture(tempRoot, releaseTag, host, "linux binary");
      const installer = await createInstaller(tempRoot, releaseTag, host, archivePath);

      const cachedBefore = await installer.findInstalledBinary();
      assert.equal(cachedBefore, undefined);

      const result = await installer.ensureInstalled();
      assert.equal(result.downloaded, true);
      assert.match(result.binaryPath, /managed-lopper/);
      assert.equal(await readFile(result.binaryPath, "utf8"), "linux binary");

      const cachedAfter = await installer.findInstalledBinary();
      assert.equal(cachedAfter, result.binaryPath);
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("installs zip managed binaries into cache", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-test-"));
    try {
      const releaseTag = "v1.0.0";
      const host = { platform: "win32" as const, arch: "x64" };
      const archivePath = await createZipFixture(tempRoot, releaseTag, host, "windows binary");
      const installer = await createInstaller(tempRoot, releaseTag, host, archivePath);

      const result = await installer.ensureInstalled();
      assert.equal(result.downloaded, true);
      assert.match(result.binaryPath, /lopper\.exe$/);
      assert.equal(await readFile(result.binaryPath, "utf8"), "windows binary");
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects managed binary archive downloads with checksum mismatch", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-test-"));
    try {
      const releaseTag = "v9.8.7";
      const host = { platform: "linux" as const, arch: "x64" };
      const archivePath = await createTarballFixture(tempRoot, releaseTag, host, "linux binary");
      const badDigest = "0".repeat(64);
      const installer = await createInstaller(tempRoot, releaseTag, host, archivePath, badDigest);

      await assert.rejects(
        installer.ensureInstalled(),
        (error: unknown) =>
          error instanceof Error &&
          error.message.includes("integrity check failed"),
      );
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects archive entries that escape the extraction directory", () => {
    assert.throws(
      () => archiveDestinationPath(path.join(process.cwd(), "extract-root"), "lopper_1.2.1_linux_amd64/../../lopper"),
      /escapes the extraction directory/,
    );
  });

  test("rejects tar.gz managed binaries with escaping archive entries", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-test-"));
    try {
      const releaseTag = "v9.8.7";
      const host = { platform: "linux" as const, arch: "x64" };
      const archivePath = await createEscapingTarballFixture(tempRoot, releaseTag, host);
      const installer = await createInstaller(tempRoot, releaseTag, host, archivePath);

      await assert.rejects(
        installer.ensureInstalled(),
        (error: unknown) =>
          error instanceof Error &&
          error.message.includes("escapes the extraction directory"),
      );
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("falls back to managed install when configured/local binaries are unavailable", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-lifecycle-test-"));
    const previousPath = process.env.PATH;
    const progressTags: string[] = [];
    let installerCallCount = 0;

    try {
      process.env.PATH = "";
      const lifecycle = new LopperBinaryLifecycleManager(
        {
          findInstalledBinary: async () => undefined,
          ensureInstalled: async (releaseTag) => {
            installerCallCount += 1;
            return {
              binaryPath: path.join(tempRoot, "managed", "lopper"),
              tag: releaseTag ?? "latest",
              downloaded: true,
            };
          },
        },
        { appendLine: () => undefined },
        {
          install: async (releaseTag, install) => {
            progressTags.push(releaseTag ?? "latest");
            return install();
          },
        },
        "linux",
      );

      const binaryPath = await lifecycle.resolveBinaryPath({
        workspaceRoot: tempRoot,
        workspaceTrusted: true,
        autoDownloadBinary: true,
        managedBinaryTag: "v2.3.4",
      });

      assert.equal(binaryPath, path.join(tempRoot, "managed", "lopper"));
      assert.equal(installerCallCount, 1);
      assert.deepEqual(progressTags, ["v2.3.4"]);
    } finally {
      restoreEnv("PATH", previousPath);
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("falls back to PATH when workspace-local bin binary is missing", async () => {
    await withPathFallbackFixture("missing", async ({ workspaceRoot, fallbackBinary, lifecycle }) => {
      const resolvedPath = await lifecycle.resolveBinaryPath({
        workspaceRoot,
        workspaceTrusted: true,
        autoDownloadBinary: false,
      });

      assert.equal(resolvedPath, fallbackBinary);
    });
  });

  test("rejects workspace-local bin paths that are directories", async () => {
    await withPathFallbackFixture("directory", async ({ workspaceRoot, localBinaryPath, lifecycle }) => {
      await mkdir(localBinaryPath, { recursive: true });

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot,
          workspaceTrusted: true,
          autoDownloadBinary: false,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("workspace-local lopper binary must point to a file") &&
          error.message.includes(localBinaryPath),
      );
    });
  });

  test("skips workspace-local bin directories in untrusted workspaces", async () => {
    await withPathFallbackFixture("directory-untrusted", async ({ workspaceRoot, localBinaryPath, fallbackBinary, lifecycle }) => {
      await mkdir(localBinaryPath, { recursive: true });

      const resolvedPath = await lifecycle.resolveBinaryPath({
        workspaceRoot,
        workspaceTrusted: false,
        autoDownloadBinary: false,
      });

      assert.equal(resolvedPath, fallbackBinary);
    });
  });

  test("rejects non-executable workspace-local bin binaries on unix hosts", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    await withPathFallbackFixture("nonexec", async ({ workspaceRoot, localBinaryPath, lifecycle }) => {
      await mkdir(path.dirname(localBinaryPath), { recursive: true });
      await writeFile(localBinaryPath, "#!/bin/sh\nexit 0\n", "utf8");
      await chmod(localBinaryPath, 0o644);

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot,
          workspaceTrusted: true,
          autoDownloadBinary: false,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("workspace-local lopper binary is not executable") &&
          error.message.includes(localBinaryPath),
      );
    });
  });

  test("skips non-executable workspace-local bin binaries in untrusted workspaces", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    await withPathFallbackFixture("nonexec-untrusted", async ({ workspaceRoot, localBinaryPath, fallbackBinary, lifecycle }) => {
      await mkdir(path.dirname(localBinaryPath), { recursive: true });
      await writeFile(localBinaryPath, "#!/bin/sh\nexit 0\n", "utf8");
      await chmod(localBinaryPath, 0o644);

      const resolvedPath = await lifecycle.resolveBinaryPath({
        workspaceRoot,
        workspaceTrusted: false,
        autoDownloadBinary: false,
      });

      assert.equal(resolvedPath, fallbackBinary);
    });
  });

  test("forwards progress cancellation signals to managed installer", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-lifecycle-signal-"));
    const controller = new AbortController();
    const previousPath = process.env.PATH;
    let observedSignal: AbortSignal | undefined;

    try {
      process.env.PATH = "";
      const lifecycle = new LopperBinaryLifecycleManager(
        {
          findInstalledBinary: async () => undefined,
          ensureInstalled: async (_releaseTag, signal) => {
            observedSignal = signal;
            return {
              binaryPath: path.join(tempRoot, "managed", "lopper"),
              tag: "latest",
              downloaded: true,
            };
          },
        },
        { appendLine: () => undefined },
        {
          install: async (_releaseTag, install) => install(controller.signal),
        },
        "linux",
      );

      const binaryPath = await lifecycle.resolveBinaryPath({
        workspaceRoot: tempRoot,
        workspaceTrusted: true,
        autoDownloadBinary: true,
      });

      assert.equal(binaryPath, path.join(tempRoot, "managed", "lopper"));
      assert.equal(observedSignal, controller.signal);
    } finally {
      restoreEnv("PATH", previousPath);
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("forwards abort signals to release lookup and asset download", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-signal-"));
    const controller = new AbortController();
    let fetchSignal: AbortSignal | undefined;
    let downloadSignal: AbortSignal | undefined;

    try {
      const releaseTag = "v9.8.7";
      const host = { platform: "linux" as const, arch: "x64" };
      const archivePath = await createTarballFixture(tempRoot, releaseTag, host, "linux binary");
      const binaryDigest = await sha256File(archivePath);
      const release: GitHubRelease = {
        tag_name: releaseTag,
        assets: [
          {
            name: assetNameForRelease(releaseTag, host),
            digest: `sha256:${binaryDigest}`,
            browser_download_url: `file://${archivePath}`,
          },
        ],
      };
      const installer = new ManagedBinaryInstaller(
        path.join(tempRoot, "storage"),
        { appendLine: () => undefined },
        {
          host,
          fetchRelease: async (_releaseTag, signal) => {
            fetchSignal = signal;
            return release;
          },
          downloadAsset: async (_asset, destinationPath, signal) => {
            downloadSignal = signal;
            await copyFile(archivePath, destinationPath);
          },
        },
      );

      await installer.ensureInstalled(undefined, controller.signal);

      assert.equal(fetchSignal, controller.signal);
      assert.equal(downloadSignal, controller.signal);
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("times out release lookup fetches", async () => {
    await withHangingFetch(async () => {
      await assert.rejects(
        fetchRelease(undefined, undefined, 10),
        /release lookup timed out after 10ms/,
      );
    });
  });

  test("times out managed asset downloads", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-download-timeout-"));
    const asset: GitHubReleaseAsset = {
      name: "lopper_v9.8.7_linux_amd64.tar.gz",
      browser_download_url: "https://example.test/lopper.tar.gz",
      digest: `sha256:${"0".repeat(64)}`,
    };

    try {
      await withHangingFetch(async () => {
        await assert.rejects(
          downloadAsset(asset, path.join(tempRoot, asset.name), undefined, 10),
          /asset download timed out after 10ms/,
        );
      });
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("fails when auto-download is disabled and no binaries resolve", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-lifecycle-test-"));
    const previousPath = process.env.PATH;

    try {
      process.env.PATH = "";
      const lifecycle = new LopperBinaryLifecycleManager(
        {
          findInstalledBinary: async () => undefined,
          ensureInstalled: async () => {
            throw new Error("should not install when auto-download is disabled");
          },
        },
        { appendLine: () => undefined },
        undefined,
        "linux",
      );

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot: tempRoot,
          workspaceTrusted: true,
          autoDownloadBinary: false,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("enable lopper.autoDownloadBinary"),
      );
    } finally {
      restoreEnv("PATH", previousPath);
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects configured binaries when canonicalization fails in untrusted workspaces", async () => {
    const workspaceRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-workspace-"));
    const binaryRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-configured-"));
    const binaryPath = path.join(binaryRoot, platformBinaryName());
    const outputLines: string[] = [];

    try {
      await writeExecutable(binaryPath);
      const lifecycle = new LopperBinaryLifecycleManager(
        {
          findInstalledBinary: async () => undefined,
          ensureInstalled: async () => {
            throw new Error("should not install when configured binary is provided");
          },
        },
        { appendLine: (value) => outputLines.push(value) },
        undefined,
        process.platform,
        {
          canonicalizePath: async (targetPath) => {
            if (targetPath === binaryPath) {
              throw new Error("boom");
            }
            return path.resolve(targetPath);
          },
        },
      );

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot,
          workspaceTrusted: false,
          autoDownloadBinary: false,
          envBinaryPath: binaryPath,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError &&
          error.message.includes("workspace-local binary in an untrusted workspace"),
      );
      assert.ok(
        outputLines.some((value) => value.includes("path canonicalization failed")),
        "expected canonicalization failure to be logged",
      );
      assert.ok(
        outputLines.some((value) => value.includes(binaryPath) && value.includes(workspaceRoot)),
        "expected canonicalization failure log to mention both paths",
      );
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
      await rm(binaryRoot, { recursive: true, force: true });
    }
  });

  test("rejects configured binaries under any open root in an untrusted multi-root workspace", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-multiroot-"));
    const currentRoot = path.join(tempRoot, "current");
    const siblingRoot = path.join(tempRoot, "sibling");
    const siblingBinary = path.join(siblingRoot, platformBinaryName());

    try {
      await mkdir(currentRoot, { recursive: true });
      await mkdir(siblingRoot, { recursive: true });
      await writeExecutable(siblingBinary);
      const lifecycle = createPathFallbackLifecycle();

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot: currentRoot,
          workspaceRoots: [currentRoot, siblingRoot],
          workspaceTrusted: false,
          autoDownloadBinary: false,
          configuredBinaryPath: siblingBinary,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError
          && error.message.includes("workspace-local binary in an untrusted workspace"),
      );
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects configured symlinks resolving under a sibling root in an untrusted workspace", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-multiroot-link-"));
    const currentRoot = path.join(tempRoot, "current");
    const siblingRoot = path.join(tempRoot, "sibling");
    const outsideRoot = path.join(tempRoot, "outside");
    const siblingBinary = path.join(siblingRoot, platformBinaryName());
    const outsideLink = path.join(outsideRoot, platformBinaryName());

    try {
      await mkdir(currentRoot, { recursive: true });
      await mkdir(siblingRoot, { recursive: true });
      await mkdir(outsideRoot, { recursive: true });
      await writeExecutable(siblingBinary);
      await symlink(siblingBinary, outsideLink);
      const lifecycle = createPathFallbackLifecycle();

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot: currentRoot,
          workspaceRoots: [currentRoot, siblingRoot],
          workspaceTrusted: false,
          autoDownloadBinary: false,
          configuredBinaryPath: outsideLink,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError
          && error.message.includes("workspace-local binary in an untrusted workspace"),
      );
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("rejects configured symlinks located under the workspace in untrusted workspaces", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const workspaceRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-workspace-link-"));
    const outsideRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-outside-target-"));
    const outsideBinary = path.join(outsideRoot, platformBinaryName());
    const workspaceLink = path.join(workspaceRoot, platformBinaryName());

    try {
      await writeExecutable(outsideBinary);
      await symlink(outsideBinary, workspaceLink);
      const lifecycle = createPathFallbackLifecycle();

      await assert.rejects(
        lifecycle.resolveBinaryPath({
          workspaceRoot,
          workspaceTrusted: false,
          autoDownloadBinary: false,
          configuredBinaryPath: workspaceLink,
        }),
        (error: unknown) =>
          error instanceof BinaryResolutionError
          && error.message.includes("workspace-local binary in an untrusted workspace"),
      );
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
      await rm(outsideRoot, { recursive: true, force: true });
    }
  });

  test("skips PATH symlinks located under the workspace in untrusted workspaces", async function () {
    if (process.platform === "win32") {
      this.skip();
    }

    const workspaceRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-path-link-workspace-"));
    const outsideRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-path-link-target-"));
    const fallbackRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-path-link-fallback-"));
    const workspacePathRoot = path.join(workspaceRoot, "path-bin");
    const workspaceLink = path.join(workspacePathRoot, platformBinaryName());
    const outsideBinary = path.join(outsideRoot, platformBinaryName());
    const fallbackBinary = path.join(fallbackRoot, platformBinaryName());
    const previousPath = process.env.PATH;

    try {
      await mkdir(workspacePathRoot, { recursive: true });
      await writeExecutable(outsideBinary);
      await writeExecutable(fallbackBinary);
      await symlink(outsideBinary, workspaceLink);
      process.env.PATH = joinPathEntries([workspacePathRoot, fallbackRoot, previousPath]);
      const lifecycle = createPathFallbackLifecycle();

      const resolvedPath = await lifecycle.resolveBinaryPath({
        workspaceRoot,
        workspaceTrusted: false,
        autoDownloadBinary: false,
      });

      assert.equal(resolvedPath, fallbackBinary);
    } finally {
      restoreEnv("PATH", previousPath);
      await rm(workspaceRoot, { recursive: true, force: true });
      await rm(outsideRoot, { recursive: true, force: true });
      await rm(fallbackRoot, { recursive: true, force: true });
    }
  });

  test("skips PATH candidates when canonicalization fails in untrusted workspaces", async () => {
    const workspaceRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-workspace-"));
    const blockedRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-path-blocked-"));
    const fallbackRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-path-fallback-"));
    const blockedBinary = path.join(blockedRoot, platformBinaryName());
    const fallbackBinary = path.join(fallbackRoot, platformBinaryName());
    const previousPath = process.env.PATH;
    const outputLines: string[] = [];

    try {
      await writeExecutable(blockedBinary);
      await writeExecutable(fallbackBinary);
      process.env.PATH = joinPathEntries([blockedRoot, fallbackRoot, previousPath]);

      const lifecycle = new LopperBinaryLifecycleManager(
        {
          findInstalledBinary: async () => undefined,
          ensureInstalled: async () => {
            throw new Error("should not install when PATH fallback resolves");
          },
        },
        { appendLine: (value) => outputLines.push(value) },
        undefined,
        process.platform,
        {
          canonicalizePath: async (targetPath) => {
            if (targetPath === blockedBinary) {
              throw new Error("boom");
            }
            return path.resolve(targetPath);
          },
        },
      );

      const resolvedPath = await lifecycle.resolveBinaryPath({
        workspaceRoot,
        workspaceTrusted: false,
        autoDownloadBinary: false,
      });

      assert.equal(resolvedPath, fallbackBinary);
      assert.ok(
        outputLines.some((value) => value.includes("path canonicalization failed")),
        "expected canonicalization failure to be logged",
      );
      assert.ok(
        outputLines.some((value) => value.includes("skipping workspace-local lopper binary from PATH")),
        "expected failed canonicalization candidate to be skipped from PATH",
      );
    } finally {
      restoreEnv("PATH", previousPath);
      await rm(workspaceRoot, { recursive: true, force: true });
      await rm(blockedRoot, { recursive: true, force: true });
      await rm(fallbackRoot, { recursive: true, force: true });
    }
  });
});

async function createInstaller(
  tempRoot: string,
  releaseTag: string,
  host: { platform: NodeJS.Platform; arch: string },
  archivePath: string,
  archiveDigest?: string,
): Promise<ManagedBinaryInstaller> {
  const binaryDigest = archiveDigest ?? (await sha256File(archivePath));

  const release: GitHubRelease = {
    tag_name: releaseTag,
    assets: [
      {
        name: assetNameForRelease(releaseTag, host),
        digest: `sha256:${binaryDigest}`,
        browser_download_url: `file://${archivePath}`,
      },
    ],
  };

  return new ManagedBinaryInstaller(
    path.join(tempRoot, "storage"),
    { appendLine: () => undefined },
    {
      host,
      fetchRelease: async () => release,
      downloadAsset: async (_asset, destinationPath) => {
        await copyFile(archivePath, destinationPath);
      },
    },
  );
}

async function sha256File(filePath: string): Promise<string> {
  const buffer = await readFile(filePath);
  return createHash("sha256").update(buffer).digest("hex");
}

async function createTarballFixture(
  tempRoot: string,
  releaseTag: string,
  host: { platform: "linux" | "darwin"; arch: string },
  contents: string,
): Promise<string> {
  const archiveDir = path.join(tempRoot, "tar-source");
  const rootDir = path.join(
    archiveDir,
    `lopper_${releaseTag}_${host.platform}_${host.arch === "x64" ? "amd64" : "arm64"}`,
  );
  await mkdir(rootDir, { recursive: true });
  await writeFile(path.join(rootDir, "lopper"), contents);

  const archivePath = path.join(tempRoot, assetNameForRelease(releaseTag, host));
  await tar.create({ gzip: true, file: archivePath, cwd: archiveDir }, [path.basename(rootDir)]);
  return archivePath;
}

async function createEscapingTarballFixture(
  tempRoot: string,
  releaseTag: string,
  host: { platform: "linux" | "darwin"; arch: string },
): Promise<string> {
  const archiveDir = path.join(tempRoot, "tar-escape-source");
  const outsideDir = path.join(tempRoot, "tar-escape-outside");
  await mkdir(archiveDir, { recursive: true });
  await mkdir(outsideDir, { recursive: true });
  await writeFile(path.join(outsideDir, "escape.txt"), "escape");

  const archivePath = path.join(tempRoot, assetNameForRelease(releaseTag, host));
  await tar.create(
    {
      gzip: true,
      file: archivePath,
      cwd: archiveDir,
      preservePaths: true,
    },
    ["../tar-escape-outside/escape.txt"],
  );
  return archivePath;
}

async function createZipFixture(
  tempRoot: string,
  releaseTag: string,
  host: { platform: "win32"; arch: string },
  contents: string,
  binaryRelativePath = "lopper.exe",
): Promise<string> {
  const zip = new AdmZip();
  const rootDir = `lopper_${releaseTag}_windows_${host.arch === "x64" ? "amd64" : "arm64"}`;
  zip.addFile(`${rootDir}/${binaryRelativePath}`, Buffer.from(contents, "utf8"));

  const archivePath = path.join(tempRoot, assetNameForRelease(releaseTag, host));
  zip.writeZip(archivePath);
  return archivePath;
}

interface PathFallbackFixture {
  workspaceRoot: string;
  fallbackBinary: string;
  localBinaryPath: string;
  lifecycle: LopperBinaryLifecycleManager;
}

async function withPathFallbackFixture(
  namePrefix: string,
  run: (fixture: PathFallbackFixture) => Promise<void>,
): Promise<void> {
  const workspaceRoot = await mkdtemp(path.join(os.tmpdir(), `lopper-managed-local-${namePrefix}-`));
  const pathRoot = await mkdtemp(path.join(os.tmpdir(), `lopper-managed-local-${namePrefix}-path-`));
  const fallbackBinary = path.join(pathRoot, platformBinaryName());
  const previousPath = process.env.PATH;

  try {
    await writeExecutable(fallbackBinary);
    process.env.PATH = joinPathEntries([pathRoot, previousPath]);
    await run({
      workspaceRoot,
      fallbackBinary,
      localBinaryPath: path.join(workspaceRoot, "bin", platformBinaryName()),
      lifecycle: createPathFallbackLifecycle(),
    });
  } finally {
    restoreEnv("PATH", previousPath);
    await rm(workspaceRoot, { recursive: true, force: true });
    await rm(pathRoot, { recursive: true, force: true });
  }
}

async function withHangingFetch(run: () => Promise<void>): Promise<void> {
  const previousFetch = globalThis.fetch;
  const hangingFetch: typeof fetch = async (_input, init) => {
    const signal = init?.signal ?? undefined;
    return new Promise<Response>((_resolve, reject) => {
      if (!signal) {
        reject(new Error("expected fetch signal"));
        return;
      }
      const abort = () => reject(abortFetchError());
      if (signal.aborted) {
        abort();
        return;
      }
      signal.addEventListener("abort", abort, { once: true });
    });
  };

  try {
    globalThis.fetch = hangingFetch;
    await run();
  } finally {
    globalThis.fetch = previousFetch;
  }
}

function abortFetchError(): Error {
  const error = new Error("This operation was aborted");
  error.name = "AbortError";
  return error;
}

function createPathFallbackLifecycle(): LopperBinaryLifecycleManager {
  return new LopperBinaryLifecycleManager(
    {
      findInstalledBinary: async () => undefined,
      ensureInstalled: async () => {
        throw new Error("should not install when PATH fallback resolves");
      },
    },
    { appendLine: () => undefined },
    undefined,
    process.platform,
  );
}

function restoreEnv(name: string, value: string | undefined): void {
  if (value === undefined) {
    delete process.env[name];
    return;
  }
  process.env[name] = value;
}

async function writeExecutable(binaryPath: string): Promise<void> {
  await mkdir(path.dirname(binaryPath), { recursive: true });
  await writeFile(binaryPath, "#!/bin/sh\nexit 0\n", "utf8");
  if (process.platform !== "win32") {
    await chmod(binaryPath, 0o755);
  }
}

function joinPathEntries(entries: Array<string | undefined>): string {
  return entries.filter((entry): entry is string => Boolean(entry && entry.length > 0)).join(path.delimiter);
}

function platformBinaryName(): string {
  return process.platform === "win32" ? "lopper.exe" : "lopper";
}
