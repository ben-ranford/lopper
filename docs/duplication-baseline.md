# Reviewed production duplication baseline

The runner from #1610 can enforce #1611's occurrence ratchet with:

```sh
python3 -B scripts/check_duplication.py --base origin/main \
  --version f008fcf5e62793d38bda510ee37aab8b0c68e76c --threshold 55 \
  --baseline .github/duplication-baseline.json --report duplication-report.json
```

This scans all Go source with the pinned dupl binary and validates the complete detector output before classifying matches. AST function metadata is collected from tracked production Go files, including unchanged helpers; test files remain outside the blocking production policy (#1619). Function declarations overlapping matched fragments are reported together. Anonymous functions belong to their enclosing named function. Package-level declarations are outside this function policy.

A new family or another occurrence in a historical family fails independently of how much unrelated code is added. Existing families are tolerated only by an explicit baseline. Deleting occurrences is allowed; `removed_pairs` records reductions that reviewers can remove from the baseline. The report orders findings and identities deterministically and contains locations, function names, stable finding IDs, and any reviewed canonical helper. Human output recommends helper adoption, extraction, or a reviewed intentional semantic difference.

## Identity and calibration

An occurrence is `relative/path.go::Receiver.Function@shape` (or just `Function` for a free function). `shape` hashes the Go AST node types and tree boundaries of the signature and body, excluding source positions, comments, and identifier/literal/operator values. Moving lines, reformatting, or renaming local variables preserves identity. Renaming the declaration, moving its file, or changing the AST structure creates a new occurrence and requires either eliminating the match or separately reviewing a baseline migration. Deleting a member never grants a free slot for a replacement. Reintroducing an identity that remains in the baseline retains its historical status; remove retired identities to prevent this.

The pinned dupl threshold remains 55 structural nodes, not a percentage or a probability. Small unrelated functions below the threshold do not produce findings. Variable-renamed matches remain detectable. Literal, operator, statement-order, and error-semantic differences can still be intentional: a structural match is **not proof of semantic equivalence**. Do not extract a helper solely because of a match. Fuzzier semantic advice is not a blocking rule.

## Policy review and rollout

`--propose-baseline PATH` creates a deterministic proposal from the checked full scan. It is an explicit review aid and does not authorize any occurrence or change the active baseline. It must not run as an automatic refresh in a pull request.

The enabled gate reads approved policy from the target merge-base. The one-time initial seed is accepted only if it exactly covers the full scan and contains no exceptions; the seed is checked in for human review and is never regenerated automatically. After the baseline exists on the target, a PR may reduce family membership but cannot authorize new members, new exceptions, or new canonical helper claims. Expansions use the separate reviewed workflow from #1612. The baseline schema is version 1: families contain explicit `members` and a nullable `canonical_helper`; exceptions contain an exact finding SHA-256, `rationale`, `owner`, `review`, and ISO-date `expires`. Directory patterns, incomplete metadata, duplicate findings, expired exceptions, and stale exceptions fail. A canonical helper must be a family member. Policy files should receive the same protected review as gate configuration.

`make dup-check` runs the occurrence gate against `.github/duplication-baseline.json`; it does not auto-refresh that file. This PR does not yet make the resulting status required by the live repository ruleset. #1612 must review the policy-change path, configure and verify the protected status, and demonstrate on the exact protected revision that a known new clone is blocked while valid and approved-exception cases pass. Keep #1610 and #1611 open until #1612 records that enforcement evidence.
