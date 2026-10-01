# Temporary v1.8.9 Windows diagnostics

This branch is for manual investigation only and must not be merged or released.
Production source files and the existing 400-execution runtime job are unchanged.

Dispatch existing `windows-runtime.yml` on this branch with
`historical_marker_diagnostic=false` and `preflight_termination_diagnostic=true`.
The historical investigation is retained unchanged and is not requested for this run.
Each supplemental job has read-only repository permissions, pinned actions and
checkouts without persisted credentials. No repository secret is requested.

The historical job applies a hash-pinned observer patch to original tested merge
41680ca9548f72e351c8ed88e98cc946347f45d4 in its own checkout. The JSON transport
decodes to the exact reviewed UTF-8 patch before the hash check and application.
Twenty separate test
processes preserve the original three-test selection and ten-second marker
assertion; each process has a thirty-second limit and the job has a fifteen-minute
limit. Any failure keeps the job failed while all attempt evidence is retained.
Pre-cancel process observations distinguish startup failures from cleanup results.
Instrumentation changes output handles and can affect scheduling; a passing batch
or ResumeThread observation alone does not establish the historical cause.

The preflight job uses the exact helper Git blob from repair
669d29704c2c21512b99ad3657568486480596e4, packaged under `.diagnostics/preflight/`.
It extracts helper/probe bytes from the dispatched Git commit and verifies both
SHA-256 hashes before execution, independently of Windows checkout line endings.
It tests TERM-spawned and TERM-ignored descendants, normal exit status 7 and exact
output, direct parent interruption, literal arguments, startup-hook poisoning,
removal of temporary state and survival of an unrelated sentinel.
It has a five-minute job limit and per-case guards sending TERM at eighteen seconds
and KILL to the same owned wrapper at twenty-one seconds if necessary.

These are supplemental investigations. They do not replace final-head PR checks,
Codex review, current-main Sonar/suppression audits or release admission.
