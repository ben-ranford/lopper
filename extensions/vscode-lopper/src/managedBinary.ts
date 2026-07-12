import AdmZip from "adm-zip";
import { createHash } from "node:crypto";
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
  digest?: string;
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

export const defaultManagedBinaryHttpTimeoutMs = 30_000;

export class BinaryResolutionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BinaryResolutionError";
  }
}

interface LopperBinaryLifecycleDeps {
  canonicalizePath?: (targetPath: string) => Promise<string>;
}

export interface BinaryResolutionRequest {
  workspaceRoot: string;
  workspaceRoots?: readonly string[];
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
  ensureInstalled(releaseTag?: string, signal?: AbortSignal): Promise<ManagedBinaryInstallResult>;
}

export interface ManagedBinaryInstallProgress {
  install(
    releaseTag: string | undefined,
    install: (signal?: AbortSignal) => Promise<ManagedBinaryInstallResult>,
  ): Promise<ManagedBinaryInstallResult>;
}

interface ManagedBinaryMetadata {
  binaryPath: string;
  tag: string;
  binaryDigest?: string;
}

export interface ManagedBinaryDeps {
  downloadAsset?: (asset: GitHubReleaseAsset, destinationPath: string, signal?: AbortSignal) => Promise<void>;
  fetchRelease?: (releaseTag?: string, signal?: AbortSignal) => Promise<GitHubRelease>;
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
    const metadata = await this.readMetadata();
    const binaryPath = explicitTag ? this.binaryPathFor(explicitTag) : metadata?.binaryPath;

    if (!binaryPath || !(await fileExists(binaryPath))) {
      return undefined;
    }

    if (
      metadata?.binaryPath !== binaryPath ||
      metadata?.tag !== (explicitTag ?? metadata?.tag) ||
      typeof metadata?.binaryDigest !== "string"
    ) {
      this.output.appendLine(`managed binary cache integrity metadata missing; re-downloading ${binaryPath}`);
      return undefined;
    }

    if (!(await verifyBinaryDigest(binaryPath, metadata.binaryDigest))) {
      this.output.appendLine(`managed binary integrity check failed; re-downloading ${binaryPath}`);
      return undefined;
    }
    return binaryPath;
  }

  async ensureInstalled(releaseTag?: string, signal?: AbortSignal): Promise<ManagedBinaryInstallResult> {
    throwIfAborted(signal);
    const cachedBinary = await this.findInstalledBinary(releaseTag);
    if (cachedBinary) {
      const tag = normalizeReleaseTag(releaseTag) ?? (await this.readMetadata())?.tag ?? "unknown";
      return { binaryPath: cachedBinary, tag, downloaded: false };
    }

    const requestedTag = normalizeReleaseTag(releaseTag);
    const release = await this.deps.fetchRelease(requestedTag, signal);
    throwIfAborted(signal);
    const asset = selectReleaseAsset(release, this.deps.host);
    const binaryPath = this.binaryPathFor(release.tag_name);
    const tmpDir = await mkdtemp(path.join(os.tmpdir(), "lopper-managed-binary-"));
    const archivePath = path.join(tmpDir, asset.name);
    const extractDir = path.join(tmpDir, "extract");

    this.output.appendLine(`downloading managed lopper binary: ${asset.name}`);

    try {
      const expectedArchiveDigest = parseAssetDigest(asset);
      await mkdir(extractDir, { recursive: true });
      await this.deps.downloadAsset(asset, archivePath, signal);
      throwIfAborted(signal);
      await verifyBinaryArchive(archivePath, expectedArchiveDigest, asset.name);
      throwIfAborted(signal);
      await extractArchive(archivePath, extractDir);
      throwIfAborted(signal);

      const extractedBinary = await findBinaryInDirectory(extractDir, binaryFileName(this.deps.host.platform));
      if (!extractedBinary) {
        throw new Error(`downloaded archive did not contain ${binaryFileName(this.deps.host.platform)}`);
      }

      await mkdir(path.dirname(binaryPath), { recursive: true });
      await copyFile(extractedBinary, binaryPath);
      if (this.deps.host.platform !== "win32") {
        await chmod(binaryPath, 0o755);
      }
      const binaryDigest = await sha256File(binaryPath);
      await this.writeMetadata({ binaryPath, tag: release.tag_name, binaryDigest });

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
      return {
        binaryPath: parsed.binaryPath,
        tag: parsed.tag,
        binaryDigest: typeof parsed.binaryDigest === "string" ? parsed.binaryDigest : undefined,
      };
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
  private readonly canonicalizePath: (targetPath: string) => Promise<string>;

  constructor(
    private readonly installer: ManagedBinaryInstallerClient,
    private readonly output: { appendLine(value: string): void },
    private readonly progress: ManagedBinaryInstallProgress = passthroughManagedInstallProgress,
    private readonly platform: NodeJS.Platform = process.platform,
    deps: LopperBinaryLifecycleDeps = {},
  ) {
    this.canonicalizePath = deps.canonicalizePath ?? canonicalPath;
  }

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
      await this.ensureWorkspaceTrustedForBinary(
        binaryPath,
        workspaceRootsForRequest(request),
        "LOPPER_BINARY_PATH",
        request.workspaceTrusted,
      );
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
    await this.ensureWorkspaceTrustedForBinary(
      binaryPath,
      workspaceRootsForRequest(request),
      "lopper.binaryPath",
      request.workspaceTrusted,
    );
    return binaryPath;
  }

  private async resolveLocalBinaryPath(request: BinaryResolutionRequest): Promise<string | undefined> {
    const localBinary = path.join(request.workspaceRoot, "bin", binaryFileName(this.platform));
    const localBinaryStats = await this.localBinaryStats(localBinary);
    if (!localBinaryStats) {
      return this.resolvePathBinary(request);
    }

    if (!request.workspaceTrusted) {
      this.output.appendLine(`skipping workspace-local lopper binary in untrusted workspace: ${localBinary}`);
      return this.resolvePathBinary(request);
    }

    if (!localBinaryStats.isFile()) {
      throw new BinaryResolutionError(`workspace-local lopper binary must point to a file: ${localBinary}`);
    }

    await this.ensureWorkspaceLocalBinaryExecutable(localBinary);

    return localBinary;
  }

  private async resolvePathBinary(request: BinaryResolutionRequest): Promise<string | undefined> {
    if (request.workspaceTrusted) {
      return findExecutableInPath(binaryFileName(this.platform), this.platform);
    }

    return findExecutableInPath(binaryFileName(this.platform), this.platform, async (candidatePath) => {
      if (!(await this.isWorkspaceLocalBinary(candidatePath, workspaceRootsForRequest(request)))) {
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

    const installResult = await this.progress.install(releaseTag, async (signal) => this.installer.ensureInstalled(releaseTag, signal));
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

  private async localBinaryStats(localBinaryPath: string) {
    try {
      return await stat(localBinaryPath);
    } catch (error) {
      if (isFileMissingError(error)) {
        return undefined;
      }
      throw new BinaryResolutionError(
        `failed to access workspace-local lopper binary: ${localBinaryPath}${formatErrorDetail(error)}`,
      );
    }
  }

  private async ensureWorkspaceLocalBinaryExecutable(localBinaryPath: string): Promise<void> {
    try {
      if (this.platform === "win32") {
        // On Windows, executability is inferred from .exe and file accessibility.
        await access(localBinaryPath);
      } else {
        // On POSIX, require execute permission.
        await access(localBinaryPath, fsConstants.X_OK);
      }
    } catch {
      throw new BinaryResolutionError(`workspace-local lopper binary is not executable: ${localBinaryPath}`);
    }
  }

  private async ensureWorkspaceTrustedForBinary(
    binaryPath: string,
    workspaceRoots: readonly string[],
    source: string,
    workspaceTrusted: boolean,
  ): Promise<void> {
    if (workspaceTrusted || !(await this.isWorkspaceLocalBinary(binaryPath, workspaceRoots))) {
      return;
    }

    throw new BinaryResolutionError(
      `${source} points to a workspace-local binary in an untrusted workspace. Trust this workspace or use a binary outside the workspace.`,
    );
  }

  private async isWorkspaceLocalBinary(binaryPath: string, workspaceRoots: readonly string[]): Promise<boolean> {
    try {
      const [canonicalBinaryPath, ...canonicalWorkspaceRoots] = await Promise.all([
        this.canonicalizePath(binaryPath),
        ...workspaceRoots.map((workspaceRoot) => this.canonicalizePath(workspaceRoot)),
      ]);
      return canonicalWorkspaceRoots.some((workspaceRoot) => isPathInsideWorkspace(canonicalBinaryPath, workspaceRoot));
    } catch (error) {
      const detail = error instanceof Error && error.message ? `: ${error.message}` : "";
      this.output.appendLine(
        `treating lopper binary as workspace-local because path canonicalization failed while checking ${binaryPath} against open workspace roots ${workspaceRoots.join(", ")}${detail}`,
      );
      return true;
    }
  }
}

function workspaceRootsForRequest(request: BinaryResolutionRequest): string[] {
  return Array.from(new Set([request.workspaceRoot, ...(request.workspaceRoots ?? [])]));
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
  const extension = host.platform === "win32" ? "zip" : "tar.gz";
  return `lopper_${normalizedTag}_${platformSegment(host.platform)}_${archSegment(host.arch)}.${extension}`;
}

export function selectReleaseAsset(release: GitHubRelease, host: HostPlatform): GitHubReleaseAsset {
  const expectedNames = Array.from(new Set(assetNameCandidates(release.tag_name, host)));
  for (const expectedName of expectedNames) {
    const asset = release.assets.find((item) => item.name === expectedName);
    if (asset) {
      return asset;
    }
  }

  const rendered = expectedNames.length === 1 ? expectedNames[0] : `${expectedNames.join(" or ")}`;
  throw new Error(`release ${release.tag_name} does not contain expected asset ${rendered}`);
}

function assetNameCandidates(releaseTag: string, host: HostPlatform): string[] {
	const normalizedTag = normalizeReleaseTag(releaseTag);
	if (!normalizedTag) {
		throw new Error("release tag is required");
	}
	const expectedName = assetNameForRelease(normalizedTag, host);

	if (!normalizedTag.startsWith("v")) {
		return [expectedName];
	}

  return [expectedName, assetNameForRelease(normalizedTag.substring(1), host)];
}

function normalizeReleaseTag(releaseTag?: string): string | undefined {
  const trimmed = releaseTag?.trim();
  return trimmed && trimmed.length > 0 ? trimmed : undefined;
}

export async function fetchRelease(
  releaseTag?: string,
  signal?: AbortSignal,
  timeoutMs = defaultManagedBinaryHttpTimeoutMs,
): Promise<GitHubRelease> {
  const normalizedTag = normalizeReleaseTag(releaseTag);
  const endpoint = normalizedTag
    ? `https://api.github.com/repos/${releaseOwner}/${releaseRepo}/releases/tags/${encodeURIComponent(normalizedTag)}`
    : `https://api.github.com/repos/${releaseOwner}/${releaseRepo}/releases/latest`;

  const abortable = abortSignalWithTimeout(signal, timeoutMs);
  try {
    const response = await fetch(endpoint, {
      headers: {
        Accept: "application/vnd.github+json",
        "User-Agent": "lopper-vscode-extension",
      },
      signal: abortable.signal,
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
  } catch (error) {
    throw managedBinaryHttpError(error, abortable, "release lookup");
  } finally {
    abortable.dispose();
  }
}

function isAssetLike(asset: unknown): asset is GitHubReleaseAsset {
  return typeof asset === "object" &&
    asset !== null &&
    typeof (asset as Partial<GitHubReleaseAsset>).name === "string" &&
    typeof (asset as Partial<GitHubReleaseAsset>).browser_download_url === "string";
}

function parseAssetDigest(asset: GitHubReleaseAsset): string {
  if (typeof asset.digest !== "string") {
    throw new TypeError(`managed release asset ${asset.name} is missing a string sha256 digest`);
  }

  const match = /^sha256:([a-fA-F0-9]{64})$/.exec(asset.digest.trim());
  if (!match) {
    throw new Error(`managed release asset ${asset.name} has invalid sha256 digest`);
  }

  return match[1].toLowerCase();
}

async function verifyBinaryArchive(archivePath: string, expectedDigest: string, assetName: string): Promise<void> {
  const observedDigest = await sha256File(archivePath);
  if (observedDigest !== expectedDigest) {
    throw new Error(`managed release archive integrity check failed for ${assetName}`);
  }
}

async function verifyBinaryDigest(filePath: string, expectedDigest: string): Promise<boolean> {
  return (await sha256File(filePath)) === expectedDigest;
}

async function sha256File(filePath: string): Promise<string> {
  const buffer = await readFile(filePath);
  return createHash("sha256").update(buffer).digest("hex");
}

export async function downloadAsset(
  asset: GitHubReleaseAsset,
  destinationPath: string,
  signal?: AbortSignal,
  timeoutMs = defaultManagedBinaryHttpTimeoutMs,
): Promise<void> {
  const abortable = abortSignalWithTimeout(signal, timeoutMs);
  try {
    const response = await fetch(asset.browser_download_url, {
      headers: {
        "User-Agent": "lopper-vscode-extension",
      },
      signal: abortable.signal,
    });
    if (!response.ok) {
      throw new Error(`asset download failed (${response.status})`);
    }

    const archiveBytes = Buffer.from(await response.arrayBuffer());
    await writeFile(destinationPath, archiveBytes);
  } catch (error) {
    throw managedBinaryHttpError(error, abortable, "asset download");
  } finally {
    abortable.dispose();
  }
}

interface AbortableSignal {
  readonly signal: AbortSignal;
  readonly timeoutMs: number | undefined;
  didTimeout(): boolean;
  dispose(): void;
}

function abortSignalWithTimeout(parentSignal: AbortSignal | undefined, timeoutMs: number | undefined): AbortableSignal {
  const controller = new AbortController();
  let timedOut = false;
  let timeout: ReturnType<typeof setTimeout> | undefined;

  const abortFromParent = () => controller.abort();
  if (parentSignal?.aborted) {
    abortFromParent();
  } else {
    parentSignal?.addEventListener("abort", abortFromParent, { once: true });
  }

  if (timeoutMs !== undefined && timeoutMs > 0) {
    timeout = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
  }

  return {
    signal: controller.signal,
    timeoutMs,
    didTimeout: () => timedOut,
    dispose: () => {
      if (timeout) {
        clearTimeout(timeout);
      }
      parentSignal?.removeEventListener("abort", abortFromParent);
    },
  };
}

function managedBinaryHttpError(error: unknown, abortable: AbortableSignal, operation: string): Error {
  if (abortable.didTimeout()) {
    return new Error(`${operation} timed out after ${abortable.timeoutMs}ms`);
  }
  if (abortable.signal.aborted || isAbortError(error)) {
    return new Error(`${operation} cancelled`);
  }
  return error instanceof Error ? error : new Error(String(error));
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

function throwIfAborted(signal: AbortSignal | undefined): void {
  if (signal?.aborted) {
    throw new Error("managed binary install cancelled");
  }
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
  let rejectedEntryError: Error | undefined;
  await tar.x({
    file: archivePath,
    cwd: extractDir,
    gzip: true,
    preservePaths: false,
    strict: true,
    filter: (entryPath) => {
      try {
        archiveDestinationPath(extractDir, entryPath);
        return true;
      } catch (error) {
        rejectedEntryError ??= error instanceof Error ? error : new Error(String(error));
        return false;
      }
    },
  });
  if (rejectedEntryError) {
    throw rejectedEntryError;
  }
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
  return realpath(targetPath);
}

function isFileMissingError(error: unknown): boolean {
  return Boolean(
    error &&
      typeof error === "object" &&
      "code" in error &&
      (error as NodeJS.ErrnoException).code === "ENOENT",
  );
}

function formatErrorDetail(error: unknown): string {
  return error instanceof Error && error.message ? `: ${error.message}` : "";
}
