import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import * as assert from "node:assert/strict";
import { suite, test } from "mocha";

import {
  inferLopperLanguageForDocument,
  resolveLopperLanguage,
  shouldAutoRefreshForDocument,
} from "../../languageConfiguration";

suite("language configuration", () => {
  test("infers adapters from VS Code language ids and file extensions", () => {
    assert.equal(inferLopperLanguageForDocument({ fileName: "/repo/src/main.go", languageId: "go" }), "go");
    assert.equal(
      inferLopperLanguageForDocument({ fileName: "/repo/src/app.tsx", languageId: "typescriptreact" }),
      "js-ts",
    );
    assert.equal(
      inferLopperLanguageForDocument({ fileName: "/repo/src/program.kt", languageId: "plaintext" }, "/repo"),
      "jvm",
    );
    assert.equal(
      inferLopperLanguageForDocument({ fileName: "/repo/src/service.exs", languageId: "plaintext" }),
      "elixir",
    );
    assert.equal(
      inferLopperLanguageForDocument({ fileName: "/repo/src/lib.cs", languageId: "csharp" }),
      "dotnet",
    );
    assert.equal(
      inferLopperLanguageForDocument({ fileName: "/repo/scripts/bootstrap.ps1", languageId: "powershell" }),
      "powershell",
    );
    assert.equal(
      inferLopperLanguageForDocument({ fileName: "/repo/modules/demo.psd1", languageId: "plaintext" }),
      "powershell",
    );
  });

  test("prefers kotlin-android for Android Gradle modules", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-vscode-language-"));
    const androidModule = path.join(tempRoot, "app");
    const kotlinSource = path.join(androidModule, "src", "main", "kotlin", "MainActivity.kt");
    const javaSource = path.join(androidModule, "src", "main", "java", "MainActivity.java");
    const androidManifest = path.join(androidModule, "src", "main", "AndroidManifest.xml");

    try {
      await mkdir(path.dirname(kotlinSource), { recursive: true });
      await mkdir(path.dirname(javaSource), { recursive: true });
      await writeFile(
        path.join(androidModule, "build.gradle.kts"),
        'plugins { id("com.android.application"); id("org.jetbrains.kotlin.android") }',
        "utf8",
      );
      await writeFile(androidManifest, "<manifest />", "utf8");
      await writeFile(kotlinSource, "class MainActivity", "utf8");
      await writeFile(javaSource, "class MainActivity {}", "utf8");

      assert.equal(
        inferLopperLanguageForDocument({ fileName: kotlinSource, languageId: "kotlin" }, tempRoot),
        "kotlin-android",
      );
      assert.equal(
        inferLopperLanguageForDocument({ fileName: javaSource, languageId: "java" }, tempRoot),
        "kotlin-android",
      );
      assert.equal(resolveLopperLanguage("auto", { fileName: kotlinSource, languageId: "kotlin" }, tempRoot), "kotlin-android");
      assert.equal(shouldAutoRefreshForDocument("kotlin-android", { fileName: kotlinSource, languageId: "kotlin" }, tempRoot), true);
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("resolves auto to the current document adapter and preserves explicit modes", () => {
    const pythonDocument = { fileName: "/repo/app/main.py", languageId: "python" };

    assert.equal(resolveLopperLanguage("auto", pythonDocument), "python");
    assert.equal(resolveLopperLanguage("auto", { fileName: "/repo/README.md", languageId: "markdown" }), "auto");
    assert.equal(resolveLopperLanguage("all", pythonDocument), "all");
    assert.equal(resolveLopperLanguage("kotlin-android", pythonDocument), "kotlin-android");
    assert.equal(resolveLopperLanguage("powershell", pythonDocument), "powershell");
    assert.equal(resolveLopperLanguage("rust", pythonDocument), "rust");
  });

  test("auto refresh only reacts to files that match the selected mode", () => {
    const goDocument = { fileName: "/repo/main.go", languageId: "go" };
    const jsDocument = { fileName: "/repo/src/index.ts", languageId: "typescript" };
    const markdownDocument = { fileName: "/repo/README.md", languageId: "markdown" };

    assert.equal(shouldAutoRefreshForDocument("auto", goDocument), true);
    assert.equal(shouldAutoRefreshForDocument("all", jsDocument), true);
    assert.equal(shouldAutoRefreshForDocument("go", goDocument), true);
    assert.equal(shouldAutoRefreshForDocument("go", jsDocument), false);
    assert.equal(shouldAutoRefreshForDocument("kotlin-android", jsDocument), false);
    assert.equal(shouldAutoRefreshForDocument("auto", markdownDocument), false);
  });
});
