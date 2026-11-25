# Safe I/O read bounds

Parser boundaries that consume repository, config, or cache input declare an
explicit byte limit. `safeio.ReadFile`, `safeio.ReadFileUnder`, and
`safeio.ReadFileWithinRoot` pass a zero limit and therefore read with unbounded
`io.ReadAll`. A zero or negative constant passed to `ReadFileLimit`,
`ReadFileUnderLimit`, or `ReadFileWithinRootLimit` has the same effect.

`make safeio-read-check` parses production Go files and fails unless every such
read is listed in [`.github/safeio-zero-limit-allowlist.txt`](../.github/safeio-zero-limit-allowlist.txt).
The check is part of `make ci` and `make ci-checks`. Test files are not
production parser boundaries. Files under `vendor`, `node_modules`, `.git`,
`bin`, `dist`, `.artifacts`, `.omx`, and `.lopper-cache` are skipped.

Each allowlist row is one callsite:

```text
path<TAB>function<TAB>callee<TAB>index<TAB>rationale
```

`index` is the 1-based source order of that callee inside the function. The
rationale has to say why that particular input may still be read without a
positive limit. Rows that do not match a callsite fail, so deleting or bounding
a read includes deleting its row. `make safeio-read-check -list` prints the
current zero-limit reads in the same column order.

New parser code uses a positive, ecosystem-specific limit and keeps the existing
root and symlink checks:

- `safeio.ReadFileUnderLimit` for a path that must stay inside a repository root
- `safeio.ReadFileWithinRootLimit` when the caller already holds that root
- `safeio.ReadFileLimit` for an exact path that is not a repository walk

Oversize input returns `safeio.ErrFileTooLarge`. Missing files stay
`os.ErrNotExist`. Symlinks stay `safeio.ErrTargetPathSymlink`. Callers that
surface diagnostics keep parse failures distinct from those read failures.
A single global limit is intentionally not the default: a Swift package
manifest, a lockfile, and a source file do not share one budget.
