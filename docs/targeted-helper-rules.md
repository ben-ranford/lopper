# Targeted helper rules

Run `make reuse-check` for both clone and helper checks, or call
`go run ./tools/reusecheck -root .` for helper rules alone.
The checker uses only the Go standard library. It reports a source path/line,
function, contract identifier and canonical helper. Syntax/read errors exit 2;
Every proven copy is reported and exits 1; clean results and unproven semantic
advice exit 0. Historical source, review metadata and issue links do not waive findings.

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
Package-level function variables and function literals in package initializers or
local scopes are checked alongside function declarations. Nested collection
findings retain their enclosing function and source location without repeating
report-mapping findings. Report literals in package variable initializers retain
their containing variable's diagnostic ownership. Each nested closure's diagnostic
identity includes its physical line and column, so sibling closures retain distinct
diagnostics even when they occupy the same display line.
Methods, package initializers, blank bindings and `init` functions also have
distinct identities; ordinary named package functions retain their function scope.
Source-declared function and interface-method result signatures, plus collection
element types, retain statistics provenance, including tuple assignments, nested
indexes and named containers. Sole tuple-valued call arguments retain their
source-proven argument count. Defined pointer types retain the ownership of
their pointee; distinct named statistics value types remain separate from the
shared helper contract. Source-proven implicit and explicit pointer dereferences
retain the same statistics receiver identity.
Files in the same directory and package share declaration bindings, with import
ownership retained from each declaration's source file. Generic functions retain
explicit statistics result types. Instantiated generic aliases retain explicit
alias targets with complete type-argument lists; unresolved type-parameter
results and alias targets remain unknown.
All production source files are scanned, including platform-specific files.
Each source file is analyzed with declarations guaranteed by its build context.
Additional platform and provider contexts preserve complementary declarations,
such as equivalent statistics aliases in Linux and Windows files used by one
consumer. A diagnostic from these contexts requires agreeing support that covers
the consumer's constraints; a violation requires complete blocking support.
Constraint reasoning is conservative and does not enumerate arbitrary tag
combinations: ambiguous alternatives retain unknown name and method bindings,
while provably incompatible files cannot supply declarations or shadows.
Platform proofs use conventional GOOS/GOARCH selection, including Go's platform
aliases; forcing additional reserved platform tags through `-tags` is outside
this model. Other tags remain symbolic and do not inherit the scanner host's values;
joint tag combinations not established by the source predicates remain unproven.
Common files do not borrow ambiguous variant declarations. Findings are emitted
once for each source location, and helper ownership follows physical files even
when `//line` directives rename positions.

The report rule requires all six common statistics fields to select the expected
members of the same local `shared.DependencyStats` value, plus name/language.
Unambiguous promoted members resolve through their source-declared embedded
statistics field; shadowed, competing or unknown embedding paths remain unproven.
Mappings with deliberate overrides or mixed sources remain advisory when four
of six fields have recognized statistics provenance. Unproven calls or channel receives in
the literal also keep a mapping advisory: they may change the statistics between
field reads, so a single helper snapshot is not proven equivalent. Proven type
conversions retain blocking findings while their operands are checked for effects.
Direct converted receivers retain both their target type and stable operand identity.
Stable address-taken and directly type-asserted receivers retain their source type
provenance and identity. Pure unary and binary index expressions retain their
operators, grouping and lexical operands. Stable slice receivers retain their
collection, low/high/max bounds and slice form; distinct expressions remain separate.
Valid helper calls
followed by language-specific changes pass. Discarded/decorative helper calls do not waive a
remaining duplicate mapping or collection implementation. Sorted operations are
not interchangeable with insertion-order, different trimming, non-nil-empty or
input-mutating variants. Private analysis helpers are suggested only in analysis;
shared set-key rules apply only inside language adapters.

Historical migration allowances and exact-source exceptions are not supported.
The default invocation and `-legacy-advisory=false` both report every proven copy
as a violation. The false spelling remains accepted for the protected combined
runner; requesting true is an error (exit 2). The retired `-exceptions` option is
rejected, including empty values and empty documents, before source scanning.

The former five migration sites must adopt their canonical helpers: identity
enrichment, PowerShell union construction and vulnerability strings are owned by
#1724; JVM/PHP report mapping is owned by #1769. Their source migrations are
prerequisites for a clean actual integration, not allowances in this checker.
Tests and testdata remain outside its production scope. Uncertain or non-equivalent
mappings retain the semantic advice described above; they are not proven copies
grandfathered by path or source hash. No metadata can hide a finding.

On the development checkout at `dfe361e`, the helper-only command took approximately
0.5 seconds with a warm Go build cache. The command scans production Go files
outside hidden directories, vendor, node_modules and testdata. Embedded-field
lookups visit each resolved source struct once, preserving ambiguity without
enumerating every embedding path. Positive/negative contract fixtures live in `internal/reusecheck` tests;
CLI fixtures exercise failing source, read errors, rejected allowance requests
and exit statuses.

Refs #1615. The dedicated required GitHub status, merge-group behavior, protected
ruleset configuration, reviewed rollout evidence and batch closure belong to
#1612. This implementation does not close either issue or claim those gates are
enforced on the protected branch.
