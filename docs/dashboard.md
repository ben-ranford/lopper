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
  --enable-feature dashboard-remote-repos-preview
```

Supported output formats:

- `json`
- `csv`
- `html` (self-contained, no external assets)

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
```

Remote Git repositories can be configured with `repoUrl` when the `dashboard-remote-repos-preview` feature is enabled:

```yaml
dashboard:
  repos:
    - repoUrl: https://github.com/example/api.git
      language: go
      name: API Service
    - repoUrl: ssh://git@github.com/example/frontend.git
      language: js-ts
      name: Frontend
    - path: ./worker
      language: python
      name: Worker
  output: json
```

Notes:

- Relative `path` values are resolved relative to the config file directory.
- `path` and `repoUrl` are mutually exclusive on each repo entry.
- `repoUrl` accepts only `https://`, `ssh://`, and local `file://` URLs. `http://`, `git://`, scp-style URLs such as `git@github.com:org/repo.git`, query strings, fragments, and embedded credentials are rejected.
- `file://` URLs must use an absolute path and an empty or `localhost` host.
- Remote repos are materialized under the user cache at `<cache-dir>/lopper/dashboard/repos/<repo-name>-<hash>`. Set `LOPPER_DASHBOARD_REPO_CACHE` to an absolute path to override the cache root in CI.
- The checkout lifecycle is deterministic: each `repoUrl` maps to a stable hash-based checkout directory, existing checkouts with a matching origin are fetched and reset to remote `HEAD`, and invalid or mismatched cache directories are replaced before analysis.
- Fetch, clone, checkout, reset, or clean failures are reported in the affected repo's dashboard `error` field and in top-level dashboard warnings, while other repos continue to analyze.
- `baseline_store` enables dashboard baseline snapshots and compare mode. Relative values are resolved against the config file directory.
- `baseline_key` selects a stored baseline snapshot when comparing. `baseline_label` is used when saving a labeled snapshot.
- `save_baseline` writes the current dashboard report to the baseline store without changing the normal report output.
- CLI flags take precedence over config for `--format` and `--output`; `--language` only fills missing repo language values, and `--top` is CLI-only.

## JSON Shape

Dashboard JSON emits:

- `generated_at`
- `repos[]` (per-repo metrics and any analysis errors)
- `baseline_comparison` when a dashboard baseline is loaded and compared
- `summary`:
  - `total_repos`
  - `total_deps`
  - `total_waste_candidates`
  - `cross_repo_duplicates`
  - `critical_cves`
- `cross_repo_deps[]` (dependencies present in 3+ repos)
