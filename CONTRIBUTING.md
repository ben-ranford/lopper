# Contributing

Thanks for contributing to Lopper.

## Development setup

Requirements:

- Go `1.26.x`
- `zig` (required for cross-CGO release builds)
- `golangci-lint` (optional for faster local runs; `make lint` auto-runs a pinned version)
- `gostyle` (optional for faster local runs; `make lint` auto-runs a pinned version)

Install dependencies and run checks:

```bash
make setup
make fmt
make test
make lint
make suppression-check
make cov
make build
```

VS Code extension smoke tests:

```bash
make build
make vscode-extension-install
make vscode-extension-test
make vscode-extension-package
```

Refresh terminal demos:

```bash
make demos
```

## Workflow

1. Create a branch for your change.
2. Add or update tests for behavior changes.
3. Keep commits focused and descriptive.
4. Open a pull request with clear context, scope, and validation steps.

## Adapter docs checklist

If you add a new language adapter, update the contributor-facing docs and user-facing metadata in the same change:

- `docs/extensibility.md`: add the adapter to `Current adapters` and keep the "Adding a new adapter" checklist current.
- `internal/cli/usage.go`: update every `--language` list and the supported adapter IDs help text.
- `extensions/vscode-lopper/package.json`: update `lopper.language` enum values, descriptions, and extension marketplace copy if the adapter changes extension support.
- `extensions/vscode-lopper/src/languageConfiguration.ts`: update editor inference and auto-refresh behavior for the new adapter.
- `extensions/vscode-lopper/README.md`: update the extension adapter-mode docs and any pinned adapter lists.
- `README.md`: update the root supported adapter lists only after the contributor/docs updates above are correct.

## What to include in PRs

- Problem statement and intended behavior
- What changed and why
- Test evidence (`go test ./...`, manual commands, fixtures)
- Backward compatibility notes (if any)

If your change impacts CLI behavior shown in docs, refresh demo GIFs with `make demos` and include regenerated assets.

Use the PR template in `.github/PULL_REQUEST_TEMPLATE.md`.

## Reporting bugs and requesting features

Use issue templates:

- Bug report: `.github/ISSUE_TEMPLATE/bug_report.yml`
- Feature request: `.github/ISSUE_TEMPLATE/feature_request.yml`

Please include reproduction steps and environment details for bugs.

## Project structure (high level)

- `cmd/lopper`: CLI entrypoint
- `internal/cli`: argument parsing and CLI shell
- `internal/analysis`: adapter orchestration and report merge
- `internal/lang/*`: language adapters
- `internal/report`: report model, formatting, baseline math
- `internal/ui`: TUI summary/detail
- `internal/runtime`: runtime trace parsing and annotation
