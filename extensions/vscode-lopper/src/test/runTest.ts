import * as os from "node:os";
import * as path from "node:path";

import { runTests } from "@vscode/test-electron";

async function main(): Promise<void> {
  const extensionDevelopmentPath = path.resolve(__dirname, "..", "..");
  const extensionTestsPath = path.resolve(__dirname, "suite", "index");
  const workspacePath = path.resolve(extensionDevelopmentPath, "test-fixtures", "smoke-workspace");
  const repoRoot = path.resolve(extensionDevelopmentPath, "..", "..");
  const binaryName = process.platform === "win32" ? "lopper.exe" : "lopper";
  const tempRoot = path.join(os.tmpdir(), "lopper-vscode-smoke");
  const userDataDir = path.join(tempRoot, "userdata");
  const extensionsDir = path.join(tempRoot, "extensions");

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
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
