# Trusted suppression provenance

Issue #1606 requires both reliable producer scheduling and trusted verification.
The required CI aggregate already waits for its producer and downloads that
producer's exact current-run artifact ID. It does not search by artifact name
across runs or start a separate artifact-wait deadline before production finishes.

The producer now writes explicit v1 evidence even for a clean scan. Aggregate
`verify` rejects missing, malformed, oversized or symlinked evidence before its
independent diff checks. These checks are still in PR-editable workflow code.

## Read-only adapter

`scripts/suppression_provenance.js` exports `verifySuppressionProvenance` for an
immutable, trusted caller. The caller supplies an authenticated GitHub client,
repository context, the exact producer artifact ID and downloaded archive bytes,
and this independently established expected snapshot:

```javascript
const expected = {
  repoId,
  headRepoId,
  pullNumber,
  headSHA,
  baseSHA,
  runId,
  runAttempt,
};
await verifySuppressionProvenance({ github, context, expected, artifactId, archive });
```

Do not take these identities from artifact contents or candidate-controlled
configuration. Both the adapter and its invocation must come from the reviewed,
immutable trusted source selected by the enforcement workflow. The adapter cannot
prove its own caller or protect a workflow that can omit the call.

The adapter checks GitHub's producer run, workflow, pull request and artifact
metadata against that snapshot. Only a completed successful pull-request run of
the repository's `ci.yml` workflow is eligible. The artifact must match the exact
run and head, repository identities, expected name and digest, and be unexpired.
An old completion or newer attempt cannot substitute for the selected run. The
[#1817 lifecycle repair](https://github.com/ben-ranford/lopper/pull/1817) defers an
authenticated newer CI attempt without returning a receipt. Invalid artifact
bytes, terminal failures and same-attempt success-to-pending contradictions still
fail; the later successful attempt must supply fresh protected verification.

The archive is bounded data. It is never extracted or executed. The reader accepts
only the known regular report files, rejects duplicate or unsafe entries and
requires explicit valid suppression evidence. Any recorded suppression fails the
strict policy; tracking metadata is not an exception.

The adapter retrieves `scripts/inline_suppression_tracker.js` through GitHub at the
exact expected base SHA and runs its existing read-only recomputation against
GitHub's diff. Candidate source, artifact-supplied code and issue-author names are
not trusted implementations. This recomputation detects suppressions omitted from
the producer document. The PR snapshot and producer attempt are checked again
before success. Temporary trusted source is removed after use.

Detection scope remains the base detector's recognized markers on changed lines.
The current detector does not recognize ShellCheck directives or inspect every
unchanged file. A repository-wide zero-suppression policy, including ShellCheck
and legitimate test-fixture handling, needs the broader queue-policy audit.

The current base exposes recomputation under `testables`; the adapter uses that
existing export without changing the tracker or duplicating its lexer. A future
export rename must update the trusted caller and adapter together.

## Activation boundary

This adapter has no workflow trigger, check publisher, credentials or repository
settings mutations. It is invoked by the protected reuse controller and queue
consumer described in [reuse enforcement](reuse-enforcement.md). Do not restore the deleted
standalone suppression polling workflow or create a competing check publisher.
Binding a check name merely to the shared GitHub Actions App does not identify
which workflow supplied it.

The selected review policy is explicit agent review and signoff bound to the exact
head and base, using the existing authenticated identity.
[#1606 closed](https://github.com/ben-ranford/lopper/issues/1606#issuecomment-5971166507)
on the recorded late-CI completion and protected artifact-provenance control.
The broader #1612 acceptance work remains separate, including real-fork proof
and automatic lifecycle recovery. Local fixtures and green PR CI do not establish
those remaining cases, and the existing non-atomic status-correction limitation
remains documented in [reuse enforcement](reuse-enforcement.md).

Relevant GitHub contracts: [artifact metadata and download API](https://docs.github.com/en/rest/actions/artifacts),
[workflow-run metadata](https://docs.github.com/en/rest/actions/workflow-runs), and
[required status-check sources](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#require-status-checks-to-pass-before-merging).
