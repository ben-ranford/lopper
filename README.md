# Lopper
[![Release](https://github.com/ben-ranford/lopper/actions/workflows/release.yml/badge.svg)](https://github.com/ben-ranford/lopper/actions/workflows/release.yml)
[![SonarCloud Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=ben-ranford_lopper&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=ben-ranford_lopper)

Lopper is a local-first CLI/TUI for measuring dependency surface area in source repos.
It compares imported dependencies to actual usage and reports waste, risk cues, and recommendations.

## Install

macOS/Linux (Homebrew tap, stable):

```bash
brew tap ben-ranford/tap
brew install lopper
```

macOS/Linux (Homebrew tap, rolling):

```bash
brew install ben-ranford/tap/lopper-rolling
```

`lopper-rolling` tracks `main` and is not a stable semver release.

Windows (GitHub Releases):

```bash
# Open latest release page and download the Windows asset for your platform.
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

Emit SARIF for code scanning:

```bash
lopper analyse --top 20 --repo . --language all --format sarif > lopper.sarif
```

Launch the interactive TUI:

```bash
lopper tui --repo . --language all
```

Tune thresholds and score weights:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --threshold-fail-on-increase 2 \
  --threshold-low-confidence-warning 35 \
  --threshold-min-usage-percent 45 \
  --score-weight-usage 0.50 \
  --score-weight-impact 0.30 \
  --score-weight-confidence 0.20
```

Save an immutable baseline snapshot keyed by commit:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --format json \
  --baseline-store .artifacts/lopper-baselines \
  --save-baseline
```

Save using a human label key:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --format json \
  --baseline-store .artifacts/lopper-baselines \
  --save-baseline \
  --baseline-label release-candidate
```

Compare against a stored baseline key and gate CI:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --format json \
  --baseline-store .artifacts/lopper-baselines \
  --baseline-key commit:abc123 \
  --threshold-fail-on-increase 2
```

## Terminal demos

| Demo | What it demonstrates | GIF preview |
| --- | --- | --- |
| Quick start ranking | End-to-end `--top` workflow and waste-ranked dependency table for fast triage. | ![Quick start top ranking demo](docs/demos/assets/quickstart-top.gif) |
| Single dependency deep dive | Focused analysis of one dependency with detailed usage signal and recommendation context. | ![Single dependency demo](docs/demos/assets/single-dependency.gif) |
| Baseline gate in CI flow | Baseline comparison and increase gating to catch regression risk in automated checks. | ![Baseline gating demo](docs/demos/assets/baseline-gate.gif) |

## Languages

- Supported adapters: `js-ts`, `python`, `cpp`, `jvm`, `go`, `php`, `ruby`, `rust`, `dotnet`, `elixir`
- Source of truth for adapter IDs: `lopper --help`
- Language modes:
  - `auto`: choose highest-confidence adapter
  - `all`: run all matching adapters and merge results
  - `<id>`: force one adapter

Repo-level config example (`.lopper.yml`):

```yaml
policy:
  packs:
    - ./policies/org-defaults.yml
    - https://example.com/lopper/policy.yml#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 35
  min_usage_percent_for_recommendations: 45
  removal_candidate_weight_usage: 0.50
  removal_candidate_weight_impact: 0.30
  removal_candidate_weight_confidence: 0.20
```

Threshold defaults:

- `fail_on_increase_percent: 0` (disabled unless set above `0`)
- `low_confidence_warning_percent: 40`
- `min_usage_percent_for_recommendations: 40`
- `removal_candidate_weight_usage: 0.50`
- `removal_candidate_weight_impact: 0.30`
- `removal_candidate_weight_confidence: 0.20`

Threshold ranges:

- `fail_on_increase_percent` must be `>= 0`
- `low_confidence_warning_percent` must be between `0` and `100`
- `min_usage_percent_for_recommendations` must be between `0` and `100`
- removal candidate weights must be `>= 0` and at least one must be greater than `0`

Precedence is `CLI > repo config > imported policy packs > defaults`.

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

Or let Lopper run the test command and capture the trace automatically:

```bash
lopper analyse --top 20 --repo . --language js-ts --runtime-test-command "npm test"
```

Use trace in analysis:

```bash
lopper analyse --top 20 --repo . --language js-ts --runtime-trace .artifacts/lopper-runtime.ndjson
```

With runtime traces enabled:

- `runtimeUsage.correlation` marks each JS/TS dependency as `static-only`, `runtime-only`, or `overlap`.
- `runtimeUsage.modules` includes runtime-loaded module paths.
- `runtimeUsage.topSymbols` includes best-effort runtime symbol hits.

If `--runtime-trace` points to a missing file, analysis continues with static results and adds a warning.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and commands.

## Docs

- Report schema: `docs/report-schema.json`, `docs/report-schema.md`
- SARIF code scanning: `docs/sarif-code-scanning.md`
- Threshold tuning: `docs/threshold-tuning.md`
- Runtime trace annotations: `scripts/runtime/`
- Adapter and architecture extensibility: `docs/extensibility.md`
- CI and release workflow: `docs/ci-usage.md`
- Contribution guide: `CONTRIBUTING.md`
