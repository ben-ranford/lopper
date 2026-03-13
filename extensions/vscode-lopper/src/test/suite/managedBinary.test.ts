import AdmZip from "adm-zip";
import * as assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile, copyFile, rm } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";
import * as tar from "tar";

import { assetNameForRelease, ManagedBinaryInstaller, type GitHubRelease } from "../../managedBinary";

suite("managed binary installer", () => {
  test("builds expected release asset names", () => {
    assert.equal(
      assetNameForRelease("v1.2.3", { platform: "linux", arch: "x64" }),
      "lopper_1.2.3_linux_amd64.tar.gz",
    );
    assert.equal(
      assetNameForRelease("v1.2.3", { platform: "win32", arch: "arm64" }),
      "lopper_1.2.3_windows_arm64.zip",
    );
  });

  test("installs tar.gz managed binaries into cache", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-test-"));
    try {
      const releaseTag = "v9.8.7";
      const host = { platform: "linux" as const, arch: "x64" };
      const archivePath = await createTarballFixture(tempRoot, releaseTag, host, "linux binary");
      const installer = createInstaller(tempRoot, releaseTag, host, archivePath);

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
      const installer = createInstaller(tempRoot, releaseTag, host, archivePath);

      const result = await installer.ensureInstalled();
      assert.equal(result.downloaded, true);
      assert.match(result.binaryPath, /lopper\.exe$/);
      assert.equal(await readFile(result.binaryPath, "utf8"), "windows binary");
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });
});

function createInstaller(
  tempRoot: string,
  releaseTag: string,
  host: { platform: NodeJS.Platform; arch: string },
  archivePath: string,
): ManagedBinaryInstaller {
  const release: GitHubRelease = {
    tag_name: releaseTag,
    assets: [
      {
        name: assetNameForRelease(releaseTag, host),
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

async function createTarballFixture(
  tempRoot: string,
  releaseTag: string,
  host: { platform: "linux" | "darwin"; arch: string },
  contents: string,
): Promise<string> {
  const archiveDir = path.join(tempRoot, "tar-source");
  const rootDir = path.join(
    archiveDir,
    `lopper_${releaseTag.replace(/^v/, "")}_${host.platform}_${host.arch === "x64" ? "amd64" : "arm64"}`,
  );
  await mkdir(rootDir, { recursive: true });
  await writeFile(path.join(rootDir, "lopper"), contents);

  const archivePath = path.join(tempRoot, assetNameForRelease(releaseTag, host));
  await tar.create({ gzip: true, file: archivePath, cwd: archiveDir }, [path.basename(rootDir)]);
  return archivePath;
}

async function createZipFixture(
  tempRoot: string,
  releaseTag: string,
  host: { platform: "win32"; arch: string },
  contents: string,
): Promise<string> {
  const zip = new AdmZip();
  const rootDir = `lopper_${releaseTag.replace(/^v/, "")}_windows_${host.arch === "x64" ? "amd64" : "arm64"}`;
  zip.addFile(`${rootDir}/lopper.exe`, Buffer.from(contents, "utf8"));

  const archivePath = path.join(tempRoot, assetNameForRelease(releaseTag, host));
  zip.writeZip(archivePath);
  return archivePath;
}
