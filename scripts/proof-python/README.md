# Private Windows proof Python provider (O source)

This source prepares the dependency for the separate Windows installer proof
route. It does not enable a workflow, invoke setup-python, install Python or
qualify a runner. The shipping workflows and regressionproof sanitizer are
unchanged. A later, separately reviewed W change must pin a real O commit.

The accepted candidate is setup-python v7 at
`5fda3b95a4ea91299a34e894583c3862153e4b97`, ordinary CPython 3.13.16 x64,
`update-environment: false`. Before that action, trusted W must prove the
completed interpreter cache is present under the canonical official tool cache
and that `AGENT_TOOLSDIRECTORY` does not select another root. A miss stops before
the action. This provider's `cache` operation is read-only. No source command
invokes the action or a cache-miss installer. Initial cache provenance is the
trusted hosted image; the subsequent byte inventory is not an upstream installed
file catalog or an SBOM claim.

## Trusted source and expectation flow

W authenticates the literal O archive and `provision.ps1` before any subject Make
or native code. It supplies the previously authenticated absolute Go executable,
its digest and canonical cache roots. Cached dependencies must already exist;
provider builds use the existing Go module and `GOPROXY=off`. The two PE aliases
embed only the absolute private interpreter path. They prepend `-I -S -B` and
preserve original arguments, cwd, streams, SERIES and child status. Both aliases
are required: actual 583 floating-version resolution calls python3 before OS
routing, while the Windows candidate also has the python fallback.

The bootstrap builds adapters before constructing the receipt. The receipt
contains the entire private inventory, both adapter identities, finite System32
module identities and the nonce/creation-bound held-probe observation. The
bootstrap returns its own, verifier, receipt, alias and Go digests. W retains
these as independently addressed trusted step outputs, never mutable
`GITHUB_ENV` defaults or values selected by a receipt. This ordering has no
receipt/adapter self-hash cycle.

Before invoking `verify.ps1`, W's trusted inline code must authenticate that
script's bytes against its independently retained digest. Its explicit arguments
come from those trusted outputs. The script authenticates the verifier before
execution and the receipt before parsing. The private `joined-command` admits
only the installer test package or the existing formal regressionproof command,
using captured absolute Go, fixed Go settings and the existing restricted
Windows system/tool PATH. Existing Go/Node authentication remains required in W.
No Python expectation is forwarded through regressionproof's fresh sanitizer.

The existing runtime owner starts the outer command in its Windows job. Exactly
one Wait is followed by checked job cleanup. Command and join errors remain
separate; an unknown/failed join forbids post-state certification and deletion.
Only a joined command reaches full inventory/probe postchecking. The scripts
perform no automatic provider deletion. W may clean up the owned paths only
following successful joined verification and its own inline post-authentication.
A transient substitution during a command that is restored before postchecking
is outside this boundary; this is not per-Python verification or hostile-OS
protection.

## Bounds and native observation

`tools/proofpython/policy.go` fixes the reviewed ceilings: 65,536 entries,
8,192 directories, 57,343 files,512 MiB/file,4 GiB logical inventory/copy bytes,
depth 32,255 UTF-16 units/component,2,048 units/path,32 MiB metadata/receipt,
8 KiB tokens,64 KiB I/O,512 modules,256 OS DLLs with64 MiB/file and512 MiB combined.
Limits do not grow to fit a runner. File bytes use recorded-size reads and one
extra probe; case collisions, unexpected links, special files, replacement,
growth and extras fail. Only the source python3.exe link to the same cached
python.exe is admitted, and it becomes a regular private copy. Private files are
bound to byte digest, size, mode and opened file identity.

The fixed native x64 observer checks PE headers and IsWow64Process2. Its owned
Python child imports the fixed standard-library workload, emits one READY nonce
and waits for RELEASE. The observer retains the process identity, takes exactly
two bounded module snapshots, checks canonical opened files and admits only
private inventory members or regular DLLs directly in canonical native System32.
It neither scans arbitrary processes nor requests debug privileges. The existing
process object's retained handle prevents PID reuse before Wait; Microsoft
[documents that process identifiers remain assigned until the object is freed](https://learn.microsoft.com/windows/win32/api/processthreadsapi/ns-processthreadsapi-process_information).
Module handles are borrowed; owned handles are closed with errors retained.

The probe has an inclusive 20-second context, within any shorter parent deadline.
A blocking OS operation is not claimed interruptible merely because a deadline
exists. Failure remains failure until the owned process/reader/job is joined;
late completion cannot become timely success. Requested0700 cwd permissions are
not claimed to prove a Windows ACL. Native ownership/access checks remain part
of runner qualification. Executable-module snapshots exclude datafile mappings
and do not predict every later load.

## Required qualifications before W admission

Portable tests and cross-compilation do not establish native Windows operation.
Still required: the actual cached catalog fitting every fixed ceiling; same-
bitness API access and native private-cwd/handle lifecycle; source-link layout
and relocation; both Git Bash alias discoveries; exact flags/args/binary streams/
status; genuine583 compile and intended UnsupportedOS behaviour followed by
candidate success; ordinary/race/formal wrapper results with existing Go/Node
custody; joint substitution and cancellation controls; actual 20-minute route
fit and scoped cleanup. Any excess or permission/API uncertainty blocks
admission. No raw direct-interpreter alias or .cmd/PATH workaround is admitted.
The 1753 SMB/owner route remains a separate issue and authority boundary.
