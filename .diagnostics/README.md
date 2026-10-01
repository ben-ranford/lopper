# Temporary native preflight repeat — draft

Do not merge, release, or dispatch until root approves this exact diagnostic commit.
Source helper: `39a17b24e6d5b8764a0b8c3f9ce9d7e8f0f2d475:scripts/hook-config-preflight.sh`, Git blob `9a2b444577ca1331cae3214f058ac0d0490cd14f`.
Packaged bytes and probe are read from immutable diagnostic Git objects, then checked
against SHA-256 and helper Git-blob pins before execution. Manifest bytes are also pinned.

Four original lifecycle cases are retained. Absent configuration status1/empty stderr
and malformed configuration status128/byte-exact real stderr each run five times,
using the same real Git binary, paths and environment for direct/wrapped results.
Both direct and wrapped commands are bounded. Owned reader/descendant/anchor cleanup,
empty state, unrelated sentinel, literal argv and poisoned BASH_ENV assertions remain.
Raw test streams and retired PID/anchor records are retained under `cases/`, including on failure.
An independent observer discovers actual ps PID/PPID/PGID headers, proves reader and
anchor group ownership and unrelated-process exclusion, and records negative-group
signal0 results. Windows evidence is MSYS/Cygwin POSIX group evidence, not Windows
kernel PGIDs. Missing or unsupported native ps semantics fail closed.

Six separate startup controls use explicitly retained instrumented helper variants:
known warning accepted; unknown warning rejected; missing group rejected; known
warning without a group rejected; pre-permit interruption; and exited child.
They preserve exact error streams, reject reader startup where required, and retain
source diffs, process records, stopped anchors, empty state and a live sentinel.
All14 exact-helper cases and all6 startup controls are mandatory and counted separately.

Existing runtime and historical jobs/assets remain at diagnostic base `b8cea55ecabd9f313f5372400020b8a7183c8e02`;
their execution is not proof for the final production PR. Dispatch preflight=true and
historical=false only after review. Existing pinned actions, contents:read permissions,
persist-credentials:false and five-minute supplemental job limit remain unchanged.

Any later helper change requires fresh source binding and appropriate review before dispatch. Never
reuse historical helper run36818780581 as evidence for changed helper bytes.
