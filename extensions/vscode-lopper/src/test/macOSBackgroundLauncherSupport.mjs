import * as path from "node:path";

export function isBackgroundMacOSTestRun(environment, platform = process.platform) {
  return platform === "darwin" && environment.LOPPER_VSCODE_TEST_MINIMIZE !== "0" && !environment.CI && !environment.GITHUB_ACTIONS;
}

export function applicationPathForExecutable(executablePath) {
  const suffix = path.join("Contents", "MacOS", "Electron");
  if (!executablePath.endsWith(suffix)) {
    throw new Error(`Downloaded VS Code executable has an unexpected path: ${executablePath}`);
  }
  return path.dirname(path.dirname(path.dirname(executablePath)));
}

export function openArguments({ applicationPath, argumentsForVSCode, environment, stdoutPath, stderrPath }) {
  const environmentArguments = Object.entries(environment)
    .filter(([name, value]) => name && value !== undefined)
    .flatMap(([name, value]) => ["--env", `${name}=${value}`]);
  return ["-n", "-g", "-j", "-W", "--stdout", stdoutPath, "--stderr", stderrPath, ...environmentArguments, applicationPath, "--args", ...argumentsForVSCode];
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
  return (
    command.includes(executablePath) &&
    (command.includes(`--user-data-dir=${userDataDir}`) || command.includes(`--user-data-dir ${userDataDir}`))
  );
}
