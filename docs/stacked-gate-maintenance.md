# Stacked gate maintenance prerequisites

Protected policy P is the authenticated default-branch source. Actual target B,
child H and their prospective merge M remain comparison data. Protected reuse
activation requires genuine default-branch deployment; an unmerged controller or
parent PR approval cannot publish a substitute trusted result. Recheck P/B/H,
current producer run/attempt/artifact and owner review before and after status
publication. Parent closure, deletion, movement or review withdrawal removes
certification; reopening does not restore an old receipt.

`pull_request_target` runs from the PR's base branch, so a parent stacked on
another parent cannot revoke its children from its own events: that source is not
the default branch and fails the protected pin. Revocation therefore also runs from
default-branch contexts. A `ci` `workflow_run` for a same-repository parent push
revokes PRs targeting that branch, a `delete` revokes PRs targeting the deleted
branch, and every such run sweeps open stacked PRs and revokes an existing success
whose parent chain to the default branch is no longer live. A closed parent whose
branch is kept is revoked at the next default-branch run, not instantly. Remote APIs provide no
transactional compare-and-swap, so final certification also requires a fresh live
pair/policy check after publication.
