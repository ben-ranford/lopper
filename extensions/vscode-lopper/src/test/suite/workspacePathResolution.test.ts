import * as assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { suite, test } from "mocha";

import { __testing } from "../../extension";

suite("workspace path resolution", () => {
  test("rejects symlinked files that resolve outside the workspace root", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-workspace-paths-"));
    const workspaceRoot = path.join(tempRoot, "workspace");
    const outsideRoot = path.join(tempRoot, "outside");
    const escapedFilePath = path.join(workspaceRoot, "linked-outside", "escape.ts");

    try {
      await mkdir(workspaceRoot, { recursive: true });
      await mkdir(outsideRoot, { recursive: true });
      await writeFile(path.join(outsideRoot, "escape.ts"), "export const escaped = true;\n", "utf8");
      await symlink(outsideRoot, path.join(workspaceRoot, "linked-outside"));

      assert.equal(__testing.isPathInsideWorkspace(escapedFilePath, workspaceRoot), false);
      assert.equal(__testing.resolveWorkspaceFilePath(workspaceRoot, "linked-outside/escape.ts"), undefined);
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("allows symlinked workspace roots when resolved files stay inside the workspace", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-workspace-paths-"));
    const realWorkspaceRoot = path.join(tempRoot, "real-workspace");
    const workspaceRoot = path.join(tempRoot, "workspace-link");
    const lexicalFilePath = path.join(workspaceRoot, "src", "index.ts");

    try {
      await mkdir(path.join(realWorkspaceRoot, "src"), { recursive: true });
      await writeFile(path.join(realWorkspaceRoot, "src", "index.ts"), "export const inside = true;\n", "utf8");
      await symlink(realWorkspaceRoot, workspaceRoot);

      assert.equal(__testing.isPathInsideWorkspace(lexicalFilePath, workspaceRoot), true);
      assert.equal(__testing.resolveWorkspaceFilePath(workspaceRoot, "src/index.ts"), lexicalFilePath);
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });
});
