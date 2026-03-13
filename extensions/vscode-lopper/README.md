# Lopper for VS Code

Lopper brings dependency-surface analysis into VS Code with inline diagnostics, hover context, and safe quick fixes powered by the local `lopper` CLI.

## What it does

- Flags unused dependency imports directly in JavaScript and TypeScript editors.
- Shows dependency usage, risk cues, and recommendation context in hovers.
- Offers deterministic quick fixes for safe `--suggest-only` subpath rewrites.
- Keeps a status-bar summary and a manual `Lopper: Refresh Diagnostics` command.

## Requirements

The extension shells out to a local `lopper` binary.

- If `lopper` is already on your `PATH`, the extension will use it automatically.
- Otherwise set `lopper.binaryPath` in VS Code settings, or export `LOPPER_BINARY_PATH`.

## Install

From the VS Code Marketplace after publish:

```bash
code --install-extension BenRanford.vscode-lopper
```

From a GitHub release VSIX:

```bash
code --install-extension lopper-vscode-<version>.vsix
```

## Settings

- `lopper.binaryPath`: explicit path to the `lopper` binary
- `lopper.topN`: max dependencies to analyse on each refresh
- `lopper.autoRefresh`: refresh on JavaScript/TypeScript save

## Development

```bash
make build
make vscode-extension-install
make vscode-extension-test
make vscode-extension-package
```

Repository docs: <https://github.com/ben-ranford/lopper#readme>
