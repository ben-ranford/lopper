# Stave TUI preview

`stave-tui-preview` is an opt-in proving client for the Stave terminal UI
runtime. It renders the existing Lopper summary report through a typed Stave
program while keeping summary analysis, command parsing, and consequential
side effects in Lopper. The legacy summary TUI remains the default and is the
rollback path.

## Enable the preview

The flag is preview-only and explicit-only. It is disabled unless both the
feature flag and the command option are present:

```sh
lopper tui --repo . --enable-feature stave-tui-preview
```

To render one deterministic frame without entering the terminal session:

```sh
lopper tui --repo . --enable-feature stave-tui-preview --snapshot -
lopper tui --repo . --enable-feature stave-tui-preview --snapshot preview.txt
```

Omitting `--enable-feature stave-tui-preview`, or disabling the feature,
selects the existing Summary implementation. No report, baseline, or action
state is shared with a previous preview process.

The preview uses `github.com/ben-ranford/stave v1.0.0-rc.1`. This proving client
is deliberately Lopper-owned; it does not require a local Stave checkout or a
module replacement.

## Terminal interaction

On a colored, cursor-capable TTY, the preview uses Stave's interactive session
and restores the terminal on quit, interrupt, cancellation, or an error. A
pipe, redirected output, `TERM=dumb`, `NO_COLOR`, or EOF selects the
plain/line-compatible path and emits no cursor-control screen updates.
The line path still detects PTY width changes and sends a typed resize event
before the next frame, so responsive layout is not limited to alternate-screen
mode.

| Input | Effect |
| --- | --- |
| `↑`/`k`, `↓`/`j` | Move the selected dependency |
| `←`/`p`/`prev`, `→`/`n`/`next` | Move between pages |
| `Tab` | Move focus between summary and an opened detail pane |
| `/` | Start a filter command; type the command and press Enter |
| `Enter` | Commit the command or open the selected item where supported |
| `?`/`h` | Toggle help text |
| `:` | Start a command directly |
| `q`, `Esc`, `Ctrl-C` | Quit and restore the terminal |

The existing summary command grammar remains available at the command prompt:
`filter [text]`, `sort name|alpha|waste`, `page N`, `size N`, `open DEPENDENCY`,
`refresh`, `apply-codemod [DEPENDENCY] --confirm [--allow-dirty]`,
`save-baseline`, and `compare-baseline`. Baseline commands accept the same
`--store`, `--key`, `--label`, and `--file` options as the Summary TUI. An
unconfirmed codemod is rejected; a confirmed codemod is issued through the
typed Stave action registry and Lopper's existing action runner.

## Capability and safety behavior

The renderer negotiates truecolor, ANSI 256-color, ANSI 16-color, monochrome,
plain, Unicode, and ASCII profiles. `NO_COLOR=1`, `TERM=dumb`,
`LOPPER_TUI_WIDTH=<n>`, `--snapshot`, and narrow terminals exercise degraded
profiles. Widths below 40 use ASCII separators and glyphs. Report text is
sanitized before it is displayed; dependency names and warnings cannot inject
terminal controls or trigger actions.

The Stave model owns interaction state (selection, focus, command buffer,
help, status, errors, viewport, and quit state). Lopper's Summary remains the
domain authority for report data and action effects. The view is deterministic
for a given model, report, viewport, and negotiated capability profile.

Interactive actions are asynchronous. The adapter first publishes a typed
`ActionInvoked` event with a call ID, runs the registered handler on a
cancellable context, and then publishes the matching `EffectResult`. The
reducer therefore represents pending work separately from its eventual typed
outcome. Session snapshots clone value-owned report, options, and interaction
state; callers cannot mutate a prior revision through a retained pointer.
Action output schemas are strict: the versioned result object must contain the
declared action-specific fields and rejects undeclared properties.

Rejected keyboard, paste, and command input is reported as a typed
`Diagnostic` (`LOPPER_INPUT_REJECTED`) and rendered as inert, sanitized text.
It does not become an action result or mutate domain state.

### Cancellation semantics

SIGINT, SIGTERM, Ctrl-C, and explicit cancellation request cancellation of an
in-flight action and restore the terminal. The client-observed status is
intentionally **indeterminate**: cancellation was requested, but the final
domain side-effect outcome is unknown unless the domain authority later
returns an authoritative result. This status must not be read as proof that a
consequential operation rolled back, nor as proof that it committed. The UI
does not claim success or retry automatically from that state.

## Coverage contract

New UI changes must add evidence at the layer they affect:

1. Reducer sequence tests prove navigation, filtering, paging, focus, help,
   resize, cancellation, and quit transitions.
2. Semantic/action tests prove labels, roles, selected/error/destructive text
   cues, action schemas, confirmation, and replayable event contracts.
3. Deterministic profile goldens cover truecolor, ANSI 256, ANSI 16,
   monochrome, plain, ASCII, Unicode, narrow, and hostile text.
4. PTY/E2E tests cover startup, keyboard input, full-screen and line-mode
   resize, interrupt/cancel, terminal restoration, non-TTY fallback, and the
   default-off legacy path. A helper subprocess blocks inside a real refresh
   action, synchronizes through an out-of-band marker pipe, receives real
   SIGINT and SIGTERM, and proves cancellation observation plus exactly one
   alternate-screen leave/cursor restore with no post-restore repaint.
5. Parity/security tests keep rows, ordering, counts, paging, warnings, action
   support, and terminal sanitization aligned with Summary behavior.

`make stave-ui-check` runs the focused reducer, semantic, golden, parity, and
PTY/E2E suites. It is part of `make smoke`; repository CI additionally keeps
the existing full `test`, race, leak, 98% total/package coverage, lint, and
static-analysis gates. The completed spike passes those repository-wide gates:
98.2% total coverage, every measured package at or above 98%, and 98.0% for
`internal/ui`. Focused tests are intentionally bounded and do not replace the
repository-wide gates.

The preview is still a proving spike. Automated evidence now covers async
action lifecycle, value isolation, strict result schemas, Diagnostic input
rejection, responsive line/full-screen rendering, and in-flight process
signals. The remaining explicit gaps are a full dirty-worktree codemod through
the PTY UI (U06/U14) and a manual screen-reader/emulator audit (U18). Graduation
requires either closing those gaps or recording an explicit release decision.
