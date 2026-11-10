# Reusable terminal fixture

On macOS or Linux, run `make test-tui` with Go and Python 3 installed. Python
uses only its standard library. The checked-in two-dependency JavaScript fixture
contains simulated exports in `node_modules`; do not run npm install. No fixture
downloads or JavaScript execution are needed.

Run `make tui-demo` for a manual session. Try `next`, `prev`, `filter alpha`,
`open alpha`, and `q`, each followed by Enter. A page contains one dependency,
so pagination and filtering are immediately visible. Filtering resets the page.

To test a different executable, run:

```sh
python3 -B scripts/test_tui.py --binary /absolute/path/to/lopper
```

`TerminalSession` in that script is a reusable context manager: `send(bytes)`
sends exact keyboard protocol bytes without adding Enter; `command(text)` adds
Enter; `resize(rows, columns)` changes the PTY size and signals the process group;
`expect(text)` consumes output incrementally; `finish()` checks successful exit
and terminal restoration. All waits are bounded. Context exit kills and reaps a
child that has not finished, including when an assertion fails.

Each child owns the PTY as its controlling terminal and foreground process group,
so `/dev/tty` and terminal-generated signals behave as in an interactive session.
A fresh Python child performs this setup before replacing itself with the target
executable; the harness does not run pre-exec callbacks in the parent test runner.

`python3 -B -m unittest scripts/test_tui_test.py` demonstrates raw Unicode and
CSI-u key press/release transport, `/dev/tty` access, terminal-generated Ctrl-C,
resize, shutdown, failed exits, and cleanup on
timeouts using a small independent terminal child. These are scaffold contracts,
not assertions that the current Lopper UI interprets CSI-u release events.

The default summary UI accepts both piped newline-delimited commands and
interactive terminal keys. At an empty prompt, left/right page immediately and
clamp at the first/last page. Within a command they move the cursor; Home/End,
Backspace and Delete edit Unicode text. Long commands scroll horizontally in
the prompt instead of wrapping. Enter executes the command. Try typing
`pag 2`, pressing left twice, inserting `e`, then pressing Enter. Ctrl-C, `q`
plus Enter and Ctrl-D at an empty prompt exit and restore terminal settings.
CSI and application-mode arrows are supported by the existing terminal decoder.
Unknown and incomplete escape sequences are ignored without executing a command.

Keyboard decoding and terminal restoration use the existing Bubble Tea backend
on supported terminals. Piped streams retain the portable line-command path.
The real PTY suite runs on macOS and Linux; Windows retains Go command/editor
tests but cannot run this POSIX PTY harness.

The opt-in Stave preview (`--enable-feature stave-tui-preview`) retains its
separate navigation/command modes documented in `docs/stave-tui-preview.md`.
This fix targets the default summary UI selected without that explicit opt-in.

The native Linux/macOS `stave-ui-check` also repeats
`TestStavePreviewParentCancellationRestoresTerminalAndReturnsCause` twenty
times. Its JSON test stream is checked for every successful run and summarized
in the CI log with individual elapsed times, the maximum test time, package
elapsed time, and remaining margin under the five-minute Go test timeout. The
test's 15-second startup/reader-control and one-second cancellation/return
phase bounds remain separate from that whole-package timeout.

When line-command input is piped to terminal output, automatic in-place redraws
retain the latest dependency detail or command feedback until the next input.
For example, after `open alpha` or an unknown command, the feedback remains below
the refreshed summary while input waits. The next command replaces it. Redirected
output retains its existing transcript without replaying duplicate feedback.

`TestSummaryRefreshPreservesCommandFeedback` exercises this path with real PTY
output and reader-handoff markers. It checks visibility after the latest screen
clear, then checks replacement after the next input, without sleep-based timing.

## Terminal restoration acceptance

The terminal input reader owns its shutdown independently of Bubble Tea. The
Unix raw-mode lease must remain active until both the program and reader stop;
cleanup then restores the caller's exact original mode without closing its file.
Windows retains its console reader's existing mode ownership and no-flush close.
On Darwin/BSD, full-screen input from `/dev/tty` at fd >= `FD_SETSIZE` is
rejected before raw-mode entry because that exact path uses a non-cancellable
`select` fallback. The error reports the descriptor and limit; high descriptors
using the kqueue path remain supported. Solaris uses `select` for every input and
rejects any descriptor >= `FD_SETSIZE`. Linux epoll and Windows console input
retain their existing behavior. Other platforms without a cancellable reader fail
closed before entering full-screen mode.

`TestStaveRestorationWaitsForReaderAcrossShutdown` gates an independent read
through normal quit, parent cancellation, program failure and reader-close error.
It observes mode after program exit and after cancellation, releases the read,
then verifies the close result, exact restoration and borrowed-file ownership.
`TestStaveRestorationSignalProcess` runs SIGINT/SIGTERM cases in isolated helper
processes with lifecycle sentinels and bounded, reaped exits. Unstarted-reader
cleanup has a separate acceptance case. Scoped leak assertions add no ignores.

These are acceptance tests for the restoration fix already merged in #1760.
They exercise the lease/input/Bubble Tea lifecycle directly; the existing
`TestStaveInFlightActionProcessSignalsRestoreTerminal` and parent-cancellation
suite exercise the real program orchestration. Mutation controls that remove
the reader join or prematurely restore raw mode must fail behaviorally. Native
Linux and Windows verification still requires their own runners; cross-compiling
is not evidence of terminal behavior.

The native Linux/macOS `stave-ui-check` runs the three restoration roots twenty
times with the race detector and leak checks enabled. Its JSON stream rejects
skips, failures, unexpected tests and incomplete package results; each root
reports its twenty elapsed samples and maximum alongside package elapsed time
and margin under the five-minute test timeout. The reader shutdown gates and
signal helper use their shared fifteen-second bound; the unstarted-reader case
has no phase timeout. These restoration bounds are separate from parent-preview
cancellation timing.
