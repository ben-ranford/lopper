<!-- PR title must use Conventional Commits, for example `fix(scope): concise summary`. Use `preview(scope): summary` for default-off feature work and `feat(flags): graduate ...` only when graduating it. Squash merges publish that title to `main`, so do not rewrite it into a non-conventional subject. -->

## Summary

Describe the problem and the intent of this change. Use `N/A` only when the section truly does not apply.

## Changes

- 

## Validation

Commands and checks run:

```bash
make ci
make demos-check
act pull_request -W .github/workflows/ci.yml --job verify
```

Additional manual validation:

- 

<!-- For fix(...) PRs, add one or more exact lines like `Regression-Test: ./package::TestName`. If no deterministic regression proof is possible, add exactly one line `Regression-Test-Exemption: <reason>` and ask a maintainer to apply the `regression-exempt` label; both the non-empty reason and maintainer-controlled label are required. -->

## Risk and compatibility

- Breaking changes:
- Migration required:
- Performance impact:
- Memory benchmark impact:

## Checklist

- [ ] Tests added/updated for behavior changes
- [ ] Docs updated (README/docs/schema) if needed
- [ ] `memory-approved` requested/applied if intentional memory benchmark regressions exceed CI thresholds
- [ ] No unrelated changes included
- [ ] Ready for review
