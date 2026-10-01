# Temporary v1.8.9 Windows diagnostics

This branch is for manual investigation only and must not be merged or released.
Production source files and the existing 400-execution runtime job are unchanged.

Dispatch existing `windows-runtime.yml` on this branch with both
`historical_marker_diagnostic=true` and `preflight_termination_diagnostic=true`.
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

The preflight job uses exact repair 545770812c52b99789c7d486f9b53f73c17ab122 and
verifies helper/probe hashes before execution. It tests TERM-spawned and TERM-ignored
descendants, normal exit status 7 and exact output, literal arguments, startup-hook
poisoning, removal of temporary state and survival of an unrelated sentinel.
It has a five-minute job limit and per-case guards sending TERM at eighteen seconds
and KILL to the same owned wrapper at twenty-one seconds if necessary.

These are supplemental investigations. They do not replace final-head PR checks,
Codex review, current-main Sonar/suppression audits or release admission.
