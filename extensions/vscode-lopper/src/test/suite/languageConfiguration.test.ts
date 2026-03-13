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
      inferLopperLanguageForDocument({ fileName: "/repo/src/program.kt", languageId: "plaintext" }),
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
  });

  test("resolves auto to the current document adapter and preserves explicit modes", () => {
    const pythonDocument = { fileName: "/repo/app/main.py", languageId: "python" };

    assert.equal(resolveLopperLanguage("auto", pythonDocument), "python");
    assert.equal(resolveLopperLanguage("auto", { fileName: "/repo/README.md", languageId: "markdown" }), "auto");
    assert.equal(resolveLopperLanguage("all", pythonDocument), "all");
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
    assert.equal(shouldAutoRefreshForDocument("auto", markdownDocument), false);
  });
});
