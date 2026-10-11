# Shared JVM traversal acceptance

This is the verification closure for [#1744](https://github.com/ben-ranford/lopper/issues/1744),
dependent on [#1731](https://github.com/ben-ranford/lopper/issues/1731). The parent
moves JVM detection onto the existing shared pinned walker. This closure changes
no traversal policy or Kotlin Android nested source-layout behavior.

| Contract | Executable coverage |
| --- | --- |
| Pinned root and child identity; replacement cannot redirect reads | `TestWalkRepoFilesWithinRootRejectsDirectoryReplacementBetweenEnumerationAndOpen`, `TestWalkRepoFilesWithinRootRejectsAncestorReplacementBeforeNestedDirectoryOpen`, JVM replacement tests |
| Separate candidate/traversal budgets, queued entries and rejected-entry floods | `TestRootedWalkMigratedJVMOverflowQueueState`, `TestRootedWalkMigratedJVMExactAndCandidateQueueState`, `TestRootedWalkMigratedJVMRejectedEntryFloodCounts`, JVM unrelated-file-flood test |
| Exact completion, one-entry probes, bounded batches and no progress | `TestWalkRepoFilesWithinRootSortsExactBudgetProbeSuccessBranch`, `TestWalkRepoFilesWithinRootProbeNoProgress`, `TestJVMDetectAndWalkBranches` |
| Pure limits retain existing signals; joined operational/close errors fail | `TestRootedWalkBudgetWarning`, `TestJVMDetectWithConfidencePreservesRootSignalWhenTraversalBudgetStopsWalk`, `TestJVMDetectWithConfidencePropagatesTraversalLimitCloseError` |
| Cancellation before signals and during enumeration | `TestJVMDetectionCancellationAfterRootSignals`, `TestJVMDetectionCancellationDuringDirectoryRead`, existing canceled-context adapter tests |
| Normal skip policy, confidence and deterministic roots | JVM detection branch tests, `TestWalkRepoFilesWithinRootSortsEntriesAndSkipsDirs` |
| Linear pinned operations and callback-relative reads beyond a directory batch | Existing shared/JVM deep-wide operation tests and `TestPinnedWalkWideBatchOperationBounds` |

For a complete tree containing D directories and F files, traversal opens D−1
child roots and D directory handles, and performs 3D−1 rooted identity checks.
The new public callback test also reads each file through its supplied pinned
parent and leaf, so total opens become D+F. It exercises root listings of 129
and 258 entries (crossing the 128-entry read batch) and a depth-seven binary
tree. Exact traversal, file and work budgets must still complete. These are
operation-count assertions, not timing or general allocation guarantees.

The parent already provides the behavioral fix and adversarial tests. Both the
parent and this closure should pass those tests; this test-only increment does
not claim a new failing product baseline. Final acceptance still requires the
genuine committed parent, full CI, race/static checks and zero outstanding
Sonar/Codex findings on the resulting PR head.
