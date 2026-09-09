import * as path from "node:path";

export function isBackgroundMacOSTestRun(environment, platform = process.platform) {
  return platform === "darwin" && environment.LOPPER_VSCODE_TEST_MINIMIZE !== "0" && !environment.CI && !environment.GITHUB_ACTIONS;
}

export function applicationPathForExecutable(executablePath) {
  const executableDirectory = path.dirname(executablePath);
  const contentsDirectory = path.dirname(executableDirectory);
  const applicationPath = path.dirname(contentsDirectory);
  if (path.basename(executableDirectory) !== "MacOS" || path.basename(contentsDirectory) !== "Contents" || path.extname(applicationPath) !== ".app") {
    throw new Error(`Downloaded VS Code executable has an unexpected path: ${executablePath}`);
  }
  return applicationPath;
}

export function openArguments({ applicationPath, argumentsForVSCode, environment, stdoutPath, stderrPath }) {
  const environmentArguments = Object.entries(environment)
    .filter(([name, value]) => name && value !== undefined)
    .flatMap(([name, value]) => ["--env", `${name}=${value}`]);
  return ["-n", "-g", "-j", "-W", "--stdout", stdoutPath, "--stderr", stderrPath, ...environmentArguments, applicationPath, "--args", ...argumentsForVSCode];
}

export function launcherEnvironment(environment) {
  return Object.fromEntries(
    ["LOPPER_BINARY_PATH", "LOPPER_VSCODE_TEST_RESULT_PATH", "PATH"]
      .filter((name) => environment[name] !== undefined)
      .map((name) => [name, environment[name]]),
  );
}

export function terminateMatchingTestProcesses(processOutput, executablePath, userDataDir, terminate) {
  for (const processLine of processOutput.split("\n")) {
    const line = processLine.trimStart();
    const separator = line.indexOf(" ");
    if (separator < 1) continue;
    const pidText = line.slice(0, separator);
    const pid = Number(pidText);
    if (Number.isSafeInteger(pid) && pid > 0 && String(pid) === pidText &&
        matchesTestProcess(line.slice(separator).trimStart(), executablePath, userDataDir)) {
      terminate(pid);
    }
  }
}

export async function cleanupMatchingTestProcesses({ listProcesses, terminate, wait, attempts = 10 }) {
  let pids = listProcesses();
  pids.forEach((pid) => terminate(pid, "SIGTERM"));
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    pids = listProcesses();
    if (pids.length === 0) return;
    await wait();
  }
  pids = listProcesses();
  pids.forEach((pid) => terminate(pid, "SIGKILL"));
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (listProcesses().length === 0) return;
    await wait();
  }
  if (listProcesses().length > 0) {
    throw new Error("Isolated VS Code test processes did not exit after bounded cleanup");
  }
}

// Join cleanup into the same promise as child exit: stopping open can emit exit
// before the independently launched VS Code process has finished shutting down.
export async function waitForBackgroundProcess(child, { cleanup, timeout, signals = process }) {
  let stopCode;
  let cleanupResult = Promise.resolve();
  const stop = (code) => {
    if (stopCode !== undefined) return;
    stopCode = code;
    cleanupResult = Promise.resolve().then(cleanup).then(
      () => undefined,
      (error) => error,
    );
    child.kill("SIGTERM");
  };
  const onInterrupt = () => stop(130);
  const onTerminate = () => stop(143);
  const timeoutHandle = setTimeout(() => stop(124), timeout);
  signals.on("SIGINT", onInterrupt);
  signals.on("SIGTERM", onTerminate);
  let exitCode;
  let childError;
  try {
    exitCode = await new Promise((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", resolve);
    });
  } catch (error) {
    childError = error;
  } finally {
    clearTimeout(timeoutHandle);
  }
  try {
    const cleanupError = await cleanupResult;
    if (cleanupError) throw cleanupError;
    if (childError) throw childError;
    return stopCode ?? exitCode;
  } finally {
    signals.removeListener("SIGINT", onInterrupt);
    signals.removeListener("SIGTERM", onTerminate);
  }
}

export function parseTestResult(contents) {
  let result;
  try {
    result = JSON.parse(contents);
  } catch {
    throw new Error("VS Code test result is missing or invalid");
  }
  if (result?.status === "passed") {
    return result;
  }
  if (result?.status === "failed" && typeof result.error === "string") {
    throw new Error(`VS Code tests failed: ${result.error}`);
  }
  throw new Error("VS Code test result is missing or invalid");
}

export function matchesTestProcess(command, executablePath, userDataDir) {
  const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, String.raw`\$&`);
  const executable = escape(executablePath);
  const userData = escape(userDataDir);
  return (
    new RegExp(String.raw`(^|\s)${executable}(?=\s|$)`).test(command) &&
    new RegExp(String.raw`(^|\s)--user-data-dir(?:=|\s+)${userData}(?=\s|$)`).test(command)
  );
}
