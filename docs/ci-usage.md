# CI and release workflow

This repository includes these GitHub Actions workflows:

- `.github/workflows/ci.yml`: runs checks on pull requests
- `.github/workflows/queue-me.yml`: serializes pull requests carrying the `queue-me` label, rebases the oldest numbered pull request onto current `main`, and arms squash auto-merge
- `.github/workflows/release.yml`: release-please-backed stable release workflow. On pushes to `main`, it creates or updates a release PR from Conventional Commits; when that release PR is merged, it creates a draft semver GitHub release, publishes assets, publishes images, optionally publishes the VS Code extension, publishes the draft release, updates the GitHub Action floating tags, and then updates the Homebrew tap with:
  - Linux/Windows artifacts from Ubuntu (cross-compiled with `zig`)
  - Darwin artifact from macOS (native arch)
  - GHCR multi-arch image (`linux/amd64`, `linux/arm64`) tagged with the release tag and `latest`
- `.github/workflows/release-orchestration.yml`: reusable workflow invoked by `release.yml` and `rolling.yml` to build release artifacts and publish GHCR images
- `.github/workflows/release-source-ci.yml`: read-only reusable workflow that validates the exact promoted source before stable or rolling artifact production starts
- `.github/workflows/rolling.yml`: on merge to `main`, publishes a rolling prerelease with Linux/Windows/Darwin build artifacts plus source bundle assets, updates GHCR `rolling`, and updates Homebrew tap formula `lopper-rolling`
- `.github/workflows/docker-ghcr.yml`: manual-only fallback to build/push the GHCR image on demand
- `.github/workflows/memory-profiles.yml`: scheduled/manual alloc-space profiling for the watched hotspot packages (`dotnet`, `rust`, `analysis`, `golang`) with uploaded artifacts and a workflow summary

Homebrew tap automation:

- The stable release workflow updates `ben-ranford/homebrew-tap` `Formula/lopper.rb` from the new semver tag.
- The rolling workflow updates `ben-ranford/homebrew-tap` `Formula/lopper-rolling.rb` from the latest rolling tag.
- The tap update step validates with `brew audit --strict --online`, `brew install --build-from-source`, and `brew test lopper` before push.
- Set repository secret `HOMEBREW_TAP_TOKEN` (with write access to `ben-ranford/homebrew-tap`) to enable automatic tap updates.

Stable release automation:

- Release versioning and changelog generation are configured in `release-please-config.json` and tracked in `.release-please-manifest.json`.
- The Go project release notes are generated into the root `CHANGELOG.md`; the VS Code extension keeps its own `extensions/vscode-lopper/CHANGELOG.md`.
- The current stable version is also synced into `extensions/vscode-lopper/package.json` and `extensions/vscode-lopper/package-lock.json` by the release-please PR.
- Manual `workflow_dispatch` runs accept only a release tag/version, normalize it to the semver tag, validate the tag ref, resolve `refs/tags/<tag>` through the GitHub API, and derive the release source SHA from that immutable tag commit.
- Manual retries only rebuild an existing GitHub release for the selected tag. The workflow reuses the existing published or draft release metadata and fails instead of creating a missing release.
- Release commits should use Conventional Commit prefixes: `fix:` for patch fixes, `preview:` for opt-in feature work that remains on the current patch line, `feat:` for graduated features and minor releases, and any non-preview type with a breaking-change marker (for example `feat!:` or `fix!:`) or a `BREAKING CHANGE:` footer for major releases.
- Release Please includes `preview:` commits under **Preview Features**. Its default versioning treats that custom type as patch, while the separate `feat(flags): graduate ...` PR requests the minor bump.
- CI rejects breaking-change markers, Release Please override/nested-commit directives, and non-preview release-note subjects in preview PR commit messages and descriptions so a preview squash commit cannot escape the patch line.
- Stable releases also publish the first-party action version refs. The release tag, such as `v1.7.0`, is the exact action ref; the workflow force-updates `v1` and `v1.7` to the same release commit for standard GitHub Actions major/minor pinning.
- Set repository secret `RELEASE_PLEASE_TOKEN` to a PAT with contents and pull request write access so release-please-created PRs can trigger normal CI. If it is not configured, the workflow falls back to `MAIN_SYNC_PAT` and then `GITHUB_TOKEN`.

## Pull request auto-merge queue

`.github/workflows/queue-me.yml` provides a repository-hosted queue for the default branch without requiring GitHub's organization-only merge queue feature. Apply the `queue-me` label to any open, non-draft pull request targeting `main`. The controller processes labeled pull requests in ascending PR-number order, rebases only the current queue leader, and enables squash auto-merge so the existing ruleset remains authoritative for checks, Sonar, metadata, and resolved conversations. A merge or any other push to `main` causes the next leader to be rebased against the new exact base. Removing `queue-me` disables that pull request's auto-merge and advances the remaining queue.

The workflow uses `pull_request_target`, but it never checks out a repository tree. It downloads only `scripts/queue_me_controller.js` from the exact trusted `github.workflow_sha` through GitHub's Contents API, writes that blob to runner temporary storage, and executes it. API writes use a repository-scoped GitHub App installation token so the rebase-generated `synchronize` event can start normal CI without the recursive-workflow restrictions of `GITHUB_TOKEN`.

Configure the controller once:

1. Enable **Allow squash merging** under **Settings > General > Pull Requests**. The controller always uses squash merges.
2. Enable **Allow auto-merge** in the same settings section. The controller relies on repository auto-merge while required checks are pending.
3. Create and install a GitHub App on this repository with repository permissions `Contents: Read and write`, `Issues: Read and write`, `Pull requests: Read and write`, and `Workflows: Read and write`. The Workflows permission lets queued maintenance pull requests update files under `.github/workflows`.
4. Add the App's client ID as repository variable `QUEUE_APP_CLIENT_ID`.
5. Add its private key as repository secret `QUEUE_APP_PRIVATE_KEY`.
6. Run the `queue me` workflow manually once. This creates the `queue-me` label when it is missing and reports an empty queue successfully.

If the App configuration is absent, the workflow exits successfully with a notice and performs no writes. Rebase conflicts, draft queue leaders, and stale fork branches pause the queue and update one sticky status comment on the blocking pull request. Fork pull requests must be rebased manually when stale because the repository-scoped App token cannot write to a contributor's fork; once current with the default branch, they can advance through normal squash auto-merge. The controller never requests reviews; ordinary review and stale-approval policy remains owned by the repository ruleset.

Validate the no-credential path locally with `act workflow_dispatch -W .github/workflows/queue-me.yml --job advance --strict`; it must report the inactive-controller notice, skip token creation and API writes, and complete successfully.

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

## First-party GitHub Action

Lopper ships the `Scan with Lopper` composite GitHub Action at the repository root. The action downloads a pinned Lopper release binary, builds Lopper arguments from named inputs, and supports the same CI outputs used by the CLI: threshold-gated analysis, JSON baselines, PR-comment markdown, SARIF, preview dashboard mode, and preview base/head PR review mode.

Pin both the action ref and the `version` input for fully reproducible CI. The default `version: action` uses the matching concrete action tag when the workflow references a full release tag such as `v1.7.0`; when the workflow references a floating action tag such as `v1` or `v1.7`, it resolves the newest stable Lopper release in that series; otherwise it resolves the latest stable release. The action intentionally does not expose a raw `extra-args` passthrough, so shell values are never re-parsed by `eval` or another shell.

The standard release workflow publishes the semver action ref and updates the floating action refs with every stable release.

Preview action modes are explicitly gated:

- `mode: dashboard` requires `enable-feature: action-dashboard-mode-preview`
- `mode: pr-review` requires `enable-feature: dependency-surface-pr-review-preview`

`mode: pr-review` accepts `pr-base` and `pr-head` as full immutable commit SHAs and does not infer merge bases. It creates detached temporary worktrees with Git hooks disabled and does not run package-manager or runtime test commands.

```yaml
- uses: ben-ranford/lopper@v1.9.0
  with:
    version: v1.9.0
    mode: pr-review
    repo: .
    pr-base: ${{ github.event.pull_request.base.sha }}
    pr-head: ${{ github.event.pull_request.head.sha }}
    pr-review-format: markdown
    pr-review-output: .artifacts/lopper-pr-review.md
    pr-fail-on-regression: 'true'
    enable-feature: dependency-surface-pr-review-preview,dependency-identity-preview
```

### Pull request comment workflow

```yaml
name: lopper-pr
on:
  pull_request:

permissions:
  contents: read
  issues: write
  pull-requests: write

jobs:
  lopper:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0

      - name: Prepare base report workspace
        env:
          BASE_REF: ${{ github.event.pull_request.base.ref }}
        run: |
          mkdir -p .artifacts
          git fetch --no-tags --depth=1 origin "${BASE_REF}"
          git worktree add --detach .artifacts/base "origin/${BASE_REF}"

      - name: Generate base report
        uses: ben-ranford/lopper@v1.7.0
        with:
          version: v1.7.0
          repo: .artifacts/base
          language: all
          top: '20'
          format: json
          output: .artifacts/lopper-base.json

      - name: Remove base report workspace
        if: ${{ always() }}
        run: git worktree remove --force .artifacts/base || true

      - name: Generate PR comment report
        id: lopper
        continue-on-error: true
        uses: ben-ranford/lopper@v1.7.0
        with:
          version: v1.7.0
          repo: .
          language: all
          top: '20'
          baseline: .artifacts/lopper-base.json
          format: pr-comment
          output: .artifacts/lopper-pr-comment.md
          threshold-fail-on-increase: '0'
          enable-feature: reachability-vulnerability-prioritization-preview
          advisory-source: security/lopper-advisories.yml
          threshold-reachable-vulnerability-priority: high

      - name: Upsert Lopper PR comment
        if: ${{ always() }}
        uses: actions/github-script@v9
        with:
          script: |
            const fs = require('node:fs');

            const marker = '<!-- lopper-pr-report -->';
            const reportPath = '.artifacts/lopper-pr-comment.md';
            const body = fs.existsSync(reportPath)
              ? `${marker}\n${fs.readFileSync(reportPath, 'utf8').trim()}`
              : `${marker}\n## Lopper\n\nLopper did not produce a PR report.`;

            const comments = await github.paginate(github.rest.issues.listComments, {
              owner: context.repo.owner,
              repo: context.repo.repo,
              issue_number: context.issue.number,
              per_page: 100,
            });
            const existing = comments.find(
              (comment) =>
                comment.user?.type === 'Bot' &&
                typeof comment.body === 'string' &&
                comment.body.includes(marker),
            );

            if (existing) {
              await github.rest.issues.updateComment({
                owner: context.repo.owner,
                repo: context.repo.repo,
                comment_id: existing.id,
                body,
              });
            } else {
              await github.rest.issues.createComment({
                owner: context.repo.owner,
                repo: context.repo.repo,
                issue_number: context.issue.number,
                body,
              });
            }

      - name: Enforce Lopper threshold
        if: ${{ steps.lopper.outcome == 'failure' }}
        run: exit 1
```

`advisory-source` points at a local JSON or YAML file in the checked-out
workspace and requires
`enable-feature: reachability-vulnerability-prioritization-preview`. It does not
fetch a network vulnerability database. When
`threshold-reachable-vulnerability-priority` is set above `off`, Lopper fails on
reachable advisory findings at or above that priority; in baseline mode it gates
only on newly introduced reachable findings.

### SARIF code-scanning workflow

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

      - name: Generate Lopper SARIF
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

## Make targets used by CI

- `make build`: build local executable at `bin/lopper`
- `make lint`: run `golangci-lint` with `govet`, `unused`, `ineffassign`, `staticcheck`, `errcheck`, `gocritic`, `errorlint`, and `revive`
- `make actionlint`: validate GitHub Actions workflow syntax and expressions
- `make shellcheck`: validate tracked shell scripts and git hooks
- `make mod-check`: enforce `go mod tidy -diff` and `go mod verify`
- `make dup-check`: fail when **new/changed Go lines** exceed duplication max percentage versus base ref (defaults: `DUPLICATION_MAX=3`, `DUPLICATION_TOKEN_THRESHOLD=55`, `DUPLICATION_BASE=origin/main`)
- Dup checker is pinned to immutable revision `DUPL_VERSION=f008fcf5e62793d38bda510ee37aab8b0c68e76c`.
- `make suppression-check`: fail when staged, working-tree, or branch-added source lines introduce inline suppression markers such as `nosonar`, `nosec`, `nolint`, `noqa`, `eslint-disable`, `ts-ignore`, `ts-expect-error`, and coverage-bypass comments
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
