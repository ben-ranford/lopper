# Lopper for VS Code
[![VS Code Marketplace](https://img.shields.io/badge/VS%20Code-Marketplace-0098ff?logo=visualstudiocode&logoColor=white)](https://marketplace.visualstudio.com/items?itemName=BenRanford.vscode-lopper)

Lopper brings dependency-surface analysis into VS Code with inline diagnostics and hover context across supported adapters, including Kotlin Android and PowerShell, plus safe remediation quick fixes powered by the `lopper` CLI.

## What it does

- Flags unused dependency imports directly in editors covered by supported Lopper adapters.
- Shows dependency usage, license, provenance, risk cues, and recommendation context in hovers.
- Surfaces a dependency explorer sidebar with folder summaries, dependency drilldown, and source-navigation links.
- Supports multi-root workspaces by analyzing each workspace folder independently.
- Offers deterministic quick fixes for safe `--suggest-only` remediation previews.
- Enables stable Python unused-import suggestions by default while preserving the CLI's explicit feature rollback.
- Applies available safe codemods through the guarded CLI flow and reports rollback artifact paths.
- Supports `package`, `repo`, and `changed-packages` analysis scope modes directly in VS Code.
- Keeps a status-bar summary and manual refresh commands, including force-fresh, runtime-aware, baseline, and export workflows.

## Adapter mode

The extension uses the same adapter IDs as the `lopper` CLI.

- `lopper.language = auto` is the default. It prefers the active or saved editor's adapter when it can infer one, including Android Gradle Kotlin/Java modules, then falls back to `lopper` CLI auto detection.
- `lopper.language = all` runs every matching adapter in the workspace and merges the results.
- You can pin any supported adapter directly: `cpp`, `dart`, `dotnet`, `elixir`, `go`, `js-ts`, `jvm`, `kotlin-android`, `php`, `powershell`, `python`, `ruby`, `rust`, or `swift`.

## Binary setup

The extension shells out to `lopper`.

- If `lopper` is already on your `PATH`, the extension will use it automatically.
- If your repo contains `bin/lopper`, the extension will use that first after you trust the workspace.
- If no local binary is available, the extension can download a matching GitHub release into extension-managed storage.
- You can always override detection with `lopper.binaryPath` or `LOPPER_BINARY_PATH`.
- Workspace-local binaries, including `bin/lopper`, configured paths, PATH entries, and symlinks resolving under any open workspace root, are blocked until the workspace is trusted.
- Codemod apply actions are disabled until the workspace is trusted and keep the CLI's clean-worktree protection unless you explicitly retry with the dirty-worktree override.
- Runtime trace files can be analysed without executing workspace code. Runtime test commands remain unavailable until the workspace is trusted and run only from the explicit runtime refresh command.

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
- `lopper.scopeMode`: analysis scope mode (`package`, `repo`, `changed-packages`)
- `lopper.binaryPath`: explicit path to the `lopper` binary
- `lopper.topN`: max dependencies to analyse on each refresh
- `lopper.autoRefresh`: refresh on saves that match the selected adapter mode
- `lopper.autoDownloadBinary`: enable or disable managed binary downloads
- `lopper.managedBinaryTag`: optional release tag override for managed installs
- `lopper.runtimeTracePath`: optional runtime trace file for JS/TS or Python runtime-aware analysis
- `lopper.runtimeTestCommand`: optional allowlisted JS/TS or Python test command; execution requires Workspace Trust and an explicit runtime refresh
- `lopper.enableFeatures`: per-workspace allowlist of safe CLI capabilities to enable explicitly
- `lopper.disableFeatures`: per-workspace allowlist of safe CLI features to disable; includes stable rollback controls and disable entries take precedence
- `lopper.advisorySourcePath`: optional local JSON or YAML advisory file for vulnerability findings
- `lopper.thresholdFailOnIncreasePercent`: waste increase gate threshold, default `-1`
- `lopper.thresholdLowConfidenceWarningPercent`: warning threshold for low-confidence dependencies
- `lopper.thresholdMinUsagePercentForRecommendations`: recommendation threshold for usage
- `lopper.thresholdMaxUncertainImportCount`: uncertain import gate threshold, default `-1`
- `lopper.thresholdReachableVulnerabilityPriority`: reachable vulnerability gate threshold (`off`, `low`, `medium`, `high`, `critical`)
- `lopper.licenseDeny`: SPDX identifiers to deny during analysis
- `lopper.licenseFailOnDeny`: fail when denied licenses are detected
- `lopper.licenseProvenanceRegistry`: enable registry provenance heuristics for JS/TS dependencies

Setting `lopper.advisorySourcePath` or a non-`off` reachable vulnerability
threshold automatically enables the
`reachability-vulnerability-prioritization-preview` feature for VS Code runs.

The extension queries the selected binary's `features --format json` catalog before forwarding an explicit feature setting. `lopper.enableFeatures` exposes only `python-runner-profiles`, `reachability-vulnerability-prioritization-preview`, and `sbom-attestation-exports-preview` because their VS Code operations are local and explicitly bounded. `lopper.disableFeatures` additionally exposes `vscode-preview-capability-parity` as a stable rollback control. Stable capabilities already enabled by the selected binary are not forwarded redundantly. Dashboard, MCP mutation, and remote-repository capabilities are not accepted by these settings.

Command discoverability is global because each folder in a multi-root window can select a different binary. Execution availability is folder-specific: Python runtime and CycloneDX actions preflight the selected folder's CLI manifest before opening file or input UI. The stable, default-on `vscode-preview-capability-parity` tracker remains required when routing those preview-backed operations; explicitly disabling it makes those actions unavailable before any file or input prompt opens.

Python codemod suggestions and first-party Python runtime capture use their stable Lopper v1.8 defaults. In v1.8.1, the stable `python-runner-profiles` capability enables the CLI's safe `unittest` and `uv` forms by default for a trusted, explicit runtime refresh; `lopper.disableFeatures` remains available as a rollback control.

## Commands

- `Lopper: Refresh Diagnostics`: refresh using the configured scope and session cache.
- `Lopper: Refresh Diagnostics (Force Fresh)`: bypass cache and re-run analysis.
- `Lopper: Refresh Diagnostics (Runtime Trace)`: run runtime-aware analysis for JS/TS or Python workspaces.
- `Lopper: Refresh Diagnostics (Scope: package|repo|changed-packages)`: run using an explicit scope mode.
- `Lopper: Save Baseline Snapshot`: save the current workspace analysis as a baseline snapshot.
- `Lopper: Compare Baseline`: compare the current workspace analysis against a saved baseline key or file.
- `Lopper: Analyse Dependency...`: open a focused dependency analysis and detail view.
- `Lopper: Apply Codemod`: apply an available safe codemod for a dependency and print applied, skipped, failed, and rollback artifact details.
- `Lopper: Export Analysis as JSON|CSV|SARIF|PR Comment|CycloneDX JSON`: export the current analysis. CycloneDX defaults to `lopper-analysis.cdx.json` and requests its preview capability only when the selected CLI does not enable it by default.

The extension deduplicates ordinary in-flight refreshes per folder/language/scope, prevents stale runs from overwriting newer diagnostics, and logs refresh lifecycle states to the `Lopper` output channel. Explicit runtime and baseline actions always execute fresh and do not replace the ordinary analysis cache.

## Development

```bash
make build
make vscode-extension-install
make vscode-extension-test
make vscode-extension-package
```

Repository docs: <https://github.com/ben-ranford/lopper#readme>
