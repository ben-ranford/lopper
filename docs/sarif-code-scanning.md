# SARIF Code Scanning

Lopper can export dependency-surface findings as SARIF 2.1.0:

```bash
lopper analyse --top 20 --repo . --language all --format sarif > lopper.sarif
```

The SARIF output includes:

- stable rule IDs for waste, risk cues, and recommendations
- finding metadata (dependency, language, module/symbol when available)
- dependency provenance and runtime context, including parent modules and entrypoints when runtime traces are present
- baseline context for compare-mode findings, including per-dependency deltas and overall waste delta
- source locations for findings when location data exists

## Upload to GitHub code scanning

Use the first-party Lopper action to generate `lopper.sarif`, then upload it with `github/codeql-action/upload-sarif`:

```yaml
name: lopper-code-scanning
on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read
  security-events: write

jobs:
  lopper:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7

      - name: Generate SARIF
        uses: ben-ranford/lopper@v1.7.0
        with:
          version: v1.7.0
          repo: .
          language: all
          top: '20'
          scope-mode: repo
          format: sarif
          output: lopper.sarif

      - name: Upload SARIF
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: lopper.sarif
```

## Notes

- Existing `table` and `json` outputs are unchanged.
- The action supports `format: sarif`; direct CLI use with `lopper analyse --format sarif` remains supported.
- For best review context in pull requests, run analysis from repository root.
