# Shared Maven POM contract

For #1730 (Refs #1327, #1612), `internal/lang/shared/pom.go` owns XML decoding and private parsed POM evidence. Both JVM inventory and analysis identity enrichment call `DecodePOM`. The owner imports neither consumer. Reports and report/model do not import lang/shared. A parsed POM exposes fresh property maps and dependency slices; consumer mutation cannot change the retained model or another view.

The two named policies deliberately preserve existing behavior:

| Policy | Properties | Caller-owned projection |
| --- | --- | --- |
| InventoryPolicy | Nonblank explicit properties, then project/pom/unqualified aliases and parent aliases; group/version fall back to parent | Embedded and multiple-token expansion with existing #1717 byte/token/per-POM budgets; direct and managed inventory membership, imported BOM and managed-version warnings, deduplication and sorting |
| IdentityPolicy | Explicit properties only, including blank values and last duplicate declaration | Whole-value references for at most eight steps; unresolved/cyclic references become empty; managed versions fill only literally blank direct versions; multiple managed versions retain conflicts; source, confidence, PURL and warning formatting stay in analysis |

Reads stay in the consumers through safeio. JVM retains its existing pinned-root traversal, read limit and cancellation/error policy. Analysis retains its snapshot cancellation check before each POM and safeio repository confinement. Decode work is bounded by a 2 MiB input limit; it does not change cancellation checkpoints or introduce external entity/network resolution.

Safety correction: identity POM reads previously used the unlimited `ReadFileUnder` entry point. They now use `ReadFileUnderLimit` at 2 MiB, matching inventory. Oversize input produces no POM identity evidence and the existing warning format: `identity manifest read failed for pom.xml: file exceeds size limit`. Valid inputs at the boundary retain late declarations. The decoder rejects oversize direct input too. No new user configuration, dependency, Gradle limit or traversal behavior is introduced.

The shared fixtures lock actual inventory descriptors/warnings and the actual public identity report JSON before extraction and after migration. Existing expansion, rooted-read and cancellation tests remain in their original suites; property-map unit tests move with their owner to shared. Separate shared tests cover defensive copying, concurrent consumers, empty collections and serialization privacy.

#1743 remains responsible for adapter/cache/live evidence transport, avoiding additional reads/decodes, and merged/cache-hit root adjustments. This change supplies the decoder boundary without claiming those reuse criteria or closing enforcement tracker #1612. Full hooks, independent reviews, Sonar and Codex validation remain delivery-leader gates.
