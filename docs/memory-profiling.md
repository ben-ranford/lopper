# Memory Profiling

Lopper tracks package-level allocation hotspots with a small watched set:

- `./internal/lang/dotnet`
- `./internal/lang/rust`
- `./internal/analysis`
- `./internal/lang/golang`

The goal is not to hard-gate merges on a single number. The workflow keeps repeatable alloc-space snapshots available so regressions can be reviewed before hotspots drift upward unnoticed.

## Local capture

Run the default watched-package sweep:

```bash
make mem-profiles
```

Artifacts are written to `.artifacts/memory-profiles/<timestamp>/` and `latest-run-dir.txt` points at the newest run.

Useful overrides:

```bash
MEM_PROFILE_STAMP=pr-317 make mem-profiles
MEM_PROFILE_NODECOUNT=30 make mem-profiles
MEM_PROFILE_PACKAGES="./internal/analysis ./internal/lang/rust" make mem-profiles
```

Each run contains:

- `summary.md`: index of watched packages with the current alloc-space headline
- `*.alloc-space.txt`: package-focused `go tool pprof -top` summaries using `alloc_space`
- `*.mem.pprof`: raw memory profiles from `go test -memprofile`
- `*.test.log`: package test output captured during profiling

## CI automation

`.github/workflows/memory-profiles.yml` provides the repeatable capture path:

- Weekly scheduled run on `main`
- Manual `workflow_dispatch` run when a branch needs an ad hoc snapshot
- Uploaded artifacts for the full run directory
- GitHub step summary populated from `summary.md`

The workflow uses the same `make mem-profiles` target as local development so the package list and command path stay aligned.

## Reviewing regressions over time

The baseline is the latest successful scheduled artifact from `main`.

When a pull request changes one of the watched packages, or shared analysis plumbing that can shift their allocations, capture a fresh run locally or with the manual workflow and compare the package summary against the current baseline artifact.

For a quick text diff:

```bash
git diff --no-index \
  --ignore-matching-lines='^(File:|Time:)' \
  old/internal_analysis.alloc-space.txt \
  new/internal_analysis.alloc-space.txt
```

Reviewers should focus on:

- Lopper-owned frames moving materially upward in the top alloc-space table
- New hot functions appearing near the top of the watched package summary
- Large changes in the `Showing nodes accounting for ... total` headline for the package-focused view

If the text summary is not enough, inspect the matching `*.mem.pprof` file with `go tool pprof`.
