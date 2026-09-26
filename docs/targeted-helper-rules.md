# Targeted helper rules

Run `make reuse-check` for both clone and helper checks, or call
`go run ./tools/reusecheck -root .` for helper rules alone.
The checker uses only the Go standard library. It reports a source path/line,
function, contract identifier and canonical helper. Syntax/read errors exit 2;
unapproved proven copies exit 1; clean/advisory-only results exit 0.

The reviewed contracts are exact-string sorted uniqueness owned by
`analysis.uniqueSorted`, trimmed sorted uniqueness owned by
`report.SortedUniqueTrimmedStrings`, language string-set keys owned by
`shared.SortedKeys`, dependency-set unions owned by
`shared.SortedDependencyUnion`, and common report mapping owned by
`shared.BuildDependencyReportFromStats`.

Collection templates retain statement order, constants, nil/empty results,
trimming and mutation. Go ASTs and `go/types` lexical bindings normalize local
renaming; import paths normalize aliases. This is deliberately not a general
semantic equivalence or similarity engine. External packages are not loaded or
executed; source-declared `DependencyStats` provenance and imported factory calls
are checked conservatively. Ordinary compilation remains responsible for full
package type validation. Unknown forms are not classified as proven copies.

The report rule requires all six common statistics fields to select the expected
members of the same local `shared.DependencyStats` value, plus name/language.
Mappings with deliberate overrides or mixed sources remain advisory when four
of six fields have recognized statistics provenance. Valid helper calls followed by
language-specific changes pass. Discarded/decorative helper calls do not waive a
remaining duplicate mapping or collection implementation. Sorted operations are
not interchangeable with insertion-order, different trimming, non-nil-empty or
input-mutating variants. Private analysis helpers are suggested only in analysis;
shared set-key rules apply only inside language adapters.

Existing migration sites are narrowly listed in `LegacyAdvisory`, frozen to whole
source digests in `legacyDigests`, while #1613 and
#1614 are outstanding. This includes pre-existing JVM/PHP report mappings found
by the scanner, without migrating those adapters here. New path/function copies and edited legacy source
are blocking unless separately reviewed. `-legacy-advisory=false` makes legacy findings blocking too. Remove
the migration entries as the owners adopt the helpers. The frozen catalog covers
11 sites; this base currently emits 10 advisories (the Go reporter does not match).
#1612 must remove these allowances or record reviewed exceptions before enforcing
the completed #1613/#1614 rollout. Tests and testdata are
excluded from production enforcement; test duplication remains advisory.

For a reviewed exception, pass `-exceptions path/to/exceptions.json`. The file is
an array of objects with `path`, `function`, `rule`, `sha256`, `reason` and `issue`.
The SHA-256 is of the complete source file; any edit invalidates the approval.
The issue must link to a repository issue documenting why the contract must
remain separate. Paths must be relative Go source paths, rules must name a supported contract,
and issue links must identify a positive issue number in this repository. Unknown
fields, null arrays, malformed entries and duplicate entries fail closed. Review both the exception and its issue in the same PR. There are no
inline suppressions or directory-wide exceptions.

On the development checkout at `dfe361e`, the helper-only command took approximately
0.5 seconds with a warm Go build cache. Cost is linear in parsed source size and
scans production Go files outside hidden directories, vendor, node_modules and
testdata. Positive/negative contract fixtures live in `internal/reusecheck` tests;
CLI fixtures exercise failing source, read errors, exceptions and exit statuses.

Refs #1615. The dedicated required GitHub status, merge-group behavior, protected
ruleset configuration, reviewed rollout evidence and batch closure belong to
#1612. This implementation does not close either issue or claim those gates are
enforced on the protected branch.
