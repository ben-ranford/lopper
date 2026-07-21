# Multi-Repo Dashboard

`lopper dashboard` aggregates dependency analysis across multiple repositories and emits a single org-level report.

## Usage

From explicit repo paths:

```bash
lopper dashboard \
  --repos "./api,./frontend,./worker" \
  --format html \
  --output org-report.html
```

From config:

```bash
lopper dashboard --config lopper-org.yml --format json
```

From config with remote repositories enabled:

```bash
lopper dashboard \
  --config lopper-org.yml \
  --enable-feature dashboard-remote-repos
```

With the remediation queue preview enabled:

```bash
lopper dashboard \
  --repos "./api,./frontend,./worker" \
  --format html \
  --enable-feature dashboard-remediation-queue-preview
```

Supported output formats:

- `json`
- `csv`
- `html` (self-contained, no external assets)
- `slack-summary` and `teams-summary` when `remediation-routing-summaries-preview` is enabled
- `cyclonedx-json` when `dashboard-cyclonedx-portfolio-preview` is enabled

## Config File

Example `lopper-org.yml`:

```yaml
dashboard:
  repos:
    - path: ./api
      language: go
      name: API Service
    - path: ./frontend
      language: js-ts
      name: Frontend
    - path: ./worker
      language: python
      name: Worker
  output: html
  ownership:
    default_team: dependency-platform
    default_status: open
    rules:
      - repo: API Service
        category: vulnerability
        team: appsec
        owner: security
        due: "2026-08-01"
```

Remote Git repositories can be configured with `repoUrl` when the `dashboard-remote-repos` feature is enabled:

```yaml
dashboard:
  repos:
    - repoUrl: https://github.com/example/api.git
      branch: release/2.0
      language: go
      name: API Service
    - repoUrl: ssh://git@github.com/example/frontend.git
      tag: v2.0.0
      language: js-ts
      name: Frontend
    - repoUrl: file:///srv/repos/worker.git
      commit: 0123456789abcdef0123456789abcdef01234567
      language: python
      name: Worker
    - path: ./worker
      language: python
      name: Local Worker
  output: json
```

Notes:

- Relative `path` values are resolved relative to the config file directory.
- `path` and `repoUrl` are mutually exclusive on each repo entry.
- `repoUrl` accepts only `https://`, `ssh://`, and local `file://` URLs. `http://`, `git://`, scp-style URLs such as `git@github.com:org/repo.git`, query strings, fragments, and embedded credentials are rejected.
- `file://` URLs must use an absolute path and an empty or `localhost` host.
- Remote `repoUrl` entries can set one of `branch`, `tag`, or `commit`. These fields are mutually exclusive, only apply to `repoUrl` entries, and `commit` must be a full 40- or 64-character hex SHA.
- Remote repos are materialized under the user cache at `<cache-dir>/lopper/dashboard/repos/<repo-name>-<hash>`. Set `LOPPER_DASHBOARD_REPO_CACHE` to an absolute path to override the cache root in CI.
- The checkout lifecycle is deterministic: unpinned `repoUrl` entries continue to track remote `HEAD`; pinned entries fetch the requested branch, tag, or commit and reset to that revision. Cache paths include the requested pin, so the same `repoUrl` at different pins uses separate checkouts.
- Dashboard JSON, CSV, HTML, and saved dashboard baselines include the resolved commit SHA for materialized remote repos. JSON repo rows also include `repo_url` and the requested `revision` when present.
- When `dashboard-remediation-queue-preview` is enabled, dashboard JSON, CSV, HTML, and saved dashboard baselines include org-level remediation queue items derived from per-repo errors, vulnerability findings, denied licenses, dependency recommendations, risk cues, runtime regressions, and cross-repo duplicate dependencies. The queue is sorted by highest severity/priority first.
- Fetch, clone, checkout, reset, or clean failures are reported in the affected repo's dashboard `error` field and in top-level dashboard warnings, while other repos continue to analyze.
- `baseline_store` enables dashboard baseline snapshots and compare mode. Relative values are resolved against the config file directory.
- `baseline_key` selects a stored baseline snapshot when comparing. `baseline_label` is used when saving a labeled snapshot.
- `save_baseline` writes the current dashboard report to the baseline store without changing the normal report output.
- CLI flags take precedence over config for `--format` and `--output`; `--language` only fills missing repo language values, and `--top` is CLI-only.
- `ownership` is preview routing metadata used by `remediation-routing-summaries-preview`; config rules are applied before CODEOWNERS and defaults.

## JSON Shape

Dashboard JSON emits:

- `generated_at`
- `repos[]` (per-repo metrics, remote `repo_url`, requested `revision`, `resolved_commit`, runtime trace/regression counts, vulnerability finding counts, and any analysis errors)
- `baseline_comparison` when a dashboard baseline is loaded and compared
- `remediation_items[]` when `dashboard-remediation-queue-preview` is enabled:
  - `id`: deterministic stable queue item ID
  - `repo`: repo label, or a comma-separated repo set for cross-repo duplicate dependency items
  - `repo_path`: local repo path when available
  - `dependency`: affected dependency when applicable
  - `category`: one of the emitted remediation categories, such as `repo_error`, `vulnerability`, `license`, `runtime_regression`, `risk`, `waste`, `recommendation`, or `duplicate_dependency`
  - `owner`, `team`, `due`, `status`, and `routing_source` when remediation routing is enabled
  - `severity` and `priority`: normalized triage levels when available
  - `evidence[]`: compact evidence strings backing the item
  - `suggested_action`: recommended next action
  - `baseline_status`: `new`, `regressed`, or `existing` when compared with a dashboard baseline
- `summary`:
  - `total_repos`
  - `total_deps`
  - `total_waste_candidates`
  - `cross_repo_duplicates`
  - `critical_cves`
  - `vulnerability_findings`
  - `reachable_vulnerabilities`
  - `repos_with_runtime_trace_data`
  - `repos_with_runtime_regressions`
- `cross_repo_deps[]` (dependencies present in 3+ repos)

## CSV Shape

Dashboard CSV is sectioned. It starts with summary key/value rows, then per-repo rows, and conditionally adds more sections.

When `dashboard-remediation-queue-preview` is enabled and the queue is non-empty, a remediation section is emitted with these columns:

- `remediation_id`
- `baseline_status`
- `repo`
- `repo_path`
- `dependency`
- `category`
- `owner`
- `team`
- `due`
- `status`
- `routing_source`
- `severity`
- `priority`
- `evidence` (`|` delimited)
- `suggested_action`

When a dashboard baseline is compared, `baseline_comparison` also includes remediation queue deltas split into `new_remediation_items`, `regressed_remediation_items`, `existing_remediation_items`, and `removed_remediation_items`. The CSV baseline section includes a remediation delta subsection with `kind`, routing fields, current severity/priority, and baseline severity/priority columns.
