# SARIF Code Scanning

Lopper can export dependency-surface findings as SARIF 2.1.0:

```bash
lopper analyse --top 20 --repo . --language all --format sarif > lopper.sarif
```

The SARIF output includes:

- stable rule IDs for waste, risk cues, and recommendations
- finding metadata (dependency, language, module/symbol when available)
- source locations for findings when location data exists

## Upload to GitHub code scanning

Use `github/codeql-action/upload-sarif` in a workflow after generating `lopper.sarif`:

```yaml
name: lopper-code-scanning
on:
  pull_request:
  push:
    branches: [main]

jobs:
  lopper:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Build lopper
        run: go build -o bin/lopper ./cmd/lopper

      - name: Generate SARIF
        run: ./bin/lopper analyse --top 20 --repo . --language all --format sarif > lopper.sarif

      - name: Upload SARIF
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: lopper.sarif
```

## Notes

- Existing `table` and `json` outputs are unchanged.
- For best review context in pull requests, run analysis from repository root.
