import { writeFile } from "node:fs/promises";
import * as path from "node:path";

import { glob } from "glob";
export async function run(): Promise<void> {
  try {
    const { default: Mocha } = await import("mocha");
    const mocha = new Mocha({
      ui: "tdd",
      color: true,
      timeout: 30_000,
    });

    const files = await glob("**/*.test.js", { cwd: __dirname });
    for (const file of files) {
      mocha.addFile(path.resolve(__dirname, file));
    }
    await mocha.loadFilesAsync();

    await new Promise<void>((resolve, reject) => {
      mocha.run((failures) => {
        if (failures > 0) {
          reject(new Error(`${failures} smoke tests failed.`));
          return;
        }
        resolve();
      });
    });
    await writeTestResult({ status: "passed" });
  } catch (error) {
    await writeTestResult({ status: "failed", error: String(error) });
    throw error;
  }
}

async function writeTestResult(result: { status: "passed" } | { status: "failed"; error: string }): Promise<void> {
  const resultPath = process.env.LOPPER_VSCODE_TEST_RESULT_PATH;
  if (resultPath) {
    await writeFile(resultPath, JSON.stringify(result), "utf8");
  }
}
