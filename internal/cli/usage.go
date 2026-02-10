package cli

const usage = `Usage:
  surfarea [tui]
  surfarea tui [--repo PATH] [--language auto|js-ts] [--top N] [--filter TEXT] [--sort name|waste] [--page-size N] [--snapshot PATH]
  surfarea analyse <dependency> [--repo PATH] [--format table|json] [--language auto|js-ts] [--baseline PATH] [--runtime-trace PATH]
  surfarea analyse --top N [--repo PATH] [--format table|json] [--language auto|js-ts] [--baseline PATH] [--runtime-trace PATH] [--fail-on-increase PERCENT]

Options:
  --repo PATH                Repository path (default: .)
  --top N                    Rank top N dependencies by waste
  --format table|json        Output format for analyse (default: table)
  --language ID              Language adapter (default: auto)
  --baseline PATH            Baseline report (JSON) for comparison
  --runtime-trace PATH       Runtime import trace (NDJSON) for annotations
  --snapshot PATH            Write a non-interactive TUI snapshot to file
  --filter TEXT              Filter dependency names (TUI)
  --sort name|waste          Sort TUI output (default: waste)
  --page-size N              TUI page size (default: 10)
  --fail-on-increase PERCENT Fail if waste increases beyond threshold
  -h, --help                 Show this help text
`

func Usage() string {
	return usage
}
