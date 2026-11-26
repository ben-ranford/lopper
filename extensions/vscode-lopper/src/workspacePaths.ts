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

// A missing leaf can resolve through its nearest existing parent.
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

export function workspaceRelativeAncestorDirectories(
  candidatePath: string,
  workspaceRoot: string,
  maxAncestors: number,
): string[] | undefined {
  const relativePath = path.relative(workspaceRoot, candidatePath);
  if (relativePath === "" || !isRelativePathInsideWorkspace(relativePath)) {
    return undefined;
  }

  const ancestors: string[] = [];
  let currentDirectory = path.dirname(relativePath);
  for (let depth = 0; depth < maxAncestors; depth++) {
    ancestors.push(currentDirectory);
    if (currentDirectory === ".") {
      break;
    }
    currentDirectory = path.dirname(currentDirectory);
  }
  return ancestors;
}

function isPathInside(candidatePath: string, workspaceRoot: string): boolean {
  return isRelativePathInsideWorkspace(path.relative(workspaceRoot, candidatePath));
}

function isRelativePathInsideWorkspace(relativePath: string): boolean {
  return relativePath !== ".." && !relativePath.startsWith(`..${path.sep}`) && !path.isAbsolute(relativePath);
}
