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
  realpath,
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

export class BinaryResolutionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BinaryResolutionError";
  }
}

export interface BinaryResolutionRequest {
  workspaceRoot: string;
  workspaceTrusted: boolean;
  autoDownloadBinary: boolean;
  envBinaryPath?: string;
  configuredBinaryPath?: string;
  managedBinaryTag?: string;
}

export interface BinaryLifecycleManager {
  resolveBinaryPath(request: BinaryResolutionRequest): Promise<string>;
}

export interface ManagedBinaryInstallerClient {
  findInstalledBinary(releaseTag?: string): Promise<string | undefined>;
  ensureInstalled(releaseTag?: string): Promise<ManagedBinaryInstallResult>;
}

export interface ManagedBinaryInstallProgress {
  install(
    releaseTag: string | undefined,
    install: () => Promise<ManagedBinaryInstallResult>,
  ): Promise<ManagedBinaryInstallResult>;
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

const passthroughManagedInstallProgress: ManagedBinaryInstallProgress = {
  install: async (_releaseTag, install) => install(),
};

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

export class LopperBinaryLifecycleManager implements BinaryLifecycleManager {
  constructor(
    private readonly installer: ManagedBinaryInstallerClient,
    private readonly output: { appendLine(value: string): void },
    private readonly progress: ManagedBinaryInstallProgress = passthroughManagedInstallProgress,
    private readonly platform: NodeJS.Platform = process.platform,
  ) {}

  async resolveBinaryPath(request: BinaryResolutionRequest): Promise<string> {
    const configuredBinaryPath = await this.resolveConfiguredBinaryPath(request);
    if (configuredBinaryPath) {
      return configuredBinaryPath;
    }

    const localBinaryPath = await this.resolveLocalBinaryPath(request);
    if (localBinaryPath) {
      return localBinaryPath;
    }

    return this.resolveManagedBinaryPath(request);
  }

  private async resolveConfiguredBinaryPath(request: BinaryResolutionRequest): Promise<string | undefined> {
    const envBinaryPath = request.envBinaryPath?.trim();
    if (envBinaryPath) {
      const binaryPath = await this.ensureConfiguredBinaryExists(envBinaryPath, "LOPPER_BINARY_PATH");
      await this.ensureWorkspaceTrustedForBinary(binaryPath, request.workspaceRoot, "LOPPER_BINARY_PATH", request.workspaceTrusted);
      return binaryPath;
    }

    const configuredBinaryPath = request.configuredBinaryPath?.trim();
    if (!configuredBinaryPath) {
      return undefined;
    }

    const resolvedPath = path.isAbsolute(configuredBinaryPath)
      ? configuredBinaryPath
      : path.join(request.workspaceRoot, configuredBinaryPath);
    const binaryPath = await this.ensureConfiguredBinaryExists(resolvedPath, "lopper.binaryPath");
    await this.ensureWorkspaceTrustedForBinary(binaryPath, request.workspaceRoot, "lopper.binaryPath", request.workspaceTrusted);
    return binaryPath;
  }

  private async resolveLocalBinaryPath(request: BinaryResolutionRequest): Promise<string | undefined> {
    const localBinary = path.join(request.workspaceRoot, "bin", binaryFileName(this.platform));
    try {
      const fileStat = await stat(localBinary);
      if (!fileStat.isFile()) {
        throw new Error("Local lopper binary is not a regular file");
      }
      if (this.platform === "win32") {
        // On Windows, executability is inferred from .exe and file accessibility.
        await access(localBinary);
      } else {
        // On POSIX, require execute permission.
        await access(localBinary, fsConstants.X_OK);
      }
      if (!request.workspaceTrusted) {
        this.output.appendLine(`skipping workspace-local lopper binary in untrusted workspace: ${localBinary}`);
        return this.resolvePathBinary(request);
      }
      return localBinary;
    } catch {
      return this.resolvePathBinary(request);
    }
  }

  private async resolvePathBinary(request: BinaryResolutionRequest): Promise<string | undefined> {
    if (request.workspaceTrusted) {
      return findExecutableInPath(binaryFileName(this.platform), this.platform);
    }

    return findExecutableInPath(binaryFileName(this.platform), this.platform, async (candidatePath) => {
      if (!(await this.isWorkspaceLocalBinary(candidatePath, request.workspaceRoot))) {
        return true;
      }

      this.output.appendLine(`skipping workspace-local lopper binary from PATH in untrusted workspace: ${candidatePath}`);
      return false;
    });
  }

  private async resolveManagedBinaryPath(request: BinaryResolutionRequest): Promise<string> {
    if (!request.autoDownloadBinary) {
      throw new BinaryResolutionError(
        "Lopper binary not found. Install it manually, set lopper.binaryPath or LOPPER_BINARY_PATH, or enable lopper.autoDownloadBinary.",
      );
    }

    const releaseTag = normalizeReleaseTag(request.managedBinaryTag);
    const cachedBinary = await this.installer.findInstalledBinary(releaseTag);
    if (cachedBinary) {
      return cachedBinary;
    }

    const installResult = await this.progress.install(releaseTag, async () => this.installer.ensureInstalled(releaseTag));
    if (installResult.downloaded) {
      this.output.appendLine(`managed lopper binary installed: ${installResult.binaryPath} (${installResult.tag})`);
    }
    return installResult.binaryPath;
  }

  private async ensureConfiguredBinaryExists(binaryPath: string, source: string): Promise<string> {
    const fileStats = await this.configuredBinaryStats(binaryPath, source);
    if (!fileStats.isFile()) {
      throw new BinaryResolutionError(`${source} must point to a file: ${binaryPath}`);
    }
    if (this.platform !== "win32") {
      await this.ensureConfiguredBinaryExecutable(binaryPath, source);
    }
    return binaryPath;
  }

  private async configuredBinaryStats(binaryPath: string, source: string) {
    try {
      return await stat(binaryPath);
    } catch {
      throw new BinaryResolutionError(`${source} points to a missing binary: ${binaryPath}`);
    }
  }

  private async ensureConfiguredBinaryExecutable(binaryPath: string, source: string): Promise<void> {
    try {
      await access(binaryPath, fsConstants.X_OK);
    } catch {
      throw new BinaryResolutionError(`${source} points to a non-executable file: ${binaryPath}`);
    }
  }

  private async ensureWorkspaceTrustedForBinary(
    binaryPath: string,
    workspaceRoot: string,
    source: string,
    workspaceTrusted: boolean,
  ): Promise<void> {
    if (workspaceTrusted || !(await this.isWorkspaceLocalBinary(binaryPath, workspaceRoot))) {
      return;
    }

    throw new BinaryResolutionError(
      `${source} points to a workspace-local binary in an untrusted workspace. Trust this workspace or use a binary outside the workspace.`,
    );
  }

  private async isWorkspaceLocalBinary(binaryPath: string, workspaceRoot: string): Promise<boolean> {
    const [canonicalBinaryPath, canonicalWorkspaceRoot] = await Promise.all([
      canonicalPath(binaryPath),
      canonicalPath(workspaceRoot),
    ]);
    return isPathInsideWorkspace(canonicalBinaryPath, canonicalWorkspaceRoot);
  }
}

export async function findExecutableInPath(
  command: string,
  platform = process.platform,
  candidateAllowed?: (candidatePath: string) => Promise<boolean>,
): Promise<string | undefined> {
  for (const candidate of executableCandidates(command, platform)) {
    if (!(await fileExists(candidate, true))) {
      continue;
    }
    if (candidateAllowed && !(await candidateAllowed(candidate))) {
      continue;
    }
    return candidate;
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
    throw new TypeError("release lookup returned an unexpected payload");
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
    await extractZipArchive(archivePath, extractDir);
    return;
  }
  if (archivePath.endsWith(".tar.gz")) {
    await extractTarArchive(archivePath, extractDir);
    return;
  }
  throw new Error(`unsupported archive type for ${path.basename(archivePath)}`);
}

function executableCandidates(command: string, platform: NodeJS.Platform): string[] {
  const pathEntries = (process.env.PATH ?? "").split(path.delimiter).filter((entry) => entry.length > 0);
  if (platform !== "win32") {
    return pathEntries.map((entry) => path.join(entry, command));
  }

  if (path.extname(command)) {
    return pathEntries.map((entry) => path.join(entry, command));
  }

  const pathExtensions = (process.env.PATHEXT ?? ".EXE;.CMD;.BAT;.COM").split(";").filter(Boolean);
  const candidates = new Set<string>();
  for (const entry of pathEntries) {
    for (const extension of pathExtensions) {
      candidates.add(path.join(entry, `${command}${extension.toLowerCase()}`));
      candidates.add(path.join(entry, `${command}${extension.toUpperCase()}`));
    }
  }
  return Array.from(candidates);
}

async function extractZipArchive(archivePath: string, extractDir: string): Promise<void> {
  const zip = new AdmZip(archivePath);
  for (const entry of zip.getEntries()) {
    const destinationPath = archiveDestinationPath(extractDir, entry.entryName);
    if (entry.isDirectory) {
      await mkdir(destinationPath, { recursive: true });
      continue;
    }
    await mkdir(path.dirname(destinationPath), { recursive: true });
    await writeFile(destinationPath, entry.getData());
  }
}

async function extractTarArchive(archivePath: string, extractDir: string): Promise<void> {
  await tar.t({
    file: archivePath,
    gzip: true,
    onentry: (entry) => {
      archiveDestinationPath(extractDir, entry.path);
    },
  });

  await tar.x({
    file: archivePath,
    cwd: extractDir,
    gzip: true,
    filter: (entryPath) => {
      archiveDestinationPath(extractDir, entryPath);
      return true;
    },
  });
}

export function archiveDestinationPath(rootDir: string, entryName: string): string {
  const normalizedEntry = entryName.replaceAll("\\", "/");
  if (normalizedEntry.length === 0) {
    throw new Error("archive entry path cannot be empty");
  }
  if (path.posix.isAbsolute(normalizedEntry)) {
    throw new Error(`archive entry ${entryName} must be relative`);
  }

  const resolvedRoot = path.resolve(rootDir);
  const destinationPath = path.resolve(resolvedRoot, normalizedEntry);
  if (destinationPath !== resolvedRoot && !destinationPath.startsWith(`${resolvedRoot}${path.sep}`)) {
    throw new Error(`archive entry ${entryName} escapes the extraction directory`);
  }
  return destinationPath;
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

function isPathInsideWorkspace(candidatePath: string, workspaceRoot: string): boolean {
  const relativePath = path.relative(path.resolve(workspaceRoot), path.resolve(candidatePath));
  return relativePath === "" || (!relativePath.startsWith("..") && !path.isAbsolute(relativePath));
}

async function canonicalPath(targetPath: string): Promise<string> {
  try {
    return await realpath(targetPath);
  } catch {
    return path.resolve(targetPath);
  }
}
