package cli

const usage = `Usage:
  lopper [tui]
  lopper tui [--repo PATH] [--language auto|all|js-ts|python|cpp|jvm|go|php|rust|dotnet] [--top N] [--filter TEXT] [--sort name|waste] [--page-size N] [--snapshot PATH]
  lopper analyse <dependency> [--repo PATH] [--format table|json] [--language auto|all|js-ts|python|cpp|jvm|go|php|rust|dotnet] [--runtime-profile node-import|node-require|browser-import|browser-require] [--baseline PATH] [--runtime-trace PATH] [--config PATH]
  lopper analyse --top N [--repo PATH] [--format table|json] [--language auto|all|js-ts|python|cpp|jvm|go|php|rust|dotnet] [--runtime-profile node-import|node-require|browser-import|browser-require] [--baseline PATH] [--runtime-trace PATH] [--config PATH] [--fail-on-increase PERCENT]

Options:
  --repo PATH                Repository path (default: .)
  --top N                    Rank top N dependencies by waste
  --format table|json        Output format for analyse (default: table)
  --language ID              Language adapter (default: auto)
  --runtime-profile PROFILE  Conditional exports runtime profile (default: node-import)
  --baseline PATH            Baseline report (JSON) for comparison
  --runtime-trace PATH       Runtime import trace (NDJSON) for annotations
  --config PATH              Config file path (default: repo .lopper.yml/.lopper.yaml/lopper.json)
  --threshold-fail-on-increase N
                              Fail when waste increase is greater than N (CLI > config > defaults)
  --threshold-low-confidence-warning N
                              Warn in --language all mode when adapter confidence is below N
  --threshold-min-usage-percent N
                              Min used export percent before low-usage recommendations are emitted
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
