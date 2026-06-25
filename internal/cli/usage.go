package cli

const usage = `Usage:
  lopper [--version] [tui]
  lopper tui [--repo PATH] [--language auto|all|js-ts|python|cpp|jvm|kotlin-android|go|php|ruby|rust|dotnet|elixir|swift|dart|powershell] [--top N] [--filter TEXT] [--sort name|waste] [--page-size N] [--snapshot PATH] [--baseline PATH] [--baseline-store DIR] [--baseline-key KEY]
  lopper analyse <dependency> [--repo PATH] [--scope-mode repo|package|changed-packages] [--format table|csv|json|sarif|pr-comment|cyclonedx-json] [--language auto|all|js-ts|python|cpp|jvm|kotlin-android|go|php|ruby|rust|dotnet|elixir|swift|dart|powershell] [--cache=true|false] [--cache-path PATH] [--cache-readonly] [--runtime-profile node-import|node-require|browser-import|browser-require] [--baseline PATH] [--baseline-store DIR] [--baseline-key KEY] [--save-baseline] [--baseline-label LABEL] [--runtime-trace PATH] [--runtime-test-command CMD] [--advisory-source PATH] [--config PATH] [--include GLOBS] [--exclude GLOBS] [--lockfile-drift-policy off|warn|fail] [--license-deny SPDXS] [--license-fail-on-deny] [--license-provenance-registry] [--notify-on always|breach|regression|improvement] [--notify-slack URL] [--notify-teams URL] [--enable-feature NAME] [--suggest-only | (--apply-codemod --apply-codemod-confirm [--allow-dirty])]
  lopper analyse --top N [--repo PATH] [--scope-mode repo|package|changed-packages] [--format table|csv|json|sarif|pr-comment|cyclonedx-json] [--language auto|all|js-ts|python|cpp|jvm|kotlin-android|go|php|ruby|rust|dotnet|elixir|swift|dart|powershell] [--cache=true|false] [--cache-path PATH] [--cache-readonly] [--runtime-profile node-import|node-require|browser-import|browser-require] [--baseline PATH] [--baseline-store DIR] [--baseline-key KEY] [--save-baseline] [--baseline-label LABEL] [--runtime-trace PATH] [--runtime-test-command CMD] [--advisory-source PATH] [--config PATH] [--include GLOBS] [--exclude GLOBS] [--lockfile-drift-policy off|warn|fail] [--license-deny SPDXS] [--license-fail-on-deny] [--license-provenance-registry] [--notify-on always|breach|regression|improvement] [--notify-slack URL] [--notify-teams URL] [--enable-feature NAME] [--fail-on-increase PERCENT]
  lopper dashboard --repos PATH1,PATH2 [--format json|csv|html] [--top N] [--language auto|all|js-ts|python|cpp|jvm|kotlin-android|go|php|ruby|rust|dotnet|elixir|swift|dart|powershell] [--output PATH] [--baseline-store DIR] [--baseline-key KEY] [--baseline-label LABEL] [--save-baseline] [--enable-feature NAME] [--disable-feature NAME]
  lopper dashboard --config lopper-org.yml [--format json|csv|html] [--top N] [--language auto|all|js-ts|python|cpp|jvm|kotlin-android|go|php|ruby|rust|dotnet|elixir|swift|dart|powershell] [--output PATH] [--baseline-store DIR] [--baseline-key KEY] [--baseline-label LABEL] [--save-baseline] [--enable-feature NAME] [--disable-feature NAME]
  lopper features [--format table|json] [--channel dev|rolling|release] [--release VERSION]
  lopper profile apply strict|balanced|noise-reduction [--output PATH] [--force] [--enable-feature threshold-profiles]
  lopper mcp

Options:
  --repo PATH                Repository path (default: .)
  --top N                    Rank top N dependencies by waste
  --scope-mode MODE          Analysis scope mode: repo, package, or changed-packages (default: package)
  --format table|csv|json|sarif|pr-comment|cyclonedx-json
                             Output format for analyse (default: table)
                             cyclonedx-json is preview-gated by sbom-attestation-exports-preview
  --language ID              Language adapter (default: auto)
                             Supported IDs: auto, all, js-ts, python, cpp, jvm, kotlin-android, go, php, ruby, rust, dotnet, elixir, swift, dart, powershell
  --cache=true|false         Enable or disable incremental analysis cache (default: true)
  --cache-path PATH          Cache directory path (default: <repo>/.lopper-cache)
  --cache-readonly           Read cache entries but do not write misses
  --runtime-profile PROFILE  Conditional exports runtime profile (default: node-import)
  --baseline PATH            Baseline report (JSON) for comparison
  --baseline-store DIR       Directory for immutable keyed baseline snapshots
  --baseline-key KEY         Key to load from baseline snapshot directory
  --save-baseline            Save current run as immutable baseline snapshot
  --baseline-label LABEL     Label key to use when saving baseline snapshots
  --runtime-trace PATH       Runtime import trace (NDJSON) for annotations
  --runtime-test-command CMD Run command with JS/TS or preview Python runtime hooks to capture trace before analysis
  --advisory-source PATH     Local vulnerability advisory source (preview-gated by reachability-vulnerability-prioritization-preview)
  --repos PATH1,PATH2        Comma-separated repo paths for org dashboard input
  --baseline-store DIR       Directory for immutable keyed dashboard baseline snapshots
  --baseline-key KEY         Baseline snapshot key for dashboard comparison
  --baseline-label LABEL     Label key to use when saving dashboard baselines
  --save-baseline            Save current dashboard run as an immutable baseline snapshot
  --include GLOBS            Comma-separated include path globs (repeatable; CLI overrides config scope.include)
  --exclude GLOBS            Comma-separated exclude path globs (repeatable; CLI overrides config scope.exclude)
  --suggest-only             Generate deterministic codemod patch previews for safe JS/TS subpath migrations (no source mutation)
  --apply-codemod            Apply deterministic codemod patch previews for safe JS/TS subpath migrations
  --apply-codemod-confirm    Required confirmation flag for --apply-codemod
  --allow-dirty              Allow --apply-codemod to run in a dirty git worktree
  --config PATH              Config file path (default: repo .lopper.yml/.lopper.yaml/lopper.json)
  --enable-feature NAME      Feature flag name or code to enable (repeatable, comma-separated)
  --disable-feature NAME     Feature flag name or code to disable (repeatable, comma-separated)
  --output PATH, -o PATH     Write command output to file instead of stdout
  --force                    Allow profile apply to overwrite an existing output file
  --lockfile-drift-policy MODE
                              Lockfile drift policy (off, warn, fail; default: warn)
  --license-deny SPDXS        Comma-separated denied SPDX IDs (e.g. GPL-3.0-only,AGPL-3.0-only)
  --license-fail-on-deny      Fail when denied licenses are detected
  --license-provenance-registry
                              Opt in to registry provenance heuristics for JS/TS dependencies
  --notify-on MODE            Notify on always|breach|regression|improvement (CLI > env > config > defaults)
  --notify-slack URL          Slack webhook URL (trusted CLI or env only; CLI > env)
  --notify-teams URL          Teams webhook URL (trusted CLI or env only; CLI > env)
  --threshold-fail-on-increase N
                              Fail when waste increase is greater than N (CLI > config > defaults)
  --threshold-low-confidence-warning N
                              Warn in --language all mode when adapter confidence is below N
  --threshold-min-usage-percent N
                              Min used export percent before low-usage recommendations are emitted
  --threshold-max-uncertain-imports N
                              Fail when unresolved dynamic import/require usage count exceeds N
  --threshold-reachable-vuln-priority PRIORITY
                              Fail when reachable vulnerability priority meets off|low|medium|high|critical (same preview gate as --advisory-source)
  --score-weight-usage N      Relative removal-candidate weight for usage signal (default: 0.50)
  --score-weight-impact N     Relative removal-candidate weight for impact signal (default: 0.30)
  --score-weight-confidence N Relative removal-candidate weight for confidence signal (default: 0.20)
  --snapshot PATH            Write a non-interactive TUI snapshot to file
  --filter TEXT              Filter dependency names (TUI)
  --sort name|waste          Sort TUI output (default: waste)
  --page-size N              TUI page size (default: 10)
  --baseline PATH            Baseline report (JSON) for TUI comparison
  --baseline-store DIR       Directory for immutable keyed TUI baseline snapshots
  --baseline-key KEY         Baseline snapshot key for TUI comparison
  TUI commands:
    apply-codemod [dep] --confirm [--allow-dirty]
                              Apply safe codemod suggestions shown in dependency detail
    save-baseline [label] [--store DIR] [--key KEY]
                              Save the current TUI report; defaults to .artifacts/lopper-baselines and commit:<HEAD>
    compare-baseline <key|file> [--store DIR]
                              Refresh TUI summary/detail views with baseline deltas
  --fail-on-increase PERCENT Legacy alias for --threshold-fail-on-increase
  --version                  Show CLI version metadata
  mcp                        Run a local stdio MCP server with read-only dependency analysis tools
  -h, --help                 Show this help text
`

func Usage() string {
	return usage
}
