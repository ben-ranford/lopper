# Contributing

Thanks for contributing to Lopper.

## Development setup

Requirements:

- Go `1.26.x`
- `zig` (required for cross-CGO release builds)
- `golangci-lint` (optional for faster local runs; `make lint` auto-runs a pinned version)

Install dependencies and run checks:

```bash
make setup
make fmt
make test
make lint
make cov
make build
```

## Workflow

1. Create a branch for your change.
2. Add or update tests for behavior changes.
3. Keep commits focused and descriptive.
4. Open a pull request with clear context, scope, and validation steps.

## What to include in PRs

- Problem statement and intended behavior
- What changed and why
- Test evidence (`go test ./...`, manual commands, fixtures)
- Backward compatibility notes (if any)

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
