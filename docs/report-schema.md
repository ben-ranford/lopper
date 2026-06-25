# Report schema

`lopper analyse --format json` emits the report structure defined in
`docs/report-schema.json`.

`lopper analyse --format csv` emits a stable dependency-level export with one row
per dependency.

`lopper analyse --format cyclonedx-json` emits a preview CycloneDX JSON SBOM
for direct dependency rows when `sbom-attestation-exports-preview` is enabled.

## Generate a report

```bash
go run ./cmd/lopper analyse --top 20 --repo . --language all --format json > report.json
```

Validate with your JSON Schema tooling against `docs/report-schema.json`.

## CSV export

Generate CSV:

```bash
go run ./cmd/lopper analyse --top 20 --repo . --language all --format csv > report.csv
```

CSV characteristics:

- Header row is fixed and emitted even when there are no dependency rows.
- Dependency rows are sorted by `language`, then `dependency_name`.
- Multi-value cells use `|` as the inner delimiter.
- Numeric columns use decimal strings; optional nested fields are emitted as empty
  cells when absent.

CSV columns:

- Report context: `generated_at`, `schema_version`, `repo_path`, `scope_mode`, `scope_packages`
- Dependency identity and waste metrics: `language`, `dependency_name`, `used_exports_count`, `total_exports_count`, `used_percent`, `waste_percent`, `estimated_unused_bytes`
- Dependency usage rollups: `top_used_symbols`, `used_imports`, `unused_imports`, `unused_exports`
- Risk and action hints: `risk_cues`, `recommendations`
- Runtime annotations: `runtime_load_count`, `runtime_correlation`, `runtime_only`, `runtime_modules`, `runtime_top_symbols`
- Reachability and candidate scoring: `reachability_model`, `reachability_score`, `reachability_summary`, `reachability_rationale_codes`, `removal_candidate_score`, `removal_candidate_usage`, `removal_candidate_impact`, `removal_candidate_confidence`, `removal_candidate_rationale`
- License metadata: `license_spdx`, `license_raw`, `license_source`, `license_confidence`, `license_unknown`, `license_denied`, `license_evidence`
- Provenance metadata: `provenance_source`, `provenance_confidence`, `provenance_signals`

## CycloneDX SBOM export

Generate a CycloneDX JSON SBOM:

```bash
go run ./cmd/lopper analyse --top 20 --repo . --language all --format cyclonedx-json --enable-feature sbom-attestation-exports-preview > lopper.cdx.json
```

CycloneDX characteristics:

- The export uses CycloneDX JSON `specVersion` `1.6` and represents Lopper's
  direct dependency rows as `library` components.
- Component ordering is deterministic by `language`, then dependency name.
- Lopper does not infer package versions or package URLs from dependency names.
  When the report lacks those values, component `version` and `purl` are omitted
  and explicit `lopper:dependency:version:status=unknown` and
  `lopper:dependency:purl:status=unavailable` properties are emitted.
- License values are emitted conservatively as CycloneDX license names, with
  normalized SPDX/raw/source/confidence/unknown/denied/evidence preserved in
  `lopper:license:*` properties.
- Lopper-specific dependency-surface metadata is preserved as CycloneDX
  component properties, including language, used/unused import counts and
  details, reachability confidence, runtime usage, license policy signals,
  provenance, removal-candidate scores, waste metrics, and baseline deltas when
  available.
- Root-level properties preserve report context such as schema version, repo
  path, scope, summary, effective policy, language breakdown, and baseline
  comparison keys when available.
- This is an analyse-only, direct-dependency SBOM. It is not a full transitive
  inventory and should complement, not replace, dedicated SBOM generators for
  full package-manager or container inventories.
- `spdx-json`, dashboard-wide combined SBOMs, and signed attestations are
  deferred from this first preview export.

## Key fields

- `summary`: aggregated totals across all dependency rows.
- `scope`: analysis scope metadata (`mode`, `packages`).
- `usageUncertainty`: JS/TS usage certainty summary (`confirmedImportUses`, `uncertainImportUses`, `samples`).
- `languageBreakdown`: aggregate totals by adapter language (`js-ts`, `python`, `cpp`, `jvm`, `kotlin-android`, `go`, `php`, `ruby`, `rust`, `dotnet`, `elixir`, `swift`, `dart`, `powershell`).
- `effectiveThresholds`: resolved threshold values applied for this run.
- `effectivePolicy`: resolved policy object, including precedence sources, merge trace, scoring weights, and license policy controls (`CLI > repo config > imported policy packs > defaults`).
- `cache`: incremental analysis cache metadata (hits/misses/writes and invalidation reasons).
- `dependencies[].language`: language tag for each dependency row.
- `dependencies[].reachabilityConfidence`: deterministic v2 per-dependency confidence artifact (`model`, `score`, `summary`, `rationaleCodes`, and weighted `signals`).
- `dependencies[].license`: normalized per-dependency license detection (`spdx`, `source`, `confidence`, `unknown`, `denied`).
- `dependencies[].provenance`: per-dependency provenance signals (`source`, `confidence`, `signals`).
- `dependencies[].riskCues`: heuristic risk signals.
- `dependencies[].recommendations`: actionable follow-up suggestions.
- `dependencies[].codemod`: optional codemod preview/apply data for JS/TS subpath migrations, including deterministic patch previews, unsafe-transform skip reason codes, and apply summaries with rollback artifact paths.
- `dependencies[].runtimeUsage`: runtime load annotations (when `--runtime-trace` is used), including `modules`, `parentModules`, `entrypoints`, and `topSymbols` when available.
- `dependencies[].usedImports[].provenance`: optional attribution chain for barrel/re-export resolution in detailed views.
- `summary.reachability`: repo-level v2 confidence rollup (`model`, `averageScore`, `lowestScore`, `highestScore`).
- `wasteIncreasePercent`: present when `--baseline` was supplied and compared.
- `baselineComparison`: deterministic dependency-level deltas between baseline and current run, including `summaryDelta`, `dependencies`, `added`, `removed`, `regressions`, `progressions`, and `newDeniedLicenses`.

## Notes

- Reachability confidence v2 uses a deterministic weighted formula: `score = sum(signal.score * signal.weight)`, bounded to `0-100` and rounded to one decimal place.
- Signal weights are fixed in v2: runtime correlation `0.20`, export inventory `0.30`, import precision `0.20`, repo usage uncertainty `0.15`, dependency dynamic-loader signal `0.10`, and risk severity `0.05`.
- `dependencies[].removalCandidate.confidence` remains as a compatibility alias for `dependencies[].reachabilityConfidence.score`.
- Finding-level `confidenceScore` and `confidenceReasonCodes` fields mirror `dependencies[].reachabilityConfidence.score` and `dependencies[].reachabilityConfidence.rationaleCodes`.
- `runtimeUsage` annotates JS/TS dependencies and, when `python-runtime-trace-preview` is enabled, Python dependencies from `{"language":"python",...}` trace events.
- `runtimeUsage.correlation` distinguishes `static-only`, `runtime-only`, and `overlap` evidence categories.
- `runtimeUsage.modules` lists runtime-loaded module paths seen for a dependency.
- `runtimeUsage.topSymbols` lists best-effort runtime symbol hits derived from module subpaths.
- Runtime annotations use the same report fields across supported languages; adapter differences are confined to trace-to-dependency mapping.
- `cache.invalidations` entries identify deterministic invalidation reasons (for example `input-changed`).
- `usedPercent` values are adapter best-effort based on static analysis signals.
- `summary.knownLicenseCount`, `summary.unknownLicenseCount`, and `summary.deniedLicenseCount` are mutually exclusive license buckets across dependency rows. Denied dependencies count only as denied, even when they also have an SPDX value or unknown license metadata.
- Stored reports or baselines created before the mutually exclusive license-bucket summary should be regenerated, or consumers should recompute `summary` from `dependencies`, before comparing license deltas.
- `schemaVersion` is currently pinned to `0.1.0`.
- Baseline snapshots created with `--save-baseline --baseline-store DIR` are stored as immutable files keyed by `commit:<sha>` (default) or `label:<name>` when `--baseline-label` is passed.
