"""Exercise the PTY scaffold independently of Lopper's keyboard implementation."""

import os
import signal
import socket
import subprocess
import sys
import tempfile
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

    def test_close_terminates_helpers_after_session_leader_exits(self):
        helper = """
import signal, socket, sys, time
signal.signal(signal.SIGHUP, signal.SIG_IGN)
connection = socket.socket(socket.AF_UNIX)
connection.connect(sys.argv[1])
connection.sendall(b'ready')
print('ready', flush=True)
time.sleep(60)
"""
        leader = """
import subprocess, sys
helper = subprocess.Popen([sys.executable, '-c', sys.argv[1], sys.argv[2]], stdout=subprocess.PIPE)
assert helper.stdout.readline() == b'ready\\n'
print(helper.pid, flush=True)
"""
        with tempfile.TemporaryDirectory(prefix="pty-", dir="/tmp") as directory:
            with socket.socket(socket.AF_UNIX) as listener:
                endpoint = os.path.join(directory, "helper")
                listener.bind(endpoint)
                listener.listen(1)
                listener.settimeout(5)
                terminal = TerminalSession([sys.executable, "-c", leader, helper, endpoint])
                helper_pid = None
                connection = None
                try:
                    with terminal:
                        connection, _ = listener.accept()
                        connection.settimeout(5)
                        self.assertEqual(connection.recv(5), b"ready")
                        terminal.finish()
                        helper_pid = int(terminal.transcript.strip())
                        self.assertEqual(terminal.process.returncode, 0)
                    self.assertEqual(connection.recv(1), b"")
                finally:
                    if connection is not None:
                        connection.close()
                    if helper_pid is not None:
                        try:
                            os.kill(helper_pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass

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
