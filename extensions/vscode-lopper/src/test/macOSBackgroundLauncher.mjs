#!/usr/bin/env node
import { readFile } from "node:fs/promises";
import { spawn, spawnSync } from "node:child_process";

import { applicationPathForExecutable, cleanupMatchingTestProcesses, launcherEnvironment, openArguments, parseTestResult, terminateMatchingTestProcesses, waitForBackgroundProcess } from "./macOSBackgroundLauncherSupport.mjs";

const executablePath = process.env.LOPPER_VSCODE_TEST_EXECUTABLE;
const resultPath = process.env.LOPPER_VSCODE_TEST_RESULT_PATH;
const userDataDir = process.env.LOPPER_VSCODE_TEST_USER_DATA_DIR;
const stdoutPath = process.env.LOPPER_VSCODE_TEST_STDOUT_PATH;
const stderrPath = process.env.LOPPER_VSCODE_TEST_STDERR_PATH;
const timeout = Number(process.env.LOPPER_VSCODE_TEST_TIMEOUT_MS ?? 300_000);

if (!executablePath || !resultPath || !userDataDir || !stdoutPath || !stderrPath || !Number.isSafeInteger(timeout) || timeout <= 0) {
  throw new Error("The macOS VS Code test launcher requires complete isolated-run configuration");
}

const argumentsForOpen = openArguments({
  applicationPath: applicationPathForExecutable(executablePath),
  argumentsForVSCode: process.argv.slice(2),
  environment: launcherEnvironment(process.env),
  stdoutPath,
  stderrPath,
});
const open = spawn("/usr/bin/open", argumentsForOpen, { stdio: "inherit" });
async function cleanupTestProcess() {
  const listProcesses = () => {
    const processOutput = spawnSync("/bin/ps", ["-axo", "pid=,command="], { encoding: "utf8" }).stdout;
    const pids = [];
    terminateMatchingTestProcesses(processOutput, executablePath, userDataDir, (pid) => pids.push(pid));
    return pids;
  };
  await cleanupMatchingTestProcesses({ listProcesses, terminate: (pid, signal) => {
    try {
      process.kill(pid, signal);
    } catch (error) {
      if (error.code !== "ESRCH") throw error;
    }
  }, wait: () => new Promise((resolve) => setTimeout(resolve, 100)) });
}

const exitCode = await waitForBackgroundProcess(open, { cleanup: cleanupTestProcess, timeout });
if (exitCode !== 0) {
  throw new Error(`open exited with code ${exitCode}`);
}

process.stdout.write(await readFile(stdoutPath, "utf8"));
process.stderr.write(await readFile(stderrPath, "utf8"));
parseTestResult(await readFile(resultPath, "utf8"));
