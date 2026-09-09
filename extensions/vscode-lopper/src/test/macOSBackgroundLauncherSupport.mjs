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
    ["LOPPER_BINARY_PATH", "LOPPER_VSCODE_TEST_RESULT_PATH"]
      .filter((name) => environment[name] !== undefined)
      .map((name) => [name, environment[name]]),
  );
}

export function terminateMatchingTestProcesses(processOutput, executablePath, userDataDir, terminate) {
  for (const processLine of processOutput.split("\n")) {
    const match = /^\s*(\d+)\s+(.*)$/.exec(processLine);
    if (match && matchesTestProcess(match[2], executablePath, userDataDir)) {
      terminate(Number(match[1]));
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
  pids.forEach((pid) => terminate(pid, "SIGKILL"));
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (listProcesses().length === 0) return;
    await wait();
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
  const escape = (value) => value.replace(/[.*+?^\${}()|[\]\\]/g, "\\$&");
  const executable = escape(executablePath);
  const userData = escape(userDataDir);
  return (
    new RegExp("(^|\\s)" + executable + "(?=\\s|$)").test(command) &&
    new RegExp("(^|\\s)--user-data-dir(?:=|\\s+)" + userData + "(?=\\s|$)").test(command)
  );
}
