# Contributing

Thanks for contributing to Lopper.

## Development setup

Requirements:

- Go `1.26.x`
- `zig` (required for cross-CGO release builds)
- `shellcheck` (required for `make ci` and git hooks)
- `golangci-lint` (optional for faster local runs; `make lint` auto-runs a pinned version)
- `gostyle` (optional for faster local runs; `make lint` auto-runs a pinned version)
- `actionlint` (optional for faster local runs; `make actionlint` auto-runs a pinned version)
- `govulncheck` (optional for faster local runs; `make vuln-check` auto-runs a pinned version)

Install dependencies and run checks:

```bash
make setup
make ci
make demos-check
```

`make ci` includes goroutine leak detection and the curated memory benchmark delta gate against `origin/main`.
It also blocks newly introduced inline suppression markers in source files, so fix the underlying finding instead of adding `nosonar`, `nosec`, `nolint`, `noqa`, `eslint-disable`, `ts-ignore`, `ts-expect-error`, or coverage-bypass comments.
If a change intentionally increases the tracked memory benchmarks beyond the configured thresholds, note that in the PR and ask a maintainer to apply the `memory-approved` label.

## VS Code Extension

The VS Code extension adds editor diagnostics and hover context across supported Lopper adapters, plus safe JS/TS quick fixes on top of the local `lopper` CLI.

- Extension ID: `BenRanford.vscode-lopper`
- Command: `Lopper: Refresh Diagnostics`
- Adapter mode: `lopper.language` with `auto` by default. `auto` follows the active or saved editor when possible, including Android Gradle Kotlin/Java modules and merged adapter matches, or you can pin a specific adapter.
- Supported adapter pins: `cpp`, `dart`, `dotnet`, `elixir`, `go`, `js-ts`, `jvm`, `kotlin-android`, `php`, `python`, `ruby`, `rust`, `swift`
- Binary resolution order: `LOPPER_BINARY_PATH`, `lopper.binaryPath`, workspace `bin/lopper`, `PATH`, then managed download from GitHub releases
- Quick fixes: deterministic JS/TS subpath rewrites when `lopper` reports a safe codemod suggestion

Local extension workflow:

```bash
make build
make vscode-extension-install
make vscode-extension-test
make vscode-extension-package
```

Extension smoke tests run in GitHub Actions on macOS and Linux with `@vscode/test-electron`.

Refresh terminal demos:

```bash
make demos
```

Capture watched-package memory profiles:

```bash
make mem-profiles
```

## Workflow

1. Create a branch for your change.
2. Add or update tests, docs, and demo assets when behavior, flags, or workflows change.
3. Keep commits focused and descriptive.
4. Open a pull request with clear context, scope, and validation steps.

## Feature flagging

Use a feature flag when new work should merge before it is ready to become a stable default.
This includes user-visible behavior changes, risky adapter heuristics, release workflow changes, new default policies, or features that need rolling-channel validation before broad release.
Small refactors, tests, and documentation-only changes usually do not need flags unless they change shipped behavior.

Feature flags are documented in `docs/feature-flags.md`.
Contributor rules:

- Generate the code with `make feature-flag NAME=... DESCRIPTION=...`; do not hand-allocate or recycle `LOP-FEAT-NNNN` codes.
- New flags start as `preview`.
- Merging the implementation PR is not graduation.
- Rolling builds enable every registered flag by default.
- Stable release builds enable `stable` flags by default and may enable specific `preview` flags through `internal/featureflags/release_locks.json`.
- Release locks are release-specific default-on decisions; they do not change the lifecycle to `stable`.
- Graduation requires a separate PR that changes the registry lifecycle to `stable` and states the rollout evidence.
- Use `make feature-flag-graduate FEATURE=...` locally, or run `graduate-feature.yml`, to prepare a graduation PR.
- PRs that use the `feat` Conventional Commit type must add at least one new feature flag entry, and new entries must stay `preview`.
- PRs that add, lock, or graduate a feature flag must run `go run ./tools/featureflag validate`.

Reviewers should ask for a flag when the change changes default user behavior but lacks enough evidence for immediate stable rollout.
Reviewers should ask for a release lock instead of graduation when a release should publish a preview feature default-on for that release only.

## Commit style and releases

Stable releases are prepared by release-please from Conventional Commits merged to `main`.
Use these commit types for changes that should appear in release notes:

- `fix(scope): summary` for patch releases.
- `docs(scope): summary` for documentation-only patch releases.
- `refactor(scope): summary` for refactoring patch releases.
- `perf(scope): summary` for performance patch releases.
- `feat(scope): summary` for minor releases.
- `type(scope)!: summary` or a `BREAKING CHANGE:` footer for major releases.

Use non-release types such as `test`, `ci`, `build`, or `chore` when the change should not by itself create a release PR.
Good scopes include `release`, `ci`, `vscode`, `js`, `jvm`, `go`, `report`, `ui`, `runtime`, and `homebrew`.

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
- Test evidence (`make ci`, `make demos-check`, `act pull_request -W .github/workflows/ci.yml --job verify`, manual commands, fixtures)
- Documentation updates for user-facing behavior, flags, workflow changes, and examples
- Feature flag lifecycle, release-lock, and graduation notes when the PR adds or changes flagged behavior
- Performance notes when memory benchmark deltas are intentional, including whether `memory-approved` is needed
- Backward compatibility notes (if any)

If your change impacts CLI behavior, flags, workflows, or documented examples, refresh the relevant docs and demo GIFs with `make demos` and include regenerated assets.

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
