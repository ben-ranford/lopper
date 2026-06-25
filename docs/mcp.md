# MCP Server

`lopper mcp` runs a local stdio Model Context Protocol server for agent workflows that need dependency surface analysis without shelling out to parse CLI text.

The server speaks JSON-RPC over stdio using `Content-Length` frames. It writes protocol responses to stdout and does not emit normal CLI output in MCP mode.

## Client Configuration

Example MCP client entry:

```json
{
  "mcpServers": {
    "lopper": {
      "command": "lopper",
      "args": ["mcp"]
    }
  }
}
```

`mcp-server` is a stable feature flag and is enabled by default in every build channel. The legacy `mcp-server-preview` name remains available for compatibility and explicit rollback testing via `--disable-feature mcp-server-preview`. During `initialize`, the server advertises tool support and returns Lopper version metadata. Use `tools/list` to inspect tool schemas at runtime.

Mutation tools are behind the stable flag `mcp-mutation-tools` and are not advertised unless the MCP server is started with that flag enabled, for example:

```json
{
  "mcpServers": {
    "lopper": {
      "command": "lopper",
      "args": ["mcp", "--enable-feature", "mcp-mutation-tools"]
    }
  }
}
```

## Tools

### `lopper_analyse_top_dependencies`

Ranks the top dependencies by unused surface area.

Required input:

- `repoPath`: local repository directory.

Common optional inputs:

- `topN`: number of dependencies to rank. Defaults to `20`.
- `language`: `auto`, `all`, or a supported adapter ID.
- `scopeMode`: `package`, `repo`, or `changed-packages`.
- `configPath`: local config file path.
- `include` / `exclude`: path glob arrays that override config scope.
- `cacheEnabled`, `cachePath`, `cacheReadOnly`: incremental cache controls.
- `runtimeProfile`: `node-import`, `node-require`, `browser-import`, or `browser-require`.
- `runtimeTracePath`: local NDJSON runtime trace path.
- `advisorySourcePath`: local JSON or YAML advisory file. MCP reads this file
  only; it does not fetch vulnerability data from a network service.
- `enableFeatures` / `disableFeatures`: feature flag names or codes.
- threshold and policy overrides: `lowConfidenceWarningPercent`, `minUsagePercentForRecommendations`, `maxUncertainImportCount`, `reachableVulnerabilityPriority`, `scoreWeightUsage`, `scoreWeightImpact`, `scoreWeightConfidence`, `licenseDeny`, `licenseFailOnDeny`, `licenseProvenanceRegistry`.
- `timeoutMillis`: per-tool timeout.

### `lopper_analyse_dependency`

Analyzes one dependency and returns the same report shape as JSON analysis.

Required input:

- `repoPath`: local repository directory.
- `dependency`: dependency name.

It accepts the same common optional inputs as `lopper_analyse_top_dependencies`, except `topN` is ignored.

### `lopper_compare_baseline`

Runs read-only analysis and compares it with an existing baseline report or immutable baseline snapshot.

Required input:

- `repoPath`: local repository directory.
- Either `baselinePath`, or `baselineStorePath` with `baselineKey`.

Optional input:

- `dependency`: compare a single dependency.
- `topN`: compare a top dependency run. Defaults to `20` when `dependency` is omitted.
- The same common optional inputs as `lopper_analyse_top_dependencies`.

The tool does not save baselines. It only loads existing baseline JSON or snapshot files and adds `wasteIncreasePercent` and `baselineComparison` to the returned report.

### `lopper_apply_codemod`

Preview mutation tool. Runs dependency analysis in codemod suggestion mode for one dependency, then applies deterministic safe patch previews through the same app workflow used by `lopper analyse --apply-codemod`.

Required input:

- `repoPath`: local repository directory.
- `dependency`: dependency name. Codemod apply is not available for top-N analysis.
- `confirmApply`: must be `true`.

Optional input:

- `allowDirty`: allow the mutation to run when git reports uncommitted changes. Defaults to `false`.
- The same common optional analysis inputs as `lopper_analyse_top_dependencies`, except `topN` and runtime test commands are not supported.

Structured output includes `appliedFiles`, `appliedPatches`, `skippedFiles`, `failedFiles`, `backupPath`, per-file `results`, and the full report containing `dependencies[].codemod.apply`.

### `lopper_save_baseline`

Preview mutation tool. Runs analysis and saves the resulting report as an immutable baseline snapshot through the same app workflow used by `lopper analyse --save-baseline`.

Required input:

- `repoPath`: local repository directory.
- `baselineStorePath`: local snapshot directory. Relative paths resolve under `repoPath`.
- `confirmSave`: must be `true`.

Optional input:

- `dependency`: analyse and save a single dependency report.
- `topN`: number of dependencies to include when `dependency` is omitted. Defaults to `20`.
- `baselineKey`: explicit snapshot key.
- `baselineLabel`: label used to save `label:<value>`. Mutually exclusive with `baselineKey`.
- The same common optional analysis inputs as `lopper_analyse_top_dependencies`, except baseline comparison inputs and runtime test commands are not supported.

When neither `baselineKey` nor `baselineLabel` is provided, the key defaults to the current git commit, `commit:<sha>`. Structured output includes `baselineKey`, `snapshotPath`, `reportSummary`, and the saved report.

### `lopper_save_dashboard_baseline`

Preview mutation tool. Runs dashboard aggregation and saves an immutable dashboard baseline snapshot through the existing dashboard baseline workflow.

Required input:

- `repoPath`: local repository directory used for default commit-key resolution.
- `baselineStorePath`: local snapshot directory. Relative paths resolve under `repoPath`.
- `confirmSave`: must be `true`.
- Either `repos` or `configPath`.

Optional input:

- `repos`: array of `{ "path": "...", "name": "...", "language": "..." }` entries.
- `configPath`: dashboard config file path.
- `topN`: number of dependencies per repo. Defaults to `20`.
- `defaultLanguage`: language applied to repo entries without a language. Defaults to `auto`.
- `baselineKey` or `baselineLabel`, with the same rules as `lopper_save_baseline`.
- `enableFeatures` / `disableFeatures`.
- `timeoutMillis`.

Structured output includes `baselineKey`, `snapshotPath`, `dashboardSummary`, and the saved dashboard report.

### `lopper_list_languages`

Lists supported language adapters and config metadata.

Optional input:

- `repoPath`: local repository directory used to resolve `.lopper.yml`, `.lopper.yaml`, or `lopper.json`.
- `configPath`: local config file path. Requires `repoPath`.
- `enableFeatures` / `disableFeatures`: feature overrides used for the metadata response.
- `timeoutMillis`: per-tool timeout.

The response includes canonical language IDs, aliases, supported language modes, runtime profiles, effective threshold defaults or config values, removal candidate weights, license policy, vulnerability advisory policy, enabled feature codes, and policy source trace when config is loaded.

## Output Shape

Analysis and mutation tools return MCP tool content with:

- `content[0].text`: concise human summary.
- `structuredContent.schemaVersion`: Lopper report schema version.
- `structuredContent.summary`: same concise summary string.
- `structuredContent.report`: full report payload aligned with `docs/report-schema.json` for analysis and analysis-baseline tools, or the dashboard report shape for dashboard baseline saves.

When `advisorySourcePath` or config `advisories.source` is set, include
`reachability-vulnerability-prioritization-preview` in `enableFeatures` or config
`features.enable`. Analysis reports then include
`dependencies[].vulnerabilities`, `summary.vulnerabilities`, and baseline
`newReachableVulnerabilities` fields when applicable. The priority is
reachability-weighted triage metadata, not an exploitability claim.

Tool failures return `isError: true` with a text message and structured error:

```json
{
  "error": {
    "code": "invalid_input",
    "message": "repoPath is required"
  }
}
```

Error codes include `invalid_input`, `timeout`, `cancelled`, and `tool_failed`.

## Safety Behavior

The MCP server is local-first and read-only by default:

- `repoPath` must be an explicit local filesystem directory.
- Read-only tools reject unknown tool arguments, including mutation or command-execution fields such as `applyCodemod`, `saveBaseline`, `baselineStorePath` on non-baseline tools, and `runtimeTestCommand`.
- Baseline comparison loads existing files only. It does not write snapshots.
- Config resolution uses the existing local config and policy-pack rules; remote policy packs are disabled before any fetch.
- Runtime trace support reads a local trace file only. MCP does not run test commands.
- Advisory support reads a local JSON or YAML file only. MCP does not fetch a
  vulnerability database.
- Analysis observes JSON-RPC request context and optional `timeoutMillis` cancellation.

Explicit mutation tools add these guardrails:

- The MCP process must be started with `mcp-mutation-tools` enabled; per-call `enableFeatures` cannot turn on hidden mutation tools after startup. The legacy `mcp-mutation-tools-preview` name remains accepted for v2.0.0 compatibility.
- Mutations require a dedicated tool name plus `confirmApply: true` or `confirmSave: true`.
- Mutation store paths and dashboard repo paths must be local filesystem paths. Relative `baselineStorePath` values resolve under `repoPath`.
- `baselineKey` and `baselineLabel` are mutually exclusive.
- Codemod apply requires a specific dependency and refuses dirty git worktrees unless `allowDirty` is true.
- Codemod apply writes rollback artifacts under `.artifacts/lopper-codemod-backups`.
- Baseline saves use immutable exclusive-create snapshots and fail if the key already exists.
