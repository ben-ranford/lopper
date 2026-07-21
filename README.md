# Lopper
[![Release](https://github.com/ben-ranford/lopper/actions/workflows/release.yml/badge.svg)](https://github.com/ben-ranford/lopper/actions/workflows/release.yml)
[![SonarCloud Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=ben-ranford_lopper&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=ben-ranford_lopper)
[![VS Code Marketplace](https://img.shields.io/badge/VS%20Code-Marketplace-0098ff?logo=visualstudiocode&logoColor=white)](https://marketplace.visualstudio.com/items?itemName=BenRanford.vscode-lopper)

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

Generate a local manpage:

```bash
make manpage
```

The generated manpage is available as `docs/man/lopper.1` and is installed automatically by the Homebrew formulas.

Run without local install:

```bash
docker run --rm ghcr.io/ben-ranford/lopper:latest --help
```

## GitHub Action

Use the first-party `Scan with Lopper` action in CI:

```yaml
- uses: ben-ranford/lopper@v1
  with:
    version: action
    repo: .
    language: all
    top: '20'
```

For reproducible CI, pin both the action ref and `version` to a concrete release such as `v1.7.0`. See [docs/ci-usage.md](docs/ci-usage.md#first-party-github-action) for PR comment and SARIF workflows.

## Terminal demos

| Demo | What it demonstrates | GIF preview |
| --- | --- | --- |
| Quick start ranking | End-to-end `--top` workflow with license, policy, and candidate-score context for fast triage. | ![Quick start top ranking demo](docs/demos/assets/quickstart-top.gif) |
| Single dependency deep dive | Focused analysis of one dependency with current usage, license, and scoring context. | ![Single dependency demo](docs/demos/assets/single-dependency.gif) |
| Baseline gate in CI flow | Baseline comparison with threshold policy and delta summary to catch regression risk in automated checks. | ![Baseline gating demo](docs/demos/assets/baseline-gate.gif) |

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

Emit CSV for spreadsheet or pipeline ingestion:

```bash
lopper analyse --top 20 --repo . --language all --format csv > lopper.csv
```

Emit SARIF for code scanning:

```bash
lopper analyse --top 20 --repo . --language all --format sarif > lopper.sarif
```

Emit a preview CycloneDX JSON SBOM for direct dependency rows:

```bash
lopper analyse --top 20 --repo . --language all \
  --format cyclonedx-json \
  --enable-feature sbom-attestation-exports-preview > lopper.cdx.json
```

The CycloneDX export preserves Lopper-specific usage, reachability, runtime,
license, provenance, waste, removal-candidate, and baseline context as
component properties. It does not infer missing versions or package URLs, and it
is not a full transitive dependency inventory. SPDX 2.3 JSON is available behind
`spdx-sbom-export-preview`, and dashboard-wide CycloneDX portfolio exports are
available behind `dashboard-cyclonedx-portfolio-preview`.

Launch the interactive TUI:

```bash
lopper tui --repo . --language all
```

Run the local MCP server for agent workflows:

```bash
lopper mcp
```

See [docs/mcp.md](docs/mcp.md) for tool names, input schemas, client configuration, and safety behavior.

Tune thresholds and score weights:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --threshold-fail-on-increase 2 \
  --threshold-low-confidence-warning 35 \
  --threshold-min-usage-percent 45 \
  --enable-feature reachability-vulnerability-prioritization-preview \
  --advisory-source security/lopper-advisories.yml \
  --threshold-reachable-vuln-priority high \
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

List the newest saved snapshots, or inspect one without printing its full report payload. Baseline discovery is stable in v1.8.1:

```bash
lopper baseline list \
  --store .artifacts/lopper-baselines \
  --limit 20

lopper baseline show label:release-candidate \
  --store .artifacts/lopper-baselines \
  --format json
```

Attach local vulnerability advisories for reachability-weighted security triage:

```bash
lopper analyse --top 20 \
  --repo . \
  --language all \
  --enable-feature reachability-vulnerability-prioritization-preview \
  --advisory-source security/lopper-advisories.yml \
  --threshold-reachable-vuln-priority high
```

Advisory ingestion is preview-gated and local-only. Lopper does not fetch a proprietary or network
vulnerability database. The current preview matches package name and ecosystem but does not evaluate
installed versions against OSV affected ranges, so use a curated local advisory snapshot and treat
`fixedVersion` as informational. The priority score ranks triage using advisory severity plus
reachability, runtime, and static import evidence; it is not an exploitability claim.

Generate an org-level dashboard across multiple repos:

```bash
lopper dashboard \
  --repos "./api,./frontend,./worker" \
  --format html \
  --output org-report.html
```

Use a dashboard config file:

```bash
lopper dashboard --config lopper-org.yml --format json
```

Remote dashboard config entries can use `repoUrl` with the `dashboard-remote-repos` feature and may pin exactly one of `branch`, `tag`, or full `commit` SHA. Unpinned remote entries continue to track remote `HEAD`; dashboard JSON, CSV, HTML, and saved dashboard baselines include the resolved commit SHA for materialized remote repos.

Preview pull request dependency-surface review:

```bash
lopper pr-review \
  --base 0123456789abcdef0123456789abcdef01234567 \
  --head fedcba9876543210fedcba9876543210fedcba98 \
  --format markdown \
  --enable-feature dependency-surface-pr-review-preview
```

`pr-review` requires explicit immutable SHAs, analyzes detached worktrees without running package-manager commands, and separates added, removed, upgraded/downgraded, policy-changed, newly reachable, and materially worsened rows.

## Languages

- Supported adapters: `js-ts`, `python`, `cpp`, `jvm`, `kotlin-android`, `go`, `php`, `ruby`, `rust`, `dotnet`, `elixir`, `swift`, `dart`, `powershell`
- `js-ts` merges workspace-level declarations from `pnpm-workspace.yaml`, `package.json#workspaces`, and Yarn `.yarnrc.yml` catalogs.
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
  reachable_vulnerability_priority: high
  removal_candidate_weight_usage: 0.50
  removal_candidate_weight_impact: 0.30
  removal_candidate_weight_confidence: 0.20
advisories:
  source: security/lopper-advisories.yml
```

Threshold defaults:

- `fail_on_increase_percent: -1` (disabled)
- `low_confidence_warning_percent: 40`
- `min_usage_percent_for_recommendations: 40`
- `reachable_vulnerability_priority: off`
- `removal_candidate_weight_usage: 0.50`
- `removal_candidate_weight_impact: 0.30`
- `removal_candidate_weight_confidence: 0.20`

Threshold ranges:

- `fail_on_increase_percent` must be `-1` or `>= 0`
- `low_confidence_warning_percent` must be between `0` and `100`
- `min_usage_percent_for_recommendations` must be between `0` and `100`
- `reachable_vulnerability_priority` must be `off`, `low`, `medium`, `high`, or `critical`
- removal candidate weights must be `>= 0` and at least one must be greater than `0`

Precedence is `CLI > repo config > imported policy packs > defaults`.

Additional guides:

- `docs/threshold-tuning.md`
- `docs/notifications.md`
- `docs/feature-flags.md`

Launch TUI:

```bash
lopper tui --repo . --language all
```

Compare the TUI summary against a stored baseline:

```bash
lopper tui \
  --repo . \
  --language all \
  --baseline-store .artifacts/lopper-baselines \
  --baseline-key commit:abc123
```

Inside the TUI, open a dependency to inspect codemod suggestions and apply safe suggestions without leaving the session:

```text
open js-ts:lodash
apply-codemod --confirm
```

`apply-codemod` refuses dirty git worktrees by default; add `--allow-dirty` only when you intentionally want to apply into a dirty tree. The action prints applied, skipped, and failed files plus the rollback backup path. Baselines can also be saved and compared in-session:

Python safe unused-import suggestions are stable and enabled by default. They are syntactically conservative rather than proof that an import has no module side effects. Mutation still requires `--apply-codemod --apply-codemod-confirm`, refuses a dirty worktree by default, and writes rollback evidence. Use `--disable-feature python-codemod-suggestions` for an explicit rollback.

```text
save-baseline
save-baseline release-candidate
compare-baseline label:release-candidate
compare-baseline --file baseline.json
```

In-session baseline commands default to `.artifacts/lopper-baselines` and `commit:<HEAD>` when no store or label/key is provided.

## Runtime trace annotations

### JS/TS

Capture a runtime trace:

```bash
export LOPPER_RUNTIME_TRACE=.artifacts/lopper-runtime.ndjson
export LOPPER_ROOT=/path/to/lopper
export NODE_OPTIONS="--require ${LOPPER_ROOT}/scripts/runtime/require-hook.cjs --loader ${LOPPER_ROOT}/scripts/runtime/loader.mjs"
npm test
```

Use the hook files from the lopper checkout or install tree. Relative hook paths resolve from the repo running `npm test`, not from lopper itself.

Or let Lopper run the test command and capture the trace automatically:

```bash
lopper analyse --top 20 --repo . --language js-ts --runtime-test-command "npm test"
```

Use trace in analysis:

```bash
lopper analyse --top 20 --repo . --language js-ts --runtime-trace .artifacts/lopper-runtime.ndjson
```

With runtime traces enabled:

- `runtimeUsage.correlation` marks each supported dependency as `static-only`, `runtime-only`, or `overlap`.
- `runtimeUsage.modules` includes runtime-loaded module paths.
- `runtimeUsage.parentModules` includes parent module paths that triggered the load.
- `runtimeUsage.entrypoints` includes entrypoint modules seen in the runtime trace.
- `runtimeUsage.topSymbols` includes best-effort runtime symbol hits.

If `--runtime-trace` points to a missing file, analysis continues with static results and adds a warning.

### Python

First-party Python runtime capture is stable for these conservative pytest-family command forms (runner arguments may follow):

```text
pytest
python -m pytest
python3 -m pytest
```

```bash
lopper analyse --top 20 --repo . --language python \
  --runtime-test-command "pytest"
```

The stable `python-runner-profiles` feature adds these direct-exec forms:

```text
python -m unittest
python3 -m unittest
uv run pytest
uv run -- pytest
uv run python -m pytest
uv run python3 -m pytest
uv run -- python -m pytest
uv run -- python3 -m pytest
uv run python -m unittest
uv run python3 -m unittest
uv run -- python -m unittest
uv run -- python3 -m unittest
```

For example:

```bash
lopper analyse --top 20 --repo . --language python \
  --runtime-test-command "uv run -- python -m unittest discover -s tests"
```

The optional `--` immediately after `uv run` is the only accepted uv wrapper delimiter. Arguments after the selected `pytest` command or `python[3] -m pytest|unittest` module are forwarded verbatim, including a later `--`; uv wrapper flags, arbitrary uv tools, Python interpreter flags, inline environment assignments, and shell operators are rejected.

Lopper resolves supported executables from trusted absolute `PATH` entries in order, then from its fixed system fallback directories. It rejects writable search directories, writable/non-executable tools, and executables outside the allowlist. Lopper injects its import hook only into the runtime command environment by prepending the shipped `scripts/runtime/sitecustomize.py` directory to `PYTHONPATH`; an existing project `sitecustomize.py` is chained exactly once. It does not install project dependencies or invoke a shell. Use `--disable-feature python-runtime-capture` or `--disable-feature python-runner-profiles` for rollback.

Explicit Python trace consumption remains compatible through `--runtime-trace`.

Each trace line should identify the Python adapter and the imported module:

```json
{"language":"python","module":"requests.sessions","parent":"/repo/main.py","entrypoint":"/repo/main.py"}
```

When the import root differs from the package name, include `dependency`:

```json
{"language":"python","dependency":"beautifulsoup4","module":"bs4"}
```

Use the trace in analysis:

```bash
lopper analyse --top 20 --repo . --language python --runtime-trace .artifacts/python-runtime.ndjson
```

Python caveats:

- Runtime module names map to the top-level import package, with common aliases such as `bs4` -> `beautifulsoup4`.
- The first-party hook emits third-party imports resolved from `site-packages` or `dist-packages` and filters local modules and stdlib imports.
- Subprocesses and pytest workers inherit the trace environment when they inherit the parent process environment; concurrent writers append NDJSON lines to the same trace file.
- Project `sitecustomize.py` runs once before Lopper installs its import wrapper; imports performed only during that startup hook are not included in the trace.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and commands.
For watched hotspot packages, run `make mem-profiles` to capture alloc-space summaries before or after performance-sensitive changes.

## Docs

- Report schema: `docs/report-schema.json`, `docs/report-schema.md`
- Multi-repo dashboard: `docs/dashboard.md`
- Pull request dependency-surface review: `docs/pr-review.md`
- Memory profiling workflow: `docs/memory-profiling.md`
- SARIF code scanning: `docs/sarif-code-scanning.md`
- Threshold tuning: `docs/threshold-tuning.md`
- MCP server integration: `docs/mcp.md`
- Runtime trace annotations: `scripts/runtime/`
- Adapter and architecture extensibility: `docs/extensibility.md`
- CI and release workflow: `docs/ci-usage.md`
- Contribution guide: `CONTRIBUTING.md`
