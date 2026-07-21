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
- Vulnerability metadata: `vulnerability_findings`, `vulnerability_highest_priority`, `vulnerability_reachable_count`

## Local advisory ingestion

With `reachability-vulnerability-prioritization-preview` enabled, use
`lopper analyse --advisory-source PATH` to read a local JSON or YAML advisory
file and attach vulnerability findings to matching dependency rows. Lopper does
not contact a vulnerability database during analysis, does not require network
access, and does not claim exploitability.

Local advisory format:

```yaml
advisories:
  - id: GHSA-example-1234
    package: example-lib
    ecosystem: npm
    severity: high
    fixedVersion: 1.2.3
    source: security-team
    aliases:
      - CVE-2026-0001
```

The loader also accepts OSV-style JSON/YAML documents with `affected` package
entries. Relative config paths under `advisories.source` resolve relative to the
config file; CLI `--advisory-source` paths are used as provided.

Vulnerability priority is a deterministic triage score, not an exploitability
claim. The score uses severity, `dependencies[].reachabilityConfidence`, runtime
usage correlation, and static import/export evidence with fixed weights:
severity `50%`, reachability `30%`, runtime `10%`, static `10%`.

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
  provenance, vulnerability findings, removal-candidate scores, waste metrics,
  and baseline deltas when available.
- Root-level properties preserve report context such as schema version, repo
  path, scope, summary, effective policy, language breakdown, and baseline
  comparison keys when available.
- This is an analyse-only, direct-dependency SBOM. It is not a full transitive
  inventory and should complement, not replace, dedicated SBOM generators for
  full package-manager or container inventories.
- `spdx-json` emits a preview SPDX 2.3 JSON direct-dependency SBOM when
  `spdx-sbom-export-preview` is enabled.
- `cyclonedx-vex-json` emits preview CycloneDX VEX vulnerability analysis
  records when `vulnerability-exceptions-vex-preview` is enabled.
- Dashboard-wide combined CycloneDX SBOMs are emitted by `lopper dashboard
  --format cyclonedx-json` when `dashboard-cyclonedx-portfolio-preview` is
  enabled.
- Signed attestations are deferred from these preview exports.

## Key fields

- `summary`: aggregated totals across all dependency rows, including
  `summary.vulnerabilities` when local advisories produce findings.
- `scope`: analysis scope metadata (`mode`, `packages`).
- `usageUncertainty`: JS/TS usage certainty summary (`confirmedImportUses`, `uncertainImportUses`, `samples`).
- `languageBreakdown`: aggregate totals by adapter language (`js-ts`, `python`, `cpp`, `jvm`, `kotlin-android`, `go`, `php`, `ruby`, `rust`, `dotnet`, `elixir`, `swift`, `dart`, `powershell`).
- `effectiveThresholds`: resolved threshold values applied for this run,
  including `reachableVulnerabilityPriority`.
- `effectivePolicy`: resolved policy object, including precedence sources, merge trace, scoring weights, license policy controls, and vulnerability advisory policy (`CLI > repo config > imported policy packs > defaults`).
- `cache`: incremental analysis cache metadata (hits/misses/writes and invalidation reasons).
- `dependencies[].language`: language tag for each dependency row.
- `dependencies[].identity`: preview dependency identity metadata (`ecosystem`,
  `name`, `namespace`, `version`, `purl`, status fields, confidence, evidence,
  and conflicts) when `dependency-identity-preview` is enabled.
- `dependencies[].reachabilityConfidence`: deterministic v2 per-dependency confidence artifact (`model`, `score`, `summary`, `rationaleCodes`, and weighted `signals`).
- `dependencies[].license`: normalized per-dependency license detection (`spdx`, `source`, `confidence`, `unknown`, `denied`).
- `dependencies[].provenance`: per-dependency provenance signals (`source`, `confidence`, `signals`).
- `dependencies[].vulnerabilities`: local advisory findings with `advisoryId`,
  `package`, `severity`, `fixedVersion`, `source`, reachability-weighted
  `priority`, numeric `priorityScore`, `reachable`, optional VEX `decision`,
  and evidence strings.
- `dependencies[].riskCues`: heuristic risk signals.
- `dependencies[].recommendations`: actionable follow-up suggestions.
- `dependencies[].codemod`: optional language-neutral codemod/remediation preview/apply data, including `language`, `dependency`, `targetFile`, deterministic `patch` previews, `safetyReasonCodes`, unsafe-transform skip reason codes, and apply summaries with rollback artifact paths. Python codemod suggestions are stable under `python-codemod-suggestions` and remain explicitly disableable for rollback.
- `dependencies[].runtimeUsage`: runtime load annotations (when `--runtime-trace` is used), including `modules`, `parentModules`, `entrypoints`, and `topSymbols` when available.
- `dependencies[].usedImports[].provenance`: optional attribution chain for barrel/re-export resolution in detailed views.
- `summary.reachability`: repo-level v2 confidence rollup (`model`, `averageScore`, `lowestScore`, `highestScore`).
- `wasteIncreasePercent`: present when `--baseline` was supplied and compared.
- `baselineComparison`: deterministic dependency-level deltas between baseline and current run, including `summaryDelta`, `dependencies`, `added`, `removed`, `regressions`, `progressions`, `runtimeRegressions`, `runtimeImprovements`, `newDeniedLicenses`, and `newReachableVulnerabilities`.
- `baselineComparison.dependencies[].runtimeDelta`: runtime trace comparison for dependencies present in both baseline and current reports when at least one side has runtime data. Comparable deltas include load-count changes, correlation transitions, new/removed runtime loads, runtime-only regressions/improvements, and module/parent/entrypoint changes.

## Notes

- Reachability confidence v2 uses a deterministic weighted formula: `score = sum(signal.score * signal.weight)`, bounded to `0-100` and rounded to one decimal place.
- Signal weights are fixed in v2: runtime correlation `0.20`, export inventory `0.30`, import precision `0.20`, repo usage uncertainty `0.15`, dependency dynamic-loader signal `0.10`, and risk severity `0.05`.
- `dependencies[].removalCandidate.confidence` remains as a compatibility alias for `dependencies[].reachabilityConfidence.score`.
- Finding-level `confidenceScore` and `confidenceReasonCodes` fields mirror `dependencies[].reachabilityConfidence.score` and `dependencies[].reachabilityConfidence.rationaleCodes`.
- `runtimeUsage` annotates JS/TS dependencies and Python dependencies from `{"language":"python",...}` trace events; first-party pytest-family Python capture is stable under `python-runtime-capture`.
- `runtimeUsage.correlation` distinguishes `static-only`, `runtime-only`, and `overlap` evidence categories.
- `runtimeUsage.modules` lists runtime-loaded module paths seen for a dependency.
- `runtimeUsage.topSymbols` lists best-effort runtime symbol hits derived from module subpaths.
- Runtime annotations use the same report fields across supported languages; adapter differences are confined to trace-to-dependency mapping.
- Runtime baseline deltas are comparable only when both reports contain `runtimeUsage` for the same dependency. If one side lacks runtime data, `runtimeDelta.comparable` is `false`, and load deltas are omitted instead of treating the missing side as zero loads.
- Added and removed dependencies do not produce `runtimeDelta` or runtime regression/improvement entries; their runtime data remains on the dependency row in the source report, while `baselineComparison.added` and `baselineComparison.removed` describe dependency presence changes.
- `cache.invalidations` entries identify deterministic invalidation reasons (for example `input-changed`).
- `usedPercent` values are adapter best-effort based on static analysis signals.
- `summary.knownLicenseCount`, `summary.unknownLicenseCount`, and `summary.deniedLicenseCount` are mutually exclusive license buckets across dependency rows. Denied dependencies count only as denied, even when they also have an SPDX value or unknown license metadata.
- `summary.vulnerabilities.reachableFindings` counts advisory findings with
  reachability evidence. Baseline comparison reports newly introduced reachable
  findings under `baselineComparison.newReachableVulnerabilities`.
- Stored reports or baselines created before the mutually exclusive license-bucket summary should be regenerated, or consumers should recompute `summary` from `dependencies`, before comparing license deltas.
- `schemaVersion` is currently pinned to `0.2.0`.
- Legacy `0.1.0` reports remain baseline-load/compare compatible; consumers that
  pin the public schema should accept `0.2.0` for current output while treating
  older saved reports as historical input rather than current-format validation
  targets.
- Baseline snapshots created with `--save-baseline --baseline-store DIR` are stored as immutable files keyed by `commit:<sha>` (default) or `label:<name>` when `--baseline-label` is passed. The stable `lopper baseline list` and `lopper baseline show KEY` commands expose bounded snapshot metadata; they do not print dependency rows from the stored report. The former `baseline-store-discovery-preview` feature name remains accepted for compatibility.
