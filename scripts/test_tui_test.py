"""Exercise the PTY scaffold independently of Lopper's keyboard implementation."""

import os
import subprocess
import sys
import unittest

from scripts.test_tui import TerminalSession


class TerminalSessionTests(unittest.TestCase):
    def test_controlling_terminal_delivers_interrupt_to_foreground_child(self):
        child = """
import os, termios, tty
terminal = os.open('/dev/tty', os.O_RDWR)
original = termios.tcgetattr(terminal)
try:
    assert os.tcgetpgrp(terminal) == os.getpgrp()
    tty.setcbreak(terminal)
    print('foreground ready', flush=True)
    try:
        os.read(terminal, 1)
    except KeyboardInterrupt:
        print('terminal SIGINT received', flush=True)
finally:
    termios.tcsetattr(terminal, termios.TCSANOW, original)
    os.close(terminal)
"""
        with TerminalSession([sys.executable, "-c", child]) as terminal:
            terminal.expect("foreground ready")
            terminal.send(b"\x03")
            terminal.expect("terminal SIGINT received")
            terminal.finish()

    def test_key_events_resize_and_shutdown(self):
        child = """
import os, termios, tty
original = termios.tcgetattr(0)
try:
    tty.setraw(0)
    print('ready', flush=True)
    data = b''
    while b'!' not in data:
        data += os.read(0, 1)
    print(data.hex(), flush=True)
    print(os.get_terminal_size(0), flush=True)
finally:
    termios.tcsetattr(0, termios.TCSANOW, original)
"""
        with TerminalSession([sys.executable, "-c", child]) as terminal:
            terminal.expect("ready")
            terminal.resize(31, 91)
            # CSI-u press/release and UTF-8 bytes are preserved verbatim.
            events = b"\x1b[97;1:1u\x1b[97;1:3u" + "é!".encode()
            terminal.send(events)
            terminal.expect(events.hex())
            terminal.expect("columns=91, lines=31")
            terminal.finish()

    def test_timeout_terminates_and_reaps_child(self):
        with TerminalSession([sys.executable, "-c", "import time; time.sleep(60)"], timeout=0.2) as terminal:
            with self.assertRaisesRegex(AssertionError, "timed out"):
                terminal.expect("never")
        self.assertIsNotNone(terminal.process.returncode)
        with self.assertRaises(ChildProcessError):
            os.waitpid(terminal.process.pid, os.WNOHANG)

    def test_failed_exit_is_reported(self):
        with TerminalSession([sys.executable, "-c", "raise SystemExit(3)"]) as terminal:
            with self.assertRaisesRegex(AssertionError, "child exited 3"):
                terminal.finish()

    def test_unrestored_terminal_is_reported(self):
        with TerminalSession([sys.executable, "-c", "import tty; tty.setraw(0)"]) as terminal:
            with self.assertRaisesRegex(AssertionError, "did not restore terminal settings"):
                terminal.finish()

    def test_finish_drains_terminal_output(self):
        with TerminalSession([sys.executable, "-c", "print('x' * 262144, flush=True)"]) as terminal:
            terminal.finish()
            self.assertGreaterEqual(len(terminal.transcript), 65536)

    def test_finish_timeout_is_bounded(self):
        with TerminalSession([sys.executable, "-c", "import time; time.sleep(60)"], timeout=0.2) as terminal:
            with self.assertRaises(subprocess.TimeoutExpired):
                terminal.finish()


if __name__ == "__main__":
    unittest.main()
