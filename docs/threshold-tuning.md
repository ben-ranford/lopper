# Threshold Tuning Guide

This guide explains how to tune `lopper` threshold behavior for CI quality gates and report noise levels.

## What each threshold does

- `fail_on_increase_percent`: fails the command when `wasteIncreasePercent` is greater than this value.
- `low_confidence_warning_percent`: emits warning in `--language all` mode when adapter confidence is below this value.
- `min_usage_percent_for_recommendations`: controls when low-usage recommendations are emitted for dependencies.
- `removal_candidate_weight_usage`: relative weight for removal-candidate usage signal.
- `removal_candidate_weight_impact`: relative weight for removal-candidate impact signal.
- `removal_candidate_weight_confidence`: relative weight for removal-candidate confidence signal.

Default values:

- `fail_on_increase_percent: 0` (disabled unless set above `0`)
- `low_confidence_warning_percent: 40`
- `min_usage_percent_for_recommendations: 40`
- `removal_candidate_weight_usage: 0.50`
- `removal_candidate_weight_impact: 0.30`
- `removal_candidate_weight_confidence: 0.20`

Validation ranges:

- `fail_on_increase_percent >= 0`
- `low_confidence_warning_percent` in `[0, 100]`
- `min_usage_percent_for_recommendations` in `[0, 100]`
- `removal_candidate_weight_*` values must be `>= 0`
- At least one removal-candidate weight must be greater than `0`

## Ways to set thresholds

Use CLI flags for per-run overrides:

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

Use repo config for team defaults (`.lopper.yml`, `.lopper.yaml`, or `lopper.json` in repo root):

```yaml
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 35
  min_usage_percent_for_recommendations: 45
  removal_candidate_weight_usage: 0.50
  removal_candidate_weight_impact: 0.30
  removal_candidate_weight_confidence: 0.20
```

You can also pass an explicit config path:

```bash
lopper analyse --top 20 --repo . --config path/to/lopper.yml
```

Use reusable policy packs in config:

```yaml
policy:
  packs:
    - ./policies/org-defaults.yml
    - ./policies/team-overrides.yml
thresholds:
  fail_on_increase_percent: 2
```

Policy precedence is deterministic:

`CLI > repo config > imported policy packs > defaults`

## Recommended tuning profiles

### Strict CI gate

Use this when you want faster regression detection and are okay with more warnings.

```yaml
thresholds:
  fail_on_increase_percent: 1
  low_confidence_warning_percent: 55
  min_usage_percent_for_recommendations: 60
  removal_candidate_weight_usage: 0.60
  removal_candidate_weight_impact: 0.25
  removal_candidate_weight_confidence: 0.15
```

### Balanced default-like behavior

Use this for stable signal without over-triggering.

```yaml
thresholds:
  fail_on_increase_percent: 2
  low_confidence_warning_percent: 40
  min_usage_percent_for_recommendations: 40
  removal_candidate_weight_usage: 0.50
  removal_candidate_weight_impact: 0.30
  removal_candidate_weight_confidence: 0.20
```

### Noise reduction

Use this when your repository currently produces too many warnings/recommendations.

```yaml
thresholds:
  fail_on_increase_percent: 5
  low_confidence_warning_percent: 25
  min_usage_percent_for_recommendations: 25
  removal_candidate_weight_usage: 0.35
  removal_candidate_weight_impact: 0.25
  removal_candidate_weight_confidence: 0.40
```

## How to verify effective values

Table output includes an `Effective thresholds` section.

JSON output includes `effectiveThresholds`:

```bash
lopper analyse --top 20 --repo . --language all --format json | jq '.effectiveThresholds'
```

The full merged policy is available as `effectivePolicy` (sources, thresholds, and removal-candidate weights):

```bash
lopper analyse --top 20 --repo . --language all --format json | jq '.effectivePolicy'
```

## CI usage notes

- If `fail_on_increase_percent` is above `0`, you need `--baseline PATH` for compare mode.
- You can use immutable keyed snapshots instead of a raw file path:
  - Save baseline: `--baseline-store DIR --save-baseline` (defaults key to `commit:<sha>`)
  - Save labeled baseline: add `--baseline-label LABEL`
  - Compare baseline: `--baseline-store DIR --baseline-key KEY`
- Legacy alias `--fail-on-increase` is supported and maps to `--threshold-fail-on-increase`.
- Avoid defining the same threshold key twice in config (for example both top-level and under `thresholds`) because that returns a validation error.
