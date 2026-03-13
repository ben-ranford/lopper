import AdmZip from "adm-zip";
import { constants as fsConstants } from "node:fs";
import {
  access,
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import * as tar from "tar";

export interface GitHubReleaseAsset {
  name: string;
  browser_download_url: string;
}

export interface GitHubRelease {
  tag_name: string;
  assets: GitHubReleaseAsset[];
}

export interface HostPlatform {
  platform: NodeJS.Platform;
  arch: string;
}

export interface ManagedBinaryInstallResult {
  binaryPath: string;
  tag: string;
  downloaded: boolean;
}

interface ManagedBinaryMetadata {
  binaryPath: string;
  tag: string;
}

interface ManagedBinaryDeps {
  downloadAsset?: (asset: GitHubReleaseAsset, destinationPath: string) => Promise<void>;
  fetchRelease?: (releaseTag?: string) => Promise<GitHubRelease>;
  host?: HostPlatform;
}

const metadataFileName = "managed-binary.json";
const releaseOwner = "ben-ranford";
const releaseRepo = "lopper";

export class ManagedBinaryInstaller {
  private readonly deps: Required<ManagedBinaryDeps>;

  constructor(
    private readonly storageRoot: string,
    private readonly output: { appendLine(value: string): void },
    deps: ManagedBinaryDeps = {},
  ) {
    this.deps = {
      downloadAsset: deps.downloadAsset ?? downloadAsset,
      fetchRelease: deps.fetchRelease ?? fetchRelease,
      host: deps.host ?? { platform: process.platform, arch: process.arch },
    };
  }

  async findInstalledBinary(releaseTag?: string): Promise<string | undefined> {
    const explicitTag = normalizeReleaseTag(releaseTag);
    if (explicitTag) {
      const binaryPath = this.binaryPathFor(explicitTag);
      return (await fileExists(binaryPath)) ? binaryPath : undefined;
    }

    const metadata = await this.readMetadata();
    if (!metadata) {
      return undefined;
    }
    return (await fileExists(metadata.binaryPath)) ? metadata.binaryPath : undefined;
  }

  async ensureInstalled(releaseTag?: string): Promise<ManagedBinaryInstallResult> {
    const cachedBinary = await this.findInstalledBinary(releaseTag);
    if (cachedBinary) {
      const tag = normalizeReleaseTag(releaseTag) ?? (await this.readMetadata())?.tag ?? "unknown";
      return { binaryPath: cachedBinary, tag, downloaded: false };
    }

    const requestedTag = normalizeReleaseTag(releaseTag);
    const release = await this.deps.fetchRelease(requestedTag);
    const asset = selectReleaseAsset(release, this.deps.host);
    const binaryPath = this.binaryPathFor(release.tag_name);
    const tmpDir = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-"));
    const archivePath = path.join(tmpDir, asset.name);
    const extractDir = path.join(tmpDir, "extract");

    this.output.appendLine(`downloading managed lopper binary: ${asset.name}`);

    try {
      await mkdir(extractDir, { recursive: true });
      await this.deps.downloadAsset(asset, archivePath);
      await extractArchive(archivePath, extractDir);

      const extractedBinary = await findBinaryInDirectory(extractDir, binaryFileName(this.deps.host.platform));
      if (!extractedBinary) {
        throw new Error(`downloaded archive did not contain ${binaryFileName(this.deps.host.platform)}`);
      }

      await mkdir(path.dirname(binaryPath), { recursive: true });
      await copyFile(extractedBinary, binaryPath);
      if (this.deps.host.platform !== "win32") {
        await chmod(binaryPath, 0o755);
      }
      await this.writeMetadata({ binaryPath, tag: release.tag_name });

      return { binaryPath, tag: release.tag_name, downloaded: true };
    } finally {
      await rm(tmpDir, { recursive: true, force: true });
    }
  }

  private binaryPathFor(releaseTag: string): string {
    return path.join(
      this.storageRoot,
      "managed-lopper",
      releaseTag,
      `${platformSegment(this.deps.host.platform)}-${archSegment(this.deps.host.arch)}`,
      binaryFileName(this.deps.host.platform),
    );
  }

  private metadataPath(): string {
    return path.join(this.storageRoot, metadataFileName);
  }

  private async readMetadata(): Promise<ManagedBinaryMetadata | undefined> {
    try {
      const raw = await readFile(this.metadataPath(), "utf8");
      const parsed = JSON.parse(raw) as Partial<ManagedBinaryMetadata>;
      if (typeof parsed.binaryPath !== "string" || typeof parsed.tag !== "string") {
        return undefined;
      }
      return { binaryPath: parsed.binaryPath, tag: parsed.tag };
    } catch {
      return undefined;
    }
  }

  private async writeMetadata(metadata: ManagedBinaryMetadata): Promise<void> {
    await mkdir(this.storageRoot, { recursive: true });
    await writeFile(this.metadataPath(), JSON.stringify(metadata, null, 2));
  }
}

export async function findExecutableInPath(command: string, platform = process.platform): Promise<string | undefined> {
  const pathValue = process.env.PATH ?? "";
  const pathEntries = pathValue.split(path.delimiter).filter((entry) => entry.length > 0);
  const candidates = new Set<string>();

  if (platform === "win32") {
    const pathExt = (process.env.PATHEXT ?? ".EXE;.CMD;.BAT;.COM").split(";").filter(Boolean);
    if (path.extname(command)) {
      for (const entry of pathEntries) {
        candidates.add(path.join(entry, command));
      }
    } else {
      for (const entry of pathEntries) {
        for (const extension of pathExt) {
          candidates.add(path.join(entry, `${command}${extension.toLowerCase()}`));
          candidates.add(path.join(entry, `${command}${extension.toUpperCase()}`));
        }
      }
    }
  } else {
    for (const entry of pathEntries) {
      candidates.add(path.join(entry, command));
    }
  }

  for (const candidate of candidates) {
    if (await fileExists(candidate, true)) {
      return candidate;
    }
  }

  return undefined;
}

export function assetNameForRelease(releaseTag: string, host: HostPlatform): string {
  const normalizedTag = normalizeReleaseTag(releaseTag);
  if (!normalizedTag) {
    throw new Error("release tag is required");
  }
  const version = normalizedTag.replace(/^v/, "");
  const extension = host.platform === "win32" ? "zip" : "tar.gz";
  return `lopper_${version}_${platformSegment(host.platform)}_${archSegment(host.arch)}.${extension}`;
}

export function selectReleaseAsset(release: GitHubRelease, host: HostPlatform): GitHubReleaseAsset {
  const expectedName = assetNameForRelease(release.tag_name, host);
  const asset = release.assets.find((item) => item.name === expectedName);
  if (!asset) {
    throw new Error(`release ${release.tag_name} does not contain asset ${expectedName}`);
  }
  return asset;
}

function normalizeReleaseTag(releaseTag?: string): string | undefined {
  const trimmed = releaseTag?.trim();
  return trimmed && trimmed.length > 0 ? trimmed : undefined;
}

async function fetchRelease(releaseTag?: string): Promise<GitHubRelease> {
  const normalizedTag = normalizeReleaseTag(releaseTag);
  const endpoint = normalizedTag
    ? `https://api.github.com/repos/${releaseOwner}/${releaseRepo}/releases/tags/${encodeURIComponent(normalizedTag)}`
    : `https://api.github.com/repos/${releaseOwner}/${releaseRepo}/releases/latest`;

  const response = await fetch(endpoint, {
    headers: {
      Accept: "application/vnd.github+json",
      "User-Agent": "lopper-vscode-extension",
    },
  });
  if (!response.ok) {
    throw new Error(`release lookup failed (${response.status})`);
  }

  const payload = (await response.json()) as Partial<GitHubRelease>;
  if (typeof payload.tag_name !== "string" || !Array.isArray(payload.assets)) {
    throw new Error("release lookup returned an unexpected payload");
  }
  return {
    tag_name: payload.tag_name,
    assets: payload.assets.filter(isAssetLike),
  };
}

function isAssetLike(asset: unknown): asset is GitHubReleaseAsset {
  return typeof asset === "object" &&
    asset !== null &&
    typeof (asset as Partial<GitHubReleaseAsset>).name === "string" &&
    typeof (asset as Partial<GitHubReleaseAsset>).browser_download_url === "string";
}

async function downloadAsset(asset: GitHubReleaseAsset, destinationPath: string): Promise<void> {
  const response = await fetch(asset.browser_download_url, {
    headers: {
      "User-Agent": "lopper-vscode-extension",
    },
  });
  if (!response.ok) {
    throw new Error(`asset download failed (${response.status})`);
  }

  const archiveBytes = Buffer.from(await response.arrayBuffer());
  await writeFile(destinationPath, archiveBytes);
}

async function extractArchive(archivePath: string, extractDir: string): Promise<void> {
  if (archivePath.endsWith(".zip")) {
    const zip = new AdmZip(archivePath);
    zip.extractAllTo(extractDir, true);
    return;
  }
  if (archivePath.endsWith(".tar.gz")) {
    await tar.x({ file: archivePath, cwd: extractDir, gzip: true });
    return;
  }
  throw new Error(`unsupported archive type for ${path.basename(archivePath)}`);
}

async function findBinaryInDirectory(rootDir: string, fileName: string): Promise<string | undefined> {
  const entries = await readdir(rootDir, { withFileTypes: true });
  for (const entry of entries) {
    const candidatePath = path.join(rootDir, entry.name);
    if (entry.isDirectory()) {
      const nested = await findBinaryInDirectory(candidatePath, fileName);
      if (nested) {
        return nested;
      }
      continue;
    }
    if (entry.isFile() && entry.name === fileName) {
      return candidatePath;
    }
  }
  return undefined;
}

async function fileExists(targetPath: string, requireExecutable = false): Promise<boolean> {
  try {
    await access(targetPath, requireExecutable ? fsConstants.X_OK : fsConstants.F_OK);
    const entry = await stat(targetPath);
    return entry.isFile();
  } catch {
    return false;
  }
}

function binaryFileName(platform: NodeJS.Platform): string {
  return platform === "win32" ? "lopper.exe" : "lopper";
}

function platformSegment(platform: NodeJS.Platform): string {
  switch (platform) {
    case "linux":
      return "linux";
    case "darwin":
      return "darwin";
    case "win32":
      return "windows";
    default:
      throw new Error(`managed downloads are not supported on platform ${platform}`);
  }
}

function archSegment(arch: string): string {
  switch (arch) {
    case "x64":
      return "amd64";
    case "arm64":
      return "arm64";
    default:
      throw new Error(`managed downloads are not supported on architecture ${arch}`);
  }
}
