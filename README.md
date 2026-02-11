# Lopper

Lopper is a local-first CLI/TUI for measuring dependency surface area in source repositories.
It analyzes what you import and what you actually use, then reports waste, risk cues, and
recommendations across supported languages.

## Features

- Analyze a single dependency or rank top dependencies by waste
- Multi-language mode (`--language all`) with per-language breakdowns
- JSON and table output formats
- Optional runtime trace annotations for JS/TS dependency loads
- Baseline comparison and CI-friendly waste increase gating
- Interactive terminal summary/detail view (`surfarea` / `surfarea tui`)

## Supported language adapters

- `js-ts` (JavaScript/TypeScript)
- `python` (Python)
- `jvm` (Java/Kotlin import analysis)

Language selection modes:

- `auto`: choose the highest-confidence detected adapter
- `all`: run all matching adapters and merge results
- `<id>`: force one adapter (`js-ts`, `python`, `jvm`)

## Quick start

Run from source:

```bash
go run ./cmd/surfarea --help
```

Analyze one dependency:

```bash
go run ./cmd/surfarea analyse lodash --repo . --language js-ts
```

Rank dependencies:

```bash
go run ./cmd/surfarea analyse --top 20 --repo . --language all --format table
```

Emit JSON report:

```bash
go run ./cmd/surfarea analyse --top 20 --repo . --language all --format json
```

Launch TUI:

```bash
go run ./cmd/surfarea tui --repo . --language all
```

## Runtime trace annotations (JS/TS)

Capture a runtime trace:

```bash
export LOPPER_RUNTIME_TRACE=.artifacts/lopper-runtime.ndjson
export NODE_OPTIONS="--require ./scripts/runtime/require-hook.cjs --loader ./scripts/runtime/loader.mjs"
npm test
```

Use trace in analysis:

```bash
go run ./cmd/surfarea analyse --top 20 --repo . --language js-ts --runtime-trace .artifacts/lopper-runtime.ndjson
```

## Development

```bash
make fmt
make test
make lint
make build
```

CI/release helper targets:

```bash
make ci
make release VERSION=v0.1.0
```

## Documentation

- Report schema: `docs/report-schema.json`, `docs/report-schema.md`
- Adapter and architecture extensibility: `docs/extensibility.md`
- CI and release workflow: `docs/ci-usage.md`
- Contribution guide: `CONTRIBUTING.md`
