#!/usr/bin/env node
import { readFile } from "node:fs/promises";
import { spawn, spawnSync } from "node:child_process";

import { applicationPathForExecutable, matchesTestProcess, openArguments, parseTestResult } from "./macOSBackgroundLauncherSupport.mjs";

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
  environment: Object.fromEntries(Object.entries(process.env).filter(([name]) => name.startsWith("LOPPER_"))),
  stdoutPath,
  stderrPath,
});
const open = spawn("/usr/bin/open", argumentsForOpen, { stdio: "inherit" });
const timeoutHandle = setTimeout(() => {
  open.kill("SIGTERM");
  const processes = spawnSync("/bin/ps", ["-axo", "pid=,command="], { encoding: "utf8" }).stdout.split("\n");
  for (const processLine of processes) {
    const match = /^\s*(\d+)\s+(.*)$/.exec(processLine);
    if (match && matchesTestProcess(match[2], executablePath, userDataDir)) {
      process.kill(Number(match[1]), "SIGTERM");
    }
  }
}, timeout);

const exitCode = await new Promise((resolve, reject) => {
  open.once("error", reject);
  open.once("exit", (code) => resolve(code));
});
clearTimeout(timeoutHandle);
if (exitCode !== 0) {
  throw new Error(`open exited with code ${exitCode}`);
}

process.stdout.write(await readFile(stdoutPath, "utf8"));
process.stderr.write(await readFile(stderrPath, "utf8"));
parseTestResult(await readFile(resultPath, "utf8"));
