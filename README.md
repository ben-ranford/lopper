# Lopper
[![Release](https://github.com/ben-ranford/lopper/actions/workflows/release.yml/badge.svg)](https://github.com/ben-ranford/lopper/actions/workflows/release.yml)
[![SonarCloud Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=ben-ranford_lopper&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=ben-ranford_lopper)

Lopper is a local-first CLI/TUI for measuring dependency surface area in source repos.
It compares imported dependencies to actual usage and reports waste, risk cues, and recommendations.

## Install

Install from GitHub Releases:

```bash
# Open latest release page and download the asset for your platform.
gh release view --repo ben-ranford/lopper --web
```

Run without local install:

```bash
docker run --rm ghcr.io/ben-ranford/lopper:latest --help
```

## Quick Start

Analyze one dependency:

```bash
lopper analyse lodash --repo . --language js-ts
```

Rank top dependencies by waste:

```bash
lopper analyse --top 20 --repo . --language all --format table
```

Emit JSON:

```bash
lopper analyse --top 20 --repo . --language all --format json
```

Launch the interactive TUI:

```bash
lopper tui --repo . --language all
```

## Languages

- Supported adapters: `js-ts`, `python`, `cpp`, `jvm`, `go`, `php`, `rust`, `dotnet`
- Source of truth for adapter IDs: `lopper --help`
- Language modes:
  - `auto`: choose highest-confidence adapter
  - `all`: run all matching adapters and merge results
  - `<id>`: force one adapter

Repo-level config example (`.lopper.yml`):

```yaml
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 35
  min_usage_percent_for_recommendations: 45
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

## Docs

- Report schema: `docs/report-schema.json`, `docs/report-schema.md`
- Threshold tuning: `docs/threshold-tuning.md`
- Runtime trace annotations: `scripts/runtime/`
- Adapter and architecture extensibility: `docs/extensibility.md`
- CI and release workflow: `docs/ci-usage.md`
- Contribution guide: `CONTRIBUTING.md`
