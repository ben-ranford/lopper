# Safe I/O read bounds

Production parser boundaries use explicit positive byte limits. `make safeio-read-check` parses production Go files and rejects zero-limit `safeio.ReadFile`, `safeio.ReadFileUnder`, and `safeio.ReadFileWithinRoot` calls, plus non-positive constant limits passed to their bounded variants.

The only temporary exceptions are recorded in [the allowlist](../.github/safeio-zero-limit-allowlist.txt) with one rationale per callsite. Delete an entry in the same change that gives its reader a positive, domain-specific limit. The check excludes tests and generated or vendored directories.

Use `safeio.ReadRegularFileUnderLimit` for repository-confined paths, `safeio.ReadRegularFileWithinRootLimit` for pinned roots, and `safeio.ReadRegularFileLimit` for exact paths. These functions require a positive byte limit and a regular file. They return `safeio.ErrFileTooLarge` for oversized input.
