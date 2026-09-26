# Workflow contract test reuse

The package-local `readWorkflowConfig` uses the existing path-aware YAML reader;
release contracts share this loading step without sharing their policy decisions.
Forty repeated load blocks now use it. Renovate policy tests remain separate.

Artifact production has one repeated requirement family: named steps must form a
contiguous sequence, not merely appear in ascending order. The shared
`assertWorkflowContiguousSteps` returns indices only after validating every named
step. Diagnostics identify the workflow path, job, and missing or misordered step.
The existing ordinary-order assertion remains available for sequences that allow
intervening steps.

The migration preserves these contracts:

| Existing contract | Shared check | Assertions retained in the original test |
| --- | --- | --- |
| VSIX release artifact staging | sync → reset → package → validate → upload, contiguous | Shell/environment hardening, deletion before mkdir, explicit package command, exact file/symlink/size validation, no repository commands after reset, upload name/path/missing-file policy |
| Stable Darwin, orchestrated Linux/Windows and Darwin, rolling Darwin archives | reset → build → validate → upload, contiguous, with existing named table cases | Shell/environment hardening, deletion before mkdir, per-platform expected runtime paths, exact upload lists, no globs, missing-file failure |

Characterization tests ran against the existing workflows before editing. New
small positive and negative fixtures cover contiguous order, missing steps,
intervening gaps, reversed steps, and repeated names. These are checks of the
sequence helper, not replacements for the original trust-boundary assertions.
Credential-role, checkout, shell execution, and artifact publication tests remain
independent and retain their full assertions.

Physical source counts (including blank lines/comments):
`release_workflow_config_test.go` decreases from 7,366 to 7,350 lines. The new
36-line regression file makes the combined delta +20 lines: repeated setup was
removed while adding independent rejection evidence, rather than weakening tests
to meet a size target. No test moves to a different repository or package.

#1617 remains open until #1612 records the protected-branch rollout evidence.
Shared writer, command, and repository fixture ownership stays with #1618.
