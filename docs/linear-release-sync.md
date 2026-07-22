# GitHub to Linear release sync

Lopper treats GitHub as the delivery system of record and Linear as the product-planning view. The `linear release sync` workflow keeps delivery issues assigned to the configured GitHub milestones correlated one-to-one with their Linear release labels and project milestones.

## What is synchronized

The checked-in [`.github/linear-release-sync.json`](../.github/linear-release-sync.json) maps `v2.0.0`, `v3.0.0`, and `v4.0.0` to stable Linear IDs. For each GitHub issue in one of those milestones, the workflow:

- finds an existing Linear issue by the GitHub URL attachment before doing anything else;
- treats an unmarked, hand-authored Linear issue as coverage and leaves every field untouched;
- creates a missing mirror in the Lopper project with matching open or closed state;
- updates only marked mirrors, preserving labels the automation does not manage;
- assigns the matching Linear project milestone and release label; and
- removes managed release assignment from an existing mirror after demilestoning without deleting the issue or its history.

Candidate research remains Linear-only. Unmapped GitHub milestones, including `Overflow`, do not create Linear mirrors.

## Triggers and reconciliation

Issue open, edit, close, reopen, label, and milestone events run a single-item reconciliation. A nightly run at 03:17 UTC reconciles every issue in the configured milestones plus every marked mirror already in the Linear project. This second set repairs missed demilestone or release-change events. `workflow_dispatch` provides the same full pass on demand and has a dry-run option.

The workflow is serialized so overlapping events cannot race to create the same mirror. Linear URL attachments are the idempotency key. If one URL resolves to more than one Linear issue, the run fails without choosing a winner.

If Linear creates an issue but its URL attachment fails, the next run scans marked mirrors in the project, recovers the issue by its sync key, and adds the missing attachment instead of creating a duplicate.

## Required secret

Create a Linear API key with access to the Ben Ranford team and add it to the GitHub repository as the Actions secret `LINEAR_API_KEY`. The workflow's GitHub token is read-only (`contents: read`, `issues: read`).

After changing the secret or mapping, run the workflow manually with dry-run enabled, inspect the Actions log, and then run it again without dry-run.

## Adding a release target

1. Create the GitHub milestone.
2. Create the corresponding Linear project milestone and a child label under the `Release` label group.
3. Add the exact GitHub milestone title and the two Linear IDs to `.github/linear-release-sync.json`.
4. Run the Python and workflow contract tests.
5. Dispatch a dry-run before enabling mutations.

Do not map discovery-candidate milestones. Candidate hypotheses stay in Linear until promoted into delivery work assigned to a release target or `Overflow`.
