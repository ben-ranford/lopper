import * as assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

import {
  canonicalizeExistingPath,
  isPathInsideWorkspace,
  isResolvedPathInsideWorkspace,
  workspaceAncestorDirectories,
} from "../../workspacePaths";

suite("workspace paths", () => {
  test("uses canonical paths for containment and bounded ancestors", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-workspace-paths-"));
    const workspaceRoot = path.join(tempRoot, "workspace");
    const sourceFile = path.join(workspaceRoot, "app", "src", "Main.kt");
    const dotPrefixedChild = path.join(workspaceRoot, "..notes", "readme.md");
    const outsideRoot = path.join(tempRoot, "outside");

    try {
      await mkdir(path.dirname(sourceFile), { recursive: true });
      await mkdir(path.dirname(dotPrefixedChild), { recursive: true });
      await mkdir(outsideRoot, { recursive: true });
      await writeFile(sourceFile, "class Main", "utf8");
      await writeFile(dotPrefixedChild, "notes", "utf8");
      await symlink(outsideRoot, path.join(workspaceRoot, "outside-link"));

      assert.equal(isPathInsideWorkspace(sourceFile, workspaceRoot), true);
      assert.equal(isPathInsideWorkspace(dotPrefixedChild, workspaceRoot), true);
      assert.equal(isPathInsideWorkspace(path.join(workspaceRoot, "outside-link", "Main.kt"), workspaceRoot), false);
      assert.equal(
        isResolvedPathInsideWorkspace(path.join(workspaceRoot, "outside-link", "Main.kt"), workspaceRoot),
        true,
      );
      const canonicalWorkspaceRoot = canonicalizeExistingPath(workspaceRoot);
      assert.ok(canonicalWorkspaceRoot);
      assert.deepEqual(
        workspaceAncestorDirectories(sourceFile, workspaceRoot, 2),
        [path.join(canonicalWorkspaceRoot, "app", "src"), path.join(canonicalWorkspaceRoot, "app")],
      );
      assert.equal(workspaceAncestorDirectories(path.join(outsideRoot, "Main.kt"), workspaceRoot, 2), undefined);
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });
});
