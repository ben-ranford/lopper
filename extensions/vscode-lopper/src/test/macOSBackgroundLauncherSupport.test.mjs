import assert from "node:assert/strict";
import test from "node:test";

import { applicationPathForExecutable, isBackgroundMacOSTestRun, matchesTestProcess, openArguments, parseTestResult } from "./macOSBackgroundLauncherSupport.mjs";

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

test("accepts only an explicit passed result and exact isolated process identity", () => {
  assert.deepEqual(parseTestResult('{"status":"passed"}'), { status: "passed" });
  assert.throws(() => parseTestResult('{"status":"failed","error":"boom"}'), /VS Code tests failed: boom/);
  assert.throws(() => parseTestResult("not json"), /missing or invalid/);
  assert.equal(applicationPathForExecutable("/tmp/Code.app/Contents/MacOS/Electron"), "/tmp/Code.app");
  assert.equal(matchesTestProcess("/tmp/Code.app/Contents/MacOS/Electron --user-data-dir=/tmp/profile", "/tmp/Code.app/Contents/MacOS/Electron", "/tmp/profile"), true);
  assert.equal(matchesTestProcess("/Applications/Code --user-data-dir=/tmp/profile", "/tmp/Code.app/Contents/MacOS/Electron", "/tmp/profile"), false);
});
