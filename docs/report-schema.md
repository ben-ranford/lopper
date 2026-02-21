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
- `effectiveThresholds`: resolved thresholds applied for this run (`CLI > config > defaults`).
- `dependencies[].language`: language tag for each dependency row.
- `dependencies[].riskCues`: heuristic risk signals.
- `dependencies[].recommendations`: actionable follow-up suggestions.
- `dependencies[].runtimeUsage`: runtime load annotations (when `--runtime-trace` is used).
- `dependencies[].usedImports[].provenance`: optional attribution chain for barrel/re-export resolution in detailed views.
- `wasteIncreasePercent`: present when `--baseline` was supplied and compared.

## Notes

- `runtimeUsage` currently annotates JS/TS dependencies.
- `runtimeUsage.correlation` distinguishes `static-only`, `runtime-only`, and `overlap` evidence categories.
- `usedPercent` values are adapter best-effort based on static analysis signals.
- `schemaVersion` is currently pinned to `0.1.0`.
