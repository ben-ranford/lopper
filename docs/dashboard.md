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
  baseline_store: /ci/baselines
  output: html
```

Notes:

- Relative `path` values are resolved relative to the config file directory.
- `repoUrl` entries are reserved for future support and are not yet executable by `lopper dashboard`.
- CLI flags take precedence over config (`--format`, `--language`, `--top`, `--output`).

## JSON Shape

Dashboard JSON emits:

- `generated_at`
- `repos[]` (per-repo metrics and any analysis errors)
- `summary`:
  - `total_repos`
  - `total_deps`
  - `total_waste_candidates`
  - `cross_repo_duplicates`
  - `critical_cves`
- `cross_repo_deps[]` (dependencies present in 3+ repos)

