# Extensibility

Lopper is built around pluggable language adapters under `internal/lang/*`.
The core contracts are in `internal/language`.

## Current adapters

- `js-ts` (JavaScript/TypeScript)
- `python` (Python)
- `jvm` (Java/Kotlin)
- `go` (Go)

## Adapter contract

All adapters implement:

- `ID() string`
- `Aliases() []string`
- `Detect(ctx, repoPath) (bool, error)`
- `Analyse(ctx, Request) (report.Report, error)`

Optional (recommended):

- `DetectWithConfidence(ctx, repoPath) (language.Detection, error)`

When `DetectWithConfidence` is implemented, the registry can:

- rank adapters for `--language auto`
- run all matches for `--language all`
- scope monorepo sub-roots via `Detection.Roots`

See:

- `internal/language/adapter.go`
- `internal/language/registry.go`

## How adapters are executed

`internal/analysis/service.go` coordinates:

1. adapter selection (`auto`, `all`, or explicit)
2. per-root adapter execution
3. merged report aggregation
4. runtime trace annotation (if requested)

Merged output deduplicates by `(language, dependency)` and computes:

- report `summary`
- `languageBreakdown`

## Adding a new adapter

1. Create `internal/lang/<id>/adapter.go`.
2. Implement the adapter interface and (ideally) confidence detection.
3. Ensure dependency rows set `DependencyReport.Language`.
4. Add tests in `internal/lang/<id>/adapter_test.go`.
5. Register the adapter in `analysis.NewService()`.
6. Update CLI/docs language lists (`internal/cli/usage.go`, `README.md`).

## Guidance for detection

- Prefer deterministic manifest signals first.
- Add source-file heuristics as fallback.
- Set conservative confidence values and include roots when possible.
- Keep directory walking bounded and skip known heavy/vendor dirs.

## Runtime tracing integration

If your adapter can consume runtime traces, extend `internal/runtime` and
its annotation logic. The current implementation annotates JS/TS dependencies.
