# JVM repository detection traversal

JVM repository detection uses `shared.WalkRepoFilesWithinRootPinned` with a root
opened before Maven/Gradle root signals are inspected. Child directories are
opened relative to that root and checked for identity changes. Symlinks cannot
redirect detection into another repository.

The detector retains two independent limits: 4,096 traversal entries (including
the root and queued entries) and 1,024 confined JVM candidate files. Unrelated
files and rejected symlinks do not consume the candidate budget. Directory reads
request at most 128 entries, with a one-entry EOF probe at the exact traversal
limit. Only complete directory listings are sorted and processed. An incomplete
listing cannot contribute a partial detection.

The existing JVM policy retains already established root signals when a pure
budget limit stops traversal. Operational errors, including errors joined with a
limit or close failure, still fail detection. Cancellation is checked before root
signals, before traversal, and during enumeration. Returned module roots retain
the shared deterministic finalization order. Identity-change errors now use the
shared walker's `root changed while opening` wording.

The nested Kotlin source-layout walk is separate. This migration implements
[#1731](https://github.com/ben-ranford/lopper/issues/1731), following
[#1189](https://github.com/ben-ranford/lopper/issues/1189); shared enforcement
closure remains tracked in [#1612](https://github.com/ben-ranford/lopper/issues/1612).
