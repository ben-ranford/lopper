import * as assert from "node:assert/strict";
import * as path from "node:path";
import * as vscode from "vscode";
import { suite, test } from "mocha";

suite("vscode-lopper smoke", () => {
  const fixturePath = path.join(
    vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "",
    "src",
    "index.ts",
  );
  const fixtureUri = vscode.Uri.file(fixturePath);

  test("refreshes diagnostics, hover details, and quick fixes", async function () {
    this.timeout(60_000);
    const extension = vscode.extensions.getExtension("BenRanford.vscode-lopper");
    assert.ok(extension, "expected vscode-lopper extension");
    const api = await extension.activate();
    assert.equal(vscode.workspace.getConfiguration("lopper").get("language"), "auto");
    assert.equal(vscode.workspace.getConfiguration("lopper").get("scopeMode"), "package");

    const document = await vscode.workspace.openTextDocument(fixtureUri);
    await vscode.window.showTextDocument(document);

    await vscode.commands.executeCommand("lopper.refreshWorkspace");
    assert.match(api.getLatestSummary(), /package/);
    const commands = await vscode.commands.getCommands(true);
    assert.ok(commands.includes("lopper.refreshWorkspace.repo"), "expected repo-scope refresh command");

    const diagnostics = await waitForDiagnostics(fixtureUri, 2);
    const unusedImportDiagnostic = diagnostics.find((item) => item.message.includes("unused"));
    assert.ok(unusedImportDiagnostic, "expected unused import diagnostic");

    const codemodDiagnostic = diagnostics.find((item) => item.message.includes("subpath import"));
    assert.ok(codemodDiagnostic, "expected codemod diagnostic");

    const hovers = await vscode.commands.executeCommand<vscode.Hover[]>(
      "vscode.executeHoverProvider",
      fixtureUri,
      new vscode.Position(0, 10),
    );
    assert.ok(hovers && hovers.length > 0, "expected hover content");
    const hoverText = hovers
      .flatMap((hover) => hover.contents)
      .map((content) => (content instanceof vscode.MarkdownString ? content.value : String(content)))
      .join("\n");
    const normalizedHoverText = hoverText
      .replaceAll(String.raw`\-`, "-")
      .replaceAll("&nbsp;", " ");
    assert.match(normalizedHoverText, /scope-lib/);
    assert.match(normalizedHoverText, /Used exports:/);

    const quickFixes = await vscode.commands.executeCommand<
      (vscode.CodeAction | vscode.Command)[]
    >("vscode.executeCodeActionProvider", fixtureUri, new vscode.Range(0, 0, 0, 10));
    assert.ok(quickFixes && quickFixes.length > 0, "expected quick fixes");
    const codeAction = quickFixes.find(
      (item): item is vscode.CodeAction =>
        item instanceof vscode.CodeAction && item.title.includes("scope-lib/chunk"),
    );
    assert.ok(codeAction?.edit, "expected subpath import code action");

    const editApplied = await vscode.workspace.applyEdit(codeAction.edit);
    assert.equal(editApplied, true, "expected code action edit to apply");

    const updatedText = document.getText();
    assert.match(updatedText, /import chunk from "scope-lib\/chunk";/);
  });
});

async function waitForDiagnostics(uri: vscode.Uri, minimumCount: number): Promise<readonly vscode.Diagnostic[]> {
  const timeoutAt = Date.now() + 20_000;
  while (Date.now() < timeoutAt) {
    const diagnostics = vscode.languages.getDiagnostics(uri);
    if (diagnostics.length >= minimumCount) {
      return diagnostics;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  return vscode.languages.getDiagnostics(uri);
}
