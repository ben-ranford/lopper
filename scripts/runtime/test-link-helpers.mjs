import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const FILE_SYMLINK_CAPABILITY = Symbol("file-symlink-capability");

function isSkippableSymlinkError(error) {
  if (!error || typeof error !== "object") {
    return false;
  }
  return error.code === "EPERM" || error.code === "EACCES";
}

function createSkipError(kind, error) {
  const reason =
    process.platform === "win32"
      ? `${kind} links require symlink privileges or Developer Mode on Windows`
      : `unable to create ${kind} link in test fixture`;
  const skipError = new Error(reason);
  skipError.code = "ERR_TEST_SKIP";
  skipError.cause = error;
  return skipError;
}

function createDirectoryLinkSync(targetPath, linkPath) {
  fs.symlinkSync(targetPath, linkPath, process.platform === "win32" ? "junction" : "dir");
}

function canCreateFileSymlink() {
  if (canCreateFileSymlink[FILE_SYMLINK_CAPABILITY] !== undefined) {
    return canCreateFileSymlink[FILE_SYMLINK_CAPABILITY];
  }

  const probeRoot = fs.mkdtempSync(path.join(os.tmpdir(), "lopper-file-symlink-"));
  const targetPath = path.join(probeRoot, "target.txt");
  const linkPath = path.join(probeRoot, "link.txt");
  try {
    fs.writeFileSync(targetPath, "ok\n", "utf8");
    fs.symlinkSync(targetPath, linkPath);
    canCreateFileSymlink[FILE_SYMLINK_CAPABILITY] = true;
  } catch (error) {
    if (isSkippableSymlinkError(error)) {
      canCreateFileSymlink[FILE_SYMLINK_CAPABILITY] = false;
    } else {
      throw error;
    }
  } finally {
    fs.rmSync(probeRoot, { recursive: true, force: true });
  }

  return canCreateFileSymlink[FILE_SYMLINK_CAPABILITY];
}

function createFileLinkSync(targetPath, linkPath) {
  if (!canCreateFileSymlink()) {
    throw createSkipError("file", Object.assign(new Error("file symlink unsupported"), { code: "EPERM" }));
  }
  try {
    fs.symlinkSync(targetPath, linkPath);
  } catch (error) {
    if (isSkippableSymlinkError(error)) {
      throw createSkipError("file", error);
    }
    throw error;
  }
}

function removeDirectoryLinkSync(linkPath) {
  if (process.platform === "win32") {
    fs.rmdirSync(linkPath);
    return;
  }
  fs.unlinkSync(linkPath);
}

function skipIfLinkUnsupported(t, error) {
  if (error?.code === "ERR_TEST_SKIP") {
    t.skip(error.message);
    return true;
  }
  return false;
}

export { createDirectoryLinkSync, createFileLinkSync, removeDirectoryLinkSync, skipIfLinkUnsupported };
