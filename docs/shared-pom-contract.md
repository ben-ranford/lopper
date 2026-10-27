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

The decoder boundary is supplied by #1730; #1743 adds the evidence transport described below. Enforcement tracker #1612 remains separately tracked.

## Maven evidence reuse and resource limits

For #1743, JVM inventory retains an immutable identity-policy projection from the same bounded POM read and decode. Identity enrichment uses that captured observation, including read/parse failures, even if the source later disappears or changes. Inventory alias and embedded-token expansion stays separate from identity's whole-value, eight-step resolution. Eligible uncaptured POMs still follow identity's existing discovery and read rules. Captured paths are rebased against the current validated root, sorted and deduplicated; conflicting observations fail explicitly.

Retained evidence is limited to 64MiB of canonical v1 envelope bytes, 2048 entries per adapter, 65536 across roots, 131072 properties or dependency declarations per record, and 1048576 aggregate properties/declarations. These count and serialized-size metrics are not Go heap limits. Overflow is a recognizable fatal analysis error in JVM, auto and all modes, before the overflowing root can publish a cache entry. Existing Maven expansion/read/work limits remain unchanged.

JVM cache hits require a valid private v1 envelope, including an explicit empty catalog. Pointer reads are capped at 4KiB and object reads at 128MiB. Object bytes must match their digest; strict private structure, counts, encoding and host-relative paths are validated before restoring immutable evidence. Cached roots cannot override the current validated root. Legacy or invalid envelopes miss and may be recomputed; accepted evidence adds no POM source read or XML decode after ordinary input fingerprint validation. Digest binding detects corruption, not an authenticated cache producer or an atomic filesystem snapshot.

JVM publication admits at most 128MiB of compact JSON after the existing serializer finishes, before that store attempt hashes or opens its write root. An oversized object or ordinary serialization error skips the whole cache root with a warning and retains complete live analysis. This cap does not bound ordinary Report or Maven-copy allocations, encoder buffers, peak heap or concurrent requests; allocation exhaustion remains possible and is not a recoverable cache warning. Read and live-evidence limits are distinct from publication admission. Non-JVM cache behavior and the cache key version remain unchanged.

Unix filename bytes remain intact in live evidence. Paths that cannot round-trip the private JSON cache format cause a whole-root cache warning/skip, never dropped or sanitized live evidence. Internal catalogs are discarded after finalization, including disabled-preview, empty, cancellation and error paths; they do not enter public JSON, CSV or SPDX. Refs #1327 and #1612 remain rollout links rather than automatic closures.
