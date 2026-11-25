import { realpathSync } from "node:fs";
import * as path from "node:path";

export function canonicalizeExistingPath(targetPath: string): string | undefined {
  const pendingSegments: string[] = [];
  let currentPath = path.resolve(targetPath);

  while (true) {
    try {
      const canonicalBasePath = realpathSync.native(currentPath);
      return pendingSegments.length === 0 ? canonicalBasePath : path.join(canonicalBasePath, ...pendingSegments);
    } catch {
      const parentPath = path.dirname(currentPath);
      if (parentPath === currentPath) {
        return undefined;
      }
      pendingSegments.unshift(path.basename(currentPath));
      currentPath = parentPath;
    }
  }
}

export function isPathInsideWorkspace(candidatePath: string, workspaceRoot: string): boolean {
  const canonicalWorkspaceRoot = canonicalizeExistingPath(workspaceRoot);
  const canonicalCandidatePath = canonicalizeExistingPath(candidatePath);
  return Boolean(
    canonicalWorkspaceRoot
      && canonicalCandidatePath
      && isPathInside(canonicalCandidatePath, canonicalWorkspaceRoot),
  );
}

export function isResolvedPathInsideWorkspace(candidatePath: string, workspaceRoot: string): boolean {
  return isPathInside(path.resolve(candidatePath), path.resolve(workspaceRoot));
}

export function resolveWorkspaceFilePath(workspaceRoot: string, relativePath: string): string | undefined {
  const resolvedWorkspaceRoot = path.resolve(workspaceRoot);
  const candidatePath = path.resolve(resolvedWorkspaceRoot, relativePath);
  return isPathInsideWorkspace(candidatePath, resolvedWorkspaceRoot) ? candidatePath : undefined;
}

export function isSameWorkspacePath(leftPath: string, rightPath: string): boolean {
  const canonicalLeftPath = canonicalizeExistingPath(leftPath);
  const canonicalRightPath = canonicalizeExistingPath(rightPath);
  if (canonicalLeftPath && canonicalRightPath) {
    return canonicalLeftPath === canonicalRightPath;
  }
  return path.resolve(leftPath) === path.resolve(rightPath);
}

export function workspaceAncestorDirectories(
  candidatePath: string,
  workspaceRoot: string,
  maxAncestors: number,
): string[] | undefined {
  const canonicalWorkspaceRoot = canonicalizeExistingPath(workspaceRoot);
  const canonicalCandidatePath = canonicalizeExistingPath(candidatePath);
  if (!canonicalWorkspaceRoot || !canonicalCandidatePath || !isPathInside(canonicalCandidatePath, canonicalWorkspaceRoot)) {
    return undefined;
  }

  const ancestors: string[] = [];
  let currentDirectory = path.dirname(canonicalCandidatePath);
  for (let depth = 0; depth < maxAncestors; depth++) {
    ancestors.push(currentDirectory);
    if (currentDirectory === canonicalWorkspaceRoot) {
      break;
    }
    const parentDirectory = path.dirname(currentDirectory);
    if (parentDirectory === currentDirectory) {
      break;
    }
    currentDirectory = parentDirectory;
  }
  return ancestors;
}

function isPathInside(candidatePath: string, workspaceRoot: string): boolean {
  const relativePath = path.relative(workspaceRoot, candidatePath);
  return relativePath === "" || (relativePath !== ".." && !relativePath.startsWith(`..${path.sep}`) && !path.isAbsolute(relativePath));
}
