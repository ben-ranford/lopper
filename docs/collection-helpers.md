# Collection helper contracts

Choose by observable behavior, not by similar names. These contracts are the
canonical targets for the targeted helper rules tracked in #1615. No helper
below changes its inputs or returns an error. Ordering is Go string lexicographic
order; case and Unicode spelling are preserved.

| Owner / helper | Intended callers | Trimming / empty strings | Empty result | Input ownership |
| --- | --- | --- | --- | --- |
| `analysis.uniqueSorted` (`pipeline.go`) | Analysis roots, provenance, evidence and diagnostics | Exact strings; preserves whitespace and empty strings | Nil for nil/empty input | Copies before sorting; result never aliases input |
| `report.SortedUniqueTrimmedStrings` (`strings.go`) | Report evidence, aliases, affected versions and diagnostics; app codemod reasons and review warnings | `strings.TrimSpace`, discard blanks, deduplicate after trimming | Nil for nil/empty input; non-nil empty for nonempty all-blank input | Allocates output; never sorts input |
| `shared.SortedDependencyUnion` (`lang/shared/dependency_usage.go`) | Python, Ruby and PowerShell declared/imported dependency sets | Exact map keys; preserves whitespace and empty strings | Nil when all sets are empty, including no sets and nil sets | Builds its own union; never writes input maps |
| `shared.SortedKeys` (`lang/shared/dependency_usage.go`) | A single language-domain string set | Exact map keys | Nil for nil/empty map | Allocates output |
| `kotlinandroid.sortedUniqueTrimmedStringsNonNil` (`gradle_lookup_index.go`) | Gradle lookup ambiguity candidates | Trim and discard blanks | Always non-nil, including nil input | Allocates output |

The Kotlin lookup variant deliberately retains its non-nil empty-slice contract.
It cannot be substituted with report normalization without changing that behavior.
License deny-list normalization, insertion-order dedupe helpers and recommendation
ranking are separate domain operations; recommendation normalization belongs to
#1324. Do not replace those helpers based on their names alone.

## Consolidation evidence

The scoped clusters started with eight implementations: two analysis sorted-unique
helpers, app/report trimmed helpers, three language dependency unions, and the
Kotlin trimmed variant. They now have four implementations: one per row above
excluding the pre-existing `SortedKeys` primitive. Removed definitions are
`analysis.sortedUnique`, `app.uniqueSortedStrings`, and the Python, Ruby and
PowerShell `sortedDependencyUnion` copies; report normalization was renamed and
moved, and one shared union was added. This is a net reduction of four definitions.

Characterization tests passed against all eight original implementations before
migration, covering nil/empty inputs, whitespace, duplicate/order behavior and
input immutability. The surviving contract tests live beside each canonical owner;
existing app and adapter tests exercise migrated callers. The app already depends
on report, and adapters already depend on lang/shared: no package edge or external
dependency was introduced, and report does not depend on language adapters.

Implementation does not close #1613. Keep it open until #1612 supplies the exact
revision, protected-target ruleset and blocked/passing examples proving the rollout
gates are enforced. Test-duplication reporting remains advisory.
