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

## Terminal restoration acceptance

The terminal input reader owns its shutdown independently of Bubble Tea. The
Unix raw-mode lease must remain active until both the program and reader stop;
cleanup then restores the caller's exact original mode without closing its file.
Windows retains its console reader's existing mode ownership and no-flush close.

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
