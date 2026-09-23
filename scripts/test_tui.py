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
import termios
import time


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
                command, stdin=self.slave, stdout=self.slave, stderr=self.slave,
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
        try:
            if self.process is not None and self.process.poll() is None:
                try:
                    os.killpg(self.process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass  # The child exited between poll and killpg.
                self.process.wait(timeout=self.timeout)
        finally:
            os.close(self.master)
            os.close(self.slave)

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
            try:
                chunk = os.read(self.master, 65536)
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
                chunk = b""
            if not chunk:
                raise AssertionError(f"child ended before {text!r}: {self.transcript!r}")
            self.pending += chunk
            self.transcript += chunk
        _, _, self.pending = self.pending.partition(expected)

    def finish(self):
        code = self.process.wait(timeout=self.timeout)
        if code != 0:
            raise AssertionError(f"child exited {code}: {self.transcript!r}")
        actual = termios.tcgetattr(self.slave)
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
    print("TUI PTY smoke passed: pagination, bounds, filter reset, detail, resize, quit, terminal restoration")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/lopper")
    smoke(parser.parse_args().binary)
