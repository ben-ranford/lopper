# Report schema

`lopper analyse --format json` emits the report structure defined in
`docs/report-schema.json`.

## Generate a report

```bash
go run ./cmd/lopper analyse --top 20 --repo . --language all --format json > report.json
```

Validate with your JSON Schema tooling against `docs/report-schema.json`.

## Key fields

- `summary`: aggregated totals across all dependency rows.
- `languageBreakdown`: aggregate totals by adapter language (`js-ts`, `python`, `cpp`, `jvm`, `go`, `php`, `rust`, `dotnet`).
- `effectiveThresholds`: resolved threshold values applied for this run.
- `effectivePolicy`: resolved policy object, including precedence sources, scoring weights, and license policy controls (`CLI > repo config > imported policy packs > defaults`).
- `cache`: incremental analysis cache metadata (hits/misses/writes and invalidation reasons).
- `dependencies[].language`: language tag for each dependency row.
- `dependencies[].license`: normalized per-dependency license detection (`spdx`, `source`, `confidence`, `unknown`, `denied`).
- `dependencies[].provenance`: per-dependency provenance signals (`source`, `confidence`, `signals`).
- `dependencies[].riskCues`: heuristic risk signals.
- `dependencies[].recommendations`: actionable follow-up suggestions.
- `dependencies[].codemod`: optional suggest-only patch previews and unsafe-transform skip reason codes for JS/TS subpath migrations.
- `dependencies[].runtimeUsage`: runtime load annotations (when `--runtime-trace` is used).
- `dependencies[].usedImports[].provenance`: optional attribution chain for barrel/re-export resolution in detailed views.
- `wasteIncreasePercent`: present when `--baseline` was supplied and compared.
- `baselineComparison`: deterministic dependency-level deltas between baseline and current run, including `newDeniedLicenses`.

## Notes

- `runtimeUsage` currently annotates JS/TS dependencies.
- `runtimeUsage.correlation` distinguishes `static-only`, `runtime-only`, and `overlap` evidence categories.
- `runtimeUsage.modules` lists runtime-loaded module paths seen for a dependency.
- `runtimeUsage.topSymbols` lists best-effort runtime symbol hits derived from module subpaths.
- `cache.invalidations` entries identify deterministic invalidation reasons (for example `input-changed`).
- `usedPercent` values are adapter best-effort based on static analysis signals.
- `summary.knownLicenseCount`, `summary.unknownLicenseCount`, and `summary.deniedLicenseCount` track license rollups across dependency rows.
- `schemaVersion` is currently pinned to `0.1.0`.
- Baseline snapshots created with `--save-baseline --baseline-store DIR` are stored as immutable files keyed by `commit:<sha>` (default) or `label:<name>` when `--baseline-label` is passed.
