package cli

const usage = `Usage:
  lopper [tui]
  lopper tui [--repo PATH] [--language auto|all|js-ts|python|cpp|jvm|go|php|rust|dotnet] [--top N] [--filter TEXT] [--sort name|waste] [--page-size N] [--snapshot PATH]
  lopper analyse <dependency> [--repo PATH] [--format table|json|sarif|pr-comment] [--language auto|all|js-ts|python|cpp|jvm|go|php|rust|dotnet] [--cache=true|false] [--cache-path PATH] [--cache-readonly] [--runtime-profile node-import|node-require|browser-import|browser-require] [--baseline PATH] [--baseline-store DIR] [--baseline-key KEY] [--save-baseline] [--baseline-label LABEL] [--runtime-trace PATH] [--runtime-test-command CMD] [--config PATH] [--include GLOBS] [--exclude GLOBS] [--lockfile-drift-policy off|warn|fail] [--suggest-only]
  lopper analyse --top N [--repo PATH] [--format table|json|sarif|pr-comment] [--language auto|all|js-ts|python|cpp|jvm|go|php|rust|dotnet] [--cache=true|false] [--cache-path PATH] [--cache-readonly] [--runtime-profile node-import|node-require|browser-import|browser-require] [--baseline PATH] [--baseline-store DIR] [--baseline-key KEY] [--save-baseline] [--baseline-label LABEL] [--runtime-trace PATH] [--runtime-test-command CMD] [--config PATH] [--include GLOBS] [--exclude GLOBS] [--lockfile-drift-policy off|warn|fail] [--fail-on-increase PERCENT]

Options:
  --repo PATH                Repository path (default: .)
  --top N                    Rank top N dependencies by waste
  --format table|json|sarif|pr-comment
                             Output format for analyse (default: table)
  --language ID              Language adapter (default: auto)
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
  --runtime-test-command CMD Run command with JS/TS runtime hooks to capture trace before analysis
  --include GLOBS          Comma-separated include path globs (repeatable; CLI overrides config scope.include)
  --exclude GLOBS          Comma-separated exclude path globs (repeatable; CLI overrides config scope.exclude)
  --suggest-only             Generate deterministic codemod patch previews for safe JS/TS subpath migrations (no source mutation)
  --config PATH              Config file path (default: repo .lopper.yml/.lopper.yaml/lopper.json)
  --lockfile-drift-policy MODE
                              Lockfile drift policy (off, warn, fail; default: warn)
  --threshold-fail-on-increase N
                              Fail when waste increase is greater than N (CLI > config > defaults)
  --threshold-low-confidence-warning N
                              Warn in --language all mode when adapter confidence is below N
  --threshold-min-usage-percent N
                              Min used export percent before low-usage recommendations are emitted
  --threshold-max-uncertain-imports N
                              Fail when unresolved dynamic import/require usage count exceeds N
  --score-weight-usage N      Relative removal-candidate weight for usage signal (default: 0.50)
  --score-weight-impact N     Relative removal-candidate weight for impact signal (default: 0.30)
  --score-weight-confidence N Relative removal-candidate weight for confidence signal (default: 0.20)
  --snapshot PATH            Write a non-interactive TUI snapshot to file
  --filter TEXT              Filter dependency names (TUI)
  --sort name|waste          Sort TUI output (default: waste)
  --page-size N              TUI page size (default: 10)
  --fail-on-increase PERCENT Legacy alias for --threshold-fail-on-increase
  -h, --help                 Show this help text
`

func Usage() string {
	return usage
}
