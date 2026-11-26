# Safe I/O read bounds

Production parser boundaries use explicit positive byte limits. `make safeio-read-check` parses production Go files and rejects references to `safeio.ReadFile`, `safeio.ReadFileUnder`, and `safeio.ReadFileWithinRoot` unless allowlisted. Calls to their legacy `Limit` variants require a statically known positive limit. Use the regular-file readers for limits chosen at runtime.

The only temporary exceptions are recorded in [the allowlist](../.github/safeio-zero-limit-allowlist.txt) with one rationale per callsite. Delete an entry in the same change that gives its reader a positive, domain-specific limit. The check excludes tests, the root vendor directory, and known repository output directories.

Use `safeio.ReadRegularFileUnderLimit` for repository-confined paths, `safeio.ReadRegularFileWithinRootLimit` for pinned roots, and `safeio.ReadRegularFileLimit` for exact paths. These functions require a positive byte limit and a regular file. They return `safeio.ErrFileTooLarge` for oversized input.
