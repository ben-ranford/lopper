# Python manifest evidence reuse

With `dependency-identity-preview` enabled, the Python adapter retains one
analysis-local catalog of decoded `pyproject.toml`, `Pipfile`, `poetry.lock`,
`uv.lock`, `Pipfile.lock`, and `requirements.txt` documents. Inventory and identity
remain separate projections: optional pins can enrich an imported dependency
without adding optional-only packages to the dependency inventory. Locks are
captured even when inventory has manifest declarations and needs no fallback.

The catalog uses the existing confined readers and per-file limits (16 MiB for
TOML manifests; 1 MiB for locks and requirements). Retained input is additionally
limited to 64 MiB per adapter analysis. Documents, including read/decode failures,
are reused. Once full-document retention is exhausted, the catalog retains only
the fields needed for identity enrichment, within a separate aggregate 64 MiB
limit on serialized projections. Inventory still consumes each initial decoded
document, while identity and cache hits consume its retained projection without
reopening the source. Projection-limit or serialization failures produce an
explicit identity warning and no package-specific evidence from that document;
they do not turn successful inventory parsing into a parse failure. These limits
measure retained input and serialized projection bytes, not total heap usage.

Older cached path markers without a projection retain the compatibility fallback
through the same confined, bounded reader. Public report JSON does not expose the
catalog. Cache schema v8 stores it as an internal sidecar. Paths are relative to
the analysis root, including nested adapters and scoped repositories.

Remaining identity discovery uses the shared cancellable walker with a maximum
of 100,000 files per traversal. Reaching the cap emits a deterministic truncation
warning. Request cancellation propagates through final identity enrichment.
Adapters without a Python catalog retain the existing discovery fallback.

Maven parsed-model and property-resolution unification is tracked separately in
[#1743](https://github.com/ben-ranford/lopper/issues/1743) for v1.8.10. This change
references [#1327](https://github.com/ben-ranford/lopper/issues/1327); its shared
rollout and enforcement evidence remains tracked by
[#1612](https://github.com/ben-ranford/lopper/issues/1612).
