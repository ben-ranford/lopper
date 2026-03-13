import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

import { runTests } from "@vscode/test-electron";

async function main() {
  const currentDir = path.dirname(fileURLToPath(import.meta.url));
  const extensionDevelopmentPath = path.resolve(currentDir, "..", "..");
  const extensionTestsPath = path.resolve(extensionDevelopmentPath, "out", "test", "suite", "index");
  const workspaceTemplatePath = path.resolve(extensionDevelopmentPath, "test-fixtures", "smoke-workspace");
  const repoRoot = path.resolve(extensionDevelopmentPath, "..", "..");
  const binaryName = process.platform === "win32" ? "lopper.exe" : "lopper";
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-vscode-smoke-"));
  const workspacePath = path.join(tempRoot, "workspace");
  const userDataDir = path.join(tempRoot, "userdata");
  const extensionsDir = path.join(tempRoot, "extensions");

  try {
    await cp(workspaceTemplatePath, workspacePath, { recursive: true });
    await rm(path.join(workspacePath, ".lopper-cache"), { recursive: true, force: true });
    await mkdir(path.join(workspacePath, "src"), { recursive: true });
    await writeFile(
      path.join(workspacePath, "src", "index.ts"),
      [
        'import { chunk } from "scope-lib";',
        'import { idle } from "scope-lib";',
        "",
        'console.log(chunk(["a", "b", "c"], 1));',
        "",
      ].join("\n"),
      "utf8",
    );

    await runTests({
      extensionDevelopmentPath,
      extensionTestsPath,
      launchArgs: [
        workspacePath,
        "--disable-extensions",
        "--user-data-dir",
        userDataDir,
        "--extensions-dir",
        extensionsDir,
      ],
      extensionTestsEnv: {
        ...process.env,
        LOPPER_BINARY_PATH:
          process.env.LOPPER_BINARY_PATH ?? path.join(repoRoot, "bin", binaryName),
      },
    });
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

try {
  await main();
} catch (error) {
  console.error(error);
  process.exit(1);
}
