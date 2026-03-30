# Lopper for VS Code
[![VS Code Marketplace](https://img.shields.io/badge/VS%20Code-Marketplace-0098ff?logo=visualstudiocode&logoColor=white)](https://marketplace.visualstudio.com/items?itemName=BenRanford.vscode-lopper)

Lopper brings dependency-surface analysis into VS Code with inline diagnostics and hover context across supported adapters, including Kotlin Android, plus safe JS/TS quick fixes powered by the `lopper` CLI.

## What it does

- Flags unused dependency imports directly in editors covered by supported Lopper adapters.
- Shows dependency usage, risk cues, and recommendation context in hovers.
- Offers deterministic quick fixes for safe `--suggest-only` JS/TS subpath rewrites.
- Keeps a status-bar summary and a manual `Lopper: Refresh Diagnostics` command.

## Adapter mode

The extension uses the same adapter IDs as the `lopper` CLI.

- `lopper.language = auto` is the default. It prefers the active or saved editor's adapter when it can infer one, including Android Gradle Kotlin/Java modules, then falls back to `lopper` CLI auto detection.
- `lopper.language = all` runs every matching adapter in the workspace and merges the results.
- You can pin any supported adapter directly: `cpp`, `dart`, `dotnet`, `elixir`, `go`, `js-ts`, `jvm`, `kotlin-android`, `php`, `python`, `ruby`, `rust`, or `swift`.

## Binary setup

The extension shells out to `lopper`.

- If `lopper` is already on your `PATH`, the extension will use it automatically.
- If your repo contains `bin/lopper`, the extension will use that first after you trust the workspace.
- If no local binary is available, the extension can download a matching GitHub release into extension-managed storage.
- You can always override detection with `lopper.binaryPath` or `LOPPER_BINARY_PATH`.
- Workspace-local binaries, including `bin/lopper` and `lopper.binaryPath` values inside the repo, are blocked until the workspace is trusted.

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

- `lopper.language`: adapter mode, defaulting to `auto`
- `lopper.binaryPath`: explicit path to the `lopper` binary
- `lopper.topN`: max dependencies to analyse on each refresh
- `lopper.autoRefresh`: refresh on saves that match the selected adapter mode
- `lopper.autoDownloadBinary`: enable or disable managed binary downloads
- `lopper.managedBinaryTag`: optional release tag override for managed installs

## Development

```bash
make build
make vscode-extension-install
make vscode-extension-test
make vscode-extension-package
```

Repository docs: <https://github.com/ben-ranford/lopper#readme>
