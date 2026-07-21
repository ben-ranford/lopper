# Pull Request Dependency-Surface Review

`lopper pr-review` is a preview, read-only CI mode for comparing two explicit
commit SHAs. It creates isolated detached worktrees for the base and head
commits, disables Git hooks for those temporary checkouts, runs static
dependency-surface analysis only, and emits a bounded Markdown report or a full
JSON artifact.

Enable it with `dependency-surface-pr-review-preview`:

```bash
lopper pr-review \
  --repo . \
  --base 0123456789abcdef0123456789abcdef01234567 \
  --head fedcba9876543210fedcba9876543210fedcba98 \
  --format markdown \
  --output .artifacts/lopper-pr-review.md \
  --fail-on-regression \
  --enable-feature dependency-surface-pr-review-preview
```

Base and head must be full 40- or 64-character immutable commit SHAs. The mode
does not infer merge bases; workflows should pass the exact trusted base commit
and the exact head commit they want reviewed. Results are reproducible for the
same two SHAs, policy, advisory snapshot, and feature set.

## Output

Markdown is designed for pull request comments and is bounded by `--max-rows`
per section. When rows overflow, the report tells readers to generate/upload the
full JSON artifact.

JSON uses `schemaVersion: "lopper.pr-review.v1"` and includes:

- `baseSha`, `headSha`, `mergeBaseMode`, and `analysisMode`
- `summary` counts for added, removed, upgraded, downgraded, version-changed,
  policy-changed, newly reachable, and materially worsened rows
- `sections[]` with row-level dependency, language, ecosystem, base/head
  versions, PURL, identity confidence, evidence confidence, waste delta, used
  percent delta, policy changes, vulnerability fields, regression status, and
  evidence

Version upgrade/downgrade rows are only emitted when dependency identity includes
known base and head versions. Unknown identity does not produce a version-change
claim.

## Regression Exit

`--fail-on-regression` exits with the CI regression code only for newly
introduced regression rows, such as downgraded dependencies, newly denied
licenses, newly reachable vulnerabilities whose priority meets the effective
`thresholds.reachable_vulnerability_priority`, or dependencies whose estimated
unused bytes increased by at least `--material-waste-bytes`. The default
reachable-vulnerability threshold is `off`, which keeps those rows visible but
does not classify them as regressions. Pre-existing base findings do not fail
the review by themselves.

## GitHub Action

The first-party action supports `mode: pr-review` behind the same preview flag:

```yaml
- uses: actions/checkout@v7
  with:
    fetch-depth: 0

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

Use `permissions: contents: read` for the review job. Upload the JSON artifact
from a separate `mode: pr-review` run with `pr-review-format: json` when
Markdown output overflows.
