# Repository size and test duplication

Run `make --silent source-size-report DUPLICATION_BASE=origin/main` for JSON on
stdout. CI uploads `source-size-report.json`, comparing the PR's exact base commit
to the checked-out revision (normally GitHub's merge commit). Both resolved SHAs
are recorded. Local changes and untracked files are intentionally excluded; commit
changes before comparing. Missing revisions and analyzer failures fail reporting;
the number of findings or lines does not impose a budget. Analyzer stderr diagnostics
fail reporting even if dupl exits successfully; tool-installation output is separate.

Each tracked blob belongs to exactly one category, in precedence order: lockfiles,
generated/dependency/build assets, fixtures, tests (including `testutil` and
`testsupport` support packages), production source, then other
configuration/docs/assets. See `scripts/source_size_report.py` for the shared
classification function intended for the production clone ratchet in #1611.
Generated Go headers and conventional dependency/output directories are recognized;
custom generated directories/markers must be added explicitly. All files remain
visible, including unknown extensions (in configuration/docs). Source counts use an
explicit extension allowlist and shebang detection, including TypeScript/JavaScript in the VS Code
extension, Go, shell, Python, and other supported language families.

Counts are physical LF-delimited lines (including an unterminated final line) and
nonblank lines, not executable statements. Blank means ASCII whitespace only.
Comments count. Binary blobs containing NUL and symlinks count as files but have no
line counts; links are never followed. Submodule entries are counted separately;
their contents are not traversed. No language parser is used for line counts.
Signed deltas cover every metric and category; source-file counts exclude binary
and symlink entries. Tests stay in their packages: Go's `_test.go` files do not ship
in ordinary builds. Moving tests elsewhere or weakening coverage is not a remedy
for growth.

Test duplication is a separate, nonblocking head-only report using the existing
pinned Go dupl tool and token threshold. It does not change production enforcement;
clone analysis is Go-only even though size reporting includes non-Go sources.
Findings distinguish nearest test/helper declarations heuristically: Test,
Benchmark, Fuzz, and Example declarations are behavioral cases; other declarations
are helper candidates. Multiline declarations may remain unclassified; these labels
are triage hints, not AST proof of safe extraction. dupl ignores values and cannot
prove semantic equivalence. Review repeated setup for package-owned helpers with a
clear purpose; retain distinct assertions, error semantics, and regression cases.
Cross-package helpers need an explicit shared owner and proven reuse, not an
abstraction added solely to reduce counts. The JSON preserves locations for review.

This reporting issue remains open after merge until #1612 records the protected
branch rollout evidence. Test findings intentionally remain advisory.
