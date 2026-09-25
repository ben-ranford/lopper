# Stave TUI preview

`stave-tui-preview` is an opt-in preview of the Stave terminal UI runtime.
It renders the existing Lopper summary report through a typed Stave program while keeping summary analysis, command parsing, and consequential
side effects in Lopper. The legacy summary TUI remains the default and is the
rollback path.

## Enable the preview

The flag remains preview-only and explicit-only (`LOP-FEAT-0029`). Both the resolved feature and explicit consent are required. Consent can come from a CLI enable or effective repository/policy configuration; dev, release, and rolling defaults alone never select Stave:

```sh
lopper tui --repo . --enable-feature stave-tui-preview
```

To render one deterministic frame without entering the terminal session:

```sh
lopper tui --repo . --enable-feature stave-tui-preview --snapshot -
lopper tui --repo . --enable-feature stave-tui-preview --snapshot preview.txt
```

To opt in for a repository, add this to `.lopper.yml` (the immutable code works too):

```yaml
features:
  enable:
    - stave-tui-preview
```

Then `lopper tui --repo .` or bare `lopper` in that repository selects Stave, including snapshots and line-compatible output. Discovery also supports `.lopper.yaml` and `lopper.json`. Use `lopper tui --repo . --config config/ui.yml` for a repository-relative path, or an absolute `--config` path. Missing explicit files, invalid discovered config, and invalid feature references fail before the UI starts.

For a temporary rollback, run:

```sh
lopper tui --repo . --disable-feature LOP-FEAT-0029
```

CLI choices override config for the same feature, so CLI enable also overrides config disable. Without explicit CLI/config enablement, or when the effective choice is disabled, the existing Summary implementation is selected. Neither activation nor rollback writes configuration or preferences, and no prompt is added. No report, baseline, or action state is shared with a previous preview process.

Repository-config activation targets v1.8.9; use a build containing this change. Earlier preview builds require explicit CLI enablement. This does not graduate the Stave preview. The [command coverage table](feature-flags.md#command-configuration-coverage) describes the current config limits for other commands.

The preview uses `github.com/ben-ranford/stave v1.0.0-rc.2`. The integration
is maintained in Lopper; it does not require a local Stave checkout or a
module replacement. The root module is published through the public Go proxy
and checksum database. Lopper retains its terminal bridge; Stave's nested
adapter modules are not part of this dependency.

## Terminal interaction

On a colored, cursor-capable input and output TTY, the preview uses Stave's
interactive session and restores the terminal on quit, interrupt,
cancellation, or an error. A pipe, redirected output, `TERM=dumb`, `NO_COLOR`,
or EOF selects the plain/line-compatible path and emits no cursor-control
screen updates. File-backed pipe input honors cancellation; custom borrowed
readers remain synchronous unless they return input or EOF.
While a preview session runs, Lopper exclusively consumes supported nonregular
file input and clears its read deadline before and after the session. Use the
session context for timeouts; existing file deadlines cannot be queried or
restored.
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

In line mode, cancellation stops waiting for the action handler. A handler
that does not promptly observe its context may continue after the preview
returns; the same indeterminate-outcome contract applies.

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
static-analysis gates. Coverage is checked against the repository's current
total and per-package thresholds on each change. Focused tests are
intentionally bounded and do not replace the repository-wide gates.

The preview remains experimental. Automated tests cover async action
lifecycle, value isolation, strict result schemas, Diagnostic input rejection,
responsive line/full-screen rendering, and in-flight process signals.
[Issue #1492](https://github.com/ben-ranford/lopper/issues/1492) tracks the separate
graduation backlog: full report/detail parity, consequential codemod interruption
through an external PTY, manual screen-reader/emulator review, and comparative
visual/usability checks. Feature graduation also requires published, immutable
Lopper parity and rollback results.
