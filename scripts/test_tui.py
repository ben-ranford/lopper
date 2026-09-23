#!/usr/bin/env python3
"""Bounded real-terminal smoke tests; standard library only, macOS and Linux."""

import argparse
import errno
import fcntl
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import sys
import termios
import time


# Run terminal setup in a fresh interpreter, avoiding preexec callbacks in a
# potentially threaded test runner. exec preserves Popen's PID and process group.
TERMINAL_CHILD = """
import fcntl, os, sys, termios
fcntl.ioctl(0, termios.TIOCSCTTY, 0)
os.tcsetpgrp(0, os.getpgrp())
os.execvp(sys.argv[1], sys.argv[1:])
"""


class TerminalSession:
    """A reusable PTY with incremental expectations and unconditional child cleanup."""

    def __init__(self, command, timeout=10):
        self.timeout = timeout
        self.master, self.slave = pty.openpty()
        self.original = termios.tcgetattr(self.slave)
        self.pending = b""
        self.transcript = b""
        self.process = None
        try:
            self.resize(24, 120)
            self.process = subprocess.Popen(
                [sys.executable, "-c", TERMINAL_CHILD, *command],
                stdin=self.slave, stdout=self.slave, stderr=self.slave,
                start_new_session=True, env={**os.environ, "TERM": "xterm-256color"},
            )
        except BaseException:
            self.close()
            raise

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def close(self):
        # Release the terminal first: Darwin can hold an exiting session leader
        # in kernel teardown until its output is drained or the master closes.
        os.close(self.master)
        os.close(self.slave)
        if self.process is not None:
            # Descendants can survive their session leader and terminal hangup.
            try:
                os.killpg(self.process.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                # An exiting Darwin process can reject signals; still require
                # successful, bounded reaping below rather than assuming exit.
                pass
            if self.process.poll() is None:
                self.process.wait(timeout=self.timeout)

    def resize(self, rows, columns):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
        if self.process is not None and self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGWINCH)

    def send(self, data):
        """Send literal key bytes, including escape sequences, without adding Enter."""
        view = memoryview(data)
        while view:
            count = os.write(self.master, view)
            view = view[count:]

    def command(self, text):
        self.send(text.encode("utf-8") + b"\r")

    def read_output(self):
        try:
            chunk = os.read(self.master, 65536)
        except OSError as error:
            if error.errno != errno.EIO:
                raise
            chunk = b""
        self.pending += chunk
        self.transcript += chunk
        return chunk

    def expect(self, text):
        expected = text.encode("utf-8")
        deadline = time.monotonic() + self.timeout
        while expected not in self.pending:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise AssertionError(f"timed out waiting for {text!r}: {self.transcript!r}")
            ready, _, _ = select.select([self.master], [], [], remaining)
            if not ready:
                continue
            chunk = self.read_output()
            if not chunk:
                raise AssertionError(f"child ended before {text!r}: {self.transcript!r}")
        _, _, self.pending = self.pending.partition(expected)

    def finish(self):
        deadline = time.monotonic() + self.timeout
        while self.process.poll() is None:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(self.process.args, self.timeout)
            # A controlling terminal may drain output during session teardown.
            # Keep reading so the child can finish exiting before we reap it.
            ready, _, _ = select.select([self.master], [], [], min(remaining, 0.05))
            if ready:
                self.read_output()
        code = self.process.returncode
        if code != 0:
            raise AssertionError(f"child exited {code}: {self.transcript!r}")
        # Darwin revokes the slave descriptor when its session leader exits;
        # the master retains the terminal's final settings for this assertion.
        actual = termios.tcgetattr(self.master)
        # Darwin may transiently set PENDIN when canonical mode is restored.
        mask = ~getattr(termios, "PENDIN", 0)
        actual[3] &= mask
        original = self.original.copy()
        original[3] &= mask
        if actual != original:
            raise AssertionError("child did not restore terminal settings")


def smoke(binary):
    fixture = Path(__file__).resolve().parents[1] / "testdata/ui/pty-fixture"
    with TerminalSession([
        str(Path(binary).resolve()), "tui", "--repo", str(fixture),
        "--language", "js-ts", "--sort", "name", "--page-size", "1",
    ]) as terminal:
        terminal.expect("Page: 1/2")
        terminal.expect("Commands:")
        terminal.resize(30, 100)
        for key, expected in [
            (b"\x1b[C", "Page: 2/2"), (b"\x1bOC", "Page: 2/2"),
            (b"\x1b[D", "Page: 1/2"), (b"\x1bOD", "Page: 1/2"),
        ]:
            terminal.send(key)
            terminal.expect(expected)
            terminal.expect(">")
        terminal.send(b"\x1b[999~")
        terminal.send(b"\x1b")
        time.sleep(0.1)  # Let the decoder finish an incomplete escape sequence.
        terminal.send(b"pag 2\x1b[D\x1b[De\r")
        terminal.expect("Page: 2/2")
        terminal.expect(">")

        for command, expected in [
            ("next", "Page: 2/2"), ("next", "Page: 2/2"),
            ("prev", "Page: 1/2"), ("prev", "Page: 1/2"),
            ("next", "Page: 2/2"), ("filter alpha", "Page: 1/1"),
        ]:
            terminal.command(command)
            terminal.expect(expected)
            terminal.expect("Commands:")
        terminal.command("open alpha")
        terminal.expect("Used exports: 1")
        terminal.expect("Commands:")
        terminal.command("q")
        terminal.finish()
    for exit_key in (b"\x03", b"\x04"):
        with TerminalSession([
            str(Path(binary).resolve()), "tui", "--repo", str(fixture),
            "--language", "js-ts", "--page-size", "1",
        ]) as terminal:
            terminal.expect(">")
            terminal.send(exit_key)
            terminal.finish()
    print("TUI PTY smoke passed: pagination, bounds, filter reset, detail, resize, quit, terminal restoration")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/lopper")
    smoke(parser.parse_args().binary)
