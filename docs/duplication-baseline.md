# Zero production clone pairs

The occurrence gate requires zero production Go function pairs:

```sh
python3 -B scripts/check_duplication.py --base origin/main \
  --version f008fcf5e62793d38bda510ee37aab8b0c68e76c --threshold 55 \
  --baseline .github/duplication-baseline.json --report duplication-report.json
```

It scans all Go source with the pinned dupl binary and validates the complete detector output before classifying matches. AST function metadata comes from validated endpoints in production Go files, including unchanged helpers. Test files remain outside the production-function classifier (#1619); package-level declarations are also outside this classifier. Anonymous functions belong to their enclosing named function. This scope does not claim zero raw, test, or semantic duplication. The legacy changed-line report includes all changed Go files, including `goleak_test.go`.

Every detected production pair is a blocking violation, whether unchanged, moved, new, or reintroduced. Human and JSON reports retain every location, function name and stable finding ID. Unrelated added code never reduces the count. Removing a pair resolves that finding; it grants no allowance for later reintroduction. The report retains `canonical_helper: null`, `stale_exceptions: []` and `removed_pairs: []` for its existing schema, without granting those fields policy authority.

## Identity and calibration

An occurrence is `relative/path.go::Receiver.Function@shape` (or `Function` for a free function). The shape hashes Go AST node types and tree boundaries of the signature and body, excluding source positions, comments, and identifier/literal/operator values. Moving lines, formatting, or renaming local variables preserves identity. Declaration renames, file moves and structural changes can change the identity; any resulting detected pair still blocks.

The pinned threshold remains 55 structural nodes. Small functions below that threshold do not produce findings. Literal, operator and error-semantic differences can be intentional: structural similarity is **not proof of semantic equivalence**. Review behavior before adopting or extracting a shared helper. The gate does not offer an exception or reviewed-allowance route.

## Policy and rollout

The version-1 policy file must contain exactly `version`, `families` and `exceptions`, with both lists empty. Both protected-target and candidate policies must satisfy that requirement. Historical families, canonical-helper allowances, documented or expired exceptions, and malformed policies are errors even when a scan finds no pairs. `--propose-baseline` is disabled and fails before scanning or writing an artifact.

When the exact resolved target commit lacks a policy file, the candidate may install only the empty policy. The ordinary full scan still reports every pair and fails if any exists. There is no initial seed that can authorize existing findings. The old baseline is not regenerated, and policy review cannot waive these constraints.

`make dup-check` invokes this occurrence gate in ordinary CI and local validation. This change supplies an analyzer, not a standalone check publisher, and requests no workflow write permissions. The checker resolves policy and scanner settings from the exact selected target commit rather than the branch fork's merge-base. When a trusted caller supplies `LOPPER_DUPLICATION_REVISION`, it scans that immutable source in an isolated checkout, keeping its own indexer and policy code outside the candidate tree. Executable capture and sanitized environments protect that analysis from tooling mutations. The caller must establish trusted checker source selection; PR-owned CI cannot prove its own source is authoritative.

The planned #1612 combined `reuse-check` controller supplies that trusted caller: it selects the protected checker and constructs the exact prospective merge, including target-side additions. It invokes this CLI directly, combines its result with the strict helper detector, and owns exact-pair policy review and status publication. It does not depend on a separate duplication check. Workflow activation and required-status configuration remain separately approved work; this analyzer's introduction does not establish either. Known production pairs, attempted exceptions, missing protected dependencies and substituted contributor tools must fail in the protected-target proof; valid zero-pair sources must pass. Keep shared rollout issues open until the actual protected revision and rules have been verified. This change does not modify live repository settings.
