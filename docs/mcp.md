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

`mcp-server-preview` is now a stable feature flag and is enabled by default in every build channel. The original flag name remains available for compatibility and explicit rollback testing via `--disable-feature mcp-server-preview`. During `initialize`, the server advertises tool support and returns Lopper version metadata. Use `tools/list` to inspect tool schemas at runtime.

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
- `enableFeatures` / `disableFeatures`: feature flag names or codes.
- threshold and policy overrides: `lowConfidenceWarningPercent`, `minUsagePercentForRecommendations`, `maxUncertainImportCount`, `scoreWeightUsage`, `scoreWeightImpact`, `scoreWeightConfidence`, `licenseDeny`, `licenseFailOnDeny`, `licenseProvenanceRegistry`.
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

### `lopper_list_languages`

Lists supported language adapters and config metadata.

Optional input:

- `repoPath`: local repository directory used to resolve `.lopper.yml`, `.lopper.yaml`, or `lopper.json`.
- `configPath`: local config file path. Requires `repoPath`.
- `enableFeatures` / `disableFeatures`: feature overrides used for the metadata response.
- `timeoutMillis`: per-tool timeout.

The response includes canonical language IDs, aliases, supported language modes, runtime profiles, effective threshold defaults or config values, removal candidate weights, license policy, enabled feature codes, and policy source trace when config is loaded.

## Output Shape

Analysis tools return MCP tool content with:

- `content[0].text`: concise human summary.
- `structuredContent.schemaVersion`: Lopper report schema version.
- `structuredContent.summary`: same concise summary string.
- `structuredContent.report`: full report payload aligned with `docs/report-schema.json`.

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

The MCP server is local-first and read-only:

- `repoPath` must be an explicit local filesystem directory.
- Unknown tool arguments are rejected, including mutation or command-execution fields such as `applyCodemod`, `saveBaseline`, and `runtimeTestCommand`.
- Baseline comparison loads existing files only. It does not write snapshots.
- Config resolution uses the existing local config and policy-pack rules; remote policy packs are disabled before any fetch.
- Runtime trace support reads a local trace file only. MCP does not run test commands.
- Analysis observes JSON-RPC request context and optional `timeoutMillis` cancellation.
