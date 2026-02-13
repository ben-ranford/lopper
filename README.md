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
- Tunable thresholds via CLI flags or repo config (`.lopper.yml` / `lopper.json`)
- Interactive terminal summary/detail view (`lopper` / `lopper tui`)

## Supported language adapters

- `js-ts` (JavaScript/TypeScript)
- `python` (Python)
- `jvm` (Java/Kotlin import analysis)
- `go` (Go module import analysis)
- `php` (Composer/PSR-4 import analysis)
- `rust` (Cargo crate import analysis)

Language selection modes:

- `auto`: choose the highest-confidence detected adapter
- `all`: run all matching adapters and merge results
- `<id>`: force one adapter (`js-ts`, `python`, `jvm`, `go`, `php`, `rust`)

## Quick start

Install binary from GitHub Releases:

```bash
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" && exit 1 ;;
esac

asset_url="$(
  curl -fsSL https://api.github.com/repos/ben-ranford/lopper/releases/latest \
  | jq -r --arg os "$OS" --arg arch "$ARCH" \
    '.assets[] | select(.name | test("^lopper_.*_" + $os + "_" + $arch + "\\.tar\\.gz$")) | .browser_download_url' \
  | head -n1
)"
[ -n "$asset_url" ] || { echo "No matching asset for ${OS}/${ARCH}"; exit 1; }

curl -fsSL -o /tmp/lopper.tar.gz "$asset_url"
tmpdir="$(mktemp -d)"
tar -xzf /tmp/lopper.tar.gz -C "$tmpdir"
sudo install "$(find "$tmpdir" -type f -name lopper | head -n1)" /usr/local/bin/lopper
```

Run without local install (Docker):

```bash
docker run --rm ghcr.io/ben-ranford/lopper:latest --help
```

Analyze one dependency:

```bash
lopper analyse lodash --repo . --language js-ts
```

Analyze a Go dependency:

```bash
lopper analyse github.com/google/uuid --repo . --language go
```

Rank dependencies:

```bash
lopper analyse --top 20 --repo . --language all --format table
```

Emit JSON report:

```bash
lopper analyse --top 20 --repo . --language all --format json
```

Run with explicit threshold tuning:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --threshold-fail-on-increase 2 \
  --threshold-low-confidence-warning 35 \
  --threshold-min-usage-percent 45
```

## Terminal demos

Regenerate all demo assets from source tapes:

```bash
make demos
```

Quick start (`--top` ranking):

![Quick start top ranking demo](docs/demos/assets/quickstart-top.gif)

Single dependency deep dive:

![Single dependency demo](docs/demos/assets/single-dependency.gif)

Baseline gating workflow:

![Baseline gating demo](docs/demos/assets/baseline-gate.gif)

Repo-level config example (`.lopper.yml`):

```yaml
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 35
  min_usage_percent_for_recommendations: 45
```

Threshold defaults:

- `fail_on_increase_percent: 0` (disabled unless set above `0`)
- `low_confidence_warning_percent: 40`
- `min_usage_percent_for_recommendations: 40`

Threshold ranges:

- `fail_on_increase_percent` must be `>= 0`
- `low_confidence_warning_percent` must be between `0` and `100`
- `min_usage_percent_for_recommendations` must be between `0` and `100`

Precedence is `CLI > config > defaults`.

Tuning guide with strict/balanced/noise-reduction profiles:

- `docs/threshold-tuning.md`

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
make setup
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
make demos
make release VERSION=v0.1.0
make toolchain-check
make toolchain-install
make hooks-install
```

Git pre-commit hook:

- Run `make hooks-install` once per clone to enable the repository hook.
- The pre-commit hook runs `make fmt`, `make ci`, and `make cov`.

## Documentation

- Report schema: `docs/report-schema.json`, `docs/report-schema.md`
- Threshold tuning: `docs/threshold-tuning.md`
- Adapter and architecture extensibility: `docs/extensibility.md`
- CI and release workflow: `docs/ci-usage.md`
- Contribution guide: `CONTRIBUTING.md`
