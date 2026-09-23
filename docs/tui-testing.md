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

`python3 -B -m unittest scripts/test_tui_test.py` demonstrates raw Unicode and
CSI-u key press/release transport, resize, shutdown, failed exits, and cleanup on
timeouts using a small independent terminal child. These are scaffold contracts,
not assertions that the current Lopper UI interprets CSI-u release events.

The current summary UI accepts newline-delimited commands. Immediate arrow
navigation and cursor editing belong to #1624; this scaffold deliberately does
not change that input implementation. It supplies a real terminal for those
future regression tests. Portable Go command tests remain usable on Windows;
the PTY suite requires POSIX terminal APIs and runs in the Linux CI partition.
