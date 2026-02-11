# Lopper
[![Release](https://github.com/ben-ranford/lopper/actions/workflows/release.yml/badge.svg)](https://github.com/ben-ranford/lopper/actions/workflows/release.yml)
[![SonarCloud Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=ben-ranford_lopper&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=ben-ranford_lopper)

Lopper is a local-first CLI/TUI for measuring dependency surface area in source repositories.
It analyzes what you import and what you actually use, then reports waste, risk cues, and
recommendations across supported languages.

## Features

- Analyze a single dependency or rank top dependencies by waste
- Multi-language mode (`--language all`) with per-language breakdowns
- JSON and table output formats
- Optional runtime trace annotations for JS/TS dependency loads
- Baseline comparison and CI-friendly waste increase gating
- Interactive terminal summary/detail view (`lopper` / `lopper tui`)

## Supported language adapters

- `js-ts` (JavaScript/TypeScript)
- `python` (Python)
- `jvm` (Java/Kotlin import analysis)
- `go` (Go module import analysis)

Language selection modes:

- `auto`: choose the highest-confidence detected adapter
- `all`: run all matching adapters and merge results
- `<id>`: force one adapter (`js-ts`, `python`, `jvm`, `go`)

## Quick start

Install binary from GitHub Releases:

```bash
VERSION=v0.1.0
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" && exit 1 ;;
esac

curl -fsSL -o /tmp/lopper.tar.gz \
  "https://github.com/ben-ranford/lopper/releases/download/${VERSION}/surfarea_${VERSION}_${OS}_${ARCH}.tar.gz"
tar -xzf /tmp/lopper.tar.gz -C /tmp
sudo install /tmp/surfarea /usr/local/bin/lopper
lopper --help
```

Run without local install (Docker):

```bash
docker run --rm ghcr.io/ben-ranford/lopper:latest --help
```

Analyze one dependency:

```bash
lopper analyse lodash --repo . --language js-ts
```

Rank dependencies:

```bash
lopper analyse --top 20 --repo . --language all --format table
```

Emit JSON report:

```bash
lopper analyse --top 20 --repo . --language all --format json
```

Launch TUI:

```bash
lopper tui --repo . --language all
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
lopper analyse --top 20 --repo . --language js-ts --runtime-trace .artifacts/lopper-runtime.ndjson
```

## Development

```bash
make fmt
make test
make lint
make cov
make build
```

CI/release helper targets:

```bash
make ci
make cov
make release VERSION=v0.1.0
make toolchain-check
```

## Documentation

- Report schema: `docs/report-schema.json`, `docs/report-schema.md`
- Adapter and architecture extensibility: `docs/extensibility.md`
- CI and release workflow: `docs/ci-usage.md`
- Contribution guide: `CONTRIBUTING.md`
