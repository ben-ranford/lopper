import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import * as assert from "node:assert/strict";
import { suite, test } from "mocha";

import {
  clearAndroidModuleSignalCache,
  invalidateAndroidModuleSignalCacheForPath,
  inferLopperLanguageForDocument,
  resolveLopperLanguage,
  shouldAutoRefreshForDocument,
} from "../../languageConfiguration";

suite("language configuration", () => {
  test("infers adapters from VS Code language ids and file extensions", async () => {
    assert.equal(await inferLopperLanguageForDocument({ fileName: "/repo/src/main.go", languageId: "go" }), "go");
    assert.equal(
      await inferLopperLanguageForDocument({ fileName: "/repo/src/app.tsx", languageId: "typescriptreact" }),
      "js-ts",
    );
    assert.equal(
      await inferLopperLanguageForDocument({ fileName: "/repo/src/program.kt", languageId: "plaintext" }, "/repo"),
      "jvm",
    );
    assert.equal(
      await inferLopperLanguageForDocument({ fileName: "/repo/src/service.exs", languageId: "plaintext" }),
      "elixir",
    );
    assert.equal(
      await inferLopperLanguageForDocument({ fileName: "/repo/src/lib.cs", languageId: "csharp" }),
      "dotnet",
    );
    assert.equal(
      await inferLopperLanguageForDocument({ fileName: "/repo/scripts/bootstrap.ps1", languageId: "powershell" }),
      "powershell",
    );
    assert.equal(
      await inferLopperLanguageForDocument({ fileName: "/repo/modules/demo.psd1", languageId: "plaintext" }),
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
        await inferLopperLanguageForDocument({ fileName: kotlinSource, languageId: "kotlin" }, tempRoot),
        "kotlin-android",
      );
      assert.equal(
        await inferLopperLanguageForDocument({ fileName: javaSource, languageId: "java" }, tempRoot),
        "kotlin-android",
      );
      assert.equal(await resolveLopperLanguage("auto", { fileName: kotlinSource, languageId: "kotlin" }, tempRoot), "kotlin-android");
      assert.equal(await shouldAutoRefreshForDocument("kotlin-android", { fileName: kotlinSource, languageId: "kotlin" }, tempRoot), true);
    } finally {
      clearAndroidModuleSignalCache();
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("invalidates cached Android module signals for Gradle file changes", async () => {
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "lopper-vscode-language-cache-"));
    const androidModule = path.join(tempRoot, "app");
    const kotlinSource = path.join(androidModule, "src", "main", "kotlin", "MainActivity.kt");
    const buildFile = path.join(androidModule, "build.gradle.kts");

    try {
      await mkdir(path.dirname(kotlinSource), { recursive: true });
      await writeFile(kotlinSource, "class MainActivity", "utf8");
      await writeFile(buildFile, "plugins { kotlin(\"jvm\") }", "utf8");

      assert.equal(
        await inferLopperLanguageForDocument({ fileName: kotlinSource, languageId: "kotlin" }, tempRoot),
        "jvm",
      );

      await writeFile(buildFile, 'plugins { id("com.android.application") }', "utf8");
      assert.equal(
        await inferLopperLanguageForDocument({ fileName: kotlinSource, languageId: "kotlin" }, tempRoot),
        "jvm",
        "cached module signal should be reused until a relevant path is invalidated",
      );

      invalidateAndroidModuleSignalCacheForPath(buildFile, tempRoot);
      assert.equal(
        await inferLopperLanguageForDocument({ fileName: kotlinSource, languageId: "kotlin" }, tempRoot),
        "kotlin-android",
      );
    } finally {
      clearAndroidModuleSignalCache();
      await rm(tempRoot, { recursive: true, force: true });
    }
  });

  test("resolves auto to the current document adapter and preserves explicit modes", async () => {
    const pythonDocument = { fileName: "/repo/app/main.py", languageId: "python" };

    assert.equal(await resolveLopperLanguage("auto", pythonDocument), "python");
    assert.equal(await resolveLopperLanguage("auto", { fileName: "/repo/README.md", languageId: "markdown" }), "auto");
    assert.equal(await resolveLopperLanguage("all", pythonDocument), "all");
    assert.equal(await resolveLopperLanguage("kotlin-android", pythonDocument), "kotlin-android");
    assert.equal(await resolveLopperLanguage("powershell", pythonDocument), "powershell");
    assert.equal(await resolveLopperLanguage("rust", pythonDocument), "rust");
  });

  test("auto refresh only reacts to files that match the selected mode", async () => {
    const goDocument = { fileName: "/repo/main.go", languageId: "go" };
    const jsDocument = { fileName: "/repo/src/index.ts", languageId: "typescript" };
    const markdownDocument = { fileName: "/repo/README.md", languageId: "markdown" };

    assert.equal(await shouldAutoRefreshForDocument("auto", goDocument), true);
    assert.equal(await shouldAutoRefreshForDocument("all", jsDocument), true);
    assert.equal(await shouldAutoRefreshForDocument("go", goDocument), true);
    assert.equal(await shouldAutoRefreshForDocument("go", jsDocument), false);
    assert.equal(await shouldAutoRefreshForDocument("kotlin-android", jsDocument), false);
    assert.equal(await shouldAutoRefreshForDocument("auto", markdownDocument), false);
  });
});
