import assert from "node:assert/strict";
import test from "node:test";
import { EventEmitter } from "node:events";

import { applicationPathForExecutable, cleanupMatchingTestProcesses, isBackgroundMacOSTestRun, launcherEnvironment, matchesTestProcess, openArguments, parseTestResult, terminateMatchingTestProcesses, waitForBackgroundProcess } from "./macOSBackgroundLauncherSupport.mjs";

test("uses the background launcher only for local macOS runs", () => {
  assert.equal(isBackgroundMacOSTestRun({}, "darwin"), true);
  assert.equal(isBackgroundMacOSTestRun({ LOPPER_VSCODE_TEST_MINIMIZE: "0" }, "darwin"), false);
  assert.equal(isBackgroundMacOSTestRun({ CI: "true" }, "darwin"), false);
  assert.equal(isBackgroundMacOSTestRun({}, "linux"), false);
});

test("builds a hidden no-focus launch with explicit test environment and arguments", () => {
  const args = openArguments({
    applicationPath: "/tmp/Visual Studio Code.app",
    argumentsForVSCode: ["--user-data-dir", "/tmp/profile", "--extensionTestsPath=/tmp/tests"],
    environment: { TEST_VALUE: "keep me", OMIT: undefined },
    stdoutPath: "/tmp/stdout.log",
    stderrPath: "/tmp/stderr.log",
  });
  assert.deepEqual(args, [
    "-n", "-g", "-j", "-W", "--stdout", "/tmp/stdout.log", "--stderr", "/tmp/stderr.log",
    "--env", "TEST_VALUE=keep me", "/tmp/Visual Studio Code.app", "--args",
    "--user-data-dir", "/tmp/profile", "--extensionTestsPath=/tmp/tests",
  ]);
});

test("forwards required test paths and terminates only the isolated test process", () => {
  assert.deepEqual(launcherEnvironment({
    LOPPER_BINARY_PATH: "/tmp/lopper",
    LOPPER_VSCODE_TEST_RESULT_PATH: "/tmp/result.json",
    LOPPER_GITHUB_TOKEN: "secret",
    LOPPER_NOTIFY_WEBHOOK: "secret",
  }), { LOPPER_BINARY_PATH: "/tmp/lopper", LOPPER_VSCODE_TEST_RESULT_PATH: "/tmp/result.json" });

  const terminated = [];
  terminateMatchingTestProcesses(
    "100 /tmp/Code.app/Contents/MacOS/Code --user-data-dir=/tmp/profile\n101 /tmp/Code.app/Contents/MacOS/Code --user-data-dir=/tmp/other\n102 /Applications/Code --user-data-dir=/tmp/profile",
    "/tmp/Code.app/Contents/MacOS/Code",
    "/tmp/profile",
    (pid) => terminated.push(pid),
  );
  assert.deepEqual(terminated, [100]);
  assert.equal(matchesTestProcess("/tmp/Code.app/Contents/MacOS/Code --user-data-dir=/tmp/profile-other", "/tmp/Code.app/Contents/MacOS/Code", "/tmp/profile"), false);
  const executable = "/tmp/Code[1].app/Contents/MacOS/Code";
  const profile = "/tmp/profile+1";
  assert.equal(matchesTestProcess(`${executable} --user-data-dir=${profile}`, executable, profile), true);
  assert.equal(matchesTestProcess("/tmp/Code1Xapp/Contents/MacOS/Code --user-data-dir=/tmp/profile11", executable, profile), false);
});

test("waits for graceful cleanup and escalates only stubborn matching processes", async () => {
  const signals = [];
  let gracefulChecks = 0;
  await cleanupMatchingTestProcesses({
    listProcesses: () => (gracefulChecks++ === 0 ? [100] : []),
    terminate: (pid, signal) => signals.push([pid, signal]),
    wait: async () => {},
    attempts: 2,
  });
  assert.deepEqual(signals, [[100, "SIGTERM"]]);

  let stubbornChecks = 0;
  await cleanupMatchingTestProcesses({
    listProcesses: () => (stubbornChecks++ < 4 ? [101] : []),
    terminate: (pid, signal) => signals.push([pid, signal]),
    wait: async () => {},
    attempts: 2,
  });
  assert.deepEqual(signals.slice(1), [[101, "SIGTERM"], [101, "SIGKILL"]]);
});

test("accepts only an explicit passed result and exact isolated process identity", () => {
  assert.deepEqual(parseTestResult('{"status":"passed"}'), { status: "passed" });
  assert.throws(() => parseTestResult('{"status":"failed","error":"boom"}'), /VS Code tests failed: boom/);
  assert.throws(() => parseTestResult("not json"), /missing or invalid/);
  assert.equal(applicationPathForExecutable("/tmp/Code.app/Contents/MacOS/Electron"), "/tmp/Code.app");
  assert.equal(matchesTestProcess("/tmp/Code.app/Contents/MacOS/Electron --user-data-dir=/tmp/profile", "/tmp/Code.app/Contents/MacOS/Electron", "/tmp/profile"), true);
  assert.equal(matchesTestProcess("/Applications/Code --user-data-dir=/tmp/profile", "/tmp/Code.app/Contents/MacOS/Electron", "/tmp/profile"), false);
});


test("resolves app bundles independently of the downloaded executable name", () => {
  for (const executable of ["Electron", "Code", "Code - Insiders"]) {
    assert.equal(applicationPathForExecutable(`/tmp/Visual Studio Code.app/Contents/MacOS/${executable}`), "/tmp/Visual Studio Code.app");
  }
  for (const executable of ["/tmp/Code", "/tmp/Code.app/Other/MacOS/Code", "/tmp/Code/Contents/MacOS/Code"]) {
    assert.throws(() => applicationPathForExecutable(executable), /unexpected path/);
  }
});


test("an immediate open exit cannot bypass interruption cleanup", async () => {
  for (const signal of ["SIGINT", "SIGTERM"]) {
    const child = new EventEmitter();
    const signals = new EventEmitter();
    child.kill = () => child.emit("exit", null);
    let finishCleanup;
    let completed = false;
    const result = waitForBackgroundProcess(child, {
      cleanup: () => new Promise((resolve) => { finishCleanup = resolve; }),
      timeout: 60_000,
      signals,
    }).then((code) => { completed = true; return code; });
    signals.emit(signal);
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(completed, false, "exit must wait for cleanup");
    assert.equal(signals.listenerCount(signal), 1, "repeated signals remain handled during cleanup");
    signals.emit(signal);
    finishCleanup();
    assert.equal(await result, signal === "SIGINT" ? 130 : 143);
    assert.equal(signals.listenerCount(signal), 0);
  }
});
