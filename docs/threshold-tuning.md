# Threshold Tuning Guide

This guide explains how to tune `lopper` threshold behavior for CI quality gates and report noise levels.

## What each threshold does

- `fail_on_increase_percent`: fails the command when `wasteIncreasePercent` is greater than this value.
- `low_confidence_warning_percent`: emits warning in `--language all` mode when adapter confidence is below this value.
- `min_usage_percent_for_recommendations`: controls when low-usage recommendations are emitted for dependencies.

Default values:

- `fail_on_increase_percent: 0` (disabled unless set above `0`)
- `low_confidence_warning_percent: 40`
- `min_usage_percent_for_recommendations: 40`

Validation ranges:

- `fail_on_increase_percent >= 0`
- `low_confidence_warning_percent` in `[0, 100]`
- `min_usage_percent_for_recommendations` in `[0, 100]`

## Ways to set thresholds

Use CLI flags for per-run overrides:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --threshold-fail-on-increase 2 \
  --threshold-low-confidence-warning 35 \
  --threshold-min-usage-percent 45
```

Use repo config for team defaults (`.lopper.yml`, `.lopper.yaml`, or `lopper.json` in repo root):

```yaml
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 35
  min_usage_percent_for_recommendations: 45
```

You can also pass an explicit config path:

```bash
lopper analyse --top 20 --repo . --config path/to/lopper.yml
```

Precedence is always:

`CLI > config > defaults`

## Recommended tuning profiles

### Strict CI gate

Use this when you want faster regression detection and are okay with more warnings.

```yaml
thresholds:
  fail_on_increase_percent: 1
  low_confidence_warning_percent: 55
  min_usage_percent_for_recommendations: 60
```

### Balanced default-like behavior

Use this for stable signal without over-triggering.

```yaml
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 40
  min_usage_percent_for_recommendations: 40
```

### Noise reduction

Use this when your repository currently produces too many warnings/recommendations.

```yaml
thresholds:
  fail_on_increase_percent: 5
  low_confidence_warning_percent: 25
  min_usage_percent_for_recommendations: 25
```

## How to verify effective values

Table output includes an `Effective thresholds` section.

JSON output includes `effectiveThresholds`:

```bash
lopper analyse --top 20 --repo . --language all --format json | jq '.effectiveThresholds'
```

## CI usage notes

- If `fail_on_increase_percent` is above `0`, you need `--baseline PATH` for compare mode.
- Legacy alias `--fail-on-increase` is supported and maps to `--threshold-fail-on-increase`.
- Avoid defining the same threshold key twice in config (for example both top-level and under `thresholds`) because that returns a validation error.
