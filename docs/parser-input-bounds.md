# Parser input bounds

Use a byte or streaming work limit before parsing repository files, configuration, and cache data. Use `safeio.ReadRegularFileUnderLimit` for a file in a known repository or cache root. Use `safeio.ReadRegularFileLimit` for an explicitly named regular file. Share the limit when two callers parse the same format.

The bounded readers preserve root confinement and reject unsuitable file types. Parsers can distinguish a missing file, a file that exceeds the limit, an invalid file type, and a syntax error. Follow the existing caller contract for optional inputs. Report missing coverage or invalidate derived data when an optional read cannot be completed.

Choose limits for the input, such as a small cache pointer, a dependency manifest, a large lockfile, or a source file. Reuse an existing limit when it covers the same format and purpose. A limit for one ecosystem is not a default for others.

## Compatibility exception

`internal/baseline/snapshot.go` allows an explicitly selected baseline snapshot to exceed the indexed store's snapshot limit. This preserves the existing explicit file-load contract for user-selected report artifacts. The baseline store continues to enforce its own limit. See `TestDashboardLoadWithKeyPreservesOversizedExplicitBaselineCompatibility`.
