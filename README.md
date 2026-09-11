# Lopper

[![Release](https://github.com/ben-ranford/lopper/actions/workflows/release.yml/badge.svg)](https://github.com/ben-ranford/lopper/actions/workflows/release.yml)
[![SonarCloud Quality Gate](https://sonarcloud.io/api/project_badges/measure?project=ben-ranford_lopper&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=ben-ranford_lopper)
[![VS Code Marketplace](https://img.shields.io/badge/VS%20Code-Marketplace-0098ff?logo=visualstudiocode&logoColor=white)](https://marketplace.visualstudio.com/items?itemName=BenRanford.vscode-lopper)

Lopper shows where a repository declares dependencies it barely uses.

Point it at a repository to compare manifests with imports, rank the largest gaps, and produce a report for local investigation or CI. It runs locally and supports a CLI, terminal UI, GitHub Action, and MCP server.

## Highlights

- Scan one language or every detected adapter in a repository
- Rank dependencies by unused surface, with confidence and policy context
- Export table, JSON, CSV, SARIF, and preview CycloneDX reports
- Compare results with a saved baseline in CI
- Inspect a result in the TUI or through an MCP client
- Review results before removing dependencies; a low-usage score is evidence to inspect, not proof that removal is safe

## Install

macOS and Linux with Homebrew:

```bash
brew tap ben-ranford/tap
brew install lopper
```

For the rolling build, which tracks `main`:

```bash
brew install ben-ranford/tap/lopper-rolling
```

On Windows, download the appropriate asset from the [latest GitHub release](https://github.com/ben-ranford/lopper/releases/latest).

To run Lopper without installing it:

```bash
docker run --rm ghcr.io/ben-ranford/lopper:latest --help
```

## Start here

Rank the 20 dependencies with the most unused surface:

```bash
lopper analyse --top 20 --repo . --language all
```

Inspect one dependency:

```bash
lopper analyse lodash --repo . --language js-ts
```

Open the terminal UI:

```bash
lopper tui --repo . --language all
```

Write a machine-readable report:

```bash
lopper analyse --top 20 --repo . --language all --format json > lopper.json
```

![Lopper ranking dependencies in the terminal](docs/demos/assets/quickstart-top.gif)

## Use it in GitHub Actions

```yaml
- uses: ben-ranford/lopper@v1
  with:
    version: action
    repo: .
    language: all
    top: "20"
```

For reproducible CI, pin both the action and `version` to a concrete release. The [CI guide](docs/ci-usage.md) covers baselines, PR comments, threshold gates, and SARIF uploads.

## Supported languages

`js-ts`, `python`, `cpp`, `jvm`, `kotlin-android`, `go`, `php`, `ruby`, `rust`, `dotnet`, `elixir`, `swift`, `dart`, and `powershell`.

Use `lopper --help` for the current adapter IDs and options. `--language auto` selects the highest-confidence adapter; `--language all` merges results from every matching adapter.

## More documentation

- [CI and GitHub Action](docs/ci-usage.md)
- [Repository configuration and threshold tuning](docs/threshold-tuning.md)
- [Feature flags and release channels](docs/feature-flags.md)
- [MCP server integration](docs/mcp.md)
- [Runtime trace annotations](scripts/runtime/)
- [Multi-repository dashboards](docs/dashboard.md)
- [Pull-request dependency review](docs/pr-review.md)
- [SARIF code scanning](docs/sarif-code-scanning.md)
- [Report schema](docs/report-schema.md)
- [Language adapter architecture](docs/extensibility.md)
- [Contributing](CONTRIBUTING.md)
