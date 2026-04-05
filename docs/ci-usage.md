# CI and release workflow

This repository includes six GitHub Actions workflows:

- `.github/workflows/ci.yml`: runs checks on pull requests
- `.github/workflows/release.yml`: scheduled weekly (Saturday 12:00 UTC) semver release workflow that runs when meaningful changes exist since the previous stable tag or when version alignment needs to promote the stable CLI tag to the VS Code extension version, then runs CI, publishes a GitHub release, and syncs the committed VS Code extension version surfaces back to the published stable version with:
  - Linux/Windows artifacts from Ubuntu (cross-compiled with `zig`)
  - Darwin artifact from macOS (native arch)
  - GHCR multi-arch image (`linux/amd64`, `linux/arm64`) tagged with the release tag and `latest`
- `.github/workflows/release-orchestration.yml`: reusable workflow invoked by `release.yml` and `rolling.yml` to build release artifacts and publish GHCR images
- `.github/workflows/rolling.yml`: on merge to `main`, publishes a rolling prerelease with Linux/Windows/Darwin build artifacts plus source bundle assets, updates GHCR `rolling`, and updates Homebrew tap formula `lopper-rolling`
- `.github/workflows/docker-ghcr.yml`: manual-only fallback to build/push the GHCR image on demand
- `.github/workflows/memory-profiles.yml`: scheduled/manual alloc-space profiling for the watched hotspot packages (`dotnet`, `rust`, `analysis`, `golang`) with uploaded artifacts and a workflow summary

Homebrew tap automation:

- The weekly release workflow updates `ben-ranford/homebrew-tap` `Formula/lopper.rb` from the new semver tag.
- The rolling workflow updates `ben-ranford/homebrew-tap` `Formula/lopper-rolling.rb` from the latest rolling tag.
- The tap update step validates with `brew audit --strict --online`, `brew install --build-from-source`, and `brew test lopper` before push.
- Set repository secret `HOMEBREW_TAP_TOKEN` (with write access to `ben-ranford/homebrew-tap`) to enable automatic tap updates.

## PR checks

Every pull request against `main` must pass the following checks before it is eligible to merge.

### `verify` (Ubuntu)

Runs `make ci` and then additional PR-specific steps.

`make ci` is composed of, in order:

| Step | Tool / command | What it enforces |
| --- | --- | --- |
| Format check | `gofmt -l .` | All Go files must be `gofmt`-formatted. |
| Module check | `go mod tidy -diff` + `go mod verify` | `go.mod`/`go.sum` must be tidy and module graph integrity must be intact. |
| Lint | `golangci-lint` + `gostyle` | Enabled linters: `govet`, `unused`, `ineffassign`, `staticcheck`, `errcheck`, `gocritic` (`boolExprSimplify`, `builtinShadow`, `emptyFallthrough`, `equalFold`), `errorlint`, `revive` (13 rules), plus `gostyle` style rules. |
| Actions lint | `actionlint` | GitHub Actions workflow YAML must be syntactically valid. |
| Shell check | `shellcheck` | Tracked shell scripts and git hooks must pass `shellcheck`. |
| Duplication check | `dupl` | New or changed Go lines must not exceed **3%** duplication (token threshold: 55, base: `origin/main`). |
| Suppression check | `scripts/check-inline-suppressions.sh` | Branch-added lines must not introduce inline linter/security suppression markers. |
| Security scan | `gosec` | Go source must pass `gosec` static security analysis. |
| Vulnerability scan | `govulncheck` | Go module dependencies must have no known vulnerabilities. |
| Tests | `go test ./...` | All unit tests must pass. |
| Leak tests | `GOLEAK=1 go test ./...` | Tests must not leak goroutines (detected via `goleak`). |
| Race tests | `go test -race ./...` | Tests must pass under the Go race detector. |
| Memory benchmark gate | `bench-gate` via `benchdelta` | Memory benchmark deltas versus `origin/main` must not exceed **15%** bytes or **10%** allocations per benchmark. Regressions are blocked unless the PR carries the `memory-approved` label. |
| Build | `go build ./cmd/lopper` | The binary must compile cleanly. |
| Coverage gate | `coveragegate` tool | Total coverage and per-package coverage must both be at or above **98%**. |

After `make ci`, the `verify` job also runs:

| Step | What it enforces |
| --- | --- |
| Demo assets check (`make demos-check`) | Committed demo GIF assets must match the current CLI output (no stale demos). |
| Lopper self-analysis delta | Informational only — posts a dependency delta comment to the PR; does not block merge. |
| SonarCloud analysis | Informational only when `SONAR_TOKEN` is configured — posts open Sonar issues and duplication metrics to the PR; does not block merge on its own. |

### `os-smoke` (Ubuntu + macOS)

Runs `make smoke` (`mod-check + test-race + build`) on both `ubuntu-latest` and `macos-latest` to confirm the module, race tests, and binary build are clean on both operating systems.

### `vscode-smoke` (Ubuntu + macOS, conditional)

Runs only when `extensions/vscode-lopper/**` files are changed in the PR. Builds the local `lopper` binary, installs the extension, and runs VS Code E2E tests via `@vscode/test-electron` on both `ubuntu-latest` (with `xvfb-run`) and `macos-latest`.

### `homebrew-tap-verify` (Ubuntu)

Verifies the current Homebrew tap formula against the existing published tap when `ben-ranford/homebrew-tap` exists. Runs `brew audit --strict --online`, `brew install --build-from-source`, and `brew test lopper`. Skipped harmlessly when the tap repository has not yet been created.

### Memory regression approval

If the memory benchmark gate in `verify` detects a regression (exit status `1`), the `verify` job fails unless the PR carries the `memory-approved` label. Add that label when a memory increase is intentional and acknowledged.

---

## Example pipeline

Equivalent minimal job:

```yaml
name: ci
on: [pull_request]
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - run: sudo apt-get update && sudo apt-get install -y shellcheck
      - run: make tools-install
      - run: echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
      - run: make ci
      - run: make demos-check
  os-smoke:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - run: make smoke
```

On pull requests, `ci.yml` also runs lopper delta analysis against the PR base and posts/updates a bot comment.
If `SONAR_TOKEN` is set, it additionally posts/updates a SonarQube summary comment.
When running locally with `act`, target the Linux-backed jobs explicitly (for example `act pull_request -W .github/workflows/ci.yml --job verify`), because Docker-based `act` environments do not provide hosted macOS runners.

## Make targets used by CI

- `make build`: build local executable at `bin/lopper`
- `make lint`: run `golangci-lint` with `govet`, `unused`, `ineffassign`, `staticcheck`, `errcheck`, `gocritic`, `errorlint`, and `revive`
- `make actionlint`: validate GitHub Actions workflow syntax and expressions
- `make shellcheck`: validate tracked shell scripts and git hooks
- `make mod-check`: enforce `go mod tidy -diff` and `go mod verify`
- `make dup-check`: fail when **new/changed Go lines** exceed duplication max percentage versus base ref (defaults: `DUPLICATION_MAX=3`, `DUPLICATION_TOKEN_THRESHOLD=55`, `DUPLICATION_BASE=origin/main`)
- Dup checker is pinned to immutable revision `DUPL_VERSION=f008fcf5e62793d38bda510ee37aab8b0c68e76c`.
- `make suppression-check`: fail when staged changes or branch-added lines introduce inline suppression markers
- `make format-check`: fail if `gofmt` changes are needed
- `make security`: run `gosec`
- `make vuln-check`: run `govulncheck`
- `make test-leaks`: run `go test ./...` with `goleak` enabled to catch leaked goroutines
- `make test-race`: run `go test -race ./...`
- `make bench-mem`: run the curated memory benchmark suite with `-benchmem`
- `make bench-gate`: compare curated memory benchmark deltas against a base ref (defaults: `MEMORY_BENCH_BASE=origin/main`, `MEMORY_BENCH_MAX_BYTES_PCT=15`, `MEMORY_BENCH_MAX_ALLOCS_PCT=10`)
- `make cov`: run tests with coverage profile and enforce both minimum total coverage (`COVERAGE_MIN`, default `98`) and minimum per-package coverage (`COVERAGE_PACKAGE_MIN`, default `98`), excluding helper-only packages such as `internal/testutil`, `internal/testsupport`, and the local CI helper tool `tools/benchdelta`
- `make smoke`: run cross-OS smoke checks (`mod-check + test-race + build`)
- `make ci`: `format-check + mod-check + lint + actionlint + shellcheck + dup-check + suppression-check + security + vuln-check + test + test-leaks + test-race + bench-gate + build + cov`
- `make mem-profiles`: capture package-focused alloc-space summaries for the watched hotspot packages and write them under `.artifacts/memory-profiles/`
- `make toolchain-check`: verify required cross toolchain binaries plus `shellcheck`
- `make toolchain-install`: install required OS toolchains (`go`, `zig`, `shellcheck`) on macOS/Linux
- `make tools-install`: install pinned Go-based CI tools locally (`golangci-lint`, `gostyle`, `gosec`, `actionlint`, `govulncheck`)
- `make setup`: bootstrap toolchain + module download + readiness checks
- `make release VERSION=<tag>`: build release archives in `dist/` (host platform by default)

Coverage artifacts:

- `.artifacts/coverage.out`: `go test` coverage profile
- `.artifacts/coverage-total.txt`: total percentage used by CI gating
- `.artifacts/coverage-packages.txt`: per-package coverage percentages emitted by the coverage gate
- `.artifacts/coverage-package-failures.txt`: packages that fell below the per-package minimum, formatted for CI comments
- `.artifacts/bench-base.out`: benchmark output captured from the base ref
- `.artifacts/bench-head.out`: benchmark output captured from the current ref
- `.artifacts/memory-bench-summary.md`: markdown summary posted to PRs
- `.artifacts/memory-bench-status.txt`: exit status from the memory benchmark gate (`0` pass, `1` regression, `2` execution/parsing error)
- `.artifacts/memory-profiles/<timestamp>/`: watched-package alloc-space summaries, raw profiles, and logs

Memory benchmark approval:

- PR CI runs the memory benchmark delta gate in report-only mode and comments the summary on the PR.
- Regressions are still blocked unless a maintainer applies the `memory-approved` label.
- Newly added benchmarks are reported but not gated until the same benchmark name exists on both the base and head refs, which keeps first-rollout noise manageable.

On pull requests, if the coverage gate fails, CI posts/updates a PR comment with total coverage and any packages that fell below the per-package minimum.
The 98% minimum is intentionally enforced in both `Makefile` and CI workflow config for the combined gate and the per-package gate to keep local and CI behavior aligned.

Cross-compilation uses `zig cc` for CGO targets.
Current cross-CGO release targets are Linux and Windows.
For Darwin artifacts, build on a macOS runner with native architecture.

To build multiple platforms:

```bash
make release VERSION=vX.Y.Z PLATFORMS="linux/amd64 darwin/arm64 windows/amd64"
```

Install toolchain support with:

```bash
make toolchain-install
```
