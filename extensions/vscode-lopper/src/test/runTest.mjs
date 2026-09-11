import { chmod, cp, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

import { downloadAndUnzipVSCode, runTests } from "@vscode/test-electron";

import { isBackgroundMacOSTestRun } from "./macOSBackgroundLauncherSupport.mjs";

async function main() {
  const vscodeVersion = process.env.LOPPER_VSCODE_TEST_VERSION ?? "1.101.0";
  const currentDir = path.dirname(fileURLToPath(import.meta.url));
  const extensionDevelopmentPath = path.resolve(currentDir, "..", "..");
  const extensionTestsPath = path.resolve(extensionDevelopmentPath, "out", "test", "suite", "index");
  const workspaceTemplatePath = path.resolve(extensionDevelopmentPath, "test-fixtures", "smoke-workspace");
  const fixtureBinaryPath = path.resolve(extensionDevelopmentPath, "test-fixtures", "lopper-smoke-binary.mjs");
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-vscode-smoke-"));
  const workspacePath = path.join(tempRoot, "workspace");
  const workspacePathTwo = path.join(tempRoot, "workspace-two");
  const outsideLocationPath = path.join(tempRoot, "outside-location");
  const fixtureBinaryCopyPath = path.join(tempRoot, "lopper-smoke-binary.mjs");
  const userDataDir = path.join(tempRoot, "userdata");
  const extensionsDir = path.join(tempRoot, "extensions");
  const resultPath = path.join(tempRoot, "test-result.json");
  const stdoutPath = path.join(tempRoot, "vscode.stdout.log");
  const stderrPath = path.join(tempRoot, "vscode.stderr.log");
  const backgroundLauncherPath = path.join(currentDir, "macOSBackgroundLauncher.mjs");

  try {
    await cp(workspaceTemplatePath, workspacePath, { recursive: true });
    await cp(workspaceTemplatePath, workspacePathTwo, { recursive: true });
    await cp(fixtureBinaryPath, fixtureBinaryCopyPath);
    await chmod(fixtureBinaryCopyPath, 0o755);
    await rm(path.join(workspacePath, ".lopper-cache"), { recursive: true, force: true });
    await rm(path.join(workspacePathTwo, ".lopper-cache"), { recursive: true, force: true });
    await mkdir(path.join(workspacePath, "src"), { recursive: true });
    await mkdir(path.join(workspacePathTwo, "src"), { recursive: true });
    await mkdir(path.join(workspacePathTwo, ".artifacts"), { recursive: true });
    await mkdir(outsideLocationPath, { recursive: true });
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
    await writeFile(
      path.join(workspacePathTwo, "src", "index.ts"),
      [
        'import { chunk } from "scope-lib";',
        'import { idle } from "scope-lib";',
        "",
        'console.log(chunk(["x", "y", "z"], 1));',
        "",
      ].join("\n"),
      "utf8",
    );
    const pythonTracePath = path.join(workspacePathTwo, ".artifacts", "python-runtime.ndjson");
    await writeFile(path.join(workspacePathTwo, "src", "runtime.py"), "import scope_lib\n", "utf8");
    await writeFile(pythonTracePath, "", "utf8");
    await writeFile(path.join(outsideLocationPath, "escape.ts"), 'export const escaped = "outside";\n', "utf8");
    await symlink(outsideLocationPath, path.join(workspacePath, "linked-outside"));

    const extensionTestsEnv = {
      ...process.env,
      LOPPER_BINARY_PATH: process.env.LOPPER_BINARY_PATH ?? fixtureBinaryCopyPath,
    };
    const useBackgroundLauncher = isBackgroundMacOSTestRun(process.env);
    const testOptions = {
      version: vscodeVersion,
      extensionDevelopmentPath,
      extensionTestsPath,
      launchArgs: [
        workspacePath,
        workspacePathTwo,
        "--new-window",
        "--disable-extensions",
        "--user-data-dir",
        userDataDir,
        "--extensions-dir",
        extensionsDir,
      ],
      extensionTestsEnv,
    };

    if (useBackgroundLauncher) {
      const vscodeExecutablePath = await downloadAndUnzipVSCode({ version: vscodeVersion });
      await chmod(backgroundLauncherPath, 0o755);
      testOptions.vscodeExecutablePath = backgroundLauncherPath;
      testOptions.extensionTestsEnv = {
        ...extensionTestsEnv,
        LOPPER_VSCODE_TEST_EXECUTABLE: vscodeExecutablePath,
        LOPPER_VSCODE_TEST_RESULT_PATH: resultPath,
        LOPPER_VSCODE_TEST_USER_DATA_DIR: userDataDir,
        LOPPER_VSCODE_TEST_STDOUT_PATH: stdoutPath,
        LOPPER_VSCODE_TEST_STDERR_PATH: stderrPath,
      };
    }

    await runTests(testOptions);
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
