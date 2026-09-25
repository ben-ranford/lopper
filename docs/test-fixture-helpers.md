# Test fixture helper contracts

Prefer `internal/testutil` for equivalent setup, while keeping behavioral assertions
in the package that owns the regression. Helpers do not justify moving tests out of
the repository or weakening coverage.

| Helper | Contract and intended callers |
| --- | --- |
| `MustWriteFile` | Creates parents with requested mode 0750 and files with requested mode 0600. |
| `MustWriteFileMode` | As above, with an explicit file mode, including executable fixtures. |
| `MustWriteFileWithModes` | Explicit file and directory modes for existing CLI/regression fixtures that require 0755 parents. Modes follow the process umask; existing permissions are not changed. Overwrites file contents and fails the test on mkdir/write errors. |
| `RunGit` / `GitOutput` | Use the resolved Git executable and sanitized Git environment, scoped with `-C`. Failure includes combined output. `GitOutput` trims surrounding whitespace; do not adopt it for tests asserting raw command output or deliberately expected errors. |
| `WriteLodashMapFixture` | Writes the minimal lodash ES-module map export tree, preserving supplied source bytes, 0644 files and 0755 directories. Returns index.js; does not initialize Git or perform behavioral assertions. Shared by CLI, JS adapter, and MCP codemod tests. |

The #1618 audit removes the CLI `writeFile` copy, the regression-proof
`runRepoCommand` dispatcher, and the scripts command runner's redundant Git branch.
Regression-proof file maps retain their package-local grouping but use the canonical
writer. Repeated lodash setup and metadata converge on one fixture definition.

Intentional variants remain:

- The scripts writer uses a temporary file plus rename to preserve atomic replacement
  and executable fixture behavior; replacing it with os.WriteFile is not equivalent.
- The application `writeTextFile` requires parents to exist; it is useful when setup
  and failure boundaries matter. App lockfile writers retain their established 0755
  parent/0600 file contract; migrating all their unrelated security fixtures is not
  required to share the codemod tree.
- App lockfile Git runners can use injected executables and production environment
  logic; they are not interchangeable with testutil's resolved-binary runner.
- Python CLI fixtures exercise CRLF imports and optionally Git; MCP fixtures use LF
  and a requests requirements file. Their package-specific construction remains
  explicit, with the CLI source writer using the existing canonical primitive.
- Generic scripts command execution preserves working-directory, raw combined-output,
  and GITHUB_EVENT_NAME filtering behavior. Only Git calls use the Git-specific helper.
- Error-returning adapter writers, symlink fixtures, permission-denied setup, and
  workflow-specific assertion models retain their own contracts.

This is setup reuse, not proof that superficially similar behavioral cases should
be merged. #1618 remains open pending the protected-branch rollout evidence in #1612.

Audit size evidence (physical lines, including blanks/comments): the changed Go
source set totals 6,154 lines before and 6,149 after, including the new shared
fixture and permission/content contract tests. This measures the final refactor,
not a mandatory size budget; retaining distinct regression assertions matters more
than maximizing deleted lines.
