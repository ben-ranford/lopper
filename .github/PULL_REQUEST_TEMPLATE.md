## Summary

Describe the problem and the intent of this change.

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
