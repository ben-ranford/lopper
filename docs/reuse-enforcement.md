# Combined reuse enforcement rollout

Refs #1612. This records implementation separately from protected-target
enforcement. Neither this document nor a passing local run closes the issue.

## Protected combined runner

`scripts/check_reuse.py` combines the production occurrence ratchet from #1611
with the targeted helper checks from #1615. Run it from a clean checkout of the
selected protected base after both dependencies and this runner have landed:

```sh
python3 -E -S -B scripts/check_reuse.py --base "$BASE_SHA" --revision "$MERGE_SHA"
```

Both arguments must be full commit SHAs. The candidate must descend from the
selected base. For a pull request, fetch its exact event head, verify that the
fetched PR ref still equals that head, and construct the prospective merge using
`git merge-tree --write-tree BASE HEAD` and `git commit-tree`. Reject conflicts
and mismatched refs. For `merge_group: checks_requested`, use the event's
`base_sha` and `head_sha` directly; do not substitute the default branch or one
constituent PR. Retain the candidate commit in the local object database so the
isolated checkout can fetch it.

The runner builds the helper CLI in the protected base and invokes the protected
duplication script/indexer against an isolated candidate checkout. Candidate
Make recipes, workflows, Go modules and checker programs are never executed.
The protected Makefile supplies the pinned clone detector and thresholds. Go
workspace, overlay, cache-program and persisted environment overrides are
disabled. Inherited duplication revision/tool overrides are removed. The runner
rejects untracked or ignored files and hidden-change index flags in protected
tool paths. A non-Go change in a tree containing symlinks triggers full analysis
because a Go source file can refer to it through a link. Root and nested
`.gitattributes` changes also trigger analysis because checkout encoding rules
can alter otherwise unchanged Go blobs. The runner
logs the immutable base and candidate revision and runs both detectors even if
one reports a violation. Exit 0 means both passed; 1 means a violation; 2 means
analysis or configuration failed. Missing dependencies fail even for a
documentation-only change. A change with no relevant source or policy files
reports explicit success after validating the protected dependencies and base.

The caller must supply a read-only analysis job, with no secrets and no persisted
checkout credentials. Removing token environment variables inside this runner
does not remove the job's ambient permissions. Publish a required result only
from a separate trusted job that validates the exact event, revisions and
analysis outcome. Missing, cancelled or incomplete analysis cannot pass.

## No-suppression policy

The combined runner never passes the helper's `-exceptions` option and always
passes `-legacy-advisory=false`. It requires both the `families` and `exceptions`
arrays to be empty in the protected and candidate duplication baselines. Missing
or malformed arrays also fail. It does not regenerate or expand the historical
occurrence baseline. Existing family allowances must be removed through reviewed
source migrations before activation; inherited allowances are not approved.

The dependency snapshots audited for this implementation were #1754 at
`95487fc8a6ddbc695dd11dfa61509635051af336` and #1757 at
`17c84ce2ffee41f8e0d5f6969b7eb603d134e945`. Those snapshots contained 26 historical clone
families (54 listed members), zero explicit duplication exceptions, and five
frozen helper migration allowances. Those counts describe inherited policy,
not a clean final integration. The subsequent #1754 audit identified 39 affected
files and 30 permanently allowed clone pairs, without expiry or ownership
metadata; default output hid their locations. That audit held #1754
admission and treated these as unresolved historical suppressions. No signoff
may approve them merely because they were inherited. Final enforcement must
reject the residual allowances. Empty-family enforcement is implemented in this
runner. These historical counts do not describe a later production revision;
rerun the strict detectors on the exact activation base and candidate. The strict
combined runner disables all five helper allowances. Unproven semantic similarities and test duplication remain
advisory by the checker contracts.

The accepted policy for #1612 and #1768 is no clone exceptions. Empty `families`
and `exceptions` arrays are the only passing baseline shape. The regression suite
proves a valid candidate with both arrays empty passes and that any non-empty
protected or candidate allowance fails closed before detector execution. There
is no approved-exception passing case. Remove remaining migration copies and
historical family allowances through reviewed changes. Do not describe the
presence of an empty baseline as proof that both strict detectors pass. Do not claim
zero findings based on an advisory-only exit status.

The historical local composed dependency scan reported 30 clone pairs with zero
new violations, and these five blocking helper copies with legacy mode disabled:

| Source | Existing migration |
| --- | --- |
| `internal/analysis/identity_enrichment.go::sortedUnique` | #1724 |
| `internal/lang/powershell/reporting.go::sortedDependencyUnion` | #1724 |
| `internal/report/vulnerability.go::sortedUniqueStrings` | #1724 |
| `internal/lang/jvm/reporting.go::buildDependencyReport` | #1769; #1765 retains the broader next-version work |
| `internal/lang/php/reporting.go::buildDependencyReport` | #1769; #1765 retains the broader next-version work |

#1756 already delivered #1614's six-adapter scope; it did not cover JVM/PHP.
Do not refresh whole-file legacy digests when another PR edits these files.

## Exact-revision agent review

The user selected agent review for this task. A signoff is a submitted GitHub
pull-request review with API event `COMMENT`, explicitly labelled
`agent-reviewed`. It is not a native approving review, a human approval, or an
independent owner review. The trusted workflow fixes the authorized identity
as `ben-ranford`; candidate content cannot appoint another reviewer.

After inspecting the exact revisions and validation evidence, the reviewing
agent submits this strict body, with the real repository, PR number and full
commit SHAs, and sets the API review `commit_id` to that same head:

```text
agent-reviewed
{"version":1,"repository":"ben-ranford/lopper","pull_request":<number>,"base":"<full base SHA>","head":"<full head SHA>","decision":"approve"}
```

`scripts/reuse_policy_review.py::format_signoff` renders the body without
submitting it. To withdraw, submit a newer `COMMENT` review with
`"decision":"withdraw"`. The controller fully paginates live reviews and uses
the latest submitted review from the configured identity. A later ordinary,
malformed, dismissed, stale or withdrawn review blocks policy approval instead
of reviving an older approval. Submitted reviews cannot be deleted through the
review API. Bodies remain editable, so the controller reads their current
content immediately before publication and rejects changes during validation.
Native `APPROVED` state alone does not establish this explicit agent signoff.

Review is required only when the prospective merge changes policy paths:
the baseline; duplication script, policy and indexer; helper CLI/rules/catalog;
combined runner, event driver and review validator; Makefile and Go build
configuration; all `.github` files; local action definitions; checkout
attributes at any depth; and root, `.github` or `docs` CODEOWNERS. A diff with
rename detection disabled includes deletions and both sides of renames. The
historical global CODEOWNERS file is not changed or activated by this design.

## Protected event controller

`reuse-check.yml` runs its controller and publisher from the immutable protected
workflow source. Pinned checkout uses its default workflow repository and event
ref with full history as bootstrap data. The supported events provide the base
repository context; no repository or ref input can select a fork. The immediately
following workflow-inline Python runs in isolated mode, validates the selected
full SHA, and requires the configured origin to equal the protected
`ben-ranford/lopper` HTTPS URL. It then requires the SHA to be an ancestor of the
fetched protected `main`, disables Git environment overrides and checkout hooks,
checks out that SHA
detached, and verifies `HEAD`. No repository code or Go setup runs before this
pin. Controller jobs select the immutable workflow-source SHA; analysis selects
the verified base SHA. A ref outside protected main's history fails.
The analysis job uses
a pinned toolchain with no shared cache, fetches and verifies the exact PR ref,
and constructs the prospective merge without candidate hooks or merge drivers.
CLI evidence reads and writes are restricted to the fixed `reuse-snapshot.json`
and `reuse-result.json` files in the runner scratch directory; symlink and
outside-path substitutions fail. API locators are reconstructed against the fixed
GitHub origin with encoded, validated path segments and bounded pagination.
Only that read-only job invokes the combined runner. Checkout credentials are
not persisted, and candidate-derived diagnostics cannot emit workflow commands.

The controller handles protected `pull_request_target` events and fresh owner
`/reuse-check` comments. A comment requests reevaluation; it grants no review
approval. A separate no-checkout `pull_request_review` signal covers submitted,
edited and dismissed reviews. Its bounded artifact contains only a PR number.
The signal selects the review's owner author, not the event sender, so another
maintainer dismissing the owner's review also wakes the controller.
The protected `workflow_run` controller validates the originating run and reads
live PR/review data itself. Artifact data cannot grant a signoff or establish a
passing detector result. If an authenticated owner review signal cannot identify
its PR safely, the controller conservatively marks all open default-target heads
pending before failing. An unreadable notification must not preserve an earlier
approval; an owner refresh recovers unaffected PRs.

A non-owner signal can trigger reevaluation only when the latest submitted
owner review is currently `DISMISSED` in GitHub's complete live review history.
The dismisser need not retain collaborator access. A newer owner decision takes
precedence; neither another reviewer nor an artifact can authorize approval.
If that signal's PR association is missing or malformed, the controller scans
open default-target PRs and invalidates only those with a live latest owner
dismissal. Unreviewed PRs and PRs with a current owner approval are untouched.
An unrelated actor can therefore request reevaluation of an already dismissed
owner review, but cannot use this path to invalidate all PRs. API failures are
reported and do not authorize blanket invalidation; an owner refresh can retry.
The live read and pending write are asynchronous: a concurrent newer approval
may temporarily be replaced with pending until reevaluation or owner refresh.

Preparation replaces prior success with a pending commit status. The final
publisher requires a successful analysis job, complete matching revision
outputs, both passing detectors, and current policy authorization. It rechecks
live head/base and review data before success. Publishers serialize per PR and
recheck after writing success, replacing it with failure if authorization or
revisions changed during publication. Failed, cancelled, skipped,
missing or malformed analysis never passes. Main pushes invalidate statuses for
open default-branch PRs to pending because an old base signoff is stale. A new
head or an owner `/reuse-check` comment runs analysis for the current pair.
Events are asynchronous; the custom merge queue must also validate current
revisions and authorization immediately before admission/merge.

An authenticated current CI run that is still queued or running is a scheduling
wait. Preparation leaves `reuse-check` pending and skips proof jobs until a
fresh event can perform the full analysis. A complete empty CI inventory also
waits for registration; malformed or incomplete inventory remains an error.
The queue similarly withholds its ticket while awaiting CI for the current
queue intent. Waiting produces no successful proof or merge approval, and a
newer failed run never falls back to older green evidence.

If a newer authenticated CI attempt supersedes verification before a receipt
is issued, that verification is deferred without a receipt. If publication
already holds an older receipt, it invalidates that proof and corrects its own
stale success to pending. Fresh CI completion must perform verification again. Genuine detector, policy, identity and artifact
errors still fail. The final queue continues to require current full CI,
protected proof, live review and GitHub `CLEAN` before its guarded merge.
Asynchronous status correction can still leave pending after a concurrent
newer result; an owner refresh retries verification of the current pair.

The required result is the explicitly published **commit status** `reuse-check`
on the exact PR head. No workflow job uses that name. GitHub documents that
native job checks from `workflow_run` or `issue_comment` do not satisfy PR
required checks; this design does not rely on those job checks. Live isolated
protected-target proof must establish that the commit status is accepted from
the expected Actions integration before changing the production ruleset.

The repository is personally owned and currently uses a custom queue. Native
GitHub merge queues are unavailable in this configuration. The event module
tests exact `merge_group.base_sha`/`head_sha` parsing but rejects live group
execution: do not mistake this for native queue enforcement. If native queues
are adopted, authenticate an immutable no-checkout collector before accepting
its original event, analyze that exact group pair, and require aggregate policy
authorization. Candidate group YAML and constituent PR signoffs are not trusted
substitutes. Live group proof remains an explicit future adoption prerequisite.

## Trusted suppression caller

The controller also invokes the separate #1606 adapter from #1770 in a read-only
job pinned to the exact protected base. That dependency must land before this
job can succeed. Candidate files and artifacts are data; the adapter and its
base detector are trusted executable source. The publisher requires both the
reuse analysis and suppression jobs to succeed, with matching exact revisions.

CI exports its upload step's artifact ID in the name of a small dependent job,
`suppression-artifact-<id>`. This is an untrusted locator transported by GitHub's
current-attempt job metadata, not a claim that candidate CI code is trusted.
The caller requires a unique completed successful locator job, validates the
artifact through the adapter, and independently scans the diff using the
protected-base detector. The artifact's run, attempt time window, head,
repository identities, digest, size and explicit zero-record schema must match.
The download has an eight-MiB bound and never forwards API credentials to the
signed storage URL. No artifact content is extracted or executed.

The caller enumerates the current head's CI runs within GitHub's filtered-query
limit, selects the newest run before checking its conclusion or base, and
re-fetches its current attempt. An incomplete listing, missing association,
newer failed run, stale base, or retry invalidates the evidence; it never searches
backward for an older green run. It rechecks the producer after verification.
The publisher checks the same live producer and artifact before and after
writing success, alongside the head/base and review checks. CI requested,
in-progress and completed events wake the controller; review wakeups still
require their authenticated completed signal. A retry therefore reconsiders an
old success, subject to asynchronous event delivery.

The returned receipt contains exactly `headSHA`, `baseSHA`, `runId`,
`runAttempt`, `artifactId`, and `suppressionCount: 0`. It travels only through
this trusted run's job outputs. Failed, cancelled, skipped, missing or malformed
suppression evidence cannot pass publication. The controller, caller, adapter,
base tracker and producer script are all included in the scoped review paths.

This proves only the protected detector's recognized suppression markers on
changed lines. It does not prove absence on unchanged files, in the prospective
merged tree, or for ShellCheck directives. The separate queue audit owns that
broader policy. Missing fork associations fail closed and remain a live proof
case. Local fixtures establish behavior, not protected-target enforcement;
#1606 and #1612 remain open until activation and recorded live proof.

## Read-only queue consumer

Load `reuse_event` and `reuse_policy_review` only from reviewed immutable
protected source. `pull_snapshot(api, repository, repository_id, pull_number)`
binds the live PR pair and checks the base against the current default-branch
ref. `analyze(api, snapshot, protected_base_checkout)` fetch-verifies the head,
builds the prospective merge and returns `{version, snapshot, candidate,
detector_exit, policy_paths}`. The checkout must equal the selected protected
base. Any exception or nonzero detector exit blocks admission.

For that freshly produced trusted result, call `validate_result(result,
snapshot)`, then `review_evidence(api, snapshot, result, ["ben-ranford"])`
twice and require identical results. Call `same_live_pair(api, snapshot)` again
at the last supported boundary before the guarded merge. These functions do
not publish statuses. The underlying pure `evaluate_signoff` function returns
the exact-pair review ID and decision from fully paginated live review input.

The workflow currently carries analysis in trusted job outputs; it does not
export a downloadable authenticated receipt. A consumer must run the protected
analyzer itself or implement a separately reviewed receipt-provenance contract.
GitHub `CLEAN`, the status name, its target URL or the Actions App identity alone
does not authenticate those internal outputs. `expectedHeadOid` guards the head
at merge, but does not atomically guard mutable review state or the base; keep
that limitation explicit.

For suppression evidence, invoke `require('./scripts/reuse_suppression.js')`
from the same reviewed protected checkout with `{github, context, snapshot,
token}`. It runs the #1606 adapter and returns the exact producer receipt.
The queue must authenticate its own source and live inputs; a copied JSON
receipt or status name is insufficient.

## Triage a missing or stale result

Check the producer installation and required-status settings separately. Read
protected `main`, the Actions workflow state, and the effective rules for `main`;
a workflow file on a PR branch does not establish installation.

| Observed configuration | Operational meaning |
| --- | --- |
| Protected `main` lacks `.github/workflows/reuse-check.yml` | Producer uninstalled; local results do not supply a live status. |
| Producer and dependencies are present, workflow enabled, but no effective required entry exists | Installed, unrequired; publication can run without making this status a merge requirement. |
| Producer and dependencies are present, workflow enabled, with required `reuse-check` from integration `15368` | Installed and required; the exact PR result and outstanding proof still need validation. |

Missing dependencies or a required entry with a missing or disabled producer
indicate a configuration problem.
An unavailable settings or workflow read leaves its state unknown.

For an individual PR:

1. Record the live full head and base SHAs; verify that the base equals current
   `main`. Inspect the latest `reuse-check` **commit status** on that head
   separately from job check runs.
   Use its target URL to locate the controller run, then inspect the workflow
   path, protected source revision, current attempt and job outcomes. A green
   context, URL, `github-actions[bot]` creator or Actions App identity does not
   authenticate the workflow. The analyzer's logged `revision` is the
   prospective merge commit; it is not the PR head.
2. For suppression evidence, select the newest `ci.yml` pull-request run for
   that exact head from the complete listing, then inspect its current
   `run_attempt`, live base/head association and that attempt's jobs/artifact.
   A newer pending or failed run, stale base, or retry makes older green
   evidence unusable. The `suppression` job reports missing or mismatched
   producer evidence; do not substitute an earlier successful attempt.
3. Inspect the first failing controller job: `prepare` binds the pair,
   `analyze` runs the protected checks, `suppression` validates CI evidence,
   and `publish` rechecks live evidence. After the underlying failure is
   resolved, repository owner `ben-ranford` can post the exact comment
   `/reuse-check` on the PR to request reevaluation. That comment grants no
   policy approval; a policy change still needs current exact-pair signoff.

Main pushes, CI events and review signals are asynchronous. Old success can
remain visible until the controller successfully replaces it; a concurrent
change can also require
correction after publication. A base-push run only invalidates to pending;
request fresh analysis after the PR/CI pair is current. Treat pending as work
still outstanding, and retain the run URL, attempt and revision evidence when
reporting a failure. Confirm deployed queue integration separately against the
[read-only consumer contract](#read-only-queue-consumer).

## Activation and minimal settings delta

All detector and adapter dependencies must land before this controller is
activated as a required gate. In particular, the protected base must contain
the production ratchet, helper checker, runner, event/review code and #1770
suppression adapter/producer, and the
strict helper copies and any inherited historical family allowances must
be removed. The runner rejects a nonempty baseline rather than approving it. Cleanup
must land before requiring this status: the protected-base check also rejects a
cleanup candidate while its base still contains allowances. A gate with missing
dependencies, baseline allowances or remaining proven helper copies fails; no
rollout exception is supplied. Deploy and validate the trusted status producer
before making its status required, then validate the integrated queue consumer
against that exact protected source. Preserve acceptance holds for any live
proof that is still outstanding; installing a workflow does not establish that
the proof passed.

At inspection, ruleset `12669248` required `verify`, `homebrew-tap-verify`,
`pr-metadata` and `enforce` from Actions integration `15368`, plus
`SonarCloud Code Analysis` from integration `12526`. Strict checking was enabled,
required approving reviews were zero, code-owner review was disabled, and
review thread resolution was required. Re-read settings immediately before any
approved mutation and preserve every existing rule and check.

After protected-target proof, the proposed ruleset delta is one appended entry:

```json
{"context":"reuse-check","integration_id":15368}
```

The workflow additionally requests ephemeral `statuses: write` only for the
protected preparation, invalidation and publication jobs. Analysis has only
read permissions. It uses the existing Actions token, with no persistent App,
new secret, global token-permission change, collaborator change, CODEOWNERS
change or global review requirement. Workflow activation and repository-rule
changes require their own action-time approval; this draft does not apply them.

Actions integration identity binds the App, not an individual workflow. The
current repository has one trusted same-repository writer, `ben-ranford`. This
design protects candidate source execution and enforces deliberate policy
review within that model; it does not establish isolation from another writer
who can grant a workflow status-write authority. GitHub documents duplicate
check-name ambiguity; no same-App overwrite bypass was reproduced. Keep all
workflow changes within reviewed policy scope and authenticate controller/run
provenance in the merge queue rather than accepting a check name alone.

Official references: [review API](https://docs.github.com/en/rest/pulls/reviews),
[required check eligibility](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks),
[check source and duplicate names](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches),
[ruleset schema](https://docs.github.com/en/rest/repos/rules#update-a-repository-ruleset),
[event contexts](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows),
and [native queue availability](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue).

## Required protected-target evidence

After dependencies, workflow integration, review policy and approved settings
are in place, preserve the exact checker/base/head/candidate revisions, ruleset
snapshot and check URLs for each scenario:

- Valid helper use and a no-relevant-source-change candidate pass explicitly.
- New clone/family/occurrence and proven helper-copy candidates are blocked.
- Parse/scanner failures, missing dependencies and a missing status block.
- A policy change without the selected review authorization blocks; stale or
  withdrawn authorization does not carry to another revision.
- Explicit exception additions are rejected under the current policy.
- Fork analysis uses no write credentials; review edits and withdrawals trigger
  fresh live authorization checks.
- A protected-base push invalidates an earlier same-head success.
- If native GitHub merge queues are adopted, their group base/head and trusted
  event provenance require separate live proof before activation.

Use a separately approved, non-merging protected fixture target for negative
proof. Do not submit an intentionally violating candidate for production-main
merge. Local regression tests are implementation evidence, not proof of live
branch protection. Keep #1612 and its batch tickets open until shared evidence
records actual enforcement and the final integration satisfies all existing
checks, zero SonarQube issues and zero unresolved review threads.
